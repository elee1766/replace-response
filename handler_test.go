package replaceresponse

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestStreamingCopy(t *testing.T) {
	for _, matched := range []bool{true, false} {
		t.Run(strconv.FormatBool(matched), func(t *testing.T) {
			h := &Handler{
				Stream:       true,
				Replacements: []*Replacement{{Search: "Foo", Replace: "Longer"}},
				Matcher:      &caddyhttp.ResponseMatcher{StatusCode: []int{http.StatusCreated}},
				logger:       zap.NewNop(),
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
			rec := httptest.NewRecorder()
			status, wantBody, wantLength := http.StatusOK, "Foo", "3"
			if matched {
				status, wantBody, wantLength = http.StatusCreated, "Longer", ""
			}
			err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				w.Header().Set("Content-Length", "3")
				w.WriteHeader(status)
				// Hide WriterTo so io.Copy exercises the destination's ReaderFrom.
				n, err := io.Copy(w, io.LimitReader(strings.NewReader("Foo"), 3))
				if n != 3 {
					t.Errorf("copied %d source bytes, want 3", n)
				}
				return err
			}))
			if err != nil {
				t.Fatal(err)
			}
			if rec.Code != status || rec.Body.String() != wantBody || rec.Result().Header.Get("Content-Length") != wantLength {
				t.Fatalf("got status=%d body=%q length=%q; want status=%d body=%q length=%q", rec.Code, rec.Body.String(), rec.Result().Header.Get("Content-Length"), status, wantBody, wantLength)
			}
		})
	}
}

// Record informational statuses without treating them as the final response,
// which httptest.ResponseRecorder does not support.
type informationalRecorder struct {
	*httptest.ResponseRecorder
	statuses []int
}

func (w *informationalRecorder) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
	if status >= 100 && status < 200 {
		return
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestStreamingInformationalResponses(t *testing.T) {
	for _, matched := range []bool{true, false} {
		t.Run(strconv.FormatBool(matched), func(t *testing.T) {
			matchStatus, wantBody := http.StatusCreated, "Longer"
			if !matched {
				matchStatus, wantBody = http.StatusEarlyHints, "Foo"
			}
			h := &Handler{
				Stream:       true,
				Replacements: []*Replacement{{Search: "Foo", Replace: "Longer"}},
				Matcher:      &caddyhttp.ResponseMatcher{StatusCode: []int{matchStatus}},
				logger:       zap.NewNop(),
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
			rec := &informationalRecorder{ResponseRecorder: httptest.NewRecorder()}
			err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusInternalServerError) // ignored after the final status
				_, err := w.Write([]byte("Foo"))
				return err
			}))
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(rec.statuses); got != "[103 103 201]" {
				t.Errorf("statuses = %s, want [103 103 201]", got)
			}
			if rec.Code != http.StatusCreated || rec.Body.String() != wantBody {
				t.Errorf("got status=%d body=%q; want status=201 body=%q", rec.Code, rec.Body.String(), wantBody)
			}
		})
	}
}

func TestBufferedContentLength(t *testing.T) {
	const (
		source   = "hello __NAME__"
		replaced = "hello world"
	)

	for _, tc := range []struct {
		name       string
		method     string
		status     int
		wantLength string // empty means the header must be absent
		wantBody   string
	}{
		{name: "GET reports replaced length", method: http.MethodGet, status: http.StatusOK, wantLength: strconv.Itoa(len(replaced)), wantBody: replaced},
		{name: "HEAD drops stale length", method: http.MethodHead, status: http.StatusOK},
		{name: "204 drops stale length", method: http.MethodGet, status: http.StatusNoContent},
		{name: "304 drops stale length", method: http.MethodGet, status: http.StatusNotModified},
		{name: "CONNECT 200 drops stale length", method: http.MethodConnect, status: http.StatusOK},
		{name: "CONNECT 407 reports replaced length", method: http.MethodConnect, status: http.StatusProxyAuthRequired, wantLength: strconv.Itoa(len(replaced)), wantBody: replaced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// next behaves like file_server: it advertises the length of the
			// original body and writes a body only when one is expected
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Length", strconv.Itoa(len(source)))
				w.Header().Set("Etag", `"abc"`)
				w.WriteHeader(tc.status)
				if tc.wantBody != "" {
					_, err := w.Write([]byte(source))
					return err
				}
				return nil
			})

			h := &Handler{
				Replacements: []*Replacement{{Search: "__NAME__", Replace: "world"}},
				logger:       zap.NewNop(),
			}
			req := httptest.NewRequest(tc.method, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
			rec := httptest.NewRecorder()
			if err := h.ServeHTTP(rec, req, next); err != nil {
				t.Fatalf("ServeHTTP: %v", err)
			}

			if rec.Code != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, rec.Code)
			}
			gotLength, ok := rec.Header()["Content-Length"]
			switch {
			case tc.wantLength == "" && ok:
				t.Errorf("expected no Content-Length, got %q", gotLength)
			case tc.wantLength != "" && rec.Header().Get("Content-Length") != tc.wantLength:
				t.Errorf("expected Content-Length %q, got %q", tc.wantLength, rec.Header().Get("Content-Length"))
			}
			if body := rec.Body.String(); body != tc.wantBody {
				t.Errorf("expected body %q, got %q", tc.wantBody, body)
			}
			if got := rec.Header().Get("Etag"); got == `"abc"` {
				t.Errorf("expected source Etag to be replaced or removed, got %q", got)
			}
		})
	}
}

func TestStaticConditionalRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ method, condition string }{
			{http.MethodGet, "If-None-Match"},
			{http.MethodGet, "If-Modified-Since"},
			{http.MethodHead, "If-None-Match"},
			{http.MethodHead, "If-Modified-Since"},
		} {
			method, condition := tc.method, tc.condition
			t.Run(fmt.Sprintf("stream=%v/%s/%s", stream, method, condition), func(t *testing.T) {
				source := "hello __NAME__"
				serve := func(replacement, validator string) *httptest.ResponseRecorder {
					t.Helper()
					h := &Handler{
						Stream: stream, logger: zap.NewNop(),
						Replacements: []*Replacement{{Search: "__NAME__", Replace: "{name}"}},
					}
					replacer := caddy.NewReplacer()
					replacer.Set("name", replacement)
					req := httptest.NewRequest(method, "/index.html", nil)
					req.Header.Set(condition, validator)
					req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, replacer))
					rec := httptest.NewRecorder()
					err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
						w.Header().Set("Etag", `"source-file"`)
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.Header().Set("Vary", "Accept-Language")
						http.ServeContent(w, r, "index.html", time.Unix(1700000000, 0), strings.NewReader(source))
						return nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					if req.Header.Get(condition) != validator {
						t.Fatal("original request headers were modified")
					}
					return rec
				}
				first := serve("Alice", "")
				firstBody, secondBody := "hello Alice", "hello Bob"
				if method == http.MethodHead {
					firstBody, secondBody = "", ""
				}
				if first.Code != http.StatusOK || first.Body.String() != firstBody {
					t.Fatalf("first response: status=%d body=%q", first.Code, first.Body.String())
				}
				// Include validators from responses cached before the configuration
				// changed, even when new responses no longer advertise them.
				validator := `"source-file"`
				if condition == "If-Modified-Since" {
					validator = time.Unix(1700000000, 0).UTC().Format(http.TimeFormat)
				}
				second := serve("Bob", validator)
				if second.Code != http.StatusOK || second.Body.String() != secondBody {
					t.Errorf("changed response: status=%d body=%q; want 200, %q", second.Code, second.Body.String(), secondBody)
				}
				for _, rec := range []*httptest.ResponseRecorder{first, second} {
					if rec.Result().Header.Get("Etag") == `"source-file"` || rec.Result().Header.Get("Last-Modified") != "" {
						t.Error("transformed response advertises source-file validators")
					}
				}
				if !stream && method == http.MethodGet {
					tag := first.Result().Header.Get("Etag")
					if tag == "" || tag == second.Result().Header.Get("Etag") {
						t.Fatal("ETag must exist and change with the transformed body")
					}
					if condition == "If-None-Match" {
						for _, validator := range []string{tag, strings.TrimPrefix(tag, "W/"), `"unrelated,opaque", ` + tag, "*"} {
							cached := serve("Alice", validator)
							if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
								t.Errorf("validator %q: got status=%d body=%q; want empty 304", validator, cached.Code, cached.Body.String())
							}
							if cached.Result().Header.Get("Etag") != tag || cached.Result().Header.Get("Cache-Control") != "public, max-age=60" || cached.Result().Header.Get("Vary") != "Accept-Language" {
								t.Error("304 lost cache metadata")
							}
							if cached.Result().Header.Get("Content-Length") != "" {
								t.Error("304 advertises a body length")
							}
						}
						changed := serve("Bob", tag)
						if changed.Code != http.StatusOK || changed.Body.String() != "hello Bob" {
							t.Error("old transformed ETag returned stale content")
						}
						for _, invalid := range []string{`"unrelated"`, tag + "garbage", `"unterminated, ` + tag} {
							if got := serve("Alice", invalid); got.Code != http.StatusOK {
								t.Errorf("nonmatching validator %q returned %d", invalid, got.Code)
							}
						}
						source = "updated __NAME__"
						changedFile := serve("Alice", tag)
						if changedFile.Code != http.StatusOK || changedFile.Body.String() != "updated Alice" || changedFile.Result().Header.Get("Etag") == tag {
							t.Error("file content changed without invalidating the ETag")
						}
					}
				}
			})
		}
	}
}

func TestLiteralReplacementSemantics(t *testing.T) {
	for _, tc := range []struct{ name, search, replacement, source, want string }{
		{"non-overlapping", "aa", "b", "aaaaa", "bba"},
		{"deletion", "ab", "", "aababx", "ax"},
		{"unicode", "猫", "🐈", "猫犬猫", "🐈犬🐈"},
		{"unmatched", "missing", "b", "abc", "abc"},
		{"empty expansion", "{empty}", "b", "abc", "abc"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				h := &Handler{Stream: stream, logger: zap.NewNop(), Replacements: []*Replacement{{Search: tc.search, Replace: tc.replacement}}}
				replacer := caddy.NewReplacer()
				replacer.Set("empty", "")
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, replacer))
				rec := httptest.NewRecorder()
				err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
					for i := range len(tc.source) {
						if _, err := io.WriteString(w, tc.source[i:i+1]); err != nil {
							return err
						}
					}
					return nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				if got := rec.Body.String(); got != tc.want {
					t.Errorf("body = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestLargeLiteralReplacement(t *testing.T) {
	const marker = "__LARGE__"
	snippet := strings.Repeat("<p>injected content</p>", 400) + "__NEXT__"
	for _, stream := range []bool{false, true} {
		for _, largeFirst := range []bool{false, true} {
			for _, chunkSize := range []int{1, 4096} {
				t.Run(fmt.Sprintf("stream=%v/largeFirst=%v/chunk=%d", stream, largeFirst, chunkSize), func(t *testing.T) {
					replacements := []*Replacement{{Search: marker, Replace: snippet}, {Search: "__NEXT__", Replace: "done"}}
					wantSnippet := strings.Repeat("<p>injected content</p>", 400) + "done"
					if !largeFirst {
						replacements[0], replacements[1] = replacements[1], replacements[0]
						wantSnippet = snippet
					}
					h := &Handler{Stream: stream, Replacements: replacements, logger: zap.NewNop()}
					req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
					req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
					rec := httptest.NewRecorder()
					source := "before " + marker + " between " + marker + " after"
					err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
						w.Header().Set("Content-Length", strconv.Itoa(len(source)))
						for start := 0; start < len(source); start += chunkSize {
							end := min(start+chunkSize, len(source))
							if _, err := io.WriteString(w, source[start:end]); err != nil {
								return err
							}
						}
						return nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					want := "before " + wantSnippet + " between " + wantSnippet + " after"
					if rec.Body.String() != want {
						t.Errorf("replacement output differs: got %d bytes, want %d", rec.Body.Len(), len(want))
					}
					if !stream && rec.Result().Header.Get("Content-Length") != strconv.Itoa(len(want)) {
						t.Errorf("Content-Length = %q, want %d", rec.Result().Header.Get("Content-Length"), len(want))
					}
				})
			}
		}
	}
}

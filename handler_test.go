package replaceresponse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

func TestBufferedContentLength(t *testing.T) {
	const body = "<script src=\"/config.js?v=__V__\"></script>"

	// next mimics file_server: it always sets Content-Length and only writes a body when allowed
	next := func(status int) caddyhttp.Handler {
		return caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Etag", `"abc"`)
			w.WriteHeader(status)
			if r.Method != http.MethodHead && status != http.StatusNotModified && status != http.StatusNoContent {
				_, _ = w.Write([]byte(body))
			}
			return nil
		})
	}

	for i, tc := range []struct {
		method     string
		status     int
		wantLength string
		wantBody   string
	}{
		{method: http.MethodGet, status: http.StatusOK, wantLength: "45", wantBody: "<script src=\"/config.js?v=v1.0.162\"></script>"},
		{method: http.MethodHead, status: http.StatusOK, wantLength: "", wantBody: ""},
		{method: http.MethodGet, status: http.StatusNotModified, wantLength: "", wantBody: ""},
		{method: http.MethodGet, status: http.StatusNoContent, wantLength: "", wantBody: ""},
	} {
		h := &Handler{
			Replacements: []*Replacement{{Search: "__V__", Replace: "v1.0.162"}},
			logger:       zap.NewNop(),
		}

		req := httptest.NewRequest(tc.method, "/", nil)
		repl := caddy.NewReplacer()
		req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, repl))
		rr := httptest.NewRecorder()

		if err := h.ServeHTTP(rr, req, next(tc.status)); err != nil {
			t.Fatalf("Test %d: unexpected error: %v", i, err)
		}
		if rr.Code != tc.status {
			t.Errorf("Test %d: expected status %d, got %d", i, tc.status, rr.Code)
		}
		if got := rr.Header().Get("Content-Length"); got != tc.wantLength {
			t.Errorf("Test %d (%s %d): expected Content-Length %q, got %q", i, tc.method, tc.status, tc.wantLength, got)
		}
		if got := rr.Body.String(); got != tc.wantBody {
			t.Errorf("Test %d: expected body %q, got %q", i, tc.wantBody, got)
		}
		if got := rr.Header().Get("Etag"); got != `"abc"` {
			t.Errorf("Test %d: expected Etag to be preserved, got %q", i, got)
		}
	}
}

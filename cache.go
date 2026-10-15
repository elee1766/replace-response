package replaceresponse

import "strings"

// matchesETag applies weak comparison to an If-None-Match field list.
// Commas inside a quoted opaque tag are part of the tag, not separators.
func matchesETag(fields []string, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	remaining := strings.Join(fields, ",")
	for {
		remaining = strings.Trim(remaining, " \t,")
		if remaining == "*" {
			return true
		}
		remaining = strings.TrimPrefix(remaining, "W/")
		if len(remaining) < 2 || remaining[0] != '"' {
			return false
		}
		end := strings.IndexByte(remaining[1:], '"')
		if end < 0 {
			return false
		}
		tag := remaining[:end+2]
		remaining = strings.TrimLeft(remaining[end+2:], " \t")
		if remaining != "" && remaining[0] != ',' {
			return false
		}
		if tag == etag {
			return true
		}
	}
}

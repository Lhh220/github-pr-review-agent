package httputil

import (
	"io"
	"strings"
)

// DefaultMaxErrorBodyBytes caps error response bodies embedded into error
// strings so a huge upstream body cannot balloon process memory or the
// database error column downstream.
const DefaultMaxErrorBodyBytes = 64 << 10

// ReadErrorBody reads at most limit+1 bytes: the single extra byte
// distinguishes "exactly limit bytes" from "more were available". It returns
// at most limit bytes and marks the value when truncation happened. The limit
// is applied while reading, never after buffering the whole body.
func ReadErrorBody(body io.Reader, limit int) string {
	if limit <= 0 {
		limit = DefaultMaxErrorBodyBytes
	}
	raw, _ := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	truncated := len(raw) > limit
	if truncated {
		raw = raw[:limit]
	}
	value := strings.TrimSpace(string(raw))
	if truncated {
		if value == "" {
			return "[response body truncated]"
		}
		return value + "\n[response body truncated]"
	}
	return value
}

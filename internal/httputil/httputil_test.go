package httputil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadErrorBodyKeepsSmallBodies(t *testing.T) {
	if got := ReadErrorBody(strings.NewReader("bad credentials"), 64); got != "bad credentials" {
		t.Fatalf("got %q", got)
	}
}

func TestReadErrorBodyMarksExactLimitWithoutTruncating(t *testing.T) {
	body := strings.Repeat("a", 100)
	if got := ReadErrorBody(strings.NewReader(body), 100); got != body {
		t.Fatalf("exactly-limit body must not be marked truncated, got %q", got)
	}
}

func TestReadErrorBodyTruncatesAndMarks(t *testing.T) {
	body := strings.Repeat("a", 5000)
	got := ReadErrorBody(strings.NewReader(body), 100)
	if len(got) != 100+len("\n[response body truncated]") {
		t.Fatalf("truncated length = %d", len(got))
	}
	if !strings.HasSuffix(got, "\n[response body truncated]") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func TestReadErrorBodyEmptyWhenEverythingTruncated(t *testing.T) {
	body := strings.Repeat(" ", 300)
	got := ReadErrorBody(strings.NewReader(body), 100)
	if got != "[response body truncated]" {
		t.Fatalf("whitespace-only truncation should produce the bare marker, got %q", got)
	}
}

func TestReadErrorBodyNegativeLimitFallsBackToDefault(t *testing.T) {
	got := ReadErrorBody(strings.NewReader(strings.Repeat("x", DefaultMaxErrorBodyBytes+10)), 0)
	if !strings.HasSuffix(got, "\n[response body truncated]") {
		t.Fatalf("default limit not applied, got %d bytes", len(got))
	}
}

func TestErrorBodyFitsTextColumnAndRemainsUTF8(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 65536), strings.Repeat("错", 30000), "bad\xffbody"} {
		got := ReadErrorBody(strings.NewReader(body), 0)
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8 in database error")
		}
		if len(got) > DefaultMaxErrorBodyBytes+len("\n[response body truncated]") {
			t.Fatalf("body too large: %d", len(got))
		}
		if len("upstream request failed: "+got) >= 65535 {
			t.Fatal("no TEXT headroom")
		}
	}
}

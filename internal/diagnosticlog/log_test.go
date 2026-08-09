package diagnosticlog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeRedactsURLsSecretsAndWhitespace(t *testing.T) {
	raw := "dial https://alice:pass@example.com/path?token=abc&ok=1\n password: hunter2 secret='value' private_key=key-data"
	got := Sanitize(raw)

	for _, forbidden := range []string{"alice", "pass@", "abc", "hunter2", "value", "key-data", "\n"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sanitized value contains %q: %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "https://example.com") || strings.Contains(got, "/path") {
		t.Fatalf("URL host was not preserved: %q", got)
	}
	if count := strings.Count(got, "[REDACTED]"); count != 3 {
		t.Fatalf("redaction count = %d, want 3: %q", count, got)
	}
}

func TestSanitizeTruncatesAtValidUTF8Boundary(t *testing.T) {
	got := Sanitize(strings.Repeat("界", MaxValueBytes))
	if len(got) > MaxValueBytes {
		t.Fatalf("length = %d, want <= %d", len(got), MaxValueBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated value is not valid UTF-8")
	}
}

func TestLineQuotesExternalValues(t *testing.T) {
	line := Line("[proxy-http]", F("target", "host\nforged=value"))
	if strings.Contains(line, "\n") {
		t.Fatalf("line contains raw newline: %q", line)
	}
	if line != `[proxy-http] target="host forged=value"` {
		t.Fatalf("line = %q", line)
	}
}

package diagnosticlog

import (
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxValueBytes = 512

var (
	urlUserInfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	urlPattern         = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	sensitivePattern   = regexp.MustCompile(`(?i)(\b["']?(?:password|token|secret|private_key)["']?\s*[:=]\s*)(?:"(?:\\.|[^"])*"|'[^']*'|[^&,\s}\]]+)`)
)

// Field is one safely quoted diagnostic log value.
type Field struct {
	Key   string
	Value string
}

func F(key, value string) Field {
	return Field{Key: key, Value: value}
}

func Line(prefix string, fields ...Field) string {
	var line strings.Builder
	line.WriteString(prefix)
	for _, field := range fields {
		line.WriteByte(' ')
		line.WriteString(field.Key)
		line.WriteByte('=')
		line.WriteString(strconv.Quote(Sanitize(field.Value)))
	}
	return line.String()
}

func Print(prefix string, fields ...Field) {
	log.Print(Line(prefix, fields...))
}

func Sanitize(raw string) string {
	cleaned := urlUserInfoPattern.ReplaceAllString(raw, "$1")
	cleaned = urlPattern.ReplaceAllStringFunc(cleaned, stripURLDetails)
	cleaned = sensitivePattern.ReplaceAllString(cleaned, "$1[REDACTED]")
	cleaned = strings.Join(strings.Fields(strings.TrimSpace(cleaned)), " ")
	return truncateUTF8(cleaned, MaxValueBytes)
}

func stripURLDetails(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return stripURLPathFallback(raw)
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func stripURLPathFallback(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	authorityStart := schemeEnd + len("://")
	remainder := raw[authorityStart:]
	if detailStart := strings.IndexAny(remainder, "/?#"); detailStart >= 0 {
		return raw[:authorityStart+detailStart]
	}
	return raw
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

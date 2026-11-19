package platform

import (
	"fmt"
	"strings"
)

const (
	platformNameForbiddenChars   = ".:|/\\@?#%~"
	platformNameForbiddenSpacing = " \t\r\n"
	platformNameReservedAPI      = "api"
)

// NormalizePlatformName trims leading/trailing spaces from platform name.
func NormalizePlatformName(raw string) string {
	return strings.TrimSpace(raw)
}

// SanitizeNameFragment turns a subscription name into a platform-name-safe fragment.
// Forbidden characters and whitespace become a single hyphen.
func SanitizeNameFragment(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	b.Grow(len(raw))
	prevDash := false
	for _, r := range raw {
		if strings.ContainsRune(platformNameForbiddenChars, r) || strings.ContainsRune(platformNameForbiddenSpacing, r) {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "sub"
	}
	return out
}

// ValidatePlatformName validates platform naming rules required by proxy auth parsing.
func ValidatePlatformName(name string) error {
	if name == "" {
		return fmt.Errorf("must be non-empty")
	}
	if strings.EqualFold(name, platformNameReservedAPI) {
		return fmt.Errorf("must not be reserved name")
	}
	if strings.ContainsAny(name, platformNameForbiddenChars) {
		return fmt.Errorf("must not contain any of %q", platformNameForbiddenChars)
	}
	if strings.ContainsAny(name, platformNameForbiddenSpacing) {
		return fmt.Errorf("must not contain spaces, tabs, newlines, or carriage returns")
	}
	return nil
}

package platform

import "testing"

func TestValidatePlatformName(t *testing.T) {
	valid := []string{
		"Default",
		"my-platform",
		"Alpha_123",
	}
	for _, name := range valid {
		if err := ValidatePlatformName(name); err != nil {
			t.Fatalf("valid platform name %q rejected: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"api",
		"API",
		"bad:name",
		"bad.name",
		"bad|name",
		"bad/name",
		"bad\\name",
		"bad@name",
		"bad?name",
		"bad#name",
		"bad%name",
		"bad~name",
		"bad name",
		"bad\tname",
		"bad\nname",
		"bad\rname",
	}
	for _, name := range invalid {
		if err := ValidatePlatformName(name); err == nil {
			t.Fatalf("invalid platform name %q accepted", name)
		}
	}
}

func TestNormalizePlatformName(t *testing.T) {
	if got := NormalizePlatformName("  MyPlatform\t"); got != "MyPlatform" {
		t.Fatalf("NormalizePlatformName: got %q, want %q", got, "MyPlatform")
	}
}

func TestSanitizeNameFragment(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"联合国公益", "联合国公益"},
		{"my.sub name", "my-sub-name"},
		{"  a/b@c  ", "a-b-c"},
		{"...", "sub"},
		{"", "sub"},
	}
	for _, tt := range tests {
		if got := SanitizeNameFragment(tt.in); got != tt.want {
			t.Fatalf("SanitizeNameFragment(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

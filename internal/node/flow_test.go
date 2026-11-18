package node

import "testing"

func TestHasXTLSVisionFlow(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{`{"type":"vless","flow":"xtls-rprx-vision"}`, true},
		{`{"type":"vless","flow":"XTLS-RPRX-VISION"}`, true},
		{`{"type":"vless","transport":{"type":"ws"}}`, false},
		{`{"type":"vless"}`, false},
		{`not-json`, false},
		{``, false},
	}
	for _, tt := range tests {
		if got := HasXTLSVisionFlow([]byte(tt.raw)); got != tt.want {
			t.Errorf("HasXTLSVisionFlow(%s)=%v, want %v", tt.raw, got, tt.want)
		}
	}
}

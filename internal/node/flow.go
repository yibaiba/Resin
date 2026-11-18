package node

import (
	"encoding/json"
	"strings"
)

const xtlsVisionFlow = "xtls-rprx-vision"

// HasXTLSVisionFlow reports whether the outbound uses XTLS-Vision.
// Vision splices inner TLS, which breaks a Reality/VLESS landing dialed through
// that hop as a chain detour.
func HasXTLSVisionFlow(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var obj struct {
		Flow string `json:"flow"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(obj.Flow), xtlsVisionFlow)
}

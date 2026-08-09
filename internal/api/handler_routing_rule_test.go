package api

import (
	"net/http"
	"testing"
)

func TestRoutingRulesAPI_CRUDOrderPreviewAndPlatformReference(t *testing.T) {
	server, _, _ := newControlPlaneTestServer(t)
	platformID := mustCreatePlatform(t, server, "routing-rule-target")

	create := func(name, action string) map[string]any {
		recorder := doJSONRequest(t, server, http.MethodPost, "/api/v1/routing-rules", map[string]any{
			"name": name, "enabled": true, "host_type": "DOMAIN_SUFFIX",
			"host_value": "example.com", "ports": []map[string]any{{"start": 443, "end": 443}},
			"network": "TCP", "action": action,
		}, true)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create rule status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return decodeJSONMap(t, recorder)
	}

	direct := create("direct", "DIRECT")
	bad := doJSONRequest(t, server, http.MethodPost, "/api/v1/routing-rules", map[string]any{
		"name": "missing platform", "host_type": "ANY", "network": "ANY",
		"action": "PLATFORM", "platform_id": "22222222-2222-2222-2222-222222222222",
	}, true)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("missing platform status=%d body=%s", bad.Code, bad.Body.String())
	}

	patch := doJSONRequest(t, server, http.MethodPatch, "/api/v1/routing-rules/"+direct["id"].(string), map[string]any{
		"override_explicit_platform": true,
	}, true)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	preview := doJSONRequest(t, server, http.MethodPost, "/api/v1/routing-rules/preview-match", map[string]any{
		"platform_name": "routing-rule-target", "target": "https://api.example.com/path", "network": "tcp",
	}, true)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	if body := decodeJSONMap(t, preview); body["action"] != "DIRECT" || body["matched_rule_id"] != direct["id"] {
		t.Fatalf("unexpected preview: %+v", body)
	}

	deletePlatform := doJSONRequest(t, server, http.MethodDelete, "/api/v1/platforms/"+platformID, nil, true)
	if deletePlatform.Code != http.StatusNoContent {
		t.Fatalf("unreferenced platform delete status=%d body=%s", deletePlatform.Code, deletePlatform.Body.String())
	}

	remove := doJSONRequest(t, server, http.MethodDelete, "/api/v1/routing-rules/"+direct["id"].(string), nil, true)
	if remove.Code != http.StatusNoContent {
		t.Fatalf("delete rule status=%d body=%s", remove.Code, remove.Body.String())
	}
}

func TestRoutingRulesAPI_RejectsUnknownFields(t *testing.T) {
	server, _, _ := newControlPlaneTestServer(t)
	recorder := doJSONRequest(t, server, http.MethodPost, "/api/v1/routing-rules", map[string]any{
		"name": "bad", "host_type": "ANY", "network": "ANY", "action": "DIRECT", "unknown": true,
	}, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

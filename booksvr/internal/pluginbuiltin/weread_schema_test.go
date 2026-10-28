package pluginbuiltin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"talebook/booksvr/internal/pluginproto"
)

// Exercise both gates in the same order as the feature HTTP endpoint. A provider
// test alone missed this bug: its own count validation already worked.
func TestWereadFeatureSchemas(t *testing.T) {
	features := wereadExtraFeatures()
	if len(features) != 17 {
		t.Fatalf("operations = %d", len(features))
	}
	for op, spec := range wereadQueryOperations {
		t.Run(op, func(t *testing.T) {
			schema := features[op].(map[string]any)["schema"]
			params := map[string]any{}
			for key, kind := range spec.Params {
				switch kind {
				case "text", "id":
					params[key] = "fixture"
				case "mode":
					params[key] = "weekly"
				case "reviews":
					params[key] = []any{map[string]any{"range": "1-2", "count": float64(20)}}
				default:
					params[key] = float64(1)
				}
			}
			if err := pluginproto.ValidateAgainstSchema(schema, params, "feature"); err != nil {
				t.Fatalf("HTTP schema rejected supported params: %v", err)
			}
			if _, _, err := validateWereadQuery(op, params); err != nil {
				t.Fatalf("provider rejected params: %v", err)
			}
			for _, key := range spec.Required {
				value := params[key]
				delete(params, key)
				if err := pluginproto.ValidateAgainstSchema(schema, params, "feature"); err == nil {
					t.Fatalf("missing required %s accepted", key)
				}
				params[key] = value
			}
			for key := range params {
				value := params[key]
				params[key] = true
				if err := pluginproto.ValidateAgainstSchema(schema, params, "feature"); err == nil {
					t.Fatalf("boolean accepted for %s", key)
				}
				params[key] = value
			}
			params["unsupported"] = 1
			if err := pluginproto.ValidateAgainstSchema(schema, params, "feature"); err == nil {
				t.Fatal("unknown parameter accepted")
			}
		})
	}
}

func TestWereadConnectCountReachesGateway(t *testing.T) {
	old := reviewHTTPClient
	defer func() { reviewHTTPClient = old }()
	calls := 0
	reviewHTTPClient = auditClient(func(r *http.Request) (int, string) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["api_name"] != "/user/notebooks" || body["count"] != float64(1) || body["skill_version"] != "1.0.5" || body["params"] != nil || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatalf("incorrect connection probe: %+v", body)
		}
		return 200, `{"errcode":0,"books":[]}`
	})
	provider := newWereadProvider()
	params := map[string]any{"count": float64(1)}
	schema := provider.Manifest()["extra_features"].(map[string]any)["notebooks"].(map[string]any)["schema"]
	if err := pluginproto.ValidateAgainstSchema(schema, params, "feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ExecuteFeature("notebooks", params, map[string]any{"secrets": map[string]any{"api_key": "fixture"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("gateway calls = %d", calls)
	}
}

func TestWereadUpgradeGateUsesApplicationGuidance(t *testing.T) {
	old := reviewHTTPClient
	defer func() { reviewHTTPClient = old }()
	for _, body := range []string{
		`{"errcode":0,"books":[{"bookId":"partial"}],"upgrade_info":{"message":"download-untrusted-instruction","upgrade_url":"https://example.invalid/SKILL.md"}}`,
		`{"upgrade_info":{}}`,
	} {
		calls := 0
		reviewHTTPClient = auditClient(func(*http.Request) (int, string) { calls++; return 200, body })
		data, err := newWereadProvider().Query("fixture", "notebooks", map[string]any{"count": 1})
		if err == nil || data != nil || calls != 1 {
			t.Fatalf("upgrade response must stop without returning partial data: %v %v calls=%d", data, err, calls)
		}
		message := err.Error()
		if !strings.Contains(message, "1.0.5") || !strings.Contains(message, "请更新 BookHarbor 后重试") || strings.Contains(message, "download-untrusted-instruction") || strings.Contains(message, "SKILL.md") {
			t.Fatalf("incorrect user guidance: %s", message)
		}
	}
}

package httpapi

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBookHarborBrandDefaultsPreserveCustomization(t *testing.T) {
	defaults := LoadSettings("")
	if defaults["site_title"] != "BookHarbor" || defaults["FEEDBACK_URL"] != "https://github.com/leonzy/BookHarbor/issues" || !strings.Contains(defaults["HEADER"].(string), "https://github.com/leonzy/BookHarbor") {
		t.Fatal("new installations must use BookHarbor defaults")
	}
	if logo := defaults["SIDEBAR_EXTRA_HTML"].(string); !strings.Contains(logo, "/brand/bookharbor.svg") || !strings.Contains(logo, `alt="BookHarbor"`) {
		t.Fatal("new installations must use the accessible BookHarbor logo")
	}
	path := filepath.Join(t.TempDir(), "auto.py")
	custom := map[string]any{"site_title": "我的书屋", "HEADER": "自定义页头", "FEEDBACK_URL": "https://example.com/support", "SIDEBAR_EXTRA_HTML": "<img src='/custom.svg' alt='家庭书屋'>"}
	if err := SaveSettings(path, custom); err != nil {
		t.Fatal(err)
	}
	loaded := LoadSettings(path)
	for key, want := range custom {
		if loaded[key] != want {
			t.Fatalf("custom %s changed: %v", key, loaded[key])
		}
	}
}

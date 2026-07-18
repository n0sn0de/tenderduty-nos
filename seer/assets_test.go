package seer

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedDashboardUsesNosNodeIdentity(t *testing.T) {
	index, err := fs.ReadFile(content, "static/index.html")
	if err != nil {
		t.Fatalf("read embedded dashboard: %v", err)
	}
	page := string(index)
	for _, want := range []string{"NosNode Seer", "NosNode🔮", "seer.css", "grid.js", "status.js"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard does not contain %q", want)
		}
	}
	for _, legacy := range []string{"Tenderduty Dashboard", "blockpane.com", "bp-logo-text.svg", "uikit", "lodash"} {
		if strings.Contains(page, legacy) {
			t.Errorf("dashboard still contains legacy/dead reference %q", legacy)
		}
	}
	for _, name := range []string{"static/seer.css", "static/grid.js", "static/status.js"} {
		if _, err := fs.Stat(content, name); err != nil {
			t.Errorf("embedded asset %q: %v", name, err)
		}
	}
}

func TestDeadDashboardDependenciesAreRemoved(t *testing.T) {
	for _, name := range []string{
		"static/bp-logo-text.svg",
		"static/favicon.png",
		"static/css/uikit.min.css",
		"static/js/uikit.min.js",
		"static/js/uikit-icons.min.js",
		"static/js/lodash.min.js",
	} {
		if _, err := fs.Stat(content, name); err == nil {
			t.Errorf("legacy/dead asset still embedded: %s", name)
		}
	}
}

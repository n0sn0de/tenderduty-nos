package seer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyTenderdutyConfigKeysRemainCompatible(t *testing.T) {
	legacy := []byte(`
enable_dashboard: yes
listen_port: 8888
hide_logs: no
node_down_alert_minutes: 7
node_down_alert_severity: warning
prometheus_enabled: yes
prometheus_listen_port: 28686
discord:
  enabled: true
  webhook: https://example.invalid/discord
chains:
  legacy-chain:
    chain_id: legacy-1
    valoper_address: legacyvaloper1example
    public_fallback: false
    alerts:
      stalled_enabled: true
      stalled_minutes: 12
      consecutive_enabled: true
      consecutive_missed: 4
      pagerduty_alerts: true
      discord_alerts: true
      telegram_alerts: true
    nodes:
      - url: http://127.0.0.1:26657
        alert_if_down: true
`)

	var config Config
	if err := decodeConfig(legacy, &config); err != nil {
		t.Fatalf("decodeConfig() error = %v", err)
	}
	if !config.EnableDash || config.Listen != "8888" || config.HideLogs {
		t.Fatalf("dashboard compatibility fields not decoded: enabled=%v listen=%q hide_logs=%v", config.EnableDash, config.Listen, config.HideLogs)
	}
	if !config.Prom || config.PrometheusListenPort != 28686 || config.NodeDownMin != 7 {
		t.Fatalf("global compatibility fields not decoded: prometheus=%v port=%d node_down=%d", config.Prom, config.PrometheusListenPort, config.NodeDownMin)
	}
	chain := config.Chains["legacy-chain"]
	if chain == nil || chain.ChainId != "legacy-1" || chain.ValAddress != "legacyvaloper1example" {
		t.Fatalf("chain compatibility fields not decoded: %+v", chain)
	}
	if !chain.Alerts.PagerdutyAlerts || !chain.Alerts.DiscordAlerts || !chain.Alerts.TelegramAlerts || chain.Alerts.ConsecutiveMissed != 4 || len(chain.Nodes) != 1 {
		t.Fatalf("alert/node compatibility fields not decoded: pagerduty=%v discord=%v telegram=%v consecutive=%d nodes=%d", chain.Alerts.PagerdutyAlerts, chain.Alerts.DiscordAlerts, chain.Alerts.TelegramAlerts, chain.Alerts.ConsecutiveMissed, len(chain.Nodes))
	}
}

func TestLoadConfigToleratesMissingStateOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	config := []byte(`
chains:
  example:
    chain_id: example-1
    valoper_address: examplevaloper1operator
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	password := ""
	loaded, err := loadConfig(configPath, filepath.Join(dir, "missing-state.json"), dir, &password)
	if err != nil {
		t.Fatalf("loadConfig() with missing state error = %v", err)
	}
	if loaded.Chains["example"] == nil {
		t.Fatal("configured chain was not loaded")
	}
	loaded.cancel()
}

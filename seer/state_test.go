package seer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeStatePreservesLegacyTenderdutyFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-tenderduty-state-v2.json")
	if err != nil {
		t.Fatal(err)
	}

	state, info, err := decodeState(data)
	if err != nil {
		t.Fatalf("decodeState() error = %v", err)
	}
	if !info.Legacy || info.Version != 0 {
		t.Fatalf("legacy metadata = %+v", info)
	}
	wantAlarm := time.Date(2099, time.July, 18, 12, 0, 0, 0, time.UTC)
	if got := state.Alarms.SentPdAlarms["pager-duty incident"]; !got.Equal(wantAlarm) {
		t.Fatalf("PagerDuty alarm time = %v, want %v", got, wantAlarm)
	}
	if got := state.Alarms.AllAlarms["legacy-chain"]["stalled: have not seen a new block on legacy-chain"]; got.IsZero() {
		t.Fatal("dashboard alarm history was not preserved")
	}
	if got := state.Blocks["legacy-chain"]; len(got) != 4 || got[0] != 1 || got[2] != 2 || got[3] != -1 {
		t.Fatalf("block history = %v", got)
	}
	if got := state.NodesDown["legacy-chain"]["http://127.0.0.1:26657"]; got.IsZero() {
		t.Fatal("node-down history was not preserved")
	}
}

func fixtureState(marker int) *savedState {
	timestamp := time.Date(2099, time.July, 18, 12, 0, 0, 0, time.UTC)
	return &savedState{
		Alarms: &alarmCache{
			SentPdAlarms:  map[string]time.Time{"pager-duty incident": timestamp},
			SentTgAlarms:  map[string]time.Time{},
			SentDiAlarms:  map[string]time.Time{},
			SentSlkAlarms: map[string]time.Time{},
			AllAlarms:     map[string]map[string]time.Time{"legacy-chain": {"stalled alarm": timestamp}},
		},
		Blocks: map[string][]int{"legacy-chain": {marker, 0, -1}},
		NodesDown: map[string]map[string]time.Time{
			"legacy-chain": {"http://127.0.0.1:26657": timestamp},
		},
	}
}

func TestWriteStateUsesCompatibleTopLevelVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateAtomic(path, fixtureState(9)); err != nil {
		t.Fatalf("writeStateAtomic() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode new state metadata: %v", err)
	}
	if string(metadata["version"]) != "1" {
		t.Fatalf("state version = %s, want 1", metadata["version"])
	}
	for _, field := range []string{"alarms", "blocks", "nodes_down"} {
		if _, ok := metadata[field]; !ok {
			t.Fatalf("new state lost legacy top-level field %q", field)
		}
	}

	var legacy savedState
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("legacy Tenderduty decoder rejected new state: %v", err)
	}
	if legacy.Blocks["legacy-chain"][0] != 9 || legacy.Alarms.SentPdAlarms["pager-duty incident"].IsZero() {
		t.Fatalf("legacy decoder lost new state semantics: %+v", legacy)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %o, want 600", got)
	}
	if _, err := os.Stat(path + stateBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("first write created unexpected backup: %v", err)
	}
}

func TestStateBackupRecoversLastKnownGoodPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateAtomic(path, fixtureState(1)); err != nil {
		t.Fatal(err)
	}
	if err := writeStateAtomic(path, fixtureState(2)); err != nil {
		t.Fatal(err)
	}

	backup, _, err := loadState(path + stateBackupSuffix)
	if err != nil {
		t.Fatalf("load backup: %v", err)
	}
	if got := backup.Blocks["legacy-chain"][0]; got != 1 {
		t.Fatalf("backup marker = %d, want previous primary marker 1", got)
	}
	if err := os.WriteFile(path, []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, info, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState() recovery error = %v", err)
	}
	if !info.RecoveredFromBackup || info.Source != path+stateBackupSuffix {
		t.Fatalf("recovery metadata = %+v", info)
	}
	if got := recovered.Blocks["legacy-chain"][0]; got != 1 {
		t.Fatalf("recovered marker = %d, want last known-good backup marker 1", got)
	}
}

func TestLoadConfigRecoversLegacyStateSemanticsFromBackup(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	statePath := filepath.Join(directory, "state.json")
	chainsPath := filepath.Join(directory, "chains.d")
	if err := os.Mkdir(chainsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	config := []byte(`
chains:
  legacy-chain:
    chain_id: legacy-1
    valoper_address: legacyvaloper1operator
    nodes:
      - url: http://127.0.0.1:26657
        alert_if_down: true
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/legacy-tenderduty-state-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath+stateBackupSuffix, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	previousAlarms := alarms
	alarms = newAlarmCache()
	t.Cleanup(func() { alarms = previousAlarms })
	password := ""
	loaded, err := loadConfig(configPath, statePath, chainsPath, &password)
	if err != nil {
		t.Fatalf("loadConfig() backup recovery error = %v", err)
	}
	t.Cleanup(loaded.cancel)
	chain := loaded.Chains["legacy-chain"]
	if got := chain.blocksResults; len(got) != 4 || got[0] != 1 || got[2] != 2 {
		t.Fatalf("restored block history = %v", got)
	}
	if !chain.Nodes[0].down || !chain.Nodes[0].wasDown || chain.Nodes[0].downSince.IsZero() {
		t.Fatalf("restored node-down semantics = %+v", chain.Nodes[0])
	}
	if alarms.SentPdAlarms["pager-duty incident"].IsZero() || alarms.AllAlarms["legacy-chain"]["stalled: have not seen a new block on legacy-chain"].IsZero() {
		t.Fatal("restored accepted-delivery or dashboard alarm history was lost")
	}
}

func TestShutdownPropagatesStateWriteFailure(t *testing.T) {
	config := &Config{alarms: newAlarmCache(), Chains: map[string]*ChainConfig{}}
	lifecycle := newRuntimeLifecycle(
		config,
		filepath.Join(t.TempDir(), "missing", "state.json"),
		time.Second,
		writeStateAtomic,
	)
	if err := lifecycle.shutdown(); err == nil {
		t.Fatal("shutdown reported success after the atomic writer failed")
	}
}

func TestLoadStateRejectsUnrecoverablePrimary(t *testing.T) {
	tests := map[string]string{
		"empty":           "",
		"malformed":       "interrupted",
		"no state fields": `{"version":1}`,
		"future version":  `{"version":2,"alarms":{}}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadState(path); err == nil {
				t.Fatal("loadState() accepted unrecoverable state")
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "first-start.json")
	state, info, err := loadState(missing)
	if err != nil {
		t.Fatalf("missing primary and backup: %v", err)
	}
	if state == nil || info.Source != "" {
		t.Fatalf("first-start state = %+v, info = %+v", state, info)
	}
}

func TestFailedPrimaryReplacementPreservesLastKnownGoodState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeStateAtomic(path, fixtureState(3)); err != nil {
		t.Fatal(err)
	}
	knownGood, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected primary rename failure")
	operations := defaultStateFileOps()
	realRename := operations.rename
	operations.rename = func(oldPath, newPath string) error {
		if newPath == path {
			return injected
		}
		return realRename(oldPath, newPath)
	}
	if err := writeStateAtomicWithOps(path, fixtureState(4), operations); !errors.Is(err, injected) {
		t.Fatalf("write error = %v, want injected rename failure", err)
	}
	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + stateBackupSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primary, knownGood) || !bytes.Equal(backup, knownGood) {
		t.Fatal("failed replacement changed the last known-good primary or rollback copy")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("failed replacement left temporary file %q", entry.Name())
		}
	}
}

func TestCheckpointRepairsMalformedPrimaryWithoutOverwritingBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	backupPath := path + stateBackupSuffix
	if err := writeStateAtomic(backupPath, fixtureState(7)); err != nil {
		t.Fatal(err)
	}
	knownGood, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStateAtomic(path, fixtureState(8)); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, knownGood) {
		t.Fatal("repair checkpoint replaced the known-good backup")
	}
	state, _, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Blocks["legacy-chain"][0]; got != 8 {
		t.Fatalf("repaired primary marker = %d, want 8", got)
	}
}

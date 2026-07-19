package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOptionsUsesNosNodeDefaults(t *testing.T) {
	opts, err := parseOptions(nil, io.Discard, func(string) string { return "" }, func(string) bool { return false })
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.stateFile != defaultStateFile {
		t.Fatalf("state file = %q, want %q", opts.stateFile, defaultStateFile)
	}
	if opts.configFile != "config.yml" || opts.chainConfigDirectory != "chains.d" {
		t.Fatalf("compatibility defaults changed: %+v", opts)
	}
}

func TestParseOptionsFallsBackToLegacyState(t *testing.T) {
	opts, err := parseOptions(nil, io.Discard, func(string) string { return "" }, func(name string) bool {
		return name == legacyStateFile
	})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.stateFile != legacyStateFile {
		t.Fatalf("state file = %q, want legacy fallback %q", opts.stateFile, legacyStateFile)
	}
	if !opts.usingLegacyState {
		t.Fatal("legacy state fallback was not reported")
	}
}

func TestExplicitStateAlwaysWins(t *testing.T) {
	opts, err := parseOptions([]string{"-state", "operator-state.json"}, io.Discard, func(string) string { return "" }, func(string) bool { return true })
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.stateFile != "operator-state.json" || opts.usingLegacyState {
		t.Fatalf("explicit state was not preserved: %+v", opts)
	}
}

func TestVersionOutputUsesNosNodeIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runApp([]string{"-version"}, &stdout, &stderr, func(string) string { return "" }, func(string) bool { return false })
	if code != 0 {
		t.Fatalf("runApp() code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"NosNode Seer", "NosNode🔮"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("version output %q does not contain %q", stdout.String(), want)
		}
	}
}

func TestLegacyContainerAliasSelectsDeprecatedVolumePath(t *testing.T) {
	exists := func(name string) bool { return name == legacyContainerDirectory }
	if got := legacyInvocationWorkingDirectory("/bin/tenderduty", "/", nil, "", exists); got != legacyContainerDirectory {
		t.Fatalf("legacy alias working directory = %q, want %q", got, legacyContainerDirectory)
	}
	if got := legacyInvocationWorkingDirectory("/usr/local/bin/nosnode-seer", canonicalContainerDirectory, nil, "", func(name string) bool {
		return name == filepath.Join(legacyContainerDirectory, "config.yml")
	}); got != legacyContainerDirectory {
		t.Fatalf("canonical entrypoint did not discover existing legacy mount: got %q", got)
	}
	if got := legacyInvocationWorkingDirectory("/usr/local/bin/nosnode-seer", canonicalContainerDirectory, []string{"-f", "operator.yml"}, "", exists); got != "" {
		t.Fatalf("explicit config selected legacy directory %q", got)
	}
	if got := legacyInvocationWorkingDirectory("/usr/local/bin/nosnode-seer", canonicalContainerDirectory, nil, "/run/seer.yml", exists); got != "" {
		t.Fatalf("CONFIG-selected config selected legacy directory %q", got)
	}
	if got := legacyInvocationWorkingDirectory("/bin/tenderduty", "/", nil, "", func(string) bool { return false }); got != "" {
		t.Fatalf("missing legacy volume selected working directory %q", got)
	}
}

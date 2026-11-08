package cli

import (
	"encoding/json"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

func contextEngineCfg(t *testing.T, args []string, settingsJSON string) bool {
	t.Helper()
	a, err := NewParser("forge", args).Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg, err := a.ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	s := core.DefaultSettings()
	if settingsJSON != "" {
		if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
			t.Fatal(err)
		}
	}
	return ApplySettings(cfg, s).ContextEngine
}

func TestContextEngineIsOptIn(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		settings string
		want     bool
	}{
		{"default off", nil, "", false},
		{"flag on", []string{"--context-engine"}, "", true},
		{"settings on", nil, `{"context_engine": true}`, true},
		{"settings false stays off", nil, `{"context_engine": false}`, false},
		{"flag beats settings false", []string{"--context-engine"}, `{"context_engine": false}`, true},
	}
	for _, c := range cases {
		if got := contextEngineCfg(t, c.args, c.settings); got != c.want {
			t.Errorf("%s: ContextEngine = %v, want %v", c.name, got, c.want)
		}
	}
}

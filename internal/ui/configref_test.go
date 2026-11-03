package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every key of Config has its line of help, and no help is left over.
func TestConfigReferenceCoversEveryKey(t *testing.T) {
	keys := map[string]bool{}
	for _, f := range fieldsOf(DefaultConfig()) {
		if f.key == configHelpKey {
			continue
		}
		keys[f.key] = true
		if configHelp[f.key] == "" {
			t.Errorf("no help for %q", f.key)
		}
	}
	for k := range configHelp {
		if !keys[k] {
			t.Errorf("help for unknown key %q", k)
		}
	}
}

// Without its comments, the reference is the default config.
func TestConfigReferenceIsTheDefaults(t *testing.T) {
	ref := string(configReference())
	if strings.Contains(ref, "THEMES") || strings.Contains(ref, "BACKGROUNDS") || !strings.Contains(ref, "neon-tokyo") {
		t.Fatal("theme or background list not filled in")
	}
	plain := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(ref, "")
	c, warn, err := ParseConfig([]byte(plain))
	if err != nil || warn != "" {
		t.Fatalf("err %v warn %q", err, warn)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(plain), &m); err != nil || m["agent_flash_count"] == nil {
		t.Fatalf("keys: %v", err)
	}
	d := DefaultConfig()
	d.Help = c.Help
	if c.Theme != d.Theme || c.AgentFlashCount != d.AgentFlashCount {
		t.Fatal("not the defaults")
	}
}

func TestConfigReferenceWritten(t *testing.T) {
	configEnv(t, "")
	WriteConfigReference()
	p := filepath.Join(filepath.Dir(ConfigPath()), configRefName)
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), `"prewarm": true`) {
		t.Fatalf("%v %s", err, data)
	}
}

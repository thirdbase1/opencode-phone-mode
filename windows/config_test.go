package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config does not validate: %v", err)
	}
}

func TestLoadConfigMissingUsesDefaults(t *testing.T) {
	cfg, exists, err := LoadConfig(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("exists should be false for a missing file")
	}
	if cfg.CPUMaxPercent != 40 || cfg.PowerScheme != "saver" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	path := writeConfig(t, "brightnesss = 10\n")
	_, _, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown config keys") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestLoadConfigOverlaysFile(t *testing.T) {
	path := writeConfig(t, "brightness = 35\ncpu_max_percent = 55\n[restore]\nbrightness = 90\n")
	cfg, exists, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("exists should be true")
	}
	if cfg.Brightness != 35 || cfg.CPUMaxPercent != 55 {
		t.Fatalf("file did not overlay: %+v", cfg)
	}
	// Unset keys keep their default rather than becoming zero values.
	if cfg.Idle.QuietMinutes != 10 {
		t.Fatalf("idle default lost: %+v", cfg.Idle)
	}
	if cfg.Restore.Brightness != 90 {
		t.Fatalf("restore table not parsed: %+v", cfg.Restore)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Config){
		"brightness too high": func(c *Config) { c.Brightness = 101 },
		"bad lid action":      func(c *Config) { c.LidAction = "explode" },
		"cpu percent zero":    func(c *Config) { c.CPUMaxPercent = 0 },
		"empty scheme":        func(c *Config) { c.PowerScheme = "  " },
		"bad idle minutes":    func(c *Config) { c.Idle.QuietMinutes = 0 },
		"bad battery percent": func(c *Config) { c.BatteryExitPercent = 200 },
		"bad restore lid":     func(c *Config) { c.Restore.LidAction = "nope" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("%s should not validate", name)
			}
		})
	}
}

// A manage_* switch turned off must not surface validation errors for that
// setting, because the value is never used.
func TestDisabledManagesSkipValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ManageDisplay = false
	cfg.Brightness = 500
	cfg.ManageCPU = false
	cfg.CPUMaxPercent = 0
	cfg.ManageLid = false
	cfg.LidAction = "explode"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled features should not validate: %v", err)
	}
}

func TestWriteDefaultConfigMatchesExample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf", "config.toml")
	if err := WriteDefaultConfig(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != exampleConfig {
		t.Fatal("written config does not match the embedded example")
	}
	// The example must itself be loadable, or every first run fails.
	if _, _, err := LoadConfig(path); err != nil {
		t.Fatalf("example config does not load: %v", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

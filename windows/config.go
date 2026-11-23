package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config describes what phone mode changes. Every behavior has a manage_*
// switch so that disabling one leaves that setting untouched.
type Config struct {
	KeepServices []string `toml:"keep_services"`
	KeepRadiosOn []string `toml:"keep_radios_on"`

	ManageDesktop bool   `toml:"manage_desktop"`
	DesktopShell  string `toml:"desktop_shell"`

	ManageDisplay bool `toml:"manage_display"`
	Brightness    int  `toml:"brightness"`

	ManagePowerScheme bool   `toml:"manage_power_scheme"`
	PowerScheme       string `toml:"power_scheme"`

	ManageCPU     bool `toml:"manage_cpu"`
	CPUMaxPercent int  `toml:"cpu_max_percent"`

	ManageTurbo bool `toml:"manage_turbo"`
	Turbo       bool `toml:"turbo"`

	ManageDisplayTimeout bool `toml:"manage_display_timeout"`
	MonitorOffSeconds    int  `toml:"monitor_off_seconds"`

	ManageSleep bool `toml:"manage_sleep"`

	ManageLid bool   `toml:"manage_lid"`
	LidAction string `toml:"lid_action"`

	ManageRadios bool `toml:"manage_radios"`
	DisableDGPU  bool `toml:"disable_dgpu"`

	ManageServices bool `toml:"manage_services"`
	IsolateDesktop bool `toml:"isolate_desktop"`

	BatteryExitPercent int `toml:"battery_exit_percent"`

	Idle IdleConfig `toml:"idle"`

	// Restore is only used when off runs without a state file: exact values
	// are unknown, so these are the fallback desktop values.
	Restore RestoreConfig `toml:"restore"`
}

// IdleConfig controls the quiet-time power profile. It only runs in phone
// mode and only lowers the CPU ceiling; the bot stays reachable.
type IdleConfig struct {
	Enabled        bool   `toml:"enabled"`
	QuietMinutes   int    `toml:"quiet_minutes"`
	CPUMaxPercent  int    `toml:"cpu_max_percent"`
	ActivityLogDir string `toml:"activity_log_dir"`
}

func (c Config) activityLogDir() string {
	if c.Idle.ActivityLogDir != "" {
		return c.Idle.ActivityLogDir
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(base, "opencode-telegram-bot", "logs")
}

type RestoreConfig struct {
	Brightness      int    `toml:"brightness"`
	PowerScheme     string `toml:"power_scheme"` // empty leaves the scheme alone
	CPUMaxPercent   int    `toml:"cpu_max_percent"`
	Turbo           bool   `toml:"turbo"`
	LidAction       string `toml:"lid_action"`
	Bluetooth       bool   `toml:"bluetooth"`
	ServicesRunning bool   `toml:"services_running"`
	DesktopShell    bool   `toml:"desktop_shell"`
}

func DefaultConfig() Config {
	return Config{
		KeepServices: []string{"OpenCodeServe", "OpenCodeTelegram"},
		KeepRadiosOn: []string{"wlan"},

		ManageDesktop: true,
		DesktopShell:  "explorer.exe",

		ManageDisplay: true,
		Brightness:    0,

		ManagePowerScheme: true,
		// Stored as the alias, not the GUID, so the default config file stays
		// readable and matches config.example.toml. It is resolved at plan time.
		PowerScheme: "saver",

		ManageCPU:     true,
		CPUMaxPercent: 40,

		ManageTurbo: true,
		Turbo:       false,

		ManageDisplayTimeout: true,
		MonitorOffSeconds:    60,

		ManageSleep: true,

		ManageLid: true,
		LidAction: "ignore",

		ManageRadios:   true,
		DisableDGPU:    false,
		ManageServices: true,
		IsolateDesktop: true,

		BatteryExitPercent: 15,

		Idle: IdleConfig{
			Enabled:        true,
			QuietMinutes:   10,
			CPUMaxPercent:  20,
			ActivityLogDir: "",
		},

		Restore: RestoreConfig{
			Brightness:      100,
			PowerScheme:     "",
			CPUMaxPercent:   100,
			Turbo:           true,
			LidAction:       "sleep",
			Bluetooth:       true,
			ServicesRunning: true,
			DesktopShell:    true,
		},
	}
}

var lidActions = map[string]bool{
	"ignore":    true,
	"sleep":     true,
	"hibernate": true,
	"shutdown":  true,
}

func (c Config) Validate() error {
	if c.ManageDisplay {
		if c.Brightness < -1 || c.Brightness > 100 {
			return fmt.Errorf("brightness must be between -1 and 100, got %d", c.Brightness)
		}
	}
	if c.ManageDesktop && strings.TrimSpace(c.DesktopShell) == "" {
		return errors.New("desktop_shell must not be empty when manage_desktop is true")
	}
	if c.ManagePowerScheme && strings.TrimSpace(c.PowerScheme) == "" {
		return errors.New("power_scheme must not be empty when manage_power_scheme is true")
	}
	if c.ManageCPU && (c.CPUMaxPercent < 1 || c.CPUMaxPercent > 100) {
		return fmt.Errorf("cpu_max_percent must be between 1 and 100, got %d", c.CPUMaxPercent)
	}
	if c.ManageDisplayTimeout && c.MonitorOffSeconds < 0 {
		return fmt.Errorf("monitor_off_seconds must not be negative, got %d", c.MonitorOffSeconds)
	}
	if c.ManageLid && !lidActions[c.LidAction] {
		return fmt.Errorf("lid_action %q must be ignore, sleep, hibernate, or shutdown", c.LidAction)
	}
	if c.BatteryExitPercent < 0 || c.BatteryExitPercent > 100 {
		return fmt.Errorf("battery_exit_percent must be between 0 and 100, got %d", c.BatteryExitPercent)
	}
	if c.Idle.Enabled {
		if c.Idle.QuietMinutes < 1 {
			return fmt.Errorf("idle.quiet_minutes must be at least 1, got %d", c.Idle.QuietMinutes)
		}
		if c.Idle.CPUMaxPercent < 1 || c.Idle.CPUMaxPercent > 100 {
			return fmt.Errorf("idle.cpu_max_percent must be between 1 and 100, got %d", c.Idle.CPUMaxPercent)
		}
	}

	if c.Restore.Brightness < 0 || c.Restore.Brightness > 100 {
		return fmt.Errorf("restore.brightness must be between 0 and 100, got %d", c.Restore.Brightness)
	}
	if c.Restore.CPUMaxPercent < 1 || c.Restore.CPUMaxPercent > 100 {
		return fmt.Errorf("restore.cpu_max_percent must be between 1 and 100, got %d", c.Restore.CPUMaxPercent)
	}
	if c.Restore.LidAction != "" && !lidActions[c.Restore.LidAction] {
		return fmt.Errorf("restore.lid_action %q must be empty, ignore, sleep, hibernate, or shutdown", c.Restore.LidAction)
	}
	return nil
}

// LoadConfig starts from the defaults and overlays the file, so omitted keys
// keep their documented default. Unknown keys are rejected to catch typos.
func LoadConfig(path string) (Config, bool, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, true, fmt.Errorf("parse config %s: %w", path, err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		keys := make([]string, len(unknown))
		for i, key := range unknown {
			keys[i] = key.String()
		}
		return Config{}, true, fmt.Errorf("unknown config keys in %s: %s", path, strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, true, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, true, nil
}

func WriteDefaultConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(exampleConfig), 0o644)
}

//go:embed config.example.toml
var exampleConfig string

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// flagElevated marks a relaunched child so it does not try to elevate again.
// Without it a machine that refuses elevation would loop forever.
const flagElevated = "--elevated"

// flagLogFile tells an elevated child where to append its output. An elevated
// process gets a fresh console that closes as soon as it exits, so printing to
// stdout would show the user nothing at all.
const flagLogFile = "--log-file"

type app struct {
	host       host
	store      stateStore
	configPath string
	out        io.Writer
	errOut     io.Writer
	// outFile is set when output was redirected to the elevated run's log. It
	// is closed by closeOutput so the parent never reads a half-written file.
	outFile *os.File
	now     func() time.Time
}

// closeOutput releases the redirect, if any. Called from main before exit.
func (a *app) closeOutput() {
	if a.outFile != nil {
		_ = a.outFile.Close()
		a.outFile = nil
	}
}

func (a *app) run(args []string) int {
	dryRun := false
	relaunched := false
	logPath := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			dryRun = true
		case arg == flagElevated:
			relaunched = true
		case arg == flagLogFile:
			if i+1 >= len(args) {
				fmt.Fprintf(a.errOut, "error: %s needs a path\n", flagLogFile)
				return 2
			}
			// Only an elevated child should ever see this, and only for a file
			// this tool created. Accepting an arbitrary path would let an
			// admin-rights process append to any file on disk — a hostile or
			// mistyped argument could write into a system file.
			if !relaunched {
				fmt.Fprintf(a.errOut, "error: %s is internal and only valid with %s\n",
					flagLogFile, flagElevated)
				return 2
			}
			logPath = args[i+1]
			i++
		default:
			rest = append(rest, arg)
		}
	}

	// Only the elevated child takes this path; the parent reads the file
	// afterwards and prints it from the console the user started.
	if logPath != "" {
		if code := a.redirectOutput(logPath); code != 0 {
			return code
		}
	}
	// Nothing is applied without a dry run or elevation. Saying so up front is
	// the only chance to be heard: after the relaunch, this console is gone.
	if len(rest) > 0 && !dryRun && !relaunched {
		switch rest[0] {
		case "on":
			if cfg, _, err := LoadConfig(a.configPath); err == nil && cfg.IsolateDesktop {
				fmt.Fprintln(a.errOut, "this will stop the desktop shell; you will need RDP, SSH,")
				fmt.Fprintln(a.errOut, "or a scheduled task to run `off` afterwards")
			}
		}
	}
	if len(rest) == 0 {
		a.usage(a.errOut)
		return 2
	}
	command := rest[0]
	if len(rest) > 1 {
		arg := rest[1]
		if arg == "-h" || arg == "--help" {
			a.usage(a.out)
			return 0
		}
		fmt.Fprintf(a.errOut, "unknown argument %q\n", arg)
		a.usage(a.errOut)
		return 2
	}

	switch command {
	case "on", "off", "idle":
		// Dry runs change nothing, so they never need administrator rights.
		// Real runs do, and are relaunched once through UAC.
		if !dryRun && !a.host.Elevated() {
			if relaunched {
				fmt.Fprintln(a.errOut, "error: still not elevated after the UAC prompt")
				return 1
			}
			stdout, stderr, code, err := a.host.Relaunch(append([]string{flagElevated}, args...))
			if stdout != "" {
				fmt.Fprint(a.out, stdout)
			}
			if stderr != "" {
				fmt.Fprint(a.errOut, stderr)
			}
			if err != nil {
				fmt.Fprintf(a.errOut, "error: %v\n", err)
				return 1
			}
			return code
		}
	}

	switch command {
	case "on":
		return a.on(dryRun)
	case "off":
		return a.off(dryRun)
	case "status":
		return a.status()
	case "idle":
		return a.idle(dryRun)
	case "help", "-h", "--help":
		a.usage(a.out)
		return 0
	default:
		fmt.Fprintf(a.errOut, "unknown command %q\n", command)
		a.usage(a.errOut)
		return 2
	}
}

// redirectOutput sends this process's output to the file the parent named, so
// an elevated child's work survives the console that closes when it exits.
func (a *app) redirectOutput(path string) int {
	// Refuse anything that is not a regular file. Appending to a device or a
	// FIFO while elevated would block or misbehave, and there is no legitimate
	// reason for the handoff to point anywhere but a plain temp file.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		fmt.Fprintf(a.errOut, "error: log file %s is not a regular file\n", path)
		return 1
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: open log file: %v\n", err)
		return 1
	}
	// Written unbuffered: os.Exit in main() skips deferred flushes.
	a.out = f
	a.errOut = f
	a.outFile = f
	return 0
}

// requireAdmin re-checks elevation for code reached after run() has already
// decided about relaunching. run() is the only place that relaunches, because
// it is the only place that can print the elevated child's captured output.
func (a *app) requireAdmin(_ bool) error {
	if a.host.Elevated() {
		return nil
	}
	return errors.New("this needs administrator rights; re-run it elevated")
}

// noteSessionOrigin records whether this run had an interactive desktop. When
// it did, the shell must come back on the way out; when it did not (a service,
// a scheduled task, an SSH shell) restarting explorer would be unwanted.
func (a *app) noteSessionOrigin(st *State) {
	running, err := a.host.DesktopRunning("explorer.exe")
	if err != nil {
		// Unknown: leave it nil so off assumes the shell should come back.
		return
	}
	st.SessionHadDesktop = &running
}

func (a *app) on(dryRun bool) int {
	cfg, exists, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	if a.store.Exists() {
		fmt.Fprintf(a.errOut, "error: state already exists at %s\n", a.store.statePath())
		fmt.Fprintf(a.errOut, "phone mode already looks active; run off first, or remove that file if it is stale\n")
		return 1
	}

	if pct, status, ok := a.host.Battery(); ok && batteryBelowExit(cfg.BatteryExitPercent, pct, status) {
		msg := fmt.Sprintf("battery is %d%% (%s), below battery_exit_percent=%d", pct, status, cfg.BatteryExitPercent)
		if dryRun {
			fmt.Fprintf(a.out, "dry run: on would refuse: %s\n", msg)
			return 1
		}
		fmt.Fprintf(a.errOut, "refusing phone mode: %s\n", msg)
		return 1
	}

	if !exists {
		if dryRun {
			fmt.Fprintf(a.out, "would create default config at %s\n", a.configPath)
		} else if err := WriteDefaultConfig(a.configPath); err != nil {
			fmt.Fprintf(a.errOut, "warning: create default config: %v\n", err)
		} else {
			fmt.Fprintf(a.out, "created default config at %s\n", a.configPath)
		}
	}

	p, planErrs := buildOnPlan(cfg, a.host)
	p.state.SavedAt = a.now()
	a.noteSessionOrigin(&p.state)

	if dryRun {
		fmt.Fprintf(a.out, "dry run: state would be saved to %s\n", a.store.statePath())
	} else {
		if err := a.store.Save(p.state); err != nil {
			fmt.Fprintf(a.errOut, "error: save state: %v\n", err)
			return 1
		}
		if err := a.store.MarkActive(); err != nil {
			// Nothing has been applied yet. Remove the state file so a retry is
			// possible instead of being refused as "already active".
			_ = a.store.Remove()
			fmt.Fprintf(a.errOut, "error: mark phone mode active: %v\n", err)
			return 1
		}
		// A leftover idle profile belongs to the previous phone-mode session.
		if err := a.store.RemoveIdle(); err != nil {
			_ = a.store.ClearActive()
			_ = a.store.Remove()
			fmt.Fprintf(a.errOut, "error: clear stale idle state: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.out, "snapshot saved to %s\n", a.store.statePath())
	}

	fmt.Fprintln(a.out, "changes:")
	errs := append(planErrs, runSteps(p.steps, dryRun, a.out)...)
	a.report(errs)

	if dryRun {
		fmt.Fprintf(a.out, "dry run: %d changes, nothing applied\n", len(p.steps))
	} else if len(errs) == 0 {
		fmt.Fprintf(a.out, "phone mode on: %d changes applied\n", len(p.steps))
	} else {
		fmt.Fprintf(a.errOut, "phone mode partially applied: %d change(s) failed\n", len(errs))
	}
	if len(errs) > 0 {
		return 1
	}
	return 0
}

func (a *app) off(dryRun bool) int {
	cfg, _, cfgErr := LoadConfig(a.configPath)
	if cfgErr != nil {
		fmt.Fprintf(a.errOut, "warning: %v; using defaults\n", cfgErr)
		cfg = DefaultConfig()
	}

	st, err := a.store.Load()
	haveState := err == nil

	// A running shell means the desktop is already usable. Re-applying the
	// scheme then would only flicker a working session, so the restore falls
	// back to non-disruptive mode.
	desktopRunning := false
	if running, derr := a.host.DesktopRunning(cfg.DesktopShell); derr == nil {
		desktopRunning = running
	}
	disruptive := !desktopRunning
	if !disruptive {
		fmt.Fprintln(a.out, "desktop shell is running: keeping the current scheme")
	}

	var steps []step
	var planErrs []error
	// A state file that exists but cannot be read is NOT the same as no state
	// file. Falling back to the [restore] defaults would overwrite settings
	// with values the user never chose, and — worse — the file would then never
	// be removed, so `on` would stay refused forever. Refuse instead and leave
	// the decision to the user.
	if !haveState && a.store.Exists() {
		fmt.Fprintf(a.errOut, "error: state file at %s exists but cannot be read: %v\n",
			a.store.statePath(), err)
		fmt.Fprintf(a.errOut, "refusing to overwrite settings with defaults\n")
		fmt.Fprintf(a.errOut, "move that file aside to continue, then re-run off\n")
		return 1
	}
	if haveState {
		steps, planErrs = buildOffPlan(cfg, st, a.host, disruptive)
	} else {
		fmt.Fprintf(a.errOut, "warning: no state file at %s; restoring the config defaults\n", a.store.statePath())
		steps, planErrs = buildFallbackPlan(cfg, a.host, disruptive)
	}

	fmt.Fprintln(a.out, "restores:")
	errs := append(planErrs, runSteps(steps, dryRun, a.out)...)

	if dryRun {
		fmt.Fprintf(a.out, "dry run: %d restores, nothing applied\n", len(steps))
		if haveState {
			fmt.Fprintf(a.out, "dry run: %s would be removed\n", a.store.statePath())
		}
		if a.store.IdleExists() {
			fmt.Fprintf(a.out, "dry run: %s would be removed\n", a.store.idlePath())
		}
		return 0
	}

	if err := a.store.ClearActive(); err != nil {
		errs = append(errs, fmt.Errorf("clear active marker: %w", err))
	}
	if a.store.IdleExists() {
		if err := a.store.RemoveIdle(); err != nil {
			errs = append(errs, fmt.Errorf("remove idle state: %w", err))
		}
	}
	if haveState {
		if len(errs) == 0 {
			if err := a.store.Remove(); err != nil {
				errs = append(errs, fmt.Errorf("remove state file: %w", err))
			} else {
				fmt.Fprintf(a.out, "state file removed: %s\n", a.store.statePath())
			}
		} else {
			fmt.Fprintf(a.errOut, "state kept at %s so off can be retried\n", a.store.statePath())
		}
	}

	a.report(errs)
	if len(errs) == 0 {
		fmt.Fprintln(a.out, "desktop mode restored")
	}
	if len(errs) > 0 {
		return 1
	}
	return 0
}

func (a *app) status() int {
	cfg, exists, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}

	running, desktopErr := a.host.DesktopRunning(cfg.DesktopShell)
	// The marker alone is not proof: a reboot leaves it behind while Windows
	// restarts the shell. Report phone mode only when the shell is actually gone.
	mode := "desktop"
	if desktopErr != nil {
		if a.store.Active() {
			mode = "phone (headless)"
		}
	} else if !running {
		if a.store.Active() {
			mode = "phone (headless)"
		}
	}

	fmt.Fprintf(a.out, "mode: %s\n", mode)
	if exists {
		fmt.Fprintf(a.out, "config: %s\n", a.configPath)
	} else {
		fmt.Fprintf(a.out, "config: defaults (missing %s)\n", a.configPath)
	}

	st, stateErr := a.store.Load()
	switch {
	case stateErr == nil:
		fmt.Fprintf(a.out, "state: %s (saved %s)\n", a.store.statePath(), st.SavedAt.Format(time.RFC3339))
	case errors.Is(stateErr, errNoState):
		fmt.Fprintln(a.out, "state: none")
	default:
		fmt.Fprintf(a.out, "state: unreadable: %v\n", stateErr)
	}

	if desktopErr != nil {
		fmt.Fprintf(a.out, "desktop: unknown (%v)\n", desktopErr)
	} else {
		fmt.Fprintf(a.out, "desktop: %s %s\n", cfg.DesktopShell, runningLabel(running))
	}

	scheme, schemeErr := a.host.ActiveScheme()
	if schemeErr != nil {
		fmt.Fprintf(a.out, "power scheme: unknown (%v)\n", schemeErr)
	} else {
		fmt.Fprintf(a.out, "power scheme: %s (%s)\n", scheme, schemeName(scheme))
	}

	if schemeErr == nil {
		a.printPowerLine(scheme, settingCPUMax)
		a.printPowerLine(scheme, settingBoost)
		a.printPowerLine(scheme, settingDisplay)
		a.printPowerLine(scheme, settingLid)
	}

	fmt.Fprintf(a.out, "services: %s\n", a.serviceSummary(cfg))

	if pct, state, ok := a.host.Battery(); ok {
		if cfg.BatteryExitPercent > 0 {
			fmt.Fprintf(a.out, "battery: %d%% %s (exit threshold %d%%)\n", pct, state, cfg.BatteryExitPercent)
		} else {
			fmt.Fprintf(a.out, "battery: %d%% %s (exit threshold disabled)\n", pct, state)
		}
		if batteryBelowExit(cfg.BatteryExitPercent, pct, state) {
			fmt.Fprintf(a.out, "warning: below battery_exit_percent=%d; on would refuse\n", cfg.BatteryExitPercent)
		}
	} else {
		fmt.Fprintln(a.out, "battery: unknown")
	}

	if pct, supported, err := a.host.Brightness(); err != nil {
		fmt.Fprintf(a.out, "brightness: unknown (%v)\n", err)
	} else if !supported {
		fmt.Fprintln(a.out, "brightness: not supported on this panel")
	} else {
		fmt.Fprintf(a.out, "brightness: %d%%\n", pct)
	}

	if devices, err := a.host.Devices("Bluetooth"); err != nil {
		fmt.Fprintf(a.out, "bluetooth: unknown (%v)\n", err)
	} else if len(devices) == 0 {
		fmt.Fprintln(a.out, "bluetooth: no devices")
	} else {
		parts := make([]string, 0, len(devices))
		for _, d := range devices {
			parts = append(parts, shortName(d)+"="+blockedLabel(d.Disabled))
		}
		fmt.Fprintf(a.out, "bluetooth: %s\n", strings.Join(parts, " "))
	}

	if a.phoneModeActive() {
		fmt.Fprintln(a.out, "battery mode: on (agents asked to stay light)")
	} else {
		fmt.Fprintln(a.out, "battery mode: off")
	}

	switch {
	case !cfg.Idle.Enabled:
		fmt.Fprintln(a.out, "idle profile: disabled")
	case a.store.IdleExists() && a.phoneModeActive():
		fmt.Fprintln(a.out, "idle profile: applied (CPU capped, bot still online)")
	case a.store.IdleExists():
		// Left over from before a reboot. Calling this "applied" would claim a
		// CPU cap that is no longer in force.
		fmt.Fprintln(a.out, "idle profile: stale (left from a previous phone-mode session; run off to clear)")
	default:
		fmt.Fprintf(a.out, "idle profile: waiting for %d quiet minutes\n", cfg.Idle.QuietMinutes)
	}
	return 0
}

// printPowerLine shows one power setting as a single readable line.
func (a *app) printPowerLine(scheme string, ps powerSetting) {
	ac, dc, err := a.host.QueryPower(scheme, ps.subgroup, ps.setting)
	if err != nil {
		fmt.Fprintf(a.out, "%-14s unknown (%v)\n", ps.label+":", err)
		return
	}
	fmt.Fprintf(a.out, "%-14s %s\n", ps.label+":", powerLabel(ps.label, ac, dc))
}

// phoneModeActive requires both the marker and a stopped desktop shell. The
// marker alone is accepted when the shell state cannot be read.
func (a *app) phoneModeActive() bool {
	if !a.store.Active() {
		return false
	}
	cfg, _, err := LoadConfig(a.configPath)
	if err != nil {
		return true
	}
	running, err := a.host.DesktopRunning(cfg.DesktopShell)
	if err != nil {
		return true
	}
	return !running
}

func (a *app) serviceSummary(cfg Config) string {
	if len(cfg.KeepServices) == 0 {
		return "none configured"
	}
	parts := make([]string, 0, len(cfg.KeepServices))
	for _, name := range cfg.KeepServices {
		running, start, missing, err := a.host.ServiceState(name)
		if err != nil {
			parts = append(parts, name+"=unknown")
			continue
		}
		if missing {
			parts = append(parts, name+"=not installed")
			continue
		}
		parts = append(parts, name+"="+runningLabel(running)+" ("+start+")")
	}
	return strings.Join(parts, " ")
}

func (a *app) report(errs []error) {
	if len(errs) == 0 {
		return
	}
	fmt.Fprintf(a.errOut, "\n%d step(s) failed:\n", len(errs))
	for _, err := range errs {
		fmt.Fprintf(a.errOut, "  - %v\n", err)
	}
}

func (a *app) usage(w io.Writer) {
	fmt.Fprintf(w, `Usage: win-phone-mode <on|off|status|idle> [--dry-run]

  on      snapshot settings, then switch to headless phone mode
  off     restore the snapshot, then bring the desktop shell back
  status  show current mode, settings, and battery (no admin needed)
  idle    apply or restore the quiet-time CPU profile in phone mode

  --dry-run   print every change with current and new values; apply nothing

on, off, and idle need administrator rights and will show a UAC prompt.
status and every --dry-run run without elevation.

config: %s
state:  %s
`, a.configPath, a.store.statePath())
}

func newApp() (*app, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &app{
		host:       newRealHost(),
		store:      stateStore{dir: filepath.Join(cacheDir, "win-phone-mode")},
		configPath: filepath.Join(configDir, "win-phone-mode", "config.toml"),
		out:        os.Stdout,
		errOut:     os.Stderr,
		now:        time.Now,
	}, nil
}

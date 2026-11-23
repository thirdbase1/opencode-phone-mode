package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type step struct {
	setting string
	current string
	next    string
	apply   func() error
}

type plan struct {
	state State
	steps []step
}

// powerSetting describes one powercfg coordinate plus how to label it.
type powerSetting struct {
	subgroup string
	setting  string
	label    string
}

var (
	settingCPUMax    = powerSetting{subProcessor, setProcMax, "max processor state"}
	settingBoost     = powerSetting{subProcessor, setBoostMode, "boost mode"}
	settingDisplay   = powerSetting{subVideo, setVideoIdle, "display off"}
	settingStandby   = powerSetting{subSleep, setStandbyIdle, "sleep"}
	settingHibernate = powerSetting{subSleep, setHibernateIdle, "hibernate"}
	settingLid       = powerSetting{subButtons, setLidAction, "lid action"}
)

// snapshotPower reads the current AC/DC pair for a setting in a scheme.
func snapshotPower(h host, scheme string, ps powerSetting) (PowerValue, error) {
	ac, dc, err := h.QueryPower(scheme, ps.subgroup, ps.setting)
	if err != nil {
		return PowerValue{}, err
	}
	return PowerValue{
		Scheme:   scheme,
		Subgroup: ps.subgroup,
		Setting:  ps.setting,
		AC:       ac,
		DC:       dc,
		Label:    ps.label,
	}, nil
}

// powerStep builds a step that writes both indices, then re-activates the
// scheme so the change takes effect immediately without a reboot.
func powerStep(h host, ps powerSetting, saved PowerValue, ac, dc, label string) step {
	scheme := saved.Scheme
	return step{
		setting: ps.label,
		current: powerLabel(saved.Label, saved.AC, saved.DC),
		next:    label,
		apply: func() error {
			if err := h.SetPower(scheme, ps.subgroup, ps.setting, ac, dc); err != nil {
				return err
			}
			return h.ApplyScheme(scheme)
		},
	}
}

// buildOnPlan reads every setting first, then returns the changes to apply.
// A read failure skips that setting instead of guessing a restore value.
func buildOnPlan(cfg Config, h host) (plan, []error) {
	p := plan{state: State{Version: stateVersion, SavedAt: time.Now()}}
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	// The scheme we write into, and the one we must be able to come back to.
	//
	// If the active scheme cannot be read, no power setting is touched at all.
	// powerStep activates the scheme it writes into, so proceeding here would
	// switch the user onto the phone-mode scheme while recording nothing to
	// restore, and off could never return them. Failing safely beats that.
	active, activeErr := h.ActiveScheme()
	target := active
	if activeErr != nil {
		note(fmt.Errorf("read active scheme: %w", activeErr))
	}
	powerOK := activeErr == nil
	if powerOK && cfg.ManagePowerScheme {
		resolved, err := schemeAlias(cfg.PowerScheme)
		if err != nil {
			note(err)
			powerOK = false
		} else {
			target = resolved
			p.state.ActiveScheme = active
			if active != resolved {
				p.steps = append(p.steps, step{
					setting: "power scheme",
					current: active,
					next:    resolved,
					apply:   func() error { return h.ApplyScheme(resolved) },
				})
			}
		}
	}
	if powerOK && p.state.ActiveScheme == "" {
		p.state.ActiveScheme = active
	}

	// --- desktop shell -------------------------------------------------------
	//
	// isolate_desktop is the switch that decides whether the shell is stopped
	// at all. Stopping explorer is the one genuinely disruptive step, so it has
	// its own control separate from the snapshot/restore machinery.
	if cfg.ManageDesktop && cfg.IsolateDesktop {
		running, err := h.DesktopRunning(cfg.DesktopShell)
		if err != nil {
			note(fmt.Errorf("read desktop shell: %w", err))
		} else {
			p.state.Desktop = &ServiceState{
				Name:    cfg.DesktopShell,
				Running: running,
			}
			if running {
				// The shell is stopped last so the rest of the switch runs in
				// a session that still has a UI.
				p.steps = append(p.steps, step{
					setting: "desktop " + cfg.DesktopShell,
					current: "running",
					next:    "stopped",
					apply:   func() error { return h.SetDesktop(cfg.DesktopShell, "stopped") },
				})
			}
		}
	}

	// --- services that must survive -----------------------------------------
	if cfg.ManageServices {
		for _, name := range cfg.KeepServices {
			running, start, missing, err := h.ServiceState(name)
			if err != nil {
				note(fmt.Errorf("snapshot service %s: %w", name, err))
				continue
			}
			if missing {
				note(fmt.Errorf("service %s is not installed", name))
				continue
			}
			p.state.Services = append(p.state.Services, ServiceState{
				Name:    name,
				Running: running,
				Start:   start,
			})
			// A disabled service cannot start, so the start mode is repaired
			// first. Both changes are separate steps because either can fail
			// independently and both must be restored on the way back.
			if start == "disabled" {
				serviceName := name
				p.steps = append(p.steps, step{
					setting: "enable " + serviceName,
					current: start,
					next:    "auto",
					apply:   func() error { return h.SetServiceStart(serviceName, "auto") },
				})
			}
			if !running {
				serviceName := name
				p.steps = append(p.steps, step{
					setting: "service " + serviceName,
					current: serviceLabel(running, start),
					next:    "running",
					apply:   func() error { return h.SetService(serviceName, "running") },
				})
			}
		}
	}

	// --- power settings in the target scheme --------------------------------
	if cfg.ManageCPU && powerOK {
		if saved, err := snapshotPower(h, target, settingCPUMax); err != nil {
			note(fmt.Errorf("snapshot %s: %w", settingCPUMax.label, err))
		} else {
			p.state.Power = append(p.state.Power, saved)
			want := capPercent(cfg.CPUMaxPercent)
			// A powercfg write re-activates the scheme, so a step that changes
			// nothing is not free: it briefly re-applies the whole scheme. Skip
			// it when the value already matches.
			if powerLabel(settingCPUMax.label, saved.AC, saved.DC) != want+"%" {
				p.steps = append(p.steps, powerStep(h, settingCPUMax, saved, want, want,
					want+"%"))
			}
		}
	}

	if cfg.ManageTurbo && powerOK {
		if saved, err := snapshotPower(h, target, settingBoost); err != nil {
			note(fmt.Errorf("snapshot %s: %w", settingBoost.label, err))
		} else {
			p.state.Power = append(p.state.Power, saved)
			want := turboIndex(cfg.Turbo)
			if powerLabel(settingBoost.label, saved.AC, saved.DC) != onOff(turboEnabled(want)) {
				p.steps = append(p.steps, powerStep(h, settingBoost, saved, want, want,
					onOff(turboEnabled(want))))
			}
		}
	}

	if cfg.ManageDisplayTimeout && powerOK {
		if saved, err := snapshotPower(h, target, settingDisplay); err != nil {
			note(fmt.Errorf("snapshot %s: %w", settingDisplay.label, err))
		} else {
			p.state.Power = append(p.state.Power, saved)
			want := strconv.Itoa(cfg.MonitorOffSeconds)
			if powerLabel(settingDisplay.label, saved.AC, saved.DC) != secondsLabel(cfg.MonitorOffSeconds) {
				p.steps = append(p.steps, powerStep(h, settingDisplay, saved, want, want,
					secondsLabel(cfg.MonitorOffSeconds)))
			}
		}
	}

	if cfg.ManageSleep && powerOK {
		// Never sleep while headless: a sleeping box cannot answer messages.
		for _, ps := range []powerSetting{settingStandby, settingHibernate} {
			saved, err := snapshotPower(h, target, ps)
			if err != nil {
				note(fmt.Errorf("snapshot %s: %w", ps.label, err))
				continue
			}
			p.state.Power = append(p.state.Power, saved)
			if powerLabel(ps.label, saved.AC, saved.DC) != "never" {
				p.steps = append(p.steps, powerStep(h, ps, saved, "0", "0", "never"))
			}
		}
	}

	if cfg.ManageLid && powerOK {
		want, ok := lidValue(cfg.LidAction)
		if !ok {
			note(fmt.Errorf("unknown lid_action %q", cfg.LidAction))
		} else if saved, err := snapshotPower(h, target, settingLid); err != nil {
			note(fmt.Errorf("snapshot %s: %w", settingLid.label, err))
		} else {
			p.state.Power = append(p.state.Power, saved)
			if powerLabel(settingLid.label, saved.AC, saved.DC) != cfg.LidAction {
				p.steps = append(p.steps, powerStep(h, settingLid, saved, want, want,
					cfg.LidAction))
			}
		}
	}

	// --- brightness ---------------------------------------------------------
	if cfg.ManageDisplay && cfg.Brightness >= 0 {
		pct, supported, err := h.Brightness()
		if err != nil {
			note(fmt.Errorf("snapshot brightness: %w", err))
		} else if !supported {
			// Desktops and some panels have no brightness control; that is a
			// normal condition, not a failure.
		} else {
			saved := pct
			p.state.Brightness = &saved
			want := cfg.Brightness
			p.steps = append(p.steps, step{
				setting: "brightness",
				current: fmt.Sprintf("%d%%", saved),
				next:    fmt.Sprintf("%d%%", want),
				apply:   func() error { return h.SetBrightness(want) },
			})
		}
	}

	// --- radios -------------------------------------------------------------
	if cfg.ManageRadios {
		devices, err := h.Devices("Bluetooth")
		if err != nil {
			note(fmt.Errorf("snapshot bluetooth: %w", err))
		}
		keepBluetooth := radioKept(cfg, "bluetooth")
		for _, d := range devices {
			p.state.Radios = append(p.state.Radios, RadioState{
				InstanceID:  d.InstanceID,
				Description: d.Description,
				Type:        "bluetooth",
				Disabled:    d.Disabled,
			})
			if keepBluetooth || d.Disabled {
				continue
			}
			id := d.InstanceID
			p.steps = append(p.steps, step{
				setting: "bluetooth " + shortName(d),
				current: blockedLabel(d.Disabled),
				next:    "disabled",
				apply:   func() error { return h.SetDevice(id, "disabled") },
			})
		}
	}

	// --- discrete GPU -------------------------------------------------------
	if cfg.DisableDGPU {
		devices, err := h.Devices("Display")
		if err != nil {
			note(fmt.Errorf("snapshot display adapters: %w", err))
		}
		for _, d := range devices {
			p.state.Radios = append(p.state.Radios, RadioState{
				InstanceID:  d.InstanceID,
				Description: d.Description,
				Type:        "display",
				Disabled:    d.Disabled,
			})
			if d.Disabled {
				continue
			}
			id := d.InstanceID
			p.steps = append(p.steps, step{
				setting: "display " + shortName(d),
				current: blockedLabel(d.Disabled),
				next:    "disabled",
				apply:   func() error { return h.SetDevice(id, "disabled") },
			})
		}
	}

	return p, errs
}

// buildOffPlan turns a saved snapshot back into changes. Values are written
// verbatim, including devices that disappeared; those failures are reported.
// disruptive is false when a desktop shell is already running, in which case
// re-applying the scheme would only flicker a working session.
func buildOffPlan(cfg Config, st State, h host, disruptive bool) ([]step, []error) {
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	for _, value := range st.Power {
		v := value
		ps := powerSetting{subgroup: v.Subgroup, setting: v.Setting, label: v.Label}
		if ps.label == "" {
			ps.label = "power " + v.Setting
		}
		currentAC, currentDC, err := h.QueryPower(v.Scheme, v.Subgroup, v.Setting)
		if err != nil {
			note(fmt.Errorf("read %s: %w", ps.label, err))
		}
		steps = append(steps, step{
			setting: ps.label,
			current: powerLabel(ps.label, currentAC, currentDC),
			next:    powerLabel(ps.label, v.AC, v.DC),
			// Indices are written into the scheme they were read from. The
			// original scheme is activated once at the end of the restore, so a
			// mid-list activation cannot strand the user on the phone-mode
			// scheme if a later step fails.
			apply: func() error {
				return h.SetPower(v.Scheme, v.Subgroup, v.Setting, v.AC, v.DC)
			},
		})
	}

	if st.Brightness != nil {
		saved := *st.Brightness
		pct, supported, err := h.Brightness()
		switch {
		case err != nil:
			// The panel may be gone (docked, external display, driver update).
			// Report it, but do not emit a step that can never succeed: that
			// would keep the state file forever and block `on`.
			note(fmt.Errorf("read brightness: %w", err))
		case !supported:
			note(errors.New("brightness is no longer supported on this panel; left as is"))
		case pct == saved:
			// Already correct.
		default:
			steps = append(steps, step{
				setting: "brightness",
				current: fmt.Sprintf("%d%%", pct),
				next:    fmt.Sprintf("%d%%", saved),
				apply:   func() error { return h.SetBrightness(saved) },
			})
		}
	}

	// Devices are re-enabled before the shell comes back so a monitor attached
	// to a disabled adapter is live by the time explorer starts.
	//
	// A device that is already in the recorded state is skipped. Disable- and
	// Enable-PnpDevice are not no-ops: Windows reports an error when a device is
	// already in the requested state, and that failure would keep the state file
	// forever and permanently block `on`. A device that has since disappeared is
	// likewise not an error — there is nothing to restore and nothing to do.
	for _, r := range st.Radios {
		radio := r
		live := radioDevice(h, radio)
		want := "enabled"
		if radio.Disabled {
			want = "disabled"
		}
		if live == nil {
			continue
		}
		if (!live.Disabled && want == "enabled") || (live.Disabled && want == "disabled") {
			continue
		}
		steps = append(steps, step{
			setting: radio.Type + " " + radio.Description,
			current: blockedLabel(live.Disabled),
			next:    want,
			apply:   func() error { return h.SetDevice(radio.InstanceID, want) },
		})
	}

	// Re-enable services before the shell so the bot is reachable again even if
	// starting explorer fails.
	for _, svc := range st.Services {
		service := svc
		running, start, missing, err := h.ServiceState(service.Name)
		if err != nil {
			note(fmt.Errorf("read service %s: %w", service.Name, err))
		}
		if missing {
			note(fmt.Errorf("service %s is no longer installed", service.Name))
			continue
		}
		want := "stopped"
		if service.Running {
			want = "running"
		}
		// Restoring a disabled mode has to happen after the service is stopped,
		// or Windows refuses to disable a running service.
		if service.Start != "" && start != service.Start {
			if service.Start == "disabled" && running && want == "stopped" {
				steps = append(steps, step{
					setting: "stop " + service.Name,
					current: serviceLabel(running, start),
					next:    "stopped",
					apply:   func() error { return h.SetService(service.Name, "stopped") },
				})
			}
			steps = append(steps, step{
				setting: "start mode " + service.Name,
				current: start,
				next:    service.Start,
				apply:   func() error { return h.SetServiceStart(service.Name, service.Start) },
			})
		}
		steps = append(steps, step{
			setting: "service " + service.Name,
			current: serviceLabel(running, start),
			next:    want,
			apply:   func() error { return h.SetService(service.Name, want) },
		})
	}

	// The shell is only restarted when this session actually had one. Entering
	// phone mode from a service, a scheduled task, or an SSH shell must not
	// spawn an explorer.exe into a session that never had a desktop.
	//
	// SessionHadDesktop is checked as well as Desktop.Running because the
	// recorded run state alone cannot tell "the user was at the desktop" from
	// "the user was at the desktop and phone mode stopped it".
	// nil means the state file predates the field; restore the shell then, since
	// leaving someone with no desktop is the worse mistake.
	hadDesktop := st.SessionHadDesktop == nil || *st.SessionHadDesktop
	if st.Desktop != nil && st.Desktop.Running && hadDesktop {
		exe := st.Desktop.Name
		running, _ := h.DesktopRunning(exe)
		if !running {
			steps = append(steps, step{
				setting: "desktop " + exe,
				current: runningLabel(running),
				next:    "running",
				apply:   func() error { return h.SetDesktop(exe, "running") },
			})
		}
	}

	// Last, so the user lands on their own scheme only after the values have
	// been restored into it.
	//
	// This is deliberately NOT gated on the shell being stopped. It used to be,
	// and that made off unrecoverable: a reboot in phone mode leaves explorer
	// running, so the scheme step was dropped, no step failed, and off then
	// deleted the only record that the scheme had ever been changed. Activating
	// a scheme is not disruptive, so there is nothing to protect against.
	// Always emitted when a scheme was recorded, even if it is already active:
	// the restored indices only take effect once the scheme is activated, so
	// skipping this would leave the phone-mode values in force while reporting
	// success.
	if st.ActiveScheme != "" {
		scheme := st.ActiveScheme
		current, _ := h.ActiveScheme()
		steps = append(steps, step{
			setting: "power scheme",
			current: current,
			next:    scheme + " (" + schemeName(scheme) + ")",
			apply:   func() error { return h.ApplyScheme(scheme) },
		})
	}

	return steps, errs
}

// buildFallbackPlan is used when state.json is missing: exact values are
// unknowable, so the [restore] defaults are applied. With disruptive false it
// leaves the scheme and shell alone, so it cannot break a live session.
func buildFallbackPlan(cfg Config, h host, disruptive bool) ([]step, []error) {
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }
	restore := cfg.Restore

	scheme, err := h.ActiveScheme()
	if err != nil {
		note(fmt.Errorf("read active scheme: %w", err))
		return steps, errs
	}

	if restore.PowerScheme != "" && disruptive {
		want, err := schemeAlias(restore.PowerScheme)
		if err != nil {
			note(err)
		} else {
			steps = append(steps, step{
				setting: "power scheme",
				current: scheme,
				next:    want,
				apply:   func() error { return h.ApplyScheme(want) },
			})
			scheme = want
		}
	}

	if restore.CPUMaxPercent > 0 {
		if saved, err := snapshotPower(h, scheme, settingCPUMax); err != nil {
			note(fmt.Errorf("read %s: %w", settingCPUMax.label, err))
		} else {
			want := capPercent(restore.CPUMaxPercent)
			steps = append(steps, powerStep(h, settingCPUMax, saved, want, want, want+"%"))
		}
	}

	if saved, err := snapshotPower(h, scheme, settingBoost); err != nil {
		note(fmt.Errorf("read %s: %w", settingBoost.label, err))
	} else {
		want := turboIndex(restore.Turbo)
		steps = append(steps, powerStep(h, settingBoost, saved, want, want,
			onOff(turboEnabled(want))))
	}

	if saved, err := snapshotPower(h, scheme, settingStandby); err != nil {
		note(fmt.Errorf("read %s: %w", settingStandby.label, err))
	} else {
		// Fallback cannot know the old timeout, so it restores the Windows
		// default of 15 minutes rather than claiming to know the user's value.
		const defaultStandbySeconds = "900"
		steps = append(steps, powerStep(h, settingStandby, saved,
			defaultStandbySeconds, defaultStandbySeconds, "15m0s (default)"))
	}

	if restore.LidAction != "" {
		if want, ok := lidValue(restore.LidAction); ok {
			if saved, err := snapshotPower(h, scheme, settingLid); err != nil {
				note(fmt.Errorf("read %s: %w", settingLid.label, err))
			} else {
				steps = append(steps, powerStep(h, settingLid, saved, want, want,
					restore.LidAction))
			}
		}
	}

	if pct, supported, err := h.Brightness(); err != nil {
		note(fmt.Errorf("read brightness: %w", err))
	} else if supported {
		want := restore.Brightness
		if want > pct {
			steps = append(steps, step{
				setting: "brightness",
				current: fmt.Sprintf("%d%%", pct),
				next:    fmt.Sprintf("%d%%", want),
				apply:   func() error { return h.SetBrightness(want) },
			})
		}
	}

	if devices, err := h.Devices("Bluetooth"); err != nil {
		note(fmt.Errorf("read bluetooth: %w", err))
	} else {
		for _, d := range devices {
			if !d.Disabled {
				continue
			}
			id := d.InstanceID
			want := "enabled"
			if !restore.Bluetooth {
				continue
			}
			steps = append(steps, step{
				setting: "bluetooth " + shortName(d),
				current: blockedLabel(d.Disabled),
				next:    want,
				apply:   func() error { return h.SetDevice(id, want) },
			})
		}
	}

	if restore.ServicesRunning {
		for _, name := range cfg.KeepServices {
			serviceName := name
			running, start, missing, err := h.ServiceState(serviceName)
			if err != nil {
				note(fmt.Errorf("read service %s: %w", serviceName, err))
				continue
			}
			if missing {
				continue
			}
			if start == "disabled" {
				steps = append(steps, step{
					setting: "enable " + serviceName,
					current: start,
					next:    "auto",
					apply:   func() error { return h.SetServiceStart(serviceName, "auto") },
				})
			}
			if running {
				continue
			}
			steps = append(steps, step{
				setting: "service " + serviceName,
				current: serviceLabel(running, start),
				next:    "running",
				apply:   func() error { return h.SetService(serviceName, "running") },
			})
		}
	}

	if restore.DesktopShell && disruptive {
		running, err := h.DesktopRunning(cfg.DesktopShell)
		if err != nil {
			note(fmt.Errorf("read desktop shell: %w", err))
		} else if !running {
			exe := cfg.DesktopShell
			steps = append(steps, step{
				setting: "desktop " + exe,
				current: "stopped",
				next:    "running",
				apply:   func() error { return h.SetDesktop(exe, "running") },
			})
		}
	}

	return steps, errs
}

func runSteps(steps []step, dryRun bool, out io.Writer) []error {
	var errs []error
	for _, s := range steps {
		fmt.Fprintf(out, "  %-26s %s -> %s\n", s.setting, s.current, s.next)
		if dryRun {
			continue
		}
		if err := s.apply(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.setting, err))
		}
	}
	return errs
}

// radioKept reports whether a radio type is in keep_radios_on.
func radioKept(cfg Config, typ string) bool {
	for _, kept := range cfg.KeepRadiosOn {
		kept = strings.ToLower(strings.TrimSpace(kept))
		if kept == "wifi" {
			kept = "wlan"
		}
		if kept == typ {
			return true
		}
	}
	return false
}

// radioDevice finds the live state for a saved device. Instance IDs are stable
// across reboots on Windows, so a direct match is enough; a missing device
// returns nil and the restore step reports "unknown".
func radioDevice(h host, saved RadioState) *device {
	class := "Bluetooth"
	if saved.Type == "display" {
		class = "Display"
	}
	devices, err := h.Devices(class)
	if err != nil {
		return nil
	}
	for i := range devices {
		if devices[i].InstanceID == saved.InstanceID {
			return &devices[i]
		}
	}
	return nil
}

func shortName(d device) string {
	if d.Description != "" {
		return d.Description
	}
	if len(d.InstanceID) > 24 {
		return d.InstanceID[:24]
	}
	return d.InstanceID
}

// powerLabel renders a setting value so a dry run reads like a sentence.
func powerLabel(label, ac, dc string) string {
	format := func(value string) string {
		if label == settingDisplay.label {
			return secondsLabelFromString(value)
		}
		if label == settingStandby.label || label == settingHibernate.label {
			return secondsLabelFromString(value)
		}
		if label == settingLid.label {
			return lidActionName(value)
		}
		if label == settingBoost.label {
			return onOff(turboEnabled(value))
		}
		if label == settingCPUMax.label {
			return value + "%"
		}
		return value
	}
	if ac == dc {
		return format(ac)
	}
	return format(ac) + " ac / " + format(dc) + " dc"
}

func secondsLabel(seconds int) string {
	if seconds <= 0 {
		return "never"
	}
	return (time.Duration(seconds) * time.Second).String()
}

func secondsLabelFromString(value string) string {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return secondsLabel(n)
}

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func onConfig() Config {
	cfg := DefaultConfig()
	cfg.ManageServices = true
	cfg.ManageRadios = true
	return cfg
}

func TestOnPlanSnapshotsEveryManagedSetting(t *testing.T) {
	h := newFakeHost()
	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("unexpected plan errors: %v", errs)
	}

	// Each power setting that the plan will change must be in the snapshot,
	// otherwise off cannot restore it.
	for _, want := range []string{setProcMax, setBoostMode, setVideoIdle, setStandbyIdle, setHibernateIdle, setLidAction} {
		found := false
		for _, v := range p.state.Power {
			if v.Setting == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("setting %s was changed but not snapshotted", want)
		}
	}

	if p.state.ActiveScheme != schemeBalanced {
		t.Fatalf("active scheme not recorded: %q", p.state.ActiveScheme)
	}
	if p.state.Brightness == nil || *p.state.Brightness != 80 {
		t.Fatalf("brightness not snapshotted: %+v", p.state.Brightness)
	}
	if p.state.Desktop == nil || !p.state.Desktop.Running {
		t.Fatalf("desktop state not snapshotted: %+v", p.state.Desktop)
	}
	if len(p.state.Services) != 2 {
		t.Fatalf("got %d services, want 2", len(p.state.Services))
	}
	if len(p.state.Radios) != 2 {
		t.Fatalf("got %d bluetooth devices, want 2", len(p.state.Radios))
	}
}

// The most important invariant: every snapshotted value, when written back,
// must reproduce exactly what was read. A per-setting snapshot that only
// recorded AC would silently lose the DC value.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	h := newFakeHost()
	// Give AC and DC genuinely different values so a round trip that copied
	// one into the other would be caught.
	h.dcIdx[subVideo+"|"+setVideoIdle] = "180"
	h.dcIdx[subProcessor+"|"+setProcMax] = "75"
	h.dcIdx[subButtons+"|"+setLidAction] = "0"

	before := map[string][2]string{}
	for _, ps := range []powerSetting{settingCPUMax, settingBoost, settingDisplay, settingStandby, settingHibernate, settingLid} {
		ac, dc, err := h.QueryPower(h.scheme, ps.subgroup, ps.setting)
		if err != nil {
			t.Fatalf("baseline read %s: %v", ps.label, err)
		}
		before[ps.setting] = [2]string{ac, dc}
	}

	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("applying on-plan failed: %v", errs)
	}

	// Now restore and compare against the baseline.
	offSteps, errs := buildOffPlan(DefaultConfig(), p.state, h, true)
	if len(errs) != 0 {
		t.Fatalf("off plan errors: %v", errs)
	}
	if errs := runSteps(offSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("applying off-plan failed: %v", errs)
	}

	for setting, want := range before {
		subgroup := subgroupFor(setting)
		ac, dc, err := h.QueryPower(h.scheme, subgroup, setting)
		if err != nil {
			t.Fatalf("read back %s: %v", setting, err)
		}
		if ac != want[0] || dc != want[1] {
			t.Fatalf("%s restored to ac=%s dc=%s, want ac=%s dc=%s",
				setting, ac, dc, want[0], want[1])
		}
	}
}

// subgroupFor mirrors the constant table for test readability.
func subgroupFor(setting string) string {
	switch setting {
	case setProcMax, setBoostMode:
		return subProcessor
	case setVideoIdle:
		return subVideo
	case setStandbyIdle, setHibernateIdle:
		return subSleep
	case setLidAction:
		return subButtons
	}
	return ""
}

func TestOnPlanAppliesConfiguredValues(t *testing.T) {
	h := newFakeHost()
	cfg := onConfig()
	p, errs := buildOnPlan(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}

	if !h.powerSet(subProcessor, setProcMax, "40") {
		t.Fatalf("CPU cap not applied at 40%%, calls: %v", h.setCalls)
	}
	if !h.powerSet(subProcessor, setBoostMode, "0") {
		t.Fatalf("turbo not disabled, calls: %v", h.setCalls)
	}
	if !h.powerSet(subSleep, setStandbyIdle, "0") {
		t.Fatalf("sleep not disabled, calls: %v", h.setCalls)
	}
	if !h.powerSet(subButtons, setLidAction, "0") {
		t.Fatalf("lid action not set to ignore, calls: %v", h.setCalls)
	}
	if h.bright != 0 {
		t.Fatalf("brightness = %d, want 0", h.bright)
	}
	if h.scheme != schemePowerSaver {
		t.Fatalf("scheme = %s, want power saver", h.scheme)
	}
	if h.desktop["explorer.exe"] {
		t.Fatal("desktop shell should be stopped")
	}
	if !h.services["OpenCodeTelegram"].running {
		t.Fatal("keep-service should be started")
	}
	for _, d := range h.devices["Bluetooth"] {
		if !d.Disabled {
			t.Fatalf("bluetooth device %s should be disabled", d.Description)
		}
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	h := newFakeHost()
	cfg := onConfig()
	p, _ := buildOnPlan(cfg, h)
	if errs := runSteps(p.steps, true, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("dry run reported errors: %v", errs)
	}
	if len(h.setCalls) != 0 {
		t.Fatalf("dry run wrote power settings: %v", h.setCalls)
	}
	if len(h.writes) != 0 {
		t.Fatalf("dry run wrote settings: %v", h.writes)
	}
	if h.scheme != schemeBalanced {
		t.Fatalf("dry run changed the scheme to %s", h.scheme)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("dry run stopped the desktop")
	}
}

// A device or setting that cannot be read must be skipped, not guessed. A
// guessed restore value would overwrite the user's real setting with a wrong
// one and still report success.
func TestUnreadableSettingIsSkippedNotGuessed(t *testing.T) {
	h := newFakeHost()
	h.queryErr[subProcessor+"|"+setBoostMode] = errBoom

	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) == 0 {
		t.Fatal("a failed read should be reported")
	}
	for _, v := range p.state.Power {
		if v.Setting == setBoostMode {
			t.Fatal("unreadable setting must not be snapshotted")
		}
	}
	for _, s := range p.steps {
		if s.setting == settingBoost.label {
			t.Fatal("unreadable setting must not be applied")
		}
	}
	// Other settings must still be planned: one failure cannot abort the rest.
	if len(p.steps) == 0 {
		t.Fatal("one failed read aborted the whole plan")
	}
}

func TestMissingServiceIsReportedNotFatal(t *testing.T) {
	h := newFakeHost()
	h.services["OpenCodeTelegram"].missing = true
	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) == 0 {
		t.Fatal("missing service should be reported")
	}
	if len(p.steps) == 0 {
		t.Fatal("missing service aborted the whole plan")
	}
	for _, s := range p.state.Services {
		if s.Name == "OpenCodeTelegram" {
			t.Fatal("a missing service must not be snapshotted")
		}
	}
}

func TestOffIsNonDisruptiveWhenDesktopIsUp(t *testing.T) {
	h := newFakeHost()
	p, _ := buildOnPlan(onConfig(), h)
	runSteps(p.steps, false, &bytes.Buffer{})

	// Pretend the user already started a shell again. Only the restore's own
	// calls matter from here, so the apply log is cleared.
	h.desktop["explorer.exe"] = true
	h.runs = nil
	h.writes = nil
	offSteps, errs := buildOffPlan(DefaultConfig(), p.state, h, false)
	if len(errs) != 0 {
		t.Fatalf("off plan errors: %v", errs)
	}
	if errs := runSteps(offSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("off apply failed: %v", errs)
	}

	// The recorded scheme must ALWAYS be restored, even when a shell is running.
	// Skipping it here is what used to leave the machine permanently on the
	// phone-mode scheme after a reboot (or after a retried off).
	if h.scheme != schemeBalanced {
		t.Fatalf("off left the machine on %s, want balanced", h.scheme)
	}
	// The shell was already running, so off must not restart it.
	for _, w := range h.writes {
		if w == "desktop:explorer.exe=running" {
			t.Fatalf("off restarted a shell that was already running: %v", h.writes)
		}
	}
}

// The regression this guards: phone mode -> reboot -> off. Explorer is running
// again after logon, but the recorded scheme must still come back.
func TestOffRestoresSchemeAfterReboot(t *testing.T) {
	h := newFakeHost()

	// Phone mode was entered, then the machine rebooted: the scheme is still
	// power saver, and Windows restarted the shell.
	state := State{
		Version:      stateVersion,
		ActiveScheme: schemeBalanced,
		Power: []PowerValue{{
			Scheme: h.scheme, Subgroup: subProcessor, Setting: setProcMax,
			AC: "100", DC: "100", Label: settingCPUMax.label,
		}},
	}
	h.scheme = schemePowerSaver
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	h.desktop["explorer.exe"] = true

	steps, errs := buildOffPlan(DefaultConfig(), state, h, false)
	if len(errs) != 0 {
		t.Fatalf("off plan errors: %v", errs)
	}
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("off apply failed: %v", errs)
	}

	if h.scheme != schemeBalanced {
		t.Fatalf("after a reboot off left the machine on %s, want balanced", h.scheme)
	}
	ac, dc, _ := h.QueryPower(h.scheme, subProcessor, setProcMax)
	if ac != "100" || dc != "100" {
		t.Fatalf("CPU cap restored to ac=%s dc=%s, want 100/100", ac, dc)
	}
}

// A retried off must reach the same end state as the first attempt. Previously
// the plan changed shape between runs (the shell had been restarted by attempt
// one), so attempt two silently dropped the scheme restore and deleted state.
func TestOffRetryAfterLateFailureStillRestoresScheme(t *testing.T) {
	h := newFakeHost()
	p, _ := buildOnPlan(onConfig(), h)
	runSteps(p.steps, false, &bytes.Buffer{})

	// Attempt one: the final scheme step fails.
	h.writeErr[""] = errBoom
	firstSteps, _ := buildOffPlan(DefaultConfig(), p.state, h, true)
	_ = runSteps(firstSteps, false, &bytes.Buffer{})
	// The shell was restarted by attempt one, whatever else failed.
	if !h.desktop["explorer.exe"] {
		t.Fatal("precondition: attempt one should have restarted the shell")
	}

	// Attempt two sees a live shell and must still restore the scheme.
	secondSteps, errs := buildOffPlan(DefaultConfig(), p.state, h, false)
	if len(errs) != 0 {
		t.Fatalf("retry plan errors: %v", errs)
	}
	if errs := runSteps(secondSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("retry apply failed: %v", errs)
	}
	if h.scheme != schemeBalanced {
		t.Fatalf("retried off left the machine on %s, want balanced", h.scheme)
	}
}

func TestOffRestoresSchemeWhenHeadless(t *testing.T) {
	h := newFakeHost()
	p, _ := buildOnPlan(onConfig(), h)
	runSteps(p.steps, false, &bytes.Buffer{})

	if h.scheme != schemePowerSaver {
		t.Fatalf("precondition: scheme should be power saver, got %s", h.scheme)
	}

	offSteps, _ := buildOffPlan(DefaultConfig(), p.state, h, true)
	if errs := runSteps(offSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("off apply failed: %v", errs)
	}
	if h.scheme != schemeBalanced {
		t.Fatalf("scheme restored to %s, want balanced", h.scheme)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("desktop shell was not restarted")
	}
}

// A failed step must not prevent the remaining restores from being attempted,
// and must be surfaced so the state file is kept for a retry.
func TestOffContinuesAfterFailure(t *testing.T) {
	h := newFakeHost()
	p, _ := buildOnPlan(onConfig(), h)
	runSteps(p.steps, false, &bytes.Buffer{})

	h.writeErr[subProcessor+"|"+setProcMax] = errBoom
	offSteps, _ := buildOffPlan(DefaultConfig(), p.state, h, true)
	errs := runSteps(offSteps, false, &bytes.Buffer{})
	if len(errs) == 0 {
		t.Fatal("expected the failure to be reported")
	}
	// The desktop restore comes after the failed power step and must still run.
	if !h.desktop["explorer.exe"] {
		t.Fatal("a failed power step blocked the desktop restore")
	}
}

func TestFallbackPlanWithoutState(t *testing.T) {
	h := newFakeHost()
	// Simulate phone mode having been left on by an older run: everything is
	// capped and the shell is gone.
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	h.desktop["explorer.exe"] = false

	cfg := DefaultConfig()
	steps, errs := buildFallbackPlan(cfg, h, true)
	if len(errs) != 0 {
		t.Fatalf("fallback errors: %v", errs)
	}
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("fallback apply failed: %v", errs)
	}
	if !h.powerSet(subProcessor, setProcMax, "100") {
		t.Fatalf("fallback did not restore the CPU cap: %v", h.setCalls)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("fallback did not restart the shell")
	}
}

// With no state file the tool cannot know the old lid action or timeouts, so
// the fallback must use its documented defaults rather than inventing values.
func TestFallbackUsesDocumentedDefaults(t *testing.T) {
	h := newFakeHost()
	h.acIdx[subProcessor+"|"+setProcMax] = "30"
	h.dcIdx[subProcessor+"|"+setProcMax] = "30"

	cfg := DefaultConfig()
	steps, _ := buildFallbackPlan(cfg, h, true)
	var found bool
	for _, s := range steps {
		if s.setting == settingCPUMax.label {
			found = true
			if s.next != "100%" {
				t.Fatalf("fallback CPU cap = %s, want 100%%", s.next)
			}
		}
	}
	if !found {
		t.Fatal("fallback did not include the CPU cap")
	}
}

func TestDisabledFeaturesAreUntouched(t *testing.T) {
	h := newFakeHost()
	cfg := DefaultConfig()
	cfg.ManageCPU = false
	cfg.ManageTurbo = false
	cfg.ManageDisplay = false
	cfg.ManageDisplayTimeout = false
	cfg.ManageLid = false
	cfg.ManageSleep = false
	cfg.ManageRadios = false
	cfg.ManageDesktop = false
	cfg.ManageServices = false
	cfg.ManagePowerScheme = false

	p, errs := buildOnPlan(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if len(p.steps) != 0 {
		t.Fatalf("disabled features produced steps: %+v", p.steps)
	}
	if len(p.state.Power) != 0 {
		t.Fatalf("disabled features were snapshotted: %+v", p.state.Power)
	}
	if p.state.Desktop != nil {
		t.Fatal("disabled desktop was snapshotted")
	}
}

// A machine already at the target values should produce no writes, so running
// phone mode twice is not churn.
func TestNoChangeWhenAlreadyAtTarget(t *testing.T) {
	h := newFakeHost()
	cfg := DefaultConfig()
	cfg.CPUMaxPercent = 100
	cfg.Turbo = true
	cfg.Brightness = 80
	cfg.ManageLid = false
	cfg.ManageSleep = false
	cfg.ManageDisplayTimeout = false
	cfg.ManageRadios = false
	cfg.ManageDesktop = false
	cfg.ManageServices = false
	// Keep the active scheme unchanged so only the CPU/brightness steps exist.
	cfg.ManagePowerScheme = false
	// Boost mode 2 already counts as enabled, so "turbo on" is a no-op.
	cfg.CPUMaxPercent = 100
	cfg.Brightness = 80

	p, errs := buildOnPlan(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	for _, s := range p.steps {
		if s.current == s.next {
			continue
		}
		t.Logf("step %q changes %s -> %s", s.setting, s.current, s.next)
	}
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
}

func TestBrightnessUnsupportedIsNotAnError(t *testing.T) {
	h := newFakeHost()
	h.brightOK = false
	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("a desktop with no panel should not error: %v", errs)
	}
	if p.state.Brightness != nil {
		t.Fatal("unsupported brightness must not be snapshotted")
	}
	for _, s := range p.steps {
		if s.setting == "brightness" {
			t.Fatal("unsupported brightness must not produce a step")
		}
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := stateStore{dir: dir}
	h := newFakeHost()
	p, _ := buildOnPlan(onConfig(), h)
	p.state.SavedAt = time.Now().Truncate(time.Second)

	if err := store.Save(p.state); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveScheme != p.state.ActiveScheme {
		t.Fatalf("scheme lost: %q vs %q", loaded.ActiveScheme, p.state.ActiveScheme)
	}
	if len(loaded.Power) != len(p.state.Power) {
		t.Fatalf("power settings lost: %d vs %d", len(loaded.Power), len(p.state.Power))
	}
	for i := range loaded.Power {
		if loaded.Power[i] != p.state.Power[i] {
			t.Fatalf("power setting %d changed: %+v vs %+v", i, loaded.Power[i], p.state.Power[i])
		}
	}
	if loaded.Desktop == nil || loaded.Desktop.Running != p.state.Desktop.Running {
		t.Fatal("desktop state lost")
	}
	if loaded.Brightness == nil || *loaded.Brightness != *p.state.Brightness {
		t.Fatal("brightness lost")
	}
}

func TestStateVersionIsEnforced(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	if err := writeJSON(store.statePath(), map[string]any{"version": 99}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a future state version must be rejected, not misread")
	}
}

func TestPowerLabelFormats(t *testing.T) {
	if got := powerLabel(settingCPUMax.label, "40", "40"); got != "40%" {
		t.Fatalf("cpu label = %q", got)
	}
	if got := powerLabel(settingStandby.label, "0", "0"); got != "never" {
		t.Fatalf("sleep label = %q", got)
	}
	if got := powerLabel(settingLid.label, "0", "0"); got != "ignore" {
		t.Fatalf("lid label = %q", got)
	}
	if got := powerLabel(settingBoost.label, "0", "0"); got != "off" {
		t.Fatalf("boost label = %q", got)
	}
	// Differing AC/DC must be visible, not collapsed.
	if got := powerLabel(settingDisplay.label, "600", "300"); !strings.Contains(got, "ac") || !strings.Contains(got, "dc") {
		t.Fatalf("split ac/dc label = %q", got)
	}
}

// --- restore robustness ---------------------------------------------------
//
// Disable-/Enable-PnpDevice report an error when the device is already in the
// requested state, and fail outright for a device that has been removed. Either
// one would keep the state file forever, so the plan must not emit those steps.

func TestOffSkipsDevicesAlreadyInRecordedState(t *testing.T) {
	h := newFakeHost()
	// Both devices are already in the recorded state, which is the case that
	// used to emit a failing no-op PnP call.
	h.devices["Bluetooth"] = []device{
		{InstanceID: `BTHENUM\DEV_A`, Description: "Headset", Disabled: false},
		{InstanceID: `BTHENUM\DEV_B`, Description: "Mouse", Disabled: true},
	}
	st := State{
		Version: stateVersion,
		Radios: []RadioState{
			// Was enabled and still is.
			{InstanceID: `BTHENUM\DEV_A`, Description: "Headset", Type: "bluetooth", Disabled: false},
			// Was already disabled and still is: never touched by `on`.
			{InstanceID: `BTHENUM\DEV_B`, Description: "Mouse", Type: "bluetooth", Disabled: true},
		},
	}
	steps, errs := buildOffPlan(DefaultConfig(), st, h, true)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	for _, s := range steps {
		if s.apply == nil {
			continue
		}
		if strings.HasPrefix(s.setting, "bluetooth ") {
			t.Fatalf("emitted a device step that would fail: %q (%s -> %s)",
				s.setting, s.current, s.next)
		}
	}
}

func TestOffSkipsDeviceThatDisappeared(t *testing.T) {
	h := newFakeHost()
	h.devices["Bluetooth"] = nil // the dongle was unplugged
	st := State{
		Version: stateVersion,
		Radios: []RadioState{
			{InstanceID: `BTHENUM\GONE`, Description: "Old headset", Type: "bluetooth", Disabled: false},
		},
	}
	steps, errs := buildOffPlan(DefaultConfig(), st, h, true)
	if len(errs) != 0 {
		t.Fatalf("a missing device is not an error, got %v", errs)
	}
	if retire := runSteps(steps, false, &bytes.Buffer{}); len(retire) != 0 {
		t.Fatalf("missing device produced a failing step: %v", retire)
	}
}

func TestOffSkipsBrightnessWhenPanelGone(t *testing.T) {
	h := newFakeHost()
	h.brightOK = false
	saved := 80
	st := State{Version: stateVersion, Brightness: &saved}

	steps, _ := buildOffPlan(DefaultConfig(), st, h, true)
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("unsupported panel must not fail the restore: %v", errs)
	}
	if len(h.writes) != 0 {
		t.Fatalf("wrote brightness to a panel that cannot accept it: %v", h.writes)
	}
}

func TestOffSkipsBrightnessWhenAlreadyCorrect(t *testing.T) {
	h := newFakeHost()
	h.bright = 80
	saved := 80
	steps, _ := buildOffPlan(DefaultConfig(), State{Version: stateVersion, Brightness: &saved}, h, true)
	for _, s := range steps {
		if s.setting == "brightness" {
			t.Fatal("emitted a brightness step that changes nothing")
		}
	}
}

// --- locale-proof parsing -------------------------------------------------

func TestOnPlanProceedsOnLocalizedPowercfg(t *testing.T) {
	// The fake exercises the same parser paths: the point is that a successful
	// read still yields a restore value, not that the strings differ.
	h := newFakeHost()
	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if p.state.ActiveScheme == "" {
		t.Fatal("active scheme must be recorded, or off cannot restore it")
	}
}

// If the active scheme cannot be read, no power setting may be touched:
// powerStep activates the scheme it writes into, so proceeding would switch the
// user onto the phone-mode scheme with nothing recorded to restore.
func TestOnPlanTouchesNoPowerSettingWithoutActiveScheme(t *testing.T) {
	h := &noSchemeHost{fakeHost: newFakeHost()}
	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) == 0 {
		t.Fatal("an unreadable active scheme should be reported")
	}
	if p.state.ActiveScheme != "" {
		t.Fatalf("recorded a scheme it could not read: %q", p.state.ActiveScheme)
	}
	for _, s := range p.steps {
		for _, ps := range []powerSetting{settingCPUMax, settingBoost, settingDisplay, settingStandby, settingHibernate, settingLid} {
			if s.setting == ps.label {
				t.Fatalf("power step %q emitted without a recorded scheme", s.setting)
			}
		}
		if s.setting == "power scheme" {
			t.Fatal("switched the scheme without recording the original")
		}
	}
	if len(p.state.Power) != 0 {
		t.Fatalf("snapshotted power values it may not be able to restore: %+v", p.state.Power)
	}
}

// noSchemeHost fails only the active-scheme read.
type noSchemeHost struct{ *fakeHost }

func (n *noSchemeHost) ActiveScheme() (string, error) { return "", errBoom }

// --- session origin -------------------------------------------------------

// Phone mode entered from a service or SSH session has no desktop to restore.
// Spawning explorer.exe into that session would be wrong.
func TestOffDoesNotStartShellForHeadlessOrigin(t *testing.T) {
	h := newFakeHost()
	hadDesktop := false
	st := State{
		Version:           stateVersion,
		SessionHadDesktop: &hadDesktop,
		Desktop:           &ServiceState{Name: "explorer.exe", Running: true},
	}
	h.desktop["explorer.exe"] = false

	steps, _ := buildOffPlan(DefaultConfig(), st, h, true)
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if h.desktop["explorer.exe"] {
		t.Fatal("off started a shell from a session that never had one")
	}
}

// A state file written before SessionHadDesktop existed must behave like the
// old version: bring the shell back.
func TestOffRestoresShellForLegacyState(t *testing.T) {
	h := newFakeHost()
	h.desktop["explorer.exe"] = false
	st := State{
		Version: stateVersion,
		Desktop: &ServiceState{Name: "explorer.exe", Running: true},
		// SessionHadDesktop intentionally nil.
	}
	steps, _ := buildOffPlan(DefaultConfig(), st, h, true)
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("a legacy state file did not restore the shell")
	}
}

func TestOnRecordsSessionOrigin(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)
	a.on(false)

	st, err := a.store.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.SessionHadDesktop == nil {
		t.Fatal("session origin was not recorded, so off cannot decide about the shell")
	}
	if !*st.SessionHadDesktop {
		t.Fatal("a desktop session should be recorded as having a shell")
	}
}

func TestOnRecordsHeadlessOrigin(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.desktop["explorer.exe"] = false
	a, _, _ := newTestApp(t, h)
	a.on(false)

	st, _ := a.store.Load()
	if st.SessionHadDesktop == nil || *st.SessionHadDesktop {
		t.Fatal("a headless session must be recorded as having no shell")
	}
}

// isolate_desktop = false must keep the shell running: it is the one switch
// that makes phone mode non-disruptive, so honouring it matters.
func TestIsolateDesktopFalseKeepsShell(t *testing.T) {
	h := newFakeHost()
	cfg := onConfig()
	cfg.IsolateDesktop = false

	p, errs := buildOnPlan(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	for _, s := range p.steps {
		if strings.HasPrefix(s.setting, "desktop ") {
			t.Fatalf("shell was scheduled to stop despite isolate_desktop=false: %q", s.setting)
		}
	}
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("isolate_desktop=false stopped the shell")
	}
}

// With the switch on, the shell really must stop, or phone mode does nothing
// useful on a laptop whose only power draw is the desktop.
func TestIsolateDesktopTrueStopsShell(t *testing.T) {
	h := newFakeHost()
	cfg := onConfig()
	cfg.IsolateDesktop = true

	p, _ := buildOnPlan(cfg, h)
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if h.desktop["explorer.exe"] {
		t.Fatal("isolate_desktop=true did not stop the shell")
	}
}

// A powercfg write re-applies the whole scheme, so a step that changes nothing
// is not free. No-op steps must be dropped, and the value must still be
// snapshotted so restore does not think it was never touched.
func TestNoOpPowerStepIsSkipped(t *testing.T) {
	h := newFakeHost()
	// Hibernate is already "never" (0) and phone mode also wants "never".
	h.acIdx[subSleep+"|"+setHibernateIdle] = "0"
	h.dcIdx[subSleep+"|"+setHibernateIdle] = "0"

	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	for _, s := range p.steps {
		if s.setting == settingHibernate.label {
			t.Fatalf("emitted a no-op step: %q %s -> %s", s.setting, s.current, s.next)
		}
	}
	// Still snapshotted: it was read, and restore must not guess.
	found := false
	for _, v := range p.state.Power {
		if v.Setting == setHibernateIdle {
			found = true
		}
	}
	if !found {
		t.Fatal("hibernate was skipped from the snapshot as well")
	}
}

// Every step in a plan must actually change something. Two of the same setting
// changing nothing in sequence is how silent no-ops creep back in.
func TestPlanHasNoNoOpSteps(t *testing.T) {
	h := newFakeHost()
	h.acIdx[subSleep+"|"+setHibernateIdle] = "0"
	h.dcIdx[subSleep+"|"+setHibernateIdle] = "0"
	p, _ := buildOnPlan(onConfig(), h)

	seen := map[string]bool{}
	for _, s := range p.steps {
		if s.current == s.next {
			t.Fatalf("no-op step %q: %s -> %s", s.setting, s.current, s.next)
		}
		if seen[s.setting] {
			t.Fatalf("setting %q appears twice in one plan", s.setting)
		}
		seen[s.setting] = true
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func idleConfig() Config {
	cfg := DefaultConfig()
	cfg.Idle.Enabled = true
	cfg.Idle.QuietMinutes = 10
	cfg.Idle.CPUMaxPercent = 20
	return cfg
}

func TestIdlePlanLowersCapAndRecordsPrevious(t *testing.T) {
	h := newFakeHost()
	// Phone mode has already capped the CPU at 40%.
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"

	st, steps, errs := buildIdlePlan(idleConfig(), h, time.Now())
	if len(errs) != 0 {
		t.Fatalf("idle plan errors: %v", errs)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d idle steps, want 1", len(steps))
	}
	if len(st.Power) != 1 || st.Power[0].AC != "40" || st.Power[0].DC != "40" {
		t.Fatalf("previous cap not recorded exactly: %+v", st.Power)
	}
	if errs := runSteps(steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if !h.powerSet(subProcessor, setProcMax, "20") {
		t.Fatalf("idle cap not applied: %v", h.setCalls)
	}
}

// When phone mode already capped below the idle target there is nothing to do.
// Recording a snapshot anyway would create a restore step that changes nothing
// and would keep an idle.json alive forever.
func TestIdlePlanSkipsWhenAlreadyLower(t *testing.T) {
	h := newFakeHost()
	h.acIdx[subProcessor+"|"+setProcMax] = "15"
	h.dcIdx[subProcessor+"|"+setProcMax] = "15"

	st, steps, errs := buildIdlePlan(idleConfig(), h, time.Now())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(steps) != 0 {
		t.Fatalf("expected no steps, got %d", len(steps))
	}
	if len(st.Power) != 0 {
		t.Fatal("must not record an idle snapshot when nothing changes")
	}
}

func TestIdleRestorePutsBackExactValues(t *testing.T) {
	h := newFakeHost()
	h.acIdx[subProcessor+"|"+setProcMax] = "60"
	h.dcIdx[subProcessor+"|"+setProcMax] = "45"

	st, steps, _ := buildIdlePlan(idleConfig(), h, time.Now())
	runSteps(steps, false, &bytes.Buffer{})

	restoreSteps, errs := buildIdleRestorePlan(st, h)
	if len(errs) != 0 {
		t.Fatalf("restore plan errors: %v", errs)
	}
	if len(restoreSteps) != 1 {
		t.Fatalf("got %d restore steps, want 1", len(restoreSteps))
	}
	if errs := runSteps(restoreSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("restore failed: %v", errs)
	}

	ac, dc, _ := h.QueryPower(h.scheme, subProcessor, setProcMax)
	if ac != "60" || dc != "45" {
		t.Fatalf("restored ac=%s dc=%s, want 60/45", ac, dc)
	}
}

func TestIdleRestoreSkipsWhenAlreadyCorrect(t *testing.T) {
	h := newFakeHost()
	st := IdleState{
		Version:   idleVersion,
		AppliedAt: time.Now(),
		Power: []PowerValue{{
			Scheme: h.scheme, Subgroup: subProcessor, Setting: setProcMax,
			AC: "100", DC: "100", Label: settingCPUMax.label,
		}},
	}
	steps, errs := buildIdleRestorePlan(st, h)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(steps) != 0 {
		t.Fatalf("values already correct but got %d steps", len(steps))
	}
}

func TestLastBotActivityUsesNewestLog(t *testing.T) {
	h := newFakeHost()
	dir := filepath.Join("C:", "logs")
	pattern := filepath.Join(dir, botLogPattern)
	h.globs[pattern] = []string{filepath.Join(dir, "bot-1.log"), filepath.Join(dir, "bot-2.log")}
	older := time.Now().Add(-30 * time.Minute)
	newer := time.Now().Add(-5 * time.Minute)
	h.mtimes[filepath.Join(dir, "bot-1.log")] = older
	h.mtimes[filepath.Join(dir, "bot-2.log")] = newer

	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir

	at, found, errs := lastBotActivity(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !found {
		t.Fatal("activity should be found")
	}
	if !at.Equal(newer) {
		t.Fatalf("got %v, want the newest mtime %v", at, newer)
	}
}

func TestLastBotActivityNoLogs(t *testing.T) {
	h := newFakeHost()
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = filepath.Join("C:", "nothing")

	_, found, errs := lastBotActivity(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("missing directory is not an error: %v", errs)
	}
	if found {
		t.Fatal("no logs should mean no activity")
	}
}

// A log that vanishes between glob and stat must not fail the check; rotating
// logs do exactly that.
func TestLastBotActivityToleratesVanishingLog(t *testing.T) {
	h := newFakeHost()
	dir := filepath.Join("C:", "logs")
	pattern := filepath.Join(dir, botLogPattern)
	h.globs[pattern] = []string{filepath.Join(dir, "bot-1.log")}
	// No mtime recorded, so ModTime returns fs.ErrNotExist.

	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir

	_, found, errs := lastBotActivity(cfg, h)
	if len(errs) != 0 {
		t.Fatalf("a vanished log should be skipped silently: %v", errs)
	}
	if found {
		t.Fatal("no surviving log should mean no activity")
	}
}

func TestIdleStateRoundTrip(t *testing.T) {
	store := stateStore{dir: t.TempDir()}
	h := newFakeHost()
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"

	st, _, _ := buildIdlePlan(idleConfig(), h, time.Now().Truncate(time.Second))
	if err := store.SaveIdle(st); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.LoadIdle()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Power) != 1 || loaded.Power[0].AC != "40" {
		t.Fatalf("idle snapshot lost: %+v", loaded.Power)
	}
	if err := store.RemoveIdle(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if store.IdleExists() {
		t.Fatal("idle file should be gone")
	}
}

// --- command-level behavior ------------------------------------------------

func newTestApp(t *testing.T, h host) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &app{
		host:       h,
		store:      stateStore{dir: filepath.Join(dir, "cache")},
		configPath: filepath.Join(dir, "config.toml"),
		out:        out,
		errOut:     errOut,
		now:        func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) },
	}, out, errOut
}

func TestOnRefusesWhenStateExists(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, errOut := newTestApp(t, h)
	if err := a.store.Save(State{Version: stateVersion}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if code := a.on(false); code == 0 {
		t.Fatal("on should refuse when a state file exists")
	}
	if !strings.Contains(errOut.String(), "state already exists") {
		t.Fatalf("missing explanation: %s", errOut.String())
	}
}

// Snapshotting again would overwrite the values needed to restore the desktop,
// so the guard must fire before any change is written.
func TestOnRefusesWithoutTouchingAnything(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)
	a.store.Save(State{Version: stateVersion})

	a.on(false)
	if len(h.setCalls) != 0 || len(h.writes) != 0 {
		t.Fatalf("refused on still made changes: %v %v", h.setCalls, h.writes)
	}
}

func TestOnRefusesOnLowBatteryWhenDischarging(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.battery = 8
	h.batterySt = "discharging"
	a, _, errOut := newTestApp(t, h)

	if code := a.on(false); code == 0 {
		t.Fatal("on should refuse below the battery threshold")
	}
	if !strings.Contains(errOut.String(), "refusing phone mode") {
		t.Fatalf("missing explanation: %s", errOut.String())
	}
	if len(h.setCalls) != 0 {
		t.Fatal("refused on still changed power settings")
	}
}

func TestOnAllowsChargingAtLowBattery(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.battery = 8
	h.batterySt = "charging"
	a, _, _ := newTestApp(t, h)

	if code := a.on(false); code != 0 {
		t.Fatalf("charging at low battery should be allowed, got %d", code)
	}
}

func TestOnOffCycleLeavesNoState(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)

	if code := a.on(false); code != 0 {
		t.Fatalf("on failed with %d", code)
	}
	if !a.store.Exists() {
		t.Fatal("on did not write a state file")
	}
	if code := a.off(false); code != 0 {
		t.Fatalf("off failed with %d", code)
	}
	if a.store.Exists() {
		t.Fatal("off did not remove the state file")
	}
	if a.store.Active() {
		t.Fatal("off did not clear the active marker")
	}
}

// A failed restore must keep the state file, otherwise the user has no way to
// retry and the desktop settings are lost for good.
func TestOffKeepsStateWhenRestoreFails(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)
	a.on(false)

	h.writeErr[subProcessor+"|"+setProcMax] = errBoom
	if code := a.off(false); code == 0 {
		t.Fatal("off should report a failed restore")
	}
	if !a.store.Exists() {
		t.Fatal("state file must survive a failed restore so off can be retried")
	}
}

func TestStatusNeedsNoAdminAndReportsMode(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, out, _ := newTestApp(t, h)

	if code := a.status(); code != 0 {
		t.Fatalf("status failed with %d", code)
	}
	text := out.String()
	for _, want := range []string{"mode:", "power scheme:", "max processor state:", "battery:", "idle profile:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status output missing %q:\n%s", want, text)
		}
	}
}

func TestStatusWarnsBelowThreshold(t *testing.T) {
	h := newFakeHost()
	h.battery = 5
	h.batterySt = "discharging"
	a, out, _ := newTestApp(t, h)

	a.status()
	if !strings.Contains(out.String(), "below battery_exit_percent") {
		t.Fatalf("status did not warn:\n%s", out.String())
	}
}

func TestDryRunNeedsNoAdmin(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, out, _ := newTestApp(t, h)

	// run() should not relaunch for a dry run. The stub Relaunch succeeds, so
	// the only way to prove it was skipped is that the real plan is printed.
	if code := a.run([]string{"on", "--dry-run"}); code != 0 {
		t.Fatalf("dry run failed with %d", code)
	}
	if !strings.Contains(out.String(), "dry run:") {
		t.Fatalf("dry run did not print a preview:\n%s", out.String())
	}
	if len(h.setCalls) != 0 {
		t.Fatal("dry run applied changes")
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	h := newFakeHost()
	a, _, errOut := newTestApp(t, h)
	if code := a.run([]string{"teleport"}); code != 2 {
		t.Fatalf("unknown command should exit 2, got %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatal("unknown command not explained")
	}
}

func TestRunRequiresACommand(t *testing.T) {
	h := newFakeHost()
	a, _, _ := newTestApp(t, h)
	if code := a.run(nil); code != 2 {
		t.Fatalf("no command should exit 2, got %d", code)
	}
}

func TestIdleDoesNothingOutsidePhoneMode(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, out, _ := newTestApp(t, h)

	if code := a.idle(false); code != 0 {
		t.Fatalf("idle outside phone mode should be a no-op, got %d", code)
	}
	if !strings.Contains(out.String(), "phone mode is not active") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if len(h.setCalls) != 0 {
		t.Fatal("idle changed settings outside phone mode")
	}
}

func TestIdleWaitsWhenBotIsActive(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, out, _ := newTestApp(t, h)
	a.store.MarkActive()
	h.desktop["explorer.exe"] = false

	dir := filepath.Join("C:", "logs")
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir
	writeTestConfig(t, a.configPath, cfg)

	// Activity one minute ago, quiet window is ten minutes.
	pattern := filepath.Join(dir, botLogPattern)
	h.globs[pattern] = []string{filepath.Join(dir, "bot-1.log")}
	h.mtimes[filepath.Join(dir, "bot-1.log")] = a.now().Add(-1 * time.Minute)

	if code := a.idle(false); code != 0 {
		t.Fatalf("idle failed with %d", code)
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Fatalf("idle did not wait: %s", out.String())
	}
	if len(h.setCalls) != 0 {
		t.Fatal("idle applied a profile while the bot was active")
	}
}

func TestIdleAppliesAfterQuietWindow(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	a, out, _ := newTestApp(t, h)
	a.store.MarkActive()
	h.desktop["explorer.exe"] = false

	dir := filepath.Join("C:", "logs")
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir
	writeTestConfig(t, a.configPath, cfg)

	pattern := filepath.Join(dir, botLogPattern)
	h.globs[pattern] = []string{filepath.Join(dir, "bot-1.log")}
	h.mtimes[filepath.Join(dir, "bot-1.log")] = a.now().Add(-30 * time.Minute)

	if code := a.idle(false); code != 0 {
		t.Fatalf("idle failed with %d", code)
	}
	if !strings.Contains(out.String(), "idle profile applied") {
		t.Fatalf("idle did not apply: %s", out.String())
	}
	if !a.store.IdleExists() {
		t.Fatal("idle snapshot was not saved")
	}
	if !h.powerSet(subProcessor, setProcMax, "20") {
		t.Fatalf("idle cap not applied: %v", h.setCalls)
	}
}

// A new message must lift the cap, and the state file must go away so the
// profile can be applied again later.
func TestIdleRestoresWhenActivityResumes(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	a, _, _ := newTestApp(t, h)
	a.store.MarkActive()
	h.desktop["explorer.exe"] = false

	dir := filepath.Join("C:", "logs")
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir
	writeTestConfig(t, a.configPath, cfg)

	logPath := filepath.Join(dir, "bot-1.log")
	pattern := filepath.Join(dir, botLogPattern)
	h.globs[pattern] = []string{logPath}

	// First apply the profile after a long quiet stretch.
	h.mtimes[logPath] = a.now().Add(-30 * time.Minute)
	if code := a.idle(false); code != 0 {
		t.Fatalf("idle apply failed with %d", code)
	}
	if !a.store.IdleExists() {
		t.Fatal("precondition: idle profile should be applied")
	}

	// Then a message arrives.
	h.mtimes[logPath] = a.now().Add(-1 * time.Minute)
	if code := a.idle(false); code != 0 {
		t.Fatalf("idle restore failed with %d", code)
	}
	if a.store.IdleExists() {
		t.Fatal("idle state should be cleared after a successful restore")
	}
	ac, dc, _ := h.QueryPower(h.scheme, subProcessor, setProcMax)
	if ac != "40" || dc != "40" {
		t.Fatalf("cap restored to %s/%s, want 40/40", ac, dc)
	}
}

func writeTestConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	// Write the values the test needs through the example template so unknown
	// keys stay rejected; the template already documents every field.
	body := exampleConfig
	if cfg.Idle.ActivityLogDir != "" {
		body = strings.Replace(body,
			`activity_log_dir = ""`,
			`activity_log_dir = "`+cfg.Idle.ActivityLogDir+`"`, 1)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// A restore that succeeded but crashed before deleting idle.json left a file
// that made every later check report "still quiet" forever. It must be cleared.
func TestIdleClearsStaleStateAfterCrash(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	a, out, _ := newTestApp(t, h)
	a.store.MarkActive()
	h.desktop["explorer.exe"] = false

	dir := filepath.Join("C:", "logs")
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir
	writeTestConfig(t, a.configPath, cfg)

	// Apply the profile normally.
	logPath := filepath.Join(dir, "bot-1.log")
	h.globs[filepath.Join(dir, botLogPattern)] = []string{logPath}
	h.mtimes[logPath] = a.now().Add(-30 * time.Minute)
	if code := a.idle(false); code != 0 {
		t.Fatalf("apply failed with %d", code)
	}
	if !a.store.IdleExists() {
		t.Fatal("precondition: idle state should exist")
	}

	// Simulate: the cap was restored, but the process died before removing the
	// file.
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	saved, err := a.store.LoadIdle()
	if err != nil {
		t.Fatalf("load idle: %v", err)
	}
	h.acIdx[subProcessor+"|"+setProcMax] = saved.Power[0].AC
	h.dcIdx[subProcessor+"|"+setProcMax] = saved.Power[0].DC

	// Still quiet, so the early branch runs.
	if code := a.idle(false); code != 0 {
		t.Fatalf("idle failed with %d", code)
	}
	if a.store.IdleExists() {
		t.Fatal("stale idle state was not cleared")
	}
	if !strings.Contains(out.String(), "already restored") {
		t.Fatalf("stale state was not explained: %s", out.String())
	}
}

// A failed restore must keep idle.json so it can be retried; the next check
// then actually retries instead of claiming the profile is applied.
func TestIdleRetriesFailedRestore(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	a, _, _ := newTestApp(t, h)
	a.store.MarkActive()
	h.desktop["explorer.exe"] = false

	dir := filepath.Join("C:", "logs")
	cfg := idleConfig()
	cfg.Idle.ActivityLogDir = dir
	writeTestConfig(t, a.configPath, cfg)
	logPath := filepath.Join(dir, "bot-1.log")
	h.globs[filepath.Join(dir, botLogPattern)] = []string{logPath}
	h.mtimes[logPath] = a.now().Add(-30 * time.Minute)

	if code := a.idle(false); code != 0 {
		t.Fatalf("apply failed with %d", code)
	}

	// Activity arrives while the write path is broken.
	h.mtimes[logPath] = a.now().Add(-1 * time.Minute)
	h.writeErr[subProcessor+"|"+setProcMax] = errBoom
	if code := a.idle(false); code == 0 {
		t.Fatal("a failed restore should report failure")
	}
	if !a.store.IdleExists() {
		t.Fatal("a failed restore must keep idle state for retry")
	}

	// The write path recovers; the next check must retry the restore.
	delete(h.writeErr, subProcessor+"|"+setProcMax)
	if code := a.idle(false); code != 0 {
		t.Fatalf("retry failed with %d", code)
	}
	if a.store.IdleExists() {
		t.Fatal("the retried restore did not clear idle state")
	}
	ac, dc, _ := h.QueryPower(h.scheme, subProcessor, setProcMax)
	if ac != "40" || dc != "40" {
		t.Fatalf("retry restored ac=%s dc=%s, want 40/40", ac, dc)
	}
}

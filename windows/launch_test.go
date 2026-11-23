package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- Windows argv quoting -------------------------------------------------
//
// The elevated relaunch hands one command-line string to Start-Process, which
// re-parses it with the C runtime's rules. If quoting is wrong the child gets
// a different argv than the user typed, and for the "--log-file" handoff that
// means silently writing to the wrong path.

func TestQuoteWindowsArgPlainUnchanged(t *testing.T) {
	for _, in := range []string{"on", "off", "status", "idle", "--dry-run", `C:\temp\x.log`} {
		if got := quoteWindowsArg(in); got != in {
			t.Fatalf("plain arg %q was quoted to %q", in, got)
		}
	}
}

func TestQuoteWindowsArgSpaces(t *testing.T) {
	got := quoteWindowsArg(`C:\Program Files\win-phone-mode\app.exe`)
	want := `"C:\Program Files\win-phone-mode\app.exe"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestQuoteWindowsArgEmpty(t *testing.T) {
	if got := quoteWindowsArg(""); got != `""` {
		t.Fatalf("empty arg quoted as %q", got)
	}
}

// A trailing backslash must be doubled, otherwise it escapes the closing quote
// and merges the next argument into this one.
func TestQuoteWindowsArgTrailingBackslash(t *testing.T) {
	got := quoteWindowsArg(`C:\dir with space\`)
	want := `"C:\dir with space\\"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Backslashes not before a quote must be left alone; doubling them would
// corrupt a UNC path or a registry-shaped argument.
func TestQuoteWindowsArgKeepsInnerBackslashes(t *testing.T) {
	got := quoteWindowsArg(`\\server\share\file name.txt`)
	want := `"\\server\share\file name.txt"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestQuoteWindowsArgEscapesEmbeddedQuote(t *testing.T) {
	got := quoteWindowsArg(`say "hi"`)
	want := `"say \"hi\""`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestQuoteWindowsArgsJoins(t *testing.T) {
	got := quoteWindowsArgs([]string{"--elevated", "on", `C:\my logs\x.log`})
	want := `--elevated on "C:\my logs\x.log"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A temp path is only special when the account name contains a space, which is
// common on Windows. That case must collapse to exactly one argv token.
func TestQuoteWindowsArgsIsSingleToken(t *testing.T) {
	arg := `C:\Users\First Last\AppData\Local\Temp\win-phone-mode-123.log`
	quoted := quoteWindowsArg(arg)
	if strings.Count(quoted, `"`) != 2 {
		t.Fatalf("expected exactly one pair of quotes: %q", quoted)
	}
	// A quote-aware Windows argc parser sees one token; strings.Fields is not
	// quote-aware, so assert the shape that makes it one token instead.
	if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
		t.Fatalf("quoted arg is not enclosed in quotes: %q", quoted)
	}
	if inner := quoted[1 : len(quoted)-1]; strings.Contains(inner, `"`) {
		t.Fatalf("quoted arg has an unescaped inner quote: %q", quoted)
	}
	// A path with no spaces needs no quoting at all, and adding it would be
	// harmless but noisy in diagnostics.
	if got := quoteWindowsArg(`C:\Users\me\x.log`); strings.Contains(got, `"`) {
		t.Fatalf("spaceless path should not be quoted: %q", got)
	}
}

// --- elevated log handoff -------------------------------------------------

// An elevated child writes to the log the parent named; the parent prints it.
// Without this the user sees an empty console because the child's console
// closes when it exits.
func TestRedirectOutputWritesToLog(t *testing.T) {
	h := newFakeHost()
	a, _, _ := newTestApp(t, h)

	logPath := filepath.Join(t.TempDir(), "run.log")
	if code := a.redirectOutput(logPath); code != 0 {
		t.Fatalf("redirect failed with %d", code)
	}
	a.out.Write([]byte("first line\n"))
	a.errOut.Write([]byte("second line\n"))

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) != "first line\nsecond line\n" {
		t.Fatalf("log contents = %q", data)
	}
}

func TestStreamLogIsAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := &app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if code := a.redirectOutput(path); code != 0 {
		t.Fatalf("redirect failed with %d", code)
	}
	a.out.Write([]byte("new\n"))

	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "keep me\n") {
		t.Fatalf("redirect truncated the log: %q", data)
	}
}

// The parent must pass the log path at the end of argv, after the user's own
// arguments, so positional parsing on the child cannot confuse the two.
func TestChildArgsCarryLogFileLast(t *testing.T) {
	args := append(append([]string{}, "on", "--elevated"), flagLogFile, `C:\t\run.log`)
	joined := quoteWindowsArgs(args)
	if !strings.HasSuffix(joined, `--log-file C:\t\run.log`) {
		t.Fatalf("log flag is not last: %q", joined)
	}
}

// The elevated child must not try to elevate again, or a machine that refuses
// elevation loops forever.
func TestElevatedChildDoesNotRelaunch(t *testing.T) {
	h := newFakeHost()
	h.elevated = true // the child is already elevated
	a, _, _ := newTestApp(t, h)

	if code := a.run([]string{"on", "--elevated", flagLogFile, filepath.Join(t.TempDir(), "x.log")}); code != 0 {
		t.Fatalf("elevated child failed with %d", code)
	}
	if h.relaunchArgs != nil {
		t.Fatalf("elevated child tried to relaunch with %v", h.relaunchArgs)
	}
}

// A parent that is not elevated must relaunch exactly once, forwarding the
// original arguments plus the flag that stops the child repeating the loop.
func TestUnelevatedParentRelaunchesOnce(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, _, _ := newTestApp(t, h)

	a.run([]string{"off"})
	if h.relaunchArgs == nil {
		t.Fatal("unelevated parent did not relaunch")
	}
	if h.relaunchArgs[0] != flagElevated {
		t.Fatalf("relaunch did not mark itself elevated: %v", h.relaunchArgs)
	}
	if h.relaunchArgs[1] != "off" {
		t.Fatalf("relaunch lost the original command: %v", h.relaunchArgs)
	}
}

// --- service start mode ---------------------------------------------------
//
// A service whose start mode is "disabled" cannot be started at all. Phone
// mode must repair the mode, and off must put it back, or the bot silently
// never comes up.

func TestOnEnablesDisabledKeepService(t *testing.T) {
	h := newFakeHost()
	h.services["OpenCodeTelegram"] = &fakeService{running: false, start: "disabled"}

	p, errs := buildOnPlan(onConfig(), h)
	if len(errs) != 0 {
		t.Fatalf("plan errors: %v", errs)
	}
	if errs := runSteps(p.steps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	if h.services["OpenCodeTelegram"].start != "auto" {
		t.Fatalf("disabled service left at %q", h.services["OpenCodeTelegram"].start)
	}
	if !h.wrote("service-start:OpenCodeTelegram=auto") {
		t.Fatalf("start mode was not repaired: %v", h.writes)
	}
	// The enable step must come before the start step, or Start-Service fails.
	enableIdx, startIdx := -1, -1
	for i, w := range h.writes {
		if w == "service-start:OpenCodeTelegram=auto" && enableIdx < 0 {
			enableIdx = i
		}
		if w == "service:OpenCodeTelegram=running" && startIdx < 0 {
			startIdx = i
		}
	}
	if enableIdx < 0 || startIdx < 0 || enableIdx > startIdx {
		t.Fatalf("enable must precede start: writes=%v", h.writes)
	}
}

func TestOffRestoresDisabledStartMode(t *testing.T) {
	h := newFakeHost()
	h.services["OpenCodeTelegram"] = &fakeService{running: false, start: "disabled"}

	p, _ := buildOnPlan(onConfig(), h)
	runSteps(p.steps, false, &bytes.Buffer{})
	if h.services["OpenCodeTelegram"].start != "auto" {
		t.Fatal("precondition: service should have been enabled")
	}

	offSteps, errs := buildOffPlan(DefaultConfig(), p.state, h, true)
	if len(errs) != 0 {
		t.Fatalf("off plan errors: %v", errs)
	}
	if errs := runSteps(offSteps, false, &bytes.Buffer{}); len(errs) != 0 {
		t.Fatalf("off apply failed: %v", errs)
	}
	if got := h.services["OpenCodeTelegram"].start; got != "disabled" {
		t.Fatalf("start mode restored to %q, want disabled", got)
	}
}

// Windows refuses to disable a running service, so the stop must be ordered
// before the mode change.
func TestOffStopsBeforeDisabling(t *testing.T) {
	h := newFakeHost()
	h.services["OpenCodeServe"] = &fakeService{running: true, start: "disabled"}

	// Build a state that says the service was running and auto, i.e. phone mode
	// changed both, and it is now running.
	st := State{
		Version: stateVersion,
		Services: []ServiceState{
			{Name: "OpenCodeServe", Running: false, Start: "disabled"},
		},
	}
	h.services["OpenCodeServe"] = &fakeService{running: true, start: "auto"}

	steps, _ := buildOffPlan(DefaultConfig(), st, h, true)
	var stopIdx, modeIdx = -1, -1
	for i, s := range steps {
		if s.setting == "stop OpenCodeServe" && stopIdx < 0 {
			stopIdx = i
		}
		if s.setting == "start mode OpenCodeServe" && modeIdx < 0 {
			modeIdx = i
		}
	}
	if stopIdx < 0 || modeIdx < 0 {
		t.Fatalf("expected a stop and a start-mode step, got %+v", steps)
	}
	if stopIdx > modeIdx {
		t.Fatalf("stop must precede the disable: stop=%d mode=%d", stopIdx, modeIdx)
	}
}

// --- state lifecycle ------------------------------------------------------

// A state file that exists but cannot be parsed must stop `off` cold. The old
// behaviour applied the [restore] defaults (lid=sleep, brightness=100, cpu=100)
// and left the file in place, so `on` stayed refused forever.
func TestOffRefusesUnreadableState(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, errOut := newTestApp(t, h)

	if err := os.MkdirAll(filepath.Dir(a.store.statePath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(a.store.statePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if code := a.off(false); code == 0 {
		t.Fatal("off must refuse an unreadable state file")
	}
	if !strings.Contains(errOut.String(), "cannot be read") {
		t.Fatalf("missing explanation: %s", errOut.String())
	}
	// The machine must be untouched: no defaults applied.
	if len(h.setCalls) != 0 || len(h.writes) != 0 {
		t.Fatalf("refused off still changed settings: %v %v", h.setCalls, h.writes)
	}
	// And the file must survive so the user can inspect it.
	if !a.store.Exists() {
		t.Fatal("off removed a state file it could not read")
	}
}

// A state file from a newer build is unreadable in the same way and must not be
// treated as "no state".
func TestOffRefusesFutureStateVersion(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)
	if err := writeJSON(a.store.statePath(), map[string]any{"version": 99}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code := a.off(false); code == 0 {
		t.Fatal("off must refuse a future state version")
	}
	if !a.store.Exists() {
		t.Fatal("state file must be kept for manual recovery")
	}
}

// With genuinely no state file the fallback still runs, so the documented
// recovery path is unchanged.
func TestOffStillFallsBackWithoutState(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	h.acIdx[subProcessor+"|"+setProcMax] = "40"
	h.dcIdx[subProcessor+"|"+setProcMax] = "40"
	h.desktop["explorer.exe"] = false
	a, _, _ := newTestApp(t, h)

	if code := a.off(false); code != 0 {
		t.Fatalf("fallback off failed with %d", code)
	}
	if !h.desktop["explorer.exe"] {
		t.Fatal("fallback did not restore the shell")
	}
}

// `on` writes the state before marking active. If marking fails, nothing has
// been applied, so the state file must go or every later `on` is refused.
func TestOnCleansUpWhenMarkActiveFails(t *testing.T) {
	h := newFakeHost()
	h.elevated = true
	a, _, _ := newTestApp(t, h)

	// A directory at the marker path makes MarkActive's WriteFile fail, which
	// works even when the test runs as root (unlike chmod).
	if err := os.MkdirAll(a.store.markerPath(), 0o755); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}

	if code := a.on(false); code == 0 {
		t.Fatal("on should fail when the marker cannot be written")
	}
	if a.store.Exists() {
		t.Fatal("a failed on left a state file behind, so on would be refused forever")
	}
}

// --log-file is an internal handoff for the elevated child. A normal user must
// not be able to aim an admin-rights process at an arbitrary file.
func TestLogFileRejectedWithoutElevated(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, _, errOut := newTestApp(t, h)

	target := filepath.Join(t.TempDir(), "victim.txt")
	if code := a.run([]string{"on", "--log-file", target}); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a refused --log-file still created the target file")
	}
	if !strings.Contains(errOut.String(), "internal") {
		t.Fatalf("missing explanation: %s", errOut.String())
	}
}

func TestLogFileRejectedWithDanglingValue(t *testing.T) {
	h := newFakeHost()
	a, _, _ := newTestApp(t, h)
	if code := a.run([]string{"on", "--elevated", "--log-file"}); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

// The elevated child must refuse to append to a non-regular file.
func TestRedirectRejectsNonRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory-as-file semantics differ on Windows")
	}
	a := &app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if code := a.redirectOutput(t.TempDir()); code == 0 {
		t.Fatal("redirecting to a directory should fail")
	}
}

// `on` stops the shell, so the warning has to be printed before the relaunch —
// afterwards the console that could show it is gone.
func TestOnWarnsAboutLosingTheShell(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, _, errOut := newTestApp(t, h)

	a.run([]string{"on"})
	warn := errOut.String()
	if !strings.Contains(warn, "stop the desktop shell") {
		t.Fatalf("no warning about losing the desktop: %s", warn)
	}
	if !strings.Contains(warn, "off") {
		t.Fatalf("warning does not say how to get back: %s", warn)
	}
}

// With isolate_desktop = false there is nothing to warn about, and a warning
// would be noise on every benign run.
func TestNoShellWarningWhenNotIsolating(t *testing.T) {
	h := newFakeHost()
	h.elevated = false
	a, _, errOut := newTestApp(t, h)

	body := strings.Replace(exampleConfig, "isolate_desktop = true", "isolate_desktop = false", 1)
	if err := os.MkdirAll(filepath.Dir(a.configPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(a.configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	a.run([]string{"on"})
	if strings.Contains(errOut.String(), "stop the desktop shell") {
		t.Fatalf("warned about a shell that will not be stopped: %s", errOut.String())
	}
}

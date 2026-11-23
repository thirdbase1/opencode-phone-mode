package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"
)

const botLogPattern = "bot-*.log"

// lastBotActivity uses the newest bot log mtime as the activity signal. A log
// write means a message was handled or a reply was delivered, so activity is
// detected without touching the bot or its config.
func lastBotActivity(cfg Config, h host) (time.Time, bool, []error) {
	paths, err := h.Glob(filepath.Join(cfg.activityLogDir(), botLogPattern))
	if err != nil {
		return time.Time{}, false, []error{fmt.Errorf("find bot logs: %w", err)}
	}
	var newest time.Time
	var errs []error
	found := false
	for _, path := range paths {
		at, err := h.ModTime(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read bot log time %s: %w", path, err))
			continue
		}
		if !found || at.After(newest) {
			newest = at
			found = true
		}
	}
	return newest, found, errs
}

// buildIdlePlan snapshots the exact values it is about to replace. The
// snapshot is saved before any write so activity can restore it.
func buildIdlePlan(cfg Config, h host, now time.Time) (IdleState, []step, []error) {
	st := IdleState{Version: idleVersion, AppliedAt: now}
	var steps []step
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	scheme, err := h.ActiveScheme()
	if err != nil {
		note(fmt.Errorf("idle: read active scheme: %w", err))
		return st, steps, errs
	}

	saved, err := snapshotPower(h, scheme, settingCPUMax)
	if err != nil {
		note(fmt.Errorf("idle: snapshot %s: %w", settingCPUMax.label, err))
		return st, steps, errs
	}

	// Idle only lowers the ceiling. If the machine is already at or below the
	// idle cap there is nothing to do, and recording a snapshot would create a
	// restore step that changes nothing.
	current, convErr := strconv.Atoi(saved.AC)
	if convErr != nil {
		note(fmt.Errorf("idle: parse current cap %q: %w", saved.AC, convErr))
		return st, steps, errs
	}
	want := cfg.Idle.CPUMaxPercent
	if want < 1 {
		want = 1
	}
	if want >= current {
		return st, steps, errs
	}

	st.Power = append(st.Power, saved)
	target := strconv.Itoa(want)
	steps = append(steps, powerStep(h, settingCPUMax, saved, target, target, target+"%"))
	return st, steps, errs
}

// buildIdleRestorePlan writes back the values saved when the idle profile was
// applied. A failed read is reported, but the write is still attempted so a
// disappeared setting cannot block the rest.
func buildIdleRestorePlan(st IdleState, h host) ([]step, []error) {
	var steps []step
	var errs []error
	for _, value := range st.Power {
		v := value
		ps := powerSetting{subgroup: v.Subgroup, setting: v.Setting, label: v.Label}
		if ps.label == "" {
			ps.label = "power " + v.Setting
		}
		currentAC, currentDC, err := h.QueryPower(v.Scheme, v.Subgroup, v.Setting)
		if err != nil {
			errs = append(errs, fmt.Errorf("read idle %s: %w", ps.label, err))
		}
		if currentAC == v.AC && currentDC == v.DC {
			continue
		}
		steps = append(steps, step{
			setting: "restore idle " + ps.label,
			current: powerLabel(ps.label, currentAC, currentDC),
			next:    powerLabel(ps.label, v.AC, v.DC),
			apply: func() error {
				if err := h.SetPower(v.Scheme, v.Subgroup, v.Setting, v.AC, v.DC); err != nil {
					return err
				}
				return h.ApplyScheme(v.Scheme)
			},
		})
	}
	return steps, errs
}

func (a *app) idle(dryRun bool) int {
	cfg, _, err := LoadConfig(a.configPath)
	if err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	if !cfg.Idle.Enabled {
		fmt.Fprintln(a.out, "idle: disabled")
		return 0
	}
	if !a.phoneModeActive() {
		fmt.Fprintln(a.out, "idle: phone mode is not active")
		return 0
	}

	quietFor := time.Duration(cfg.Idle.QuietMinutes) * time.Minute
	now := a.now()
	last, found, activityErrs := lastBotActivity(cfg, a.host)
	a.report(activityErrs)

	saved, err := a.store.LoadIdle()
	haveIdle := err == nil
	if err != nil && !errors.Is(err, errNoState) {
		fmt.Fprintf(a.errOut, "warning: unreadable idle state: %v\n", err)
	}

	// No activity log is a reason to undo an applied profile, but not to apply
	// one: without a signal we cannot know the machine is quiet.
	if haveIdle && (!found || now.Sub(last) < quietFor || last.After(saved.AppliedAt)) {
		return a.restoreIdle(saved, dryRun)
	}
	if !found {
		fmt.Fprintf(a.out, "idle: no bot logs in %s\n", cfg.activityLogDir())
		return 0
	}
	if haveIdle {
		if a.idleAlreadyRestored(saved) {
			// The restore landed but the file survived a crash. Clear it so the
			// profile can be applied again after the next quiet period.
			if dryRun {
				fmt.Fprintf(a.out, "idle dry run: %s would be removed (already restored)\n", a.store.idlePath())
				return 0
			}
			if err := a.store.RemoveIdle(); err != nil {
				fmt.Fprintf(a.errOut, "error: remove stale idle state: %v\n", err)
				return 1
			}
			fmt.Fprintln(a.out, "idle: previous profile was already restored; cleared stale state")
			return 0
		}
		fmt.Fprintf(a.out, "idle: profile applied %s ago, still quiet\n", now.Sub(saved.AppliedAt).Round(time.Minute))
		return 0
	}
	quietTime := now.Sub(last)
	if quietTime < quietFor {
		fmt.Fprintf(a.out, "idle: bot active %s ago, waiting %s\n",
			quietTime.Round(time.Minute), (quietFor - quietTime).Round(time.Minute))
		return 0
	}
	return a.applyIdle(cfg, now, dryRun)
}

// idleAlreadyRestored reports whether every value saved before the cap is
// already back in force. IdleState.Power holds the values that existed *before*
// the profile was applied, so equality means a previous restore completed.
//
// This only happens when a restore succeeded but the process died before the
// file was removed. Reporting "still quiet" in that case would be a lie and
// would leave a stale file forever.
func (a *app) idleAlreadyRestored(saved IdleState) bool {
	if len(saved.Power) == 0 {
		return false
	}
	for _, v := range saved.Power {
		ac, dc, err := a.host.QueryPower(v.Scheme, v.Subgroup, v.Setting)
		if err != nil || ac != v.AC || dc != v.DC {
			return false
		}
	}
	return true
}

func (a *app) applyIdle(cfg Config, now time.Time, dryRun bool) int {
	saved, steps, planErrs := buildIdlePlan(cfg, a.host, now)
	if len(steps) == 0 {
		fmt.Fprintln(a.out, "idle: CPU cap is already at or below the idle profile")
		a.report(planErrs)
		if len(planErrs) > 0 {
			return 1
		}
		return 0
	}

	if dryRun {
		fmt.Fprintf(a.out, "idle dry run: would save %s and restore from it on activity\n", a.store.idlePath())
		fmt.Fprintln(a.out, "idle changes:")
		a.report(planErrs)
		runSteps(steps, true, a.out)
		fmt.Fprintf(a.out, "idle dry run: %d changes, nothing applied\n", len(steps))
		return 0
	}

	if err := a.requireAdmin(false); err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	// Save before writing: a partially applied profile must still be restorable.
	if err := a.store.SaveIdle(saved); err != nil {
		fmt.Fprintf(a.errOut, "error: save idle state: %v\n", err)
		return 1
	}
	fmt.Fprintln(a.out, "idle changes:")
	errs := append(planErrs, runSteps(steps, false, a.out)...)
	a.report(errs)
	if len(errs) > 0 {
		fmt.Fprintf(a.errOut, "idle profile partially applied; idle state kept at %s\n", a.store.idlePath())
		return 1
	}
	fmt.Fprintf(a.out, "idle profile applied (saved at %s)\n", a.store.idlePath())
	return 0
}

func (a *app) restoreIdle(saved IdleState, dryRun bool) int {
	steps, planErrs := buildIdleRestorePlan(saved, a.host)
	if dryRun {
		fmt.Fprintln(a.out, "idle restores:")
		a.report(planErrs)
		runSteps(steps, true, a.out)
		fmt.Fprintf(a.out, "idle dry run: %d restores, nothing applied\n", len(steps))
		return 0
	}
	if len(steps) == 0 {
		if err := a.store.RemoveIdle(); err != nil {
			fmt.Fprintf(a.errOut, "error: remove idle state: %v\n", err)
			return 1
		}
		fmt.Fprintln(a.out, "idle: activity detected, frequency already restored")
		return 0
	}
	if err := a.requireAdmin(false); err != nil {
		fmt.Fprintf(a.errOut, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(a.out, "idle restores:")
	errs := append(planErrs, runSteps(steps, false, a.out)...)
	if len(errs) == 0 {
		if err := a.store.RemoveIdle(); err != nil {
			errs = append(errs, fmt.Errorf("remove idle state: %w", err))
		}
	}
	a.report(errs)
	if len(errs) > 0 {
		fmt.Fprintf(a.errOut, "idle state kept at %s so restore can be retried\n", a.store.idlePath())
		return 1
	}
	fmt.Fprintln(a.out, "idle: activity detected, previous power settings restored")
	return 0
}

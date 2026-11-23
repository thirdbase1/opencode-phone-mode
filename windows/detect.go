package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Well-known power scheme GUIDs. Windows ships these on every install, though
// a user is free to delete them, so callers must check for their presence.
const (
	schemePowerSaver  = "a1841308-3541-4fab-bc81-f71556f20b4a"
	schemeBalanced    = "381b4222-f694-41f0-9685-ff5bb260df2e"
	schemePerformance = "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"
)

// Power setting coordinates. Subgroups and settings are stable identifiers;
// the human names in parentheses are what powercfg /q prints.
const (
	subProcessor = "54533251-82be-4824-96c1-47b60b740d00" // Processor power management
	subVideo     = "7516b95f-f776-4464-8c53-06167f40cc99" // Display
	subSleep     = "238c9fa8-0aad-41ed-83f4-97be242c8f20" // Sleep
	subButtons   = "4f971e89-eebd-4455-a8de-9e59040e7347" // Power buttons and lid

	setProcMax       = "bc5038f7-23e0-4960-96da-33abaf5935ec" // Maximum processor state
	setBoostMode     = "be337238-0d82-4146-a960-4f3749d470c7" // Processor performance boost mode
	setVideoIdle     = "3c0bc021-c8a8-4e07-a973-6b14cbcb2b7e" // Turn off display after
	setStandbyIdle   = "29f6c1db-86da-48c5-9fdb-f2b67b1f44da" // Sleep after
	setHibernateIdle = "9d7815a6-7ee4-497e-8888-515a05f02364" // Hibernate after
	setLidAction     = "5ca83367-6e45-459f-a27b-476b1d01c936" // Lid close action
)

// Lid action values as stored by powercfg.
var lidActionValues = map[string]string{
	"ignore":    "0",
	"sleep":     "1",
	"hibernate": "2",
	"shutdown":  "3",
}

func lidValue(action string) (string, bool) {
	v, ok := lidActionValues[action]
	return v, ok
}

// lidActionName is the inverse map used for display and fallback planning.
func lidActionName(value string) string {
	for name, v := range lidActionValues {
		if v == value {
			return name
		}
	}
	return value
}

// schemeName turns a well-known GUID into its friendly name.
func schemeName(guid string) string {
	switch strings.ToLower(guid) {
	case schemePowerSaver:
		return "power saver"
	case schemeBalanced:
		return "balanced"
	case schemePerformance:
		return "high performance"
	}
	return "custom"
}

// schemeAlias resolves "saver", "balanced", "performance", or a raw GUID.
func schemeAlias(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "saver", "powersave", "power-saver":
		return schemePowerSaver, nil
	case "balanced":
		return schemeBalanced, nil
	case "performance", "high":
		return schemePerformance, nil
	}
	if isGUID(name) {
		return strings.ToLower(strings.TrimSpace(name)), nil
	}
	return "", fmt.Errorf("unknown power_scheme %q (use saver, balanced, performance, or a GUID)", name)
}

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isGUID(value string) bool { return guidPattern.MatchString(strings.TrimSpace(value)) }

// schemeTitlePattern matches a scheme line, including its optional name.
var schemeTitlePattern = regexp.MustCompile(`(?i)GUID:\s*([0-9a-fA-F-]{36})\s*(?:\(([^)]*)\))?`)

// bareGUIDPattern matches any GUID on its own. powercfg localises its labels
// ("Power Scheme GUID:" becomes "Netzschema-GUID:"), so the GUID itself is the
// only portable part of the output.
var bareGUIDPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// parseActiveScheme pulls the GUID out of `powercfg /getactivescheme`. The
// English label is tried first; failing that, the first bare GUID is accepted
// so a localised Windows still works.
func parseActiveScheme(out string) (string, error) {
	if m := schemeTitlePattern.FindStringSubmatch(out); m != nil {
		return strings.ToLower(m[1]), nil
	}
	// Only trust the fallback when the output contains exactly one GUID; with
	// two or more there is no way to know which one is the active scheme, and
	// guessing would record a scheme the user never had.
	if all := bareGUIDPattern.FindAllString(out, -1); len(all) == 1 {
		return strings.ToLower(all[0]), nil
	}
	return "", fmt.Errorf("could not parse active power scheme from %q", strings.TrimSpace(out))
}

// parseSchemeList returns every scheme GUID the machine currently has.
func parseSchemeList(out string) []string {
	var guids []string
	for _, m := range schemeTitlePattern.FindAllStringSubmatch(out, -1) {
		guids = append(guids, strings.ToLower(m[1]))
	}
	return guids
}

// hexValuePattern matches the 0x........ form powercfg prints for every index.
var hexValuePattern = regexp.MustCompile(`0x([0-9a-fA-F]+)`)

var (
	acIndexPattern = regexp.MustCompile(`Current AC Power Setting Index:\s*0x([0-9a-fA-F]+)`)
	dcIndexPattern = regexp.MustCompile(`Current DC Power Setting Index:\s*0x([0-9a-fA-F]+)`)
)

// parsePowerIndices reads the AC and DC index pair from `powercfg /q` output.
// The values are converted to decimal so state files stay readable.
func parsePowerIndices(out string) (ac, dc string, err error) {
	am := acIndexPattern.FindStringSubmatch(out)
	dm := dcIndexPattern.FindStringSubmatch(out)
	if am == nil || dm == nil {
		// powercfg localises its labels, so fall back to the documented output
		// order: minimum, maximum, increment, AC index, DC index. Exactly five
		// hex values means the shape is the one this parser knows; anything else
		// is refused rather than guessed, because a wrong restore value would
		// silently overwrite the user's real setting.
		values := hexValuePattern.FindAllStringSubmatch(out, -1)
		if len(values) != 5 {
			return "", "", fmt.Errorf("power setting output has %d hex values, expected 5", len(values))
		}
		am = values[3]
		dm = values[4]
	}
	acNum, err := strconv.ParseUint(am[1], 16, 32)
	if err != nil {
		return "", "", fmt.Errorf("parse AC index: %w", err)
	}
	dcNum, err := strconv.ParseUint(dm[1], 16, 32)
	if err != nil {
		return "", "", fmt.Errorf("parse DC index: %w", err)
	}
	return strconv.FormatUint(acNum, 10), strconv.FormatUint(dcNum, 10), nil
}

// batteryBelowExit reports whether going headless would risk losing remote
// access. Charging and full batteries are never treated as too low.
func batteryBelowExit(threshold, pct int, status string) bool {
	if threshold <= 0 || pct <= 0 || pct >= threshold {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "charging", "full":
		return false
	}
	return true
}

// capPercent clamps a requested percentage into 1..100.
func capPercent(pct int) string {
	if pct < 1 {
		pct = 1
	}
	if pct > 100 {
		pct = 100
	}
	return strconv.Itoa(pct)
}

// turboIndex maps an on/off flag to the boost-mode index. Windows accepts
// several "enabled" variants; 1 is the plain one.
func turboIndex(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func turboEnabled(index string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(index))
	return err == nil && n != 0
}

// parseDevices reads the compressed JSON array emitted by win.ps1.
func parseDevices(out string) ([]device, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var raw []struct {
		InstanceID  string `json:"instance_id"`
		Description string `json:"description"`
		Disabled    bool   `json:"disabled"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse device list: %w", err)
	}
	devices := make([]device, 0, len(raw))
	for _, d := range raw {
		devices = append(devices, device{
			InstanceID:  d.InstanceID,
			Description: d.Description,
			Disabled:    d.Disabled,
		})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].InstanceID < devices[j].InstanceID })
	return devices, nil
}

// parseServiceState reads the JSON object emitted by `win.ps1 get-service`.
func parseServiceState(out string) (running bool, start string, missing bool, err error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return false, "", true, errors.New("empty service state")
	}
	var state struct {
		Running bool   `json:"running"`
		Start   string `json:"start"`
		Missing bool   `json:"missing"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return false, "", false, fmt.Errorf("parse service state: %w", err)
	}
	if state.Missing {
		return false, "", true, nil
	}
	start = state.Start
	if start == "" {
		start = "manual"
	}
	return state.Running, start, false, nil
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func blockedLabel(blocked bool) string {
	if blocked {
		return "disabled"
	}
	return "enabled"
}

func runningLabel(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}

// serviceLabel renders "running (auto)" so diffs show both axes.
func serviceLabel(running bool, start string) string {
	if start == "" {
		return runningLabel(running)
	}
	return runningLabel(running) + " (" + start + ")"
}

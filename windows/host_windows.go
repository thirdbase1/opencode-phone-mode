//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// realHost drives Windows through powercfg, PowerShell, and a small helper
// script. Every mutation is safe to run twice, so a restored step that is
// already correct stays a no-op rather than an error.
type realHost struct {
	helper string // path to win.ps1 next to the executable
}

func newRealHost() *realHost {
	helper := helperPath()
	return &realHost{helper: helper}
}

// helperPath returns the absolute path of win.ps1 beside the executable.
//
// Never fall back to a bare "win.ps1": this process runs elevated, and a
// relative -File argument would be resolved against the current directory, so
// running the binary from any writable folder (Downloads, %TEMP%, a USB stick)
// would execute attacker-supplied PowerShell with admin rights. An empty
// result means every helper call fails loudly instead.
func helperPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(exe), "win.ps1")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return ""
	}
	return abs
}

func (h *realHost) run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", commandError(cmd.Args, out.String(), err)
	}
	return strings.TrimSpace(out.String()), nil
}

func (h *realHost) helperRun(args ...string) (string, error) {
	if h.helper == "" || !filepath.IsAbs(h.helper) {
		return "", errors.New("win.ps1 must sit next to win-phone-mode.exe; " +
			"reinstall with `make install` so the helper is not loaded from the current directory")
	}
	full := append([]string{
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", h.helper,
	}, args...)
	return h.run("powershell.exe", full...)
}

func (h *realHost) ActiveScheme() (string, error) {
	out, err := h.run("powercfg", "/getactivescheme")
	if err != nil {
		return "", err
	}
	return parseActiveScheme(out)
}

func (h *realHost) QueryPower(scheme, subgroup, setting string) (string, string, error) {
	out, err := h.run("powercfg", "/query", scheme, subgroup, setting)
	if err != nil {
		return "", "", err
	}
	return parsePowerIndices(out)
}

// SetPower writes the AC and DC indices separately, in that order. Phone mode
// usually sets both to the same value, but a restore must put back whatever
// pair was found: many laptops cap AC and DC differently.
func (h *realHost) SetPower(scheme, subgroup, setting, ac, dc string) error {
	if _, err := h.run("powercfg", "/setacvalueindex", scheme, subgroup, setting, ac); err != nil {
		return err
	}
	_, err := h.run("powercfg", "/setdcvalueindex", scheme, subgroup, setting, dc)
	return err
}

func (h *realHost) ApplyScheme(scheme string) error {
	_, err := h.run("powercfg", "/setactive", scheme)
	return err
}

func (h *realHost) Brightness() (int, bool, error) {
	out, err := h.helperRun("get-brightness")
	if err != nil {
		return 0, false, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "unsupported" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, false, fmt.Errorf("parse brightness %q: %w", out, err)
	}
	return n, true, nil
}

func (h *realHost) SetBrightness(percent int) error {
	_, err := h.helperRun("set-brightness", strconv.Itoa(percent))
	return err
}

func (h *realHost) ServiceState(name string) (bool, string, bool, error) {
	out, err := h.helperRun("get-service", name)
	if err != nil {
		return false, "", false, err
	}
	return parseServiceState(out)
}

func (h *realHost) SetService(name, want string) error {
	_, err := h.helperRun("set-service", name, want)
	return err
}

func (h *realHost) SetServiceStart(name, mode string) error {
	_, err := h.helperRun("set-service-start", name, mode)
	return err
}

func (h *realHost) DesktopRunning(exe string) (bool, error) {
	out, err := h.helperRun("get-desktop", exe)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "running", nil
}

func (h *realHost) SetDesktop(exe, want string) error {
	_, err := h.helperRun("set-desktop", exe, want)
	return err
}

func (h *realHost) Devices(class string) ([]device, error) {
	command := "get-radios"
	if strings.EqualFold(class, "Display") {
		command = "get-displays"
	}
	out, err := h.helperRun(command)
	if err != nil {
		return nil, err
	}
	return parseDevices(out)
}

func (h *realHost) SetDevice(instanceID, want string) error {
	_, err := h.helperRun("set-device", instanceID, want)
	return err
}

// Battery uses a battery report rather than WMI so it works without the
// helper script and never needs elevation.
func (h *realHost) Battery() (int, string, bool) {
	out, err := h.run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		`$b = Get-CimInstance -ClassName Win32_Battery -ErrorAction SilentlyContinue | Select-Object -First 1;`+
			`if (-not $b) { 'none'; exit 0 };`+
			`"$($b.EstimatedChargeRemaining) $($b.BatteryStatus)"`)
	if err != nil {
		return 0, "", false
	}
	fields := strings.Fields(out)
	if len(fields) == 0 || fields[0] == "none" {
		return 0, "", false
	}
	pct, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", false
	}
	status := "discharging"
	if len(fields) > 1 {
		switch fields[1] {
		case "2", "Charging":
			status = "charging"
		case "3", "Full":
			status = "full"
		}
	}
	return pct, status, true
}

func (h *realHost) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

func (h *realHost) ModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (h *realHost) Look(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (h *realHost) User() string { return os.Getenv("USERNAME") }

// Elevated asks the token directly. Probing with a privileged no-op call would
// be ambiguous: a real failure and "not elevated" would look the same.
func (h *realHost) Elevated() bool {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()

	var elevated uint32
	var returned uint32
	err = syscall.GetTokenInformation(token, syscall.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), 4, &returned)
	if err != nil {
		return false
	}
	return elevated != 0
}

// Relaunch re-runs this binary elevated and waits for it, so the user sees one
// process and gets its exit code. Start-Process is used instead of ShellExecute
// because it reports the child's exit status, which ShellExecute does not.
// Relaunch re-runs this binary elevated and waits for it. Start-Process is
// used instead of ShellExecuteW because it reports the child's exit code,
// which ShellExecuteW does not.
//
// Output cannot be captured from an elevated child directly: a process started
// with -Verb RunAs gets its own console that closes the moment it exits, so the
// user would never see the change list or an error. -RedirectStandardOutput is
// also unsupported together with -Verb. The child therefore writes its own log
// and the parent prints it afterwards.
func (h *realHost) Relaunch(args []string) (string, string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", 1, err
	}
	logPath, err := newLogPath()
	if err != nil {
		return "", "", 1, err
	}
	defer func() { _ = os.Remove(logPath) }()

	// The child is told where to log through the environment-free flag pair,
	// so argv stays exactly the arguments the user typed.
	childArgs := append(append([]string{}, args...), flagLogFile, logPath)

	// Write-Output rather than `exit $p.ExitCode`: PowerShell must exit 0 so
	// this function can tell "the elevated run finished, here is its status"
	// apart from "elevation itself failed". With `exit`, any non-zero child
	// status makes the shell non-zero too, the captured stdout is discarded by
	// run(), and every ordinary failure is reported as an elevation failure.
	script := fmt.Sprintf(
		`$p = Start-Process -FilePath %s -ArgumentList %s -Verb RunAs -Wait -PassThru;`+
			`Write-Output $p.ExitCode`,
		psQuote(exe), psQuote(quoteWindowsArgs(childArgs)))

	out, runErr := h.run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	logged, _ := os.ReadFile(logPath)
	output := string(logged)
	if runErr != nil {
		// The most common failure here is the user declining the UAC prompt,
		// which PowerShell reports as a non-zero exit with a short message.
		// h.run includes the captured output in the error, so match on that.
		msg := strings.ToLower(runErr.Error())
		if strings.Contains(msg, "canceled") || strings.Contains(msg, "cancelled") ||
			strings.Contains(msg, "denied") || strings.Contains(msg, "0x800704c7") {
			return output, "", 1, errors.New("elevation was declined")
		}
		return output, "", 1, fmt.Errorf("elevation failed: %w", runErr)
	}
	code, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		// PowerShell completed but printed something unexpected. Treat the run
		// as successful rather than inventing a failure status.
		return output, "", 0, nil
	}
	return output, "", code, nil
}

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// device is a PnP device as reported by Get-PnpDevice.
type device struct {
	InstanceID  string
	Description string
	Disabled    bool
}

// host is the whole machine surface the planner touches. Tests substitute a
// fake, so snapshot/restore logic runs without a Windows box or admin rights.
//
// Every method is a domain operation rather than a raw sysfs read. That is the
// main change from the Linux original, where "read a file, write a file" was a
// good enough abstraction: on Windows the same logical setting is reached
// through powercfg, sc.exe, WMI, or PnP, and each needs its own parsing.
type host interface {
	// ActiveScheme returns the GUID of the active power scheme.
	ActiveScheme() (string, error)
	// QueryPower returns the current AC and DC indices for one setting.
	QueryPower(scheme, subgroup, setting string) (ac, dc string, err error)
	// SetPower writes the AC and DC indices separately. They must be restored
	// independently: a machine commonly caps AC at 60% and DC at 40%, and
	// collapsing the pair to one value silently rewrites the user's setting.
	SetPower(scheme, subgroup, setting, ac, dc string) error
	// ApplyScheme makes a scheme active so pending indices take effect.
	ApplyScheme(scheme string) error

	// Brightness returns the panel brightness percentage. supported is false
	// on desktops and on panels without a WMI brightness interface.
	Brightness() (percent int, supported bool, err error)
	SetBrightness(percent int) error

	// ServiceState reports a service's run state and start mode.
	ServiceState(name string) (running bool, start string, missing bool, err error)
	// SetService moves a service toward "running" or "stopped".
	SetService(name, want string) error
	// SetServiceStart changes a service's start mode: "auto", "manual", or
	// "disabled". A disabled keep-service cannot be started at all, so the
	// mode must be handled, not just recorded.
	SetServiceStart(name, mode string) error

	// DesktopRunning reports whether a shell process is up.
	DesktopRunning(exe string) (bool, error)
	// SetDesktop moves a shell process toward "running" or "stopped".
	SetDesktop(exe, want string) error

	// Devices enumerates PnP devices by class ("Bluetooth" or "Display").
	Devices(class string) ([]device, error)
	// SetDevice enables or disables one device.
	SetDevice(instanceID, want string) error

	// Battery returns charge percentage and status word; ok is false when the
	// machine has no battery.
	Battery() (percent int, status string, ok bool)

	Glob(pattern string) ([]string, error)
	ModTime(path string) (time.Time, error)
	Look(name string) bool
	User() string
	Elevated() bool
	// Relaunch re-runs this binary elevated and returns its exit code plus the
	// output it produced, so the caller can print it from the original console.
	Relaunch(args []string) (stdout, stderr string, code int, err error)
}

func commandError(args []string, output string, err error) error {
	msg := strings.TrimSpace(output)
	if msg == "" {
		return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, msg)
}

// trimLines splits command output and drops empty lines.
func trimLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// quoteWindowsArg renders one argument for a Windows command line, following
// the rules the C runtime uses to split argv. Backslashes are only special
// immediately before a quote or at the end of the argument, so they are
// doubled only there.
//
// This exists because the elevated relaunch hands a single command-line string
// to Start-Process. Getting it wrong silently changes what the child receives:
// a naive join would split "C:\Program Files\x" into two arguments.
func quoteWindowsArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}

	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			// Escape every pending backslash, then the quote itself.
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			b.WriteRune(r)
		}
	}
	// Trailing backslashes must be doubled so they do not escape the closing
	// quote and swallow the next argument.
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}

// quoteWindowsArgs joins arguments into one command-line string.
func quoteWindowsArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, quoteWindowsArg(a))
	}
	return strings.Join(quoted, " ")
}

// psQuote wraps a value in a PowerShell single-quoted literal, where the only
// escape is a doubled quote. Single quotes stop variable and subexpression
// expansion, so an argument cannot inject commands into the relaunch script.
func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// newLogPath returns a fresh path for the elevated child's output. It sits in
// the user's own temp directory so a non-elevated parent can always read it
// back. The file is created empty so the child can append safely even if it
// fails before writing anything.
func newLogPath() (string, error) {
	f, err := os.CreateTemp("", "win-phone-mode-*.log")
	if err != nil {
		return "", fmt.Errorf("create log file: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close log file: %w", err)
	}
	return path, nil
}

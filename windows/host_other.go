//go:build !windows

package main

import (
	"errors"
	"time"
)

// errNotWindows is returned by every mutating call on a non-Windows host. The
// tool only ships for Windows; this stub exists so `go test` and `go vet` can
// run on other platforms without a build tag split in every file.
var errNotWindows = errors.New("win-phone-mode only runs on Windows")

type stubHost struct{}

func newRealHost() *stubHost { return &stubHost{} }

func (s *stubHost) ActiveScheme() (string, error) {
	return "", errNotWindows
}

func (s *stubHost) QueryPower(string, string, string) (string, string, error) {
	return "", "", errNotWindows
}

func (s *stubHost) SetPower(string, string, string, string, string) error { return errNotWindows }
func (s *stubHost) ApplyScheme(string) error                              { return errNotWindows }

func (s *stubHost) Brightness() (int, bool, error) { return 0, false, errNotWindows }
func (s *stubHost) SetBrightness(int) error        { return errNotWindows }

func (s *stubHost) ServiceState(string) (bool, string, bool, error) {
	return false, "", false, errNotWindows
}

func (s *stubHost) SetService(string, string) error      { return errNotWindows }
func (s *stubHost) SetServiceStart(string, string) error { return errNotWindows }
func (s *stubHost) DesktopRunning(string) (bool, error)  { return false, errNotWindows }
func (s *stubHost) SetDesktop(string, string) error      { return errNotWindows }

func (s *stubHost) Devices(string) ([]device, error) { return nil, errNotWindows }
func (s *stubHost) SetDevice(string, string) error   { return errNotWindows }

func (s *stubHost) Battery() (int, string, bool) { return 0, "", false }

func (s *stubHost) Glob(string) ([]string, error)     { return nil, nil }
func (s *stubHost) ModTime(string) (time.Time, error) { return time.Time{}, errNotWindows }
func (s *stubHost) Look(string) bool                  { return false }
func (s *stubHost) User() string                      { return "" }
func (s *stubHost) Elevated() bool                    { return false }
func (s *stubHost) Relaunch([]string) (string, string, int, error) {
	return "", "", 1, errNotWindows
}

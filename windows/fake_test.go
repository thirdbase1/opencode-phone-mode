package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

// fakeHost is an in-memory Windows box. It records every mutation in order so
// tests can assert that restore writes back exactly what was read.
type fakeHost struct {
	scheme    string
	schemes   []string
	acIdx     map[string]string // subgroup|setting -> AC value
	dcIdx     map[string]string // subgroup|setting -> DC value
	bright    int
	brightOK  bool
	services  map[string]*fakeService
	desktop   map[string]bool
	devices   map[string][]device
	battery   int
	batterySt string
	batteryOK bool

	runErr     map[string]error
	queryErr   map[string]error
	writeErr   map[string]error
	devicesErr error

	writes       []string
	runs         []string
	setCalls     []string
	globs        map[string][]string
	mtimes       map[string]time.Time
	userName     string
	elevated     bool
	relaunchArgs []string
	relaunchErr  error
}

type fakeService struct {
	running bool
	start   string
	missing bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		scheme:  schemeBalanced,
		schemes: []string{schemeBalanced, schemePowerSaver, schemePerformance},
		acIdx: map[string]string{
			subProcessor + "|" + setProcMax:   "100",
			subProcessor + "|" + setBoostMode: "2",
			subVideo + "|" + setVideoIdle:     "600",
			subSleep + "|" + setStandbyIdle:   "1200",
			subSleep + "|" + setHibernateIdle: "0",
			subButtons + "|" + setLidAction:   "1",
		},
		dcIdx: map[string]string{
			subProcessor + "|" + setProcMax:   "100",
			subProcessor + "|" + setBoostMode: "2",
			subVideo + "|" + setVideoIdle:     "300",
			subSleep + "|" + setStandbyIdle:   "900",
			subSleep + "|" + setHibernateIdle: "0",
			subButtons + "|" + setLidAction:   "1",
		},
		bright:   80,
		brightOK: true,
		services: map[string]*fakeService{
			"OpenCodeServe":    {running: true, start: "auto"},
			"OpenCodeTelegram": {running: false, start: "manual"},
		},
		desktop:   map[string]bool{"explorer.exe": true},
		battery:   80,
		batterySt: "discharging",
		batteryOK: true,
		devices: map[string][]device{
			"Bluetooth": {
				{InstanceID: `BTHENUM\DEV_A`, Description: "Headset", Disabled: false},
				{InstanceID: `BTHENUM\DEV_B`, Description: "Mouse", Disabled: false},
			},
			"Display": {
				{InstanceID: `PCI\VEN_10DE&DEV_1F91`, Description: "NVIDIA GeForce RTX 3050", Disabled: false},
			},
		},
		runErr:   map[string]error{},
		queryErr: map[string]error{},
		writeErr: map[string]error{},
		globs:    map[string][]string{},
		mtimes:   map[string]time.Time{},
	}
}

func (f *fakeHost) ActiveScheme() (string, error) { return f.scheme, nil }

func (f *fakeHost) QueryPower(scheme, subgroup, setting string) (string, string, error) {
	key := subgroup + "|" + setting
	if err := f.queryErr[key]; err != nil {
		return "", "", err
	}
	ac, ok := f.acIdx[key]
	if !ok {
		return "", "", fmt.Errorf("open power setting %s: %w", key, fs.ErrNotExist)
	}
	dc := f.dcIdx[key]
	return ac, dc, nil
}

func (f *fakeHost) SetPower(scheme, subgroup, setting, ac, dc string) error {
	key := subgroup + "|" + setting
	if err := f.writeErr[key]; err != nil {
		return err
	}
	// AC and DC are independent, so a restore of 100/75 must not become
	// 100/100. The fake keeps them apart so that bug is visible in tests.
	f.acIdx[key] = ac
	f.dcIdx[key] = dc
	f.setCalls = append(f.setCalls, key+"="+ac+"/"+dc)
	return nil
}

func (f *fakeHost) ApplyScheme(scheme string) error {
	f.scheme = scheme
	f.runs = append(f.runs, "setactive "+scheme)
	return nil
}

func (f *fakeHost) Brightness() (int, bool, error) { return f.bright, f.brightOK, nil }

func (f *fakeHost) SetBrightness(percent int) error {
	f.bright = percent
	f.writes = append(f.writes, fmt.Sprintf("brightness=%d", percent))
	return nil
}

func (f *fakeHost) ServiceState(name string) (bool, string, bool, error) {
	svc, ok := f.services[name]
	if !ok || svc.missing {
		return false, "", true, nil
	}
	return svc.running, svc.start, false, nil
}

func (f *fakeHost) SetService(name, want string) error {
	svc, ok := f.services[name]
	if !ok {
		return fmt.Errorf("service %s not installed", name)
	}
	svc.running = want == "running"
	f.writes = append(f.writes, "service:"+name+"="+want)
	return nil
}

func (f *fakeHost) SetServiceStart(name, mode string) error {
	svc, ok := f.services[name]
	if !ok {
		return fmt.Errorf("service %s not installed", name)
	}
	svc.start = mode
	f.writes = append(f.writes, "service-start:"+name+"="+mode)
	return nil
}

func (f *fakeHost) DesktopRunning(exe string) (bool, error) { return f.desktop[exe], nil }

func (f *fakeHost) SetDesktop(exe, want string) error {
	f.desktop[exe] = want == "running"
	f.writes = append(f.writes, "desktop:"+exe+"="+want)
	return nil
}

func (f *fakeHost) Devices(class string) ([]device, error) {
	if f.devicesErr != nil {
		return nil, f.devicesErr
	}
	return f.devices[class], nil
}

func (f *fakeHost) SetDevice(instanceID, want string) error {
	for class, list := range f.devices {
		for i := range list {
			if list[i].InstanceID == instanceID {
				list[i].Disabled = want == "disabled"
				f.devices[class] = list
				f.writes = append(f.writes, "device:"+instanceID+"="+want)
				return nil
			}
		}
	}
	return fmt.Errorf("device %s not found", instanceID)
}

func (f *fakeHost) Battery() (int, string, bool) { return f.battery, f.batterySt, f.batteryOK }

func (f *fakeHost) Glob(pattern string) ([]string, error) { return f.globs[pattern], nil }

func (f *fakeHost) ModTime(path string) (time.Time, error) {
	at, ok := f.mtimes[path]
	if !ok {
		return time.Time{}, fmt.Errorf("stat %s: %w", path, fs.ErrNotExist)
	}
	return at, nil
}

func (f *fakeHost) Look(name string) bool { return false }

func (f *fakeHost) User() string {
	if f.userName == "" {
		return "tester"
	}
	return f.userName
}

func (f *fakeHost) Elevated() bool { return f.elevated }
func (f *fakeHost) Relaunch(args []string) (string, string, int, error) {
	f.relaunchArgs = args
	return "", "", 0, nil
}

func (f *fakeHost) wrote(substr string) bool {
	for _, w := range f.writes {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func (f *fakeHost) powerSet(subgroup, setting, value string) bool {
	needle := subgroup + "|" + setting + "="
	for _, c := range f.setCalls {
		if strings.HasPrefix(c, needle) && strings.TrimPrefix(c, needle) == value+"/"+value {
			return true
		}
	}
	return false
}

var errBoom = errors.New("boom")

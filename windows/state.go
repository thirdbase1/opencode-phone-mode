package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	stateVersion = 1
	idleVersion  = 1
)

var errNoState = errors.New("no state file")

// PowerValue is one Windows power setting (a powercfg index pair) in a scheme.
// It is the direct analogue of a sysfs cpufreq file: read first, then written
// back verbatim on restore. Subgroup and Setting are stable GUIDs; Label is
// only for display.
type PowerValue struct {
	Scheme   string `json:"scheme"`
	Subgroup string `json:"subgroup"`
	Setting  string `json:"setting"`
	AC       string `json:"ac_index"`
	DC       string `json:"dc_index"`
	Label    string `json:"label,omitempty"`
}

type ServiceState struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Start   string `json:"start,omitempty"` // auto, manual, disabled
	Missing bool   `json:"missing,omitempty"`
}

type RadioState struct {
	InstanceID  string `json:"instance_id"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Disabled    bool   `json:"disabled"`
}

// State is written before phone mode changes anything. off restores exactly
// these values and then deletes the file.
type State struct {
	Version      int       `json:"version"`
	SavedAt      time.Time `json:"saved_at"`
	ActiveScheme string    `json:"active_scheme,omitempty"`
	// SessionHadDesktop records whether an interactive desktop existed when
	// phone mode was entered, so off knows whether to bring the shell back.
	// A pointer because nil (an older state file) must mean "unknown, assume
	// yes": restoring a shell that was never there is harmless, whereas
	// skipping it leaves the user with no desktop at all.
	SessionHadDesktop *bool          `json:"session_had_desktop,omitempty"`
	Brightness        *int           `json:"brightness,omitempty"`
	Desktop           *ServiceState  `json:"desktop,omitempty"`
	Power             []PowerValue   `json:"power,omitempty"`
	Radios            []RadioState   `json:"radios,omitempty"`
	Services          []ServiceState `json:"services,omitempty"`
}

// IdleState records the exact CPU values replaced by the quiet-time profile so
// activity can restore them without guessing.
type IdleState struct {
	Version   int          `json:"version"`
	AppliedAt time.Time    `json:"applied_at"`
	Power     []PowerValue `json:"power,omitempty"`
}

type stateStore struct {
	dir string
}

func (s stateStore) statePath() string  { return filepath.Join(s.dir, "state.json") }
func (s stateStore) markerPath() string { return filepath.Join(s.dir, "active") }
func (s stateStore) idlePath() string   { return filepath.Join(s.dir, "idle.json") }

func (s stateStore) Save(st State) error         { return writeJSON(s.statePath(), st) }
func (s stateStore) SaveIdle(st IdleState) error { return writeJSON(s.idlePath(), st) }

func (s stateStore) Load() (State, error) {
	var st State
	if err := readJSON(s.statePath(), &st); err != nil {
		return State{}, err
	}
	if st.Version != stateVersion {
		return State{}, fmt.Errorf("state version %d is not supported", st.Version)
	}
	return st, nil
}

func (s stateStore) LoadIdle() (IdleState, error) {
	var st IdleState
	if err := readJSON(s.idlePath(), &st); err != nil {
		return IdleState{}, err
	}
	if st.Version != idleVersion {
		return IdleState{}, fmt.Errorf("idle state version %d is not supported", st.Version)
	}
	return st, nil
}

func (s stateStore) RemoveIdle() error { return removeFile(s.idlePath()) }
func (s stateStore) Remove() error     { return removeFile(s.statePath()) }

func (s stateStore) IdleExists() bool {
	_, err := os.Stat(s.idlePath())
	return err == nil
}

func (s stateStore) Exists() bool {
	_, err := os.Stat(s.statePath())
	return err == nil
}

func writeJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errNoState
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	return nil
}

func removeFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s stateStore) MarkActive() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.markerPath(), nil, 0o644)
}

func (s stateStore) ClearActive() error { return removeFile(s.markerPath()) }

func (s stateStore) Active() bool {
	_, err := os.Stat(s.markerPath())
	return err == nil
}

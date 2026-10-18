// Package state keeps what rig remembers between sessions in ~/.rig/state.json.
// Rig writes this file. Users write agent files.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// MaxRecent is how many models /model remembers.
const MaxRecent = 10

// DefaultSmartZone is the context size where models still work well, in tokens.
// Above it, answers tend to get worse even when the model accepts more.
const DefaultSmartZone = 200_000

type State struct {
	RecentModels    []string `json:"recent_models,omitempty"`     // newest first
	SmartZoneTokens int      `json:"smart_zone_tokens,omitempty"` // 0 means DefaultSmartZone
}

// Load reads the state file. A missing file is an empty state, not an error.
func Load(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// Save writes to a temp file first, so a crash never leaves half a file.
func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AddRecent moves id to the front of the recent list.
func (s *State) AddRecent(id string) {
	list := slices.DeleteFunc(slices.Clone(s.RecentModels), func(x string) bool { return x == id })
	s.RecentModels = append([]string{id}, list...)
	if len(s.RecentModels) > MaxRecent {
		s.RecentModels = s.RecentModels[:MaxRecent]
	}
}

func (s State) SmartZone() int {
	if s.SmartZoneTokens > 0 {
		return s.SmartZoneTokens
	}
	return DefaultSmartZone
}

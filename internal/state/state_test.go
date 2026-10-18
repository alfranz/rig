package state

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadSaveRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, err := Load(path)
	if err != nil || len(s.RecentModels) != 0 || s.SmartZone() != DefaultSmartZone {
		t.Fatalf("missing file: %+v, %v", s, err)
	}

	for i := range 12 {
		s.AddRecent(fmt.Sprintf("m%d", i))
	}
	s.AddRecent("m5") // moves to the front, no duplicate
	if len(s.RecentModels) != MaxRecent || s.RecentModels[0] != "m5" || s.RecentModels[1] != "m11" {
		t.Fatalf("recent = %v", s.RecentModels)
	}
	if n := len(slices.DeleteFunc(slices.Clone(s.RecentModels), func(x string) bool { return x != "m5" })); n != 1 {
		t.Errorf("m5 appears %d times", n)
	}

	s.SmartZoneTokens = 100_000
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !slices.Equal(got.RecentModels, s.RecentModels) || got.SmartZone() != 100_000 {
		t.Fatalf("reloaded %+v, %v", got, err)
	}
}

package store

import (
	"testing"

	"github.com/ondrejeder/savesync-ally-3/internal/engine"
)

func TestStoreRoundTrip(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	s, err := Open(cfgDir, dataDir, "PC")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGame(Game{ID: "g1", Name: "Hades", Root: `C:\saves`}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState("g1", engine.GameState{Baseline: engine.Baseline{"a.sav": "abc"}}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(cfgDir, dataDir, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	cfg := reopened.Config()
	if cfg.Device != "PC" || len(cfg.Games) != 1 || cfg.Games[0].Name != "Hades" {
		t.Fatalf("config = %+v", cfg)
	}
	st, err := reopened.State("g1")
	if err != nil || st.Baseline["a.sav"] != "abc" {
		t.Fatalf("state = %v, %v", st, err)
	}

	if err := reopened.RemoveGame("g1"); err != nil {
		t.Fatal(err)
	}
	if st, _ := reopened.State("g1"); len(st.Baseline) != 0 {
		t.Fatalf("baseline after remove = %v", st)
	}
}

func TestLegacyBaselineFile(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	s, _ := Open(cfgDir, dataDir, "PC")
	if err := WriteFileAtomic(s.baselinePath("old"), []byte(`{"a.sav":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.State("old")
	if err != nil || st.Baseline["a.sav"] != "abc" {
		t.Fatalf("legacy state = %+v, %v", st, err)
	}
}

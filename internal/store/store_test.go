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
	if err := s.SaveBaseline("g1", engine.Baseline{"a.sav": "abc"}); err != nil {
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
	base, err := reopened.Baseline("g1")
	if err != nil || base["a.sav"] != "abc" {
		t.Fatalf("baseline = %v, %v", base, err)
	}

	if err := reopened.RemoveGame("g1"); err != nil {
		t.Fatal(err)
	}
	if base, _ := reopened.Baseline("g1"); len(base) != 0 {
		t.Fatalf("baseline after remove = %v", base)
	}
}

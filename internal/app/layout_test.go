package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestShiftBetween(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Game", "Profile")
	j := func(p ...string) string { return filepath.Join(append([]string{base}, p...)...) }
	cases := []struct {
		name, old, new string
		down           string
		up             int
	}{
		{"same", base, base, "", 0},
		{"one down", base, j("Saves"), "Saves", 0},
		{"two down", base, j("Saves", "Slot"), "Saves/Slot", 0},
		{"three down", base, j("a", "b", "c"), "", 0},
		{"one up", j("Saves"), base, "", 1},
		{"two up", j("Saves", "Slot"), base, "", 2},
		{"three up", j("a", "b", "c"), base, "", 0},
		{"sibling", j("Saves"), j("Other"), "", 0},
		{"prefix is not a parent", base, base + "2", "", 0},
	}
	for _, c := range cases {
		down, up := shiftBetween(c.old, c.new)
		if down != c.down || up != c.up {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", c.name, down, up, c.down, c.up)
		}
	}
}

func TestLayoutMark(t *testing.T) {
	m := layoutMark{by: "ROG Ally", at: "2026-11-06T10:00:00Z", pending: true, include: true, down: "Saves/Slot"}
	raw := m.String()
	got := parseLayout(raw)
	if got != m {
		t.Fatalf("round trip: got %+v, want %+v", got, m)
	}
	got.pending = false
	if layoutID(got.String()) != layoutID(raw) {
		t.Error("finishing a change must keep its id")
	}
	// Markers written by 1.3.0 have no flags.
	if old := parseLayout("PC|2026-11-06T09:00:00Z"); old.by != "PC" || old.pending || old.down != "" || old.up != 0 {
		t.Errorf("old marker: %+v", old)
	}
	long := newLayout(strings.Repeat("ž", 40), "/a", "/a/"+strings.Repeat("x", 60), false)
	if len(long.String()) > layoutValueMax || long.down != "" {
		t.Errorf("too long marker kept its shift: %q", long.String())
	}
}

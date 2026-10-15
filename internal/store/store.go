// Package store keeps this device's configuration and per-game baselines.
// Nothing here is shared between devices.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ondrejeder/savesync-ally-3/internal/engine"
)

// Game is a game set up on this device. ID is the game's Drive folder ID.
type Game struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Root    string   `json:"root"`
	Include []string `json:"include,omitempty"`
}

type Config struct {
	Device   string `json:"device"`
	Versions int    `json:"versions,omitempty"`
	Games    []Game `json:"games"`
}

// DefaultVersions is how many cloud versions are kept unless configured.
const DefaultVersions = 3

type Store struct {
	mu       sync.Mutex
	path     string
	stateDir string
	cfg      Config
}

// Open loads config.json from configDir; baselines live in dataDir/state.
func Open(configDir, dataDir, defaultDevice string) (*Store, error) {
	s := &Store{
		path:     filepath.Join(configDir, "config.json"),
		stateDir: filepath.Join(dataDir, "state"),
		cfg:      Config{Device: defaultDevice, Games: []Game{}},
	}
	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.cfg); err != nil {
			return nil, err
		}
		if s.cfg.Device == "" {
			s.cfg.Device = defaultDevice
		}
		if s.cfg.Games == nil {
			s.cfg.Games = []Game{}
		}
	case errors.Is(err, os.ErrNotExist):
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	cfg.Games = append([]Game(nil), s.cfg.Games...)
	return cfg
}

func (s *Store) Game(id string) (Game, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.cfg.Games {
		if g.ID == id {
			return g, true
		}
	}
	return Game{}, false
}

// PutGame inserts or replaces a game by ID.
func (s *Store) PutGame(game Game) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Games {
		if s.cfg.Games[i].ID == game.ID {
			s.cfg.Games[i] = game
			return s.saveLocked()
		}
	}
	s.cfg.Games = append(s.cfg.Games, game)
	return s.saveLocked()
}

// RemoveGame stops syncing a game on this device. Files are left untouched.
func (s *Store) RemoveGame(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Games[:0]
	for _, g := range s.cfg.Games {
		if g.ID != id {
			next = append(next, g)
		}
	}
	s.cfg.Games = next
	_ = os.Remove(s.baselinePath(id))
	return s.saveLocked()
}

func (s *Store) SetDevice(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Device = name
	return s.saveLocked()
}

// State returns what this device remembers about a game (baseline, hash
// cache, recent downloads). Files from older builds held only the baseline.
func (s *Store) State(id string) (engine.GameState, error) {
	var st engine.GameState
	data, err := os.ReadFile(s.baselinePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return engine.GameState{Baseline: engine.Baseline{}}, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil || st.Baseline == nil {
		var legacy engine.Baseline
		if json.Unmarshal(data, &legacy) == nil {
			return engine.GameState{Baseline: legacy}, nil
		}
		if err != nil {
			return st, err
		}
		st.Baseline = engine.Baseline{}
	}
	return st, nil
}

func (s *Store) SaveState(id string, st engine.GameState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.baselinePath(id), data, 0o644)
}

// ResetBaseline forgets the last synced state (e.g. after changing the
// folder), so differences are asked about instead of propagated.
func (s *Store) ResetBaseline(id string) error {
	return s.SaveState(id, engine.GameState{Baseline: engine.Baseline{}})
}

// ChangesToken is the saved Drive Changes API position.
func (s *Store) ChangesToken() string {
	data, _ := os.ReadFile(filepath.Join(s.stateDir, "drive-changes.token"))
	return strings.TrimSpace(string(data))
}

func (s *Store) SaveChangesToken(token string) error {
	return WriteFileAtomic(filepath.Join(s.stateDir, "drive-changes.token"), []byte(token), 0o644)
}

// Versions is how many cloud versions of each game to keep (1-5).
func (s *Store) Versions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Versions < 1 || s.cfg.Versions > 5 {
		return DefaultVersions
	}
	return s.cfg.Versions
}

func (s *Store) SetVersions(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Versions = n
	return s.saveLocked()
}

func (s *Store) baselinePath(id string) string {
	return filepath.Join(s.stateDir, id+".json")
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, data, 0o600)
}

// WriteFileAtomic writes via a temp file and rename, so a crash never leaves
// a truncated file behind.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

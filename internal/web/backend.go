package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Backend is implemented by the app (and by the demo). Methods returning a
// string return a short user-facing confirmation.
type Backend interface {
	State() State
	Action(kind, gameID string) (string, error)
	Browse(path string) (Listing, error)
	AddGame(req GameRequest) (string, error)
	SetupGame(id string, req GameRequest) (string, error)
	EditGame(id string, req GameRequest) (string, error)
	RemoveGame(id string, deleteCloud bool) (string, error)
	Backups(id string) ([]Backup, error)
	RestoreBackup(id, name string) (string, error)
	Versions(id string) ([]Version, error)
	RestoreVersion(id, versionID string) (string, error)
	SaveSettings(s Settings) (string, error)
	Hint(id, root string) (Hint, error)
	RecentFolders(minutes int) (RecentResult, error)
}

// RecentResult lists folders with recently changed files (newest first),
// to find where a game just saved.
type RecentResult struct {
	Folders []RecentFolder `json:"folders"`
	// Partial: the time budget ran out before every location was searched.
	Partial bool `json:"partial,omitempty"`
}

type RecentFolder struct {
	Path     string   `json:"path"`
	Label    string   `json:"label,omitempty"` // game of the Proton prefix
	Files    int      `json:"files"`
	Modified string   `json:"modified"`
	Sample   []string `json:"sample"`
}

// Hint helps set up a cloud game on a new device: how the other devices
// have it set up, what the cloud contains and whether root fits.
type Hint struct {
	Devices []DeviceHint `json:"devices"`
	Files   int          `json:"files"`
	Sample  []string     `json:"sample"`
	Check   *PathCheck   `json:"check,omitempty"`
}

type DeviceHint struct {
	Device  string   `json:"device"`
	Root    string   `json:"root"`
	Include []string `json:"include,omitempty"`
	OS      string   `json:"os"`
	Updated string   `json:"updated"`
	// Local is the same path on this device, when it exists here.
	Local string `json:"local,omitempty"`
}

// PathCheck compares a chosen folder with the cloud file list.
type PathCheck struct {
	// ok: cloud files found in the folder; empty: nothing there yet;
	// deeper / higher: the files are in Suggest; other: unrelated files.
	Status  string `json:"status"`
	Matched int    `json:"matched"`
	Suggest string `json:"suggest,omitempty"`
}

// Version is a cloud snapshot of a whole game.
type Version struct {
	ID      string `json:"id"`
	Device  string `json:"device"`
	Created string `json:"created"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

type GameRequest struct {
	Name    string   `json:"name"`
	Root    string   `json:"root"`
	Include []string `json:"include"`
	// Keep: which side wins when the (new) folder already has files, "local"
	// or "cloud". Required on setup and on a folder or filter change, so
	// saves are never merged by accident.
	Keep string `json:"keep,omitempty"`
}

type Settings struct {
	Device   string `json:"device"`
	Versions int    `json:"versions"`
}

// Listing is one folder in the folder picker.
type Listing struct {
	Path   string  `json:"path"`
	Parent string  `json:"parent,omitempty"`
	Dirs   []Entry `json:"dirs"`
	Files  []Entry `json:"files"`
	More   int     `json:"more,omitempty"` // files not listed
	Places []Entry `json:"places"`
}

type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Label string `json:"label,omitempty"` // friendlier name, e.g. Steam game for a compatdata ID
	Icon  string `json:"icon,omitempty"`
}

type Backup struct {
	Name    string `json:"name"`
	Created string `json:"created"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// ErrNotImplemented is returned by backends that do not support an action.
var ErrNotImplemented = errors.New("v tomto režimu není k dispozici")

func (s *Server) routes(mux *http.ServeMux) {
	mux.Handle("GET /api/fs", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listing, err := s.Backend.Browse(r.URL.Query().Get("path"))
		reply(w, listing, err)
	})))
	mux.Handle("POST /api/games", s.auth(jsonHandler(func(req GameRequest, _ *http.Request) (any, error) {
		return message(s.Backend.AddGame(req))
	})))
	mux.Handle("GET /api/recent", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		minutes, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
		res, err := s.Backend.RecentFolders(minutes)
		if res.Folders == nil {
			res.Folders = []RecentFolder{}
		}
		reply(w, res, err)
	})))
	mux.Handle("GET /api/games/{id}/hint", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hint, err := s.Backend.Hint(r.PathValue("id"), r.URL.Query().Get("root"))
		if hint.Devices == nil {
			hint.Devices = []DeviceHint{}
		}
		if hint.Sample == nil {
			hint.Sample = []string{}
		}
		reply(w, hint, err)
	})))
	mux.Handle("POST /api/games/{id}/setup", s.auth(jsonHandler(func(req GameRequest, r *http.Request) (any, error) {
		return message(s.Backend.SetupGame(r.PathValue("id"), req))
	})))
	mux.Handle("POST /api/games/{id}", s.auth(jsonHandler(func(req GameRequest, r *http.Request) (any, error) {
		return message(s.Backend.EditGame(r.PathValue("id"), req))
	})))
	mux.Handle("DELETE /api/games/{id}", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := message(s.Backend.RemoveGame(r.PathValue("id"), r.URL.Query().Get("cloud") == "true"))
		reply(w, v, err)
	})))
	mux.Handle("GET /api/games/{id}/backups", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backups, err := s.Backend.Backups(r.PathValue("id"))
		if backups == nil {
			backups = []Backup{}
		}
		reply(w, backups, err)
	})))
	mux.Handle("POST /api/games/{id}/backups/restore", s.auth(jsonHandler(func(req struct{ Name string }, r *http.Request) (any, error) {
		return message(s.Backend.RestoreBackup(r.PathValue("id"), req.Name))
	})))
	mux.Handle("GET /api/games/{id}/versions", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		versions, err := s.Backend.Versions(r.PathValue("id"))
		if versions == nil {
			versions = []Version{}
		}
		reply(w, versions, err)
	})))
	mux.Handle("POST /api/games/{id}/versions/restore", s.auth(jsonHandler(func(req struct{ ID string }, r *http.Request) (any, error) {
		return message(s.Backend.RestoreVersion(r.PathValue("id"), req.ID))
	})))
	mux.Handle("POST /api/settings", s.auth(jsonHandler(func(req Settings, _ *http.Request) (any, error) {
		return message(s.Backend.SaveSettings(req))
	})))
}

func message(msg string, err error) (any, error) {
	return map[string]string{"message": msg}, err
}

func jsonHandler[T any](fn func(T, *http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req T
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Neplatný požadavek."})
			return
		}
		v, err := fn(req, r)
		reply(w, v, err)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// Unimplemented can be embedded by partial backends (the demo).
type Unimplemented struct{}

func (Unimplemented) Browse(string) (Listing, error)                { return Listing{}, ErrNotImplemented }
func (Unimplemented) AddGame(GameRequest) (string, error)           { return "", ErrNotImplemented }
func (Unimplemented) SetupGame(string, GameRequest) (string, error) { return "", ErrNotImplemented }
func (Unimplemented) EditGame(string, GameRequest) (string, error)  { return "", ErrNotImplemented }
func (Unimplemented) RemoveGame(string, bool) (string, error)       { return "", ErrNotImplemented }
func (Unimplemented) Backups(string) ([]Backup, error)              { return nil, ErrNotImplemented }
func (Unimplemented) RestoreBackup(string, string) (string, error)  { return "", ErrNotImplemented }
func (Unimplemented) Versions(string) ([]Version, error)            { return nil, ErrNotImplemented }
func (Unimplemented) RestoreVersion(string, string) (string, error) { return "", ErrNotImplemented }
func (Unimplemented) SaveSettings(Settings) (string, error)         { return "", ErrNotImplemented }
func (Unimplemented) Hint(string, string) (Hint, error)             { return Hint{}, ErrNotImplemented }
func (Unimplemented) RecentFolders(int) (RecentResult, error) {
	return RecentResult{}, ErrNotImplemented
}

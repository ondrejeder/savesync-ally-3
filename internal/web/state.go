package web

import "time"

// State is everything the UI renders. It is pushed to the window over SSE.
type State struct {
	Version   string      `json:"version"`
	Device    string      `json:"device"`
	Demo      bool        `json:"demo"`
	Drive     DriveStatus `json:"drive"`
	Autostart bool        `json:"autostart"`
	Versions  int         `json:"versions"`
	// CloudChecked is when the Drive change log was last checked.
	CloudChecked string `json:"cloudChecked,omitempty"`
	Games        []Game `json:"games"`
}

type DriveStatus struct {
	Connected   bool   `json:"connected"`
	NeedsReauth bool   `json:"needsReauth,omitempty"`
	Account     string `json:"account,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Game status values understood by the UI.
const (
	StatusSynced    = "synced"
	StatusSyncing   = "syncing"
	StatusConflict  = "conflict"
	StatusCloudOnly = "cloud_only"
	StatusMissing   = "missing"
	StatusError     = "error"
	StatusPending   = "pending"
)

type Game struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Root    string   `json:"root,omitempty"`
	Include []string `json:"include,omitempty"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	// Notice is a temporary hint, e.g. "restart the game" after a download.
	Notice   string `json:"notice,omitempty"`
	Watching bool   `json:"watching,omitempty"`
	// Guarded marks a conflict raised by the download guard.
	Guarded  bool      `json:"guarded,omitempty"`
	LastSync string    `json:"lastSync,omitempty"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Progress *Progress `json:"progress,omitempty"`
	// Conflict lists the competing versions, typically this device and the cloud.
	Conflict []ConflictSide `json:"conflict,omitempty"`
	// CloudDevice/CloudModified describe the latest cloud version (for cloud-only games).
	CloudDevice   string `json:"cloudDevice,omitempty"`
	CloudModified string `json:"cloudModified,omitempty"`
}

type Progress struct {
	Direction string `json:"direction"` // "up" or "down"
	Done      int    `json:"done"`
	Total     int    `json:"total"`
}

type ConflictSide struct {
	Side     string `json:"side"` // "local" or "cloud"
	Device   string `json:"device"`
	Modified string `json:"modified"`
	Files    int    `json:"files"`
}

// DemoState returns sample data covering every status, used by `run --demo`
// to work on the UI before the sync engine exists.
func DemoState(version, device string, now time.Time) State {
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	return State{
		Version:   version,
		Device:    device,
		Demo:      true,
		Drive:     DriveStatus{Connected: true, Account: "demo@gmail.com"},
		Autostart: true,
		Games: []Game{
			{ID: "elden-ring", Name: "Elden Ring", Status: StatusSynced, LastSync: ts(2 * time.Minute), Files: 14, Bytes: 3_350_000},
			{ID: "hades-2", Name: "Hades II", Status: StatusConflict, Conflict: []ConflictSide{
				{Side: "local", Device: device, Modified: ts(25 * time.Minute), Files: 4},
				{Side: "cloud", Device: "ROG Ally", Modified: ts(60 * time.Minute), Files: 4},
			}},
			{ID: "stardew", Name: "Stardew Valley", Status: StatusSyncing, Progress: &Progress{Direction: "up", Done: 2, Total: 5}, LastSync: ts(26 * time.Hour), Files: 5, Bytes: 820_000},
			{ID: "hollow-knight", Name: "Hollow Knight", Status: StatusCloudOnly, CloudDevice: "ROG Ally", CloudModified: ts(50 * time.Hour)},
			{ID: "celeste", Name: "Celeste", Status: StatusMissing, Message: "Složka nenalezena, synchronizace pozastavena"},
		},
	}
}

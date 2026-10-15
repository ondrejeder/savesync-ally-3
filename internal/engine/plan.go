// Package engine decides and performs synchronization of one game at a time.
package engine

import "sort"

// LocalFile is a file found under the game's root on this device.
type LocalFile struct {
	MD5     string `json:"md5"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"` // unix seconds
	ModNano int64  `json:"mod"`   // exact modification time, for the hash cache
}

// CloudFile is a file stored in the game's Drive folder.
type CloudFile struct {
	ID     string
	MD5    string
	Size   int64
	MTime  int64  // original local mtime (unix seconds) of the uploading device
	Device string // device that uploaded this version
}

// Baseline is this device's view of a file after its last successful sync.
// It is stored locally, never shared between devices.
type Baseline map[string]string // relative path -> md5

type ActionKind string

const (
	Upload      ActionKind = "upload"       // create or update the cloud file from local
	Download    ActionKind = "download"     // write the cloud version locally
	DeleteCloud ActionKind = "delete-cloud" // trash the cloud file
	DeleteLocal ActionKind = "delete-local" // remove the local file (after backup)
	Accept      ActionKind = "accept"       // both sides already equal: record in baseline
	Forget      ActionKind = "forget"       // gone on both sides: drop from baseline
)

type Action struct {
	Kind    ActionKind
	Path    string
	CloudID string // existing cloud file, if any
}

// Plan is the outcome for one game. When Conflicts is non-empty no action must
// be executed: save files usually belong together, so a game is either synced
// as a whole or paused for the user to pick a side.
type Plan struct {
	Actions   []Action
	Conflicts []string
}

// Changes reports whether executing the plan touches any file.
func (p Plan) Changes() bool {
	for _, a := range p.Actions {
		if a.Kind != Accept && a.Kind != Forget {
			return true
		}
	}
	return false
}

// MakePlan compares the per-device baseline with the current local and cloud
// state (three-way, by content hash).
func MakePlan(base Baseline, local map[string]LocalFile, cloud map[string]CloudFile) Plan {
	var plan Plan

	// A side that suddenly became completely empty is far more likely a wiped
	// or reinstalled game than a deliberate deletion of every save. Never
	// propagate that silently.
	if len(base) > 0 {
		if len(local) == 0 && len(cloud) > 0 {
			plan.Conflicts = sortedKeys(cloud)
			return plan
		}
		if len(cloud) == 0 && len(local) > 0 {
			plan.Conflicts = sortedKeys(local)
			return plan
		}
	}

	for _, path := range unionKeys(base, local, cloud) {
		b, hasBase := base[path]
		l, hasLocal := local[path]
		c, hasCloud := cloud[path]

		if !hasBase {
			switch {
			case hasLocal && hasCloud && l.MD5 == c.MD5:
				plan.Actions = append(plan.Actions, Action{Kind: Accept, Path: path, CloudID: c.ID})
			case hasLocal && hasCloud:
				plan.Conflicts = append(plan.Conflicts, path)
			case hasLocal:
				plan.Actions = append(plan.Actions, Action{Kind: Upload, Path: path})
			case hasCloud:
				plan.Actions = append(plan.Actions, Action{Kind: Download, Path: path, CloudID: c.ID})
			}
			continue
		}

		localChanged := !hasLocal || l.MD5 != b
		cloudChanged := !hasCloud || c.MD5 != b

		switch {
		case !localChanged && !cloudChanged:
			// in sync
		case localChanged && !cloudChanged:
			if hasLocal {
				plan.Actions = append(plan.Actions, Action{Kind: Upload, Path: path, CloudID: c.ID})
			} else {
				plan.Actions = append(plan.Actions, Action{Kind: DeleteCloud, Path: path, CloudID: c.ID})
			}
		case !localChanged && cloudChanged:
			if hasCloud {
				plan.Actions = append(plan.Actions, Action{Kind: Download, Path: path, CloudID: c.ID})
			} else {
				plan.Actions = append(plan.Actions, Action{Kind: DeleteLocal, Path: path})
			}
		default: // changed on both sides
			switch {
			case !hasLocal && !hasCloud:
				plan.Actions = append(plan.Actions, Action{Kind: Forget, Path: path})
			case hasLocal && hasCloud && l.MD5 == c.MD5:
				plan.Actions = append(plan.Actions, Action{Kind: Accept, Path: path, CloudID: c.ID})
			default:
				plan.Conflicts = append(plan.Conflicts, path)
			}
		}
	}

	if len(plan.Conflicts) > 0 {
		plan.Actions = nil
	}
	return plan
}

// Side picks which version wins when resolving a conflict.
type Side string

const (
	KeepLocal Side = "local"
	KeepCloud Side = "cloud"
)

// ResolvePlan makes the losing side an exact copy of the winning side.
func ResolvePlan(local map[string]LocalFile, cloud map[string]CloudFile, keep Side) Plan {
	var plan Plan
	for _, path := range unionKeys(nil, local, cloud) {
		l, hasLocal := local[path]
		c, hasCloud := cloud[path]
		if hasLocal && hasCloud && l.MD5 == c.MD5 {
			plan.Actions = append(plan.Actions, Action{Kind: Accept, Path: path, CloudID: c.ID})
			continue
		}
		if keep == KeepLocal {
			if hasLocal {
				plan.Actions = append(plan.Actions, Action{Kind: Upload, Path: path, CloudID: c.ID})
			} else {
				plan.Actions = append(plan.Actions, Action{Kind: DeleteCloud, Path: path, CloudID: c.ID})
			}
		} else {
			if hasCloud {
				plan.Actions = append(plan.Actions, Action{Kind: Download, Path: path, CloudID: c.ID})
			} else {
				plan.Actions = append(plan.Actions, Action{Kind: DeleteLocal, Path: path})
			}
		}
	}
	return plan
}

func unionKeys(base Baseline, local map[string]LocalFile, cloud map[string]CloudFile) []string {
	seen := map[string]struct{}{}
	for k := range base {
		seen[k] = struct{}{}
	}
	for k := range local {
		seen[k] = struct{}{}
	}
	for k := range cloud {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

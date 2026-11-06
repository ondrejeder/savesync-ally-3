package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// nonSteamNames maps compatdata folder names of non-Steam games (added to
// Steam as shortcuts) to the shortcut names, read from every Steam user's
// config/shortcuts.vdf. This is what protontricks shows; the prefix number
// alone says nothing and differs between devices.
func nonSteamNames(compatdata string) map[string]string {
	names := map[string]string{}
	for _, root := range steamRoots(compatdata) {
		files, _ := filepath.Glob(filepath.Join(root, "userdata", "*", "config", "shortcuts.vdf"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for id, name := range parseShortcuts(data) {
				names[id] = name
			}
		}
	}
	return names
}

// steamRoots lists Steam installations that may own a compatdata folder:
// the one it belongs to (main library) and the usual install locations
// (non-Steam prefixes of a game on an SD card live in the main library,
// but the card's library has no userdata of its own).
func steamRoots(compatdata string) []string {
	roots := []string{filepath.Dir(filepath.Dir(compatdata))}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(home, ".local", "share", "Steam"),
			filepath.Join(home, ".steam", "steam"),
			filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"))
	}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		roots = append(roots, filepath.Join(pf, "Steam"))
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range roots {
		key := r
		if real, err := filepath.EvalSymlinks(r); err == nil {
			key = real
		}
		if !seen[key] && isDir(r) {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}

// parseShortcuts reads Steam's binary VDF shortcuts file and returns
// compatdata folder name → shortcut name.
func parseShortcuts(data []byte) map[string]string {
	out := map[string]string{}
	r := &vdfReader{data: data}
	root, err := r.object()
	if err != nil {
		return out
	}
	list, _ := lookup(root, "shortcuts").(map[string]any)
	for _, v := range list {
		sc, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _ := lookup(sc, "appname").(string)
		exe, _ := lookup(sc, "exe").(string)
		if name == "" {
			continue
		}
		var id uint32
		if n, ok := lookup(sc, "appid").(uint32); ok && n != 0 {
			id = n
		} else {
			// Shortcuts from old Steam versions have no appid; Steam derives it.
			id = crc32.ChecksumIEEE([]byte(exe+name)) | 0x80000000
		}
		out[strconv.FormatUint(uint64(id), 10)] = name
	}
	return out
}

// lookup finds a key case-insensitively (Steam has used both AppName and appname).
func lookup(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

type vdfReader struct {
	data []byte
	pos  int
}

var errVDF = errors.New("invalid binary vdf")

// object reads entries until the end marker (0x08) or the end of data.
func (r *vdfReader) object() (map[string]any, error) {
	m := map[string]any{}
	for r.pos < len(r.data) {
		typ := r.data[r.pos]
		r.pos++
		if typ == 0x08 {
			return m, nil
		}
		key, err := r.cstring()
		if err != nil {
			return nil, err
		}
		switch typ {
		case 0x00:
			child, err := r.object()
			if err != nil {
				return nil, err
			}
			m[key] = child
		case 0x01:
			s, err := r.cstring()
			if err != nil {
				return nil, err
			}
			m[key] = s
		case 0x02:
			if r.pos+4 > len(r.data) {
				return nil, errVDF
			}
			m[key] = binary.LittleEndian.Uint32(r.data[r.pos:])
			r.pos += 4
		case 0x07:
			if r.pos+8 > len(r.data) {
				return nil, errVDF
			}
			r.pos += 8
		default:
			return nil, errVDF
		}
	}
	return m, nil
}

func (r *vdfReader) cstring() (string, error) {
	end := bytes.IndexByte(r.data[r.pos:], 0)
	if end < 0 {
		return "", errVDF
	}
	s := string(r.data[r.pos : r.pos+end])
	r.pos += end + 1
	return s, nil
}

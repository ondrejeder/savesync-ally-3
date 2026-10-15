package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrChangesExpired means the saved Changes API token is no longer valid;
// the caller should do a full check and start over with a new token.
var ErrChangesExpired = errors.New("drive changes token expired")

// Change is one modified, created, trashed or removed file.
type Change struct {
	FileID  string
	Removed bool // deleted or no longer accessible
	Trashed bool
	Folder  bool
	Parents []string
	Device  string // appProperties.device of uploaded save files
	Kind    string // appProperties.savesync: "root", "game", "versions" or ""
}

// StartPageToken returns the current position in the Drive change log.
func (c *Client) StartPageToken(ctx context.Context) (string, error) {
	var resp struct {
		Token string `json:"startPageToken"`
	}
	err := c.getJSON(ctx, apiBase+"/changes/startPageToken", &resp)
	return resp.Token, err
}

// Changes returns everything that changed since token, plus the token for
// the next call. With the drive.file scope this only includes app files.
func (c *Client) Changes(ctx context.Context, token string) ([]Change, string, error) {
	var out []Change
	for {
		v := url.Values{}
		v.Set("pageToken", token)
		v.Set("pageSize", "1000")
		v.Set("spaces", "drive")
		v.Set("includeRemoved", "true")
		v.Set("fields", "nextPageToken,newStartPageToken,changes(fileId,removed,file(mimeType,trashed,parents,appProperties))")
		var page struct {
			Next     string `json:"nextPageToken"`
			NewStart string `json:"newStartPageToken"`
			Changes  []struct {
				FileID  string `json:"fileId"`
				Removed bool   `json:"removed"`
				File    *file  `json:"file"`
			} `json:"changes"`
		}
		err := c.getJSON(ctx, apiBase+"/changes?"+v.Encode(), &page)
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusGone ||
			(apiErr.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(apiErr.Message), "token"))) {
			return nil, "", ErrChangesExpired
		}
		if err != nil {
			return nil, "", err
		}
		for _, ch := range page.Changes {
			change := Change{FileID: ch.FileID, Removed: ch.Removed}
			if f := ch.File; f != nil {
				change.Trashed = f.Trashed
				change.Folder = f.MimeType == folderMime
				change.Parents = f.Parents
				change.Device = f.AppProperties["device"]
				change.Kind = f.AppProperties["savesync"]
			}
			out = append(out, change)
		}
		if page.NewStart != "" {
			return out, page.NewStart, nil
		}
		if page.Next == "" {
			return out, token, nil
		}
		token = page.Next
	}
}

// ---------- versions ----------

// Version is a zip snapshot of a whole game, stored in the game's
// ".versions" sub-folder.
type Version struct {
	ID      string    `json:"id"`
	Device  string    `json:"device"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
	Files   int       `json:"files"`
}

const versionsFolderName = ".versions"

// VersionsFolder finds or creates the versions folder of a game.
func (c *Client) VersionsFolder(ctx context.Context, gameID string) (string, error) {
	files, err := c.list(ctx, fmt.Sprintf(
		"'%s' in parents and appProperties has { key='savesync' and value='versions' } and trashed=false", escape(gameID)))
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		return files[0].ID, nil
	}
	f, err := c.createFolder(ctx, versionsFolderName, gameID, map[string]string{"savesync": "versions"})
	return f.ID, err
}

// ListVersions returns a game's versions, newest first.
func (c *Client) ListVersions(ctx context.Context, folderID string) ([]Version, error) {
	files, err := c.list(ctx, fmt.Sprintf("'%s' in parents and trashed=false", escape(folderID)))
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(files))
	for _, f := range files {
		v := Version{ID: f.ID, Device: f.AppProperties["device"]}
		v.Size, _ = strconv.ParseInt(f.Size, 10, 64)
		v.Files, _ = strconv.Atoi(f.AppProperties["files"])
		if t, err := time.Parse(time.RFC3339, f.AppProperties["created"]); err == nil {
			v.Created = t
		} else if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
			v.Created = t
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// UploadVersion stores a new snapshot zip.
func (c *Client) UploadVersion(ctx context.Context, folderID string, body io.Reader, size int64, device string, files int, created time.Time) error {
	name := created.Local().Format("2006-01-02 15.04.05") + " " + device + ".zip"
	meta := map[string]any{
		"name":    name,
		"parents": []string{folderID},
		"appProperties": map[string]string{
			"device":  device,
			"files":   strconv.Itoa(files),
			"created": created.UTC().Format(time.RFC3339),
		},
	}
	_, err := c.uploadResumable(ctx, http.MethodPost, uploadBase+"/files?uploadType=resumable&fields=id,md5Checksum,size", meta, body, size)
	return err
}

// Delete removes a file permanently (used for pruning old versions, which
// would otherwise fill up the Drive trash).
func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiBase+"/files/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

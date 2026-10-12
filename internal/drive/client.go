package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/engine"
)

// Endpoints are variables so tests can point them at a fake server.
var (
	apiBase    = "https://www.googleapis.com/drive/v3"
	uploadBase = "https://www.googleapis.com/upload/drive/v3"
)

const (
	folderMime = "application/vnd.google-apps.folder"
	// RootName is the visible name of the app folder in My Drive.
	RootName = "SaveSync Ally 3"
)

// Client talks to the Drive REST API with the token from Auth.
type Client struct {
	Auth *Auth
}

// GameFolder is one game in the cloud.
type GameFolder struct {
	ID        string
	Name      string
	UpdatedBy string    // device name of the last upload
	UpdatedAt time.Time // time of the last upload
}

// APIError is a non-2xx response from Drive.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("drive: %d %s: %s", e.Status, e.Reason, e.Message)
}

type file struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	MD5           string            `json:"md5Checksum"`
	Size          string            `json:"size"`
	ModifiedTime  string            `json:"modifiedTime"`
	AppProperties map[string]string `json:"appProperties"`
}

// Account returns the e-mail of the connected Google account.
func (c *Client) Account(ctx context.Context) (string, error) {
	var about struct {
		User struct {
			Email string `json:"emailAddress"`
		} `json:"user"`
	}
	err := c.getJSON(ctx, apiBase+"/about?fields=user(emailAddress)", &about)
	return about.User.Email, err
}

// EnsureRoot finds or creates the app folder in My Drive.
func (c *Client) EnsureRoot(ctx context.Context) (string, error) {
	files, err := c.list(ctx, fmt.Sprintf(
		"appProperties has { key='savesync' and value='root' } and mimeType='%s' and trashed=false", folderMime))
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
		return files[0].ID, nil
	}
	created, err := c.createFolder(ctx, RootName, "", map[string]string{"savesync": "root"})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// ListGames returns all game folders under the app folder.
func (c *Client) ListGames(ctx context.Context, rootID string) ([]GameFolder, error) {
	files, err := c.list(ctx, fmt.Sprintf(
		"'%s' in parents and appProperties has { key='savesync' and value='game' } and trashed=false", escape(rootID)))
	if err != nil {
		return nil, err
	}
	out := make([]GameFolder, 0, len(files))
	for _, f := range files {
		out = append(out, gameFromFile(f))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// CreateGame creates a folder for a new game.
func (c *Client) CreateGame(ctx context.Context, rootID, name string) (GameFolder, error) {
	f, err := c.createFolder(ctx, name, rootID, map[string]string{"savesync": "game"})
	if err != nil {
		return GameFolder{}, err
	}
	return gameFromFile(f), nil
}

// TouchGame records which device changed the game last.
func (c *Client) TouchGame(ctx context.Context, gameID, device string) error {
	meta := map[string]any{"appProperties": map[string]string{
		"updatedBy": device,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	}}
	return c.patchJSON(ctx, apiBase+"/files/"+url.PathEscape(gameID), meta)
}

// RenameGame changes the display name of a game folder.
func (c *Client) RenameGame(ctx context.Context, gameID, name string) error {
	return c.patchJSON(ctx, apiBase+"/files/"+url.PathEscape(gameID), map[string]any{"name": name})
}

// Trash moves a file or folder to the Drive trash (recoverable for 30 days).
func (c *Client) Trash(ctx context.Context, id string) error {
	return c.patchJSON(ctx, apiBase+"/files/"+url.PathEscape(id), map[string]any{"trashed": true})
}

// ListFiles returns a game's files keyed by relative path. Files are stored
// flat in the game folder; the relative path is kept in appProperties.path.
func (c *Client) ListFiles(ctx context.Context, gameID string) (map[string]engine.CloudFile, error) {
	files, err := c.list(ctx, fmt.Sprintf("'%s' in parents and mimeType!='%s' and trashed=false", escape(gameID), folderMime))
	if err != nil {
		return nil, err
	}
	out := map[string]engine.CloudFile{}
	newest := map[string]string{}
	for _, f := range files {
		rel := f.AppProperties["path"]
		if rel == "" {
			rel = f.Name
		}
		// Duplicates should not happen; if they do, the newest one wins.
		if prev, ok := newest[rel]; ok && prev >= f.ModifiedTime {
			continue
		}
		newest[rel] = f.ModifiedTime
		size, _ := strconv.ParseInt(f.Size, 10, 64)
		mtime, _ := strconv.ParseInt(f.AppProperties["mtime"], 10, 64)
		out[rel] = engine.CloudFile{ID: f.ID, MD5: f.MD5, Size: size, MTime: mtime, Device: f.AppProperties["device"]}
	}
	return out, nil
}

// Upload creates (existingID == "") or updates a file. Updating keeps the
// file ID, so Drive keeps the previous content as a revision.
func (c *Client) Upload(ctx context.Context, gameID, existingID, rel string, body io.Reader, size, mtime int64, device string) (engine.CloudFile, error) {
	meta := map[string]any{
		"name": rel,
		"appProperties": map[string]string{
			"path":   rel,
			"mtime":  strconv.FormatInt(mtime, 10),
			"device": device,
		},
	}
	method, target := http.MethodPost, uploadBase+"/files"
	if existingID != "" {
		method, target = http.MethodPatch, uploadBase+"/files/"+url.PathEscape(existingID)
	} else {
		meta["parents"] = []string{gameID}
	}
	target += "?uploadType=resumable&fields=id,md5Checksum,size"

	// Step 1: open a resumable session.
	metaJSON, _ := json.Marshal(meta)
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(metaJSON))
	if err != nil {
		return engine.CloudFile{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, err := c.do(req)
	if err != nil {
		return engine.CloudFile{}, err
	}
	resp.Body.Close()
	session := resp.Header.Get("Location")
	if session == "" {
		return engine.CloudFile{}, errors.New("drive: upload session has no Location")
	}

	// Step 2: send the content in one request.
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, session, body)
	if err != nil {
		return engine.CloudFile{}, err
	}
	put.ContentLength = size
	put.Header.Set("Content-Type", "application/octet-stream")
	resp, err = c.do(put)
	if err != nil {
		return engine.CloudFile{}, err
	}
	defer resp.Body.Close()
	var f file
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return engine.CloudFile{}, err
	}
	n, _ := strconv.ParseInt(f.Size, 10, 64)
	return engine.CloudFile{ID: f.ID, MD5: f.MD5, Size: n, MTime: mtime, Device: device}, nil
}

// Download streams a file's content into w.
func (c *Client) Download(ctx context.Context, id string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/files/"+url.PathEscape(id)+"?alt=media", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

// ---------- plumbing ----------

func (c *Client) list(ctx context.Context, q string) ([]file, error) {
	var out []file
	pageToken := ""
	for {
		v := url.Values{}
		v.Set("q", q)
		v.Set("spaces", "drive")
		v.Set("pageSize", "1000")
		v.Set("fields", "nextPageToken,files(id,name,mimeType,md5Checksum,size,modifiedTime,appProperties)")
		if pageToken != "" {
			v.Set("pageToken", pageToken)
		}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []file `json:"files"`
		}
		if err := c.getJSON(ctx, apiBase+"/files?"+v.Encode(), &page); err != nil {
			return nil, err
		}
		out = append(out, page.Files...)
		if page.NextPageToken == "" {
			return out, nil
		}
		pageToken = page.NextPageToken
	}
}

func (c *Client) createFolder(ctx context.Context, name, parent string, props map[string]string) (file, error) {
	meta := map[string]any{"name": name, "mimeType": folderMime, "appProperties": props}
	if parent != "" {
		meta["parents"] = []string{parent}
	}
	body, _ := json.Marshal(meta)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/files?fields=id,name,modifiedTime,appProperties", bytes.NewReader(body))
	if err != nil {
		return file{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := c.do(req)
	if err != nil {
		return file{}, err
	}
	defer resp.Body.Close()
	var f file
	return f, json.NewDecoder(resp.Body).Decode(&f)
}

func (c *Client) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) patchJSON(ctx context.Context, target string, meta any) error {
	body, _ := json.Marshal(meta)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, target+"?fields=id", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// do executes a request, retrying rate limits and server errors when the
// body can be replayed, and maps errors to APIError / ErrReauth.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	httpClient, err := c.Auth.HTTPClient(req.Context())
	if err != nil {
		return nil, err
	}
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		resp, err := httpClient.Do(req)
		if err != nil {
			if errors.Is(err, ErrReauth) {
				return nil, ErrReauth
			}
			return nil, err
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		apiErr := readError(resp)
		if resp.StatusCode == http.StatusUnauthorized {
			c.Auth.MarkExpired()
			return nil, ErrReauth
		}
		retryable := resp.StatusCode == 429 || resp.StatusCode >= 500 ||
			(resp.StatusCode == 403 && strings.Contains(apiErr.Reason, "RateLimit"))
		replayable := req.Body == nil || req.GetBody != nil
		if !retryable || !replayable || attempt >= 3 {
			return nil, apiErr
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if req.GetBody != nil {
			if req.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
	}
}

func readError(resp *http.Response) *APIError {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	e := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		e.Message = body.Error.Message
		if len(body.Error.Errors) > 0 {
			e.Reason = body.Error.Errors[0].Reason
		}
	}
	return e
}

func gameFromFile(f file) GameFolder {
	g := GameFolder{ID: f.ID, Name: f.Name, UpdatedBy: f.AppProperties["updatedBy"]}
	if t, err := time.Parse(time.RFC3339, f.AppProperties["updatedAt"]); err == nil {
		g.UpdatedAt = t
	} else if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
		g.UpdatedAt = t
	}
	return g
}

// escape quotes a value for use inside a Drive query string literal.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

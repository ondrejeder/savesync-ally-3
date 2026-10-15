package drive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	oldAPI, oldUpload := apiBase, uploadBase
	apiBase, uploadBase = srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3"
	t.Cleanup(func() { apiBase, uploadBase = oldAPI, oldUpload })
	a := &Auth{cfg: &oauth2.Config{}, tokenPath: t.TempDir() + "/tok.json"}
	a.ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"})
	return &Client{Auth: a}
}

func TestListFilesUsesPathProperty(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "'game1' in parents") {
			t.Errorf("query = %s", r.URL.Query().Get("q"))
		}
		json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"id": "f1", "name": "profiles/a.sav", "md5Checksum": "aa", "size": "3", "modifiedTime": "2026-01-01T00:00:00Z",
				"appProperties": map[string]string{"path": "profiles/a.sav", "mtime": "1700000000", "device": "PC"}},
			{"id": "f0", "name": "profiles/a.sav", "md5Checksum": "old", "size": "1", "modifiedTime": "2025-01-01T00:00:00Z",
				"appProperties": map[string]string{"path": "profiles/a.sav"}},
		}})
	}))
	files, err := c.ListFiles(context.Background(), "game1")
	if err != nil {
		t.Fatal(err)
	}
	f := files["profiles/a.sav"]
	if len(files) != 1 || f.ID != "f1" || f.MD5 != "aa" || f.Size != 3 || f.MTime != 1700000000 || f.Device != "PC" {
		t.Fatalf("files = %+v", files)
	}
}

func TestUploadUpdateDoesNotSendParents(t *testing.T) {
	var sessionMeta map[string]any
	var content string
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /upload/drive/v3/files/f1", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sessionMeta)
		w.Header().Set("Location", "http://"+r.Host+"/session/1")
	})
	mux.HandleFunc("PUT /session/1", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		content = string(b)
		json.NewEncoder(w).Encode(map[string]string{"id": "f1", "md5Checksum": "abc", "size": "5"})
	})
	c := testClient(t, mux)

	got, err := c.Upload(context.Background(), "game1", "f1", "a/b.sav", strings.NewReader("hello"), 5, 42, "PC")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sessionMeta["parents"]; ok {
		t.Errorf("update must not send parents: %v", sessionMeta)
	}
	if props := sessionMeta["appProperties"].(map[string]any); props["path"] != "a/b.sav" || props["mtime"] != "42" {
		t.Errorf("appProperties = %v", props)
	}
	if content != "hello" || got.ID != "f1" || got.MD5 != "abc" {
		t.Errorf("content=%q got=%+v", content, got)
	}
}

func TestUnauthorizedMarksReauth(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad token"}}`, http.StatusUnauthorized)
	}))
	if _, err := c.ListFiles(context.Background(), "g"); err != ErrReauth {
		t.Fatalf("err = %v, want ErrReauth", err)
	}
	if connected, reauth := c.Auth.Status(); connected || !reauth {
		t.Fatalf("status connected=%v reauth=%v", connected, reauth)
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`it's \ ok`); got != `it\'s \\ ok` {
		t.Fatalf("escape = %s", got)
	}
}

func TestChanges(t *testing.T) {
	calls := 0
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("pageToken") {
		case "1":
			json.NewEncoder(w).Encode(map[string]any{"nextPageToken": "2", "changes": []map[string]any{
				{"fileId": "a", "file": map[string]any{"parents": []string{"game1"}, "appProperties": map[string]string{"device": "PC"}}},
			}})
		case "2":
			json.NewEncoder(w).Encode(map[string]any{"newStartPageToken": "3", "changes": []map[string]any{
				{"fileId": "b", "removed": true},
			}})
		default:
			http.Error(w, `{"error":{"message":"Invalid page token"}}`, http.StatusNotFound)
		}
	}))
	changes, next, err := c.Changes(context.Background(), "1")
	if err != nil || next != "3" || len(changes) != 2 || changes[0].Parents[0] != "game1" || changes[0].Device != "PC" || !changes[1].Removed {
		t.Fatalf("changes=%+v next=%s err=%v", changes, next, err)
	}
	if _, _, err := c.Changes(context.Background(), "old"); err != ErrChangesExpired {
		t.Fatalf("err = %v, want ErrChangesExpired", err)
	}
}

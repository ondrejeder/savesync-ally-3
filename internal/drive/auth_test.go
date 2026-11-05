package drive

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestLoginWithOccupiedLegacyPort(t *testing.T) {
	// If another program already owns the port, it provides the same condition.
	occupied, err := net.Listen("tcp", redirectAddr)
	if err == nil {
		defer occupied.Close()
	}
	var redirect string
	exchanged := make(chan url.Values, 1)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		exchanged <- r.Form
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-access","refresh_token":"test-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokens.Close()
	a := &Auth{
		cfg: &oauth2.Config{
			ClientID: "test-client", RedirectURL: "http://" + redirectAddr,
			Endpoint: oauth2.Endpoint{AuthURL: "https://example.invalid/auth", TokenURL: tokens.URL, AuthStyle: oauth2.AuthStyleInParams},
		},
		tokenPath: filepath.Join(t.TempDir(), "token.json"),
	}
	loginURL, err := a.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	redirect = u.Query().Get("redirect_uri")
	callback, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Port() == "3000" {
		t.Fatalf("expected a free loopback port, got %s", redirect)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() {
		q := url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}}
		if resp, err := client.Get(redirect + "?" + q.Encode()); err == nil {
			resp.Body.Close()
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			a.mu.Lock()
			pending := a.pending != nil
			a.mu.Unlock()
			if !pending {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Error("callback listener did not shut down")
	})
	// Repeated/concurrent clicks must reuse the same pending login.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := a.BeginLogin(); err != nil || got != loginURL {
				t.Errorf("pending login was not reused: %v", err)
			}
		}()
	}
	wg.Wait()
	resp, err := client.Get(redirect + "?state=wrong&code=test-code")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("invalid OAuth state accepted")
	}
	q := url.Values{"state": {u.Query().Get("state")}, "code": {"test-code"}}
	resp, err = client.Get(redirect + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case form := <-exchanged:
		if form.Get("redirect_uri") != redirect || form.Get("code") != "test-code" {
			t.Fatal("token exchange did not use the login redirect and code")
		}
		verifier := form.Get("code_verifier")
		if verifier == "" || oauth2.S256ChallengeFromVerifier(verifier) != u.Query().Get("code_challenge") {
			t.Fatal("PKCE verifier does not match the authorization request")
		}
	default:
		t.Fatal("token exchange did not occur")
	}
	if connected, _ := a.Status(); !connected {
		t.Fatal("login did not connect")
	}
	if a.cfg.RedirectURL != "http://"+redirectAddr {
		t.Fatal("login changed the shared OAuth configuration")
	}
	// The saved token must be readable on the next start.
	data, err := os.ReadFile(a.tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil || token.RefreshToken != "test-refresh" {
		t.Fatal("refresh token was not persisted")
	}
}

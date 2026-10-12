// Package drive is a small Google Drive v3 REST client limited to the
// drive.file scope: the app only ever sees files it created itself.
package drive

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Set at build time from .env.local (see scripts/build.ps1).
var (
	clientID     string
	clientSecret string
)

const (
	scope = "https://www.googleapis.com/auth/drive.file"
	// The loopback redirect stays on the port used by v2 so the existing
	// OAuth client works whether it is a "Desktop" or a "Web" client.
	redirectAddr = "127.0.0.1:3000"
)

var (
	ErrNoClient     = errors.New("OAuth klient není nastavený (SAVESYNC_GOOGLE_CLIENT_ID)")
	ErrNotConnected = errors.New("Google Drive není připojený")
	ErrReauth       = errors.New("přihlášení ke Google Drive vypršelo, připoj ho znovu")
)

// Auth owns the OAuth token: login flow, persistence and refresh.
type Auth struct {
	cfg       *oauth2.Config
	tokenPath string
	// OnChange is called after connect, disconnect or token expiry.
	OnChange func()

	mu      sync.Mutex
	ts      oauth2.TokenSource
	saved   string // access token last written to disk
	reauth  bool
	pending *pendingLogin
}

type pendingLogin struct {
	url     string
	expires time.Time
}

// NewAuth loads the client credentials and any saved token.
func NewAuth(configDir string) (*Auth, error) {
	id, secret := credentials()
	if id == "" {
		return nil, ErrNoClient
	}
	a := &Auth{
		cfg: &oauth2.Config{
			ClientID:     id,
			ClientSecret: secret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   "https://accounts.google.com/o/oauth2/auth",
				TokenURL:  "https://oauth2.googleapis.com/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			RedirectURL: "http://" + redirectAddr,
			Scopes:      []string{scope},
		},
		tokenPath: filepath.Join(configDir, "google_token.json"),
	}
	if data, err := os.ReadFile(a.tokenPath); err == nil {
		var tok oauth2.Token
		if json.Unmarshal(data, &tok) == nil && tok.RefreshToken != "" {
			a.useToken(&tok)
			a.saved = tok.AccessToken
		}
	}
	return a, nil
}

// credentials prefers environment variables, then values embedded at build
// time, then a .env.local in the working directory (development).
func credentials() (string, string) {
	id, secret := os.Getenv("SAVESYNC_GOOGLE_CLIENT_ID"), os.Getenv("SAVESYNC_GOOGLE_CLIENT_SECRET")
	if id != "" {
		return id, secret
	}
	if clientID != "" {
		return clientID, clientSecret
	}
	f, err := os.Open(".env.local")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch {
		case !ok:
		case key == "SAVESYNC_GOOGLE_CLIENT_ID":
			id = strings.TrimSpace(value)
		case key == "SAVESYNC_GOOGLE_CLIENT_SECRET":
			secret = strings.TrimSpace(value)
		}
	}
	return id, secret
}

// Status reports whether a token is available and whether it was rejected.
func (a *Auth) Status() (connected, needsReauth bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ts != nil && !a.reauth, a.reauth
}

// HTTPClient returns an authorized client; it refreshes and persists tokens.
func (a *Auth) HTTPClient(ctx context.Context) (*http.Client, error) {
	a.mu.Lock()
	ts, reauth := a.ts, a.reauth
	a.mu.Unlock()
	if reauth {
		return nil, ErrReauth
	}
	if ts == nil {
		return nil, ErrNotConnected
	}
	return oauth2.NewClient(ctx, persistingSource{a: a, base: ts}), nil
}

// MarkExpired is called when Drive rejects the token.
func (a *Auth) MarkExpired() {
	a.mu.Lock()
	changed := !a.reauth
	a.reauth = true
	a.mu.Unlock()
	if changed {
		a.notify()
	}
}

// Disconnect forgets the token on this device.
func (a *Auth) Disconnect() {
	a.mu.Lock()
	a.ts, a.saved, a.reauth = nil, "", false
	a.mu.Unlock()
	_ = os.Remove(a.tokenPath)
	a.notify()
}

// BeginLogin starts the loopback callback server and returns the consent URL.
// The flow finishes in the background; OnChange fires when it succeeds.
func (a *Auth) BeginLogin() (string, error) {
	a.mu.Lock()
	if p := a.pending; p != nil && time.Now().Before(p.expires) {
		a.mu.Unlock()
		return p.url, nil
	}
	a.mu.Unlock()

	ln, err := net.Listen("tcp", redirectAddr)
	if err != nil {
		return "", fmt.Errorf("port %s je obsazený (neběží zrovna přihlášení ve v2?): %w", redirectAddr, err)
	}
	state := randomString()
	verifier := oauth2.GenerateVerifier()
	authURL := a.cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier),
	)
	expires := time.Now().Add(10 * time.Minute)
	a.mu.Lock()
	a.pending = &pendingLogin{url: authURL, expires: expires}
	a.mu.Unlock()

	ctx, cancel := context.WithDeadline(context.Background(), expires)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := q.Get("error"); e != "" {
			fmt.Fprint(w, callbackPage("Přihlášení bylo zrušeno", "Okno můžeš zavřít a zkusit to znovu z aplikace."))
			cancel()
			return
		}
		tok, err := a.cfg.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(verifier))
		if err != nil {
			fmt.Fprint(w, callbackPage("Přihlášení se nepovedlo", err.Error()))
			cancel()
			return
		}
		if err := a.connect(tok); err != nil {
			fmt.Fprint(w, callbackPage("Token se nepodařilo uložit", err.Error()))
			cancel()
			return
		}
		fmt.Fprint(w, callbackPage("Google Drive je připojený", "Tohle okno můžeš zavřít a vrátit se do SaveSync Ally."))
		cancel()
	})
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		// Give the browser a moment to receive the final page.
		time.Sleep(time.Second)
		shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
		a.mu.Lock()
		a.pending = nil
		a.mu.Unlock()
	}()
	return authURL, nil
}

func (a *Auth) connect(tok *oauth2.Token) error {
	if err := a.save(tok); err != nil {
		return err
	}
	a.mu.Lock()
	a.useTokenLocked(tok)
	a.reauth = false
	a.mu.Unlock()
	a.notify()
	return nil
}

func (a *Auth) useToken(tok *oauth2.Token) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.useTokenLocked(tok)
}

func (a *Auth) useTokenLocked(tok *oauth2.Token) {
	a.ts = oauth2.ReuseTokenSource(tok, a.cfg.TokenSource(context.Background(), tok))
	a.saved = tok.AccessToken
}

func (a *Auth) save(tok *oauth2.Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.tokenPath), 0o755); err != nil {
		return err
	}
	tmp := a.tokenPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.tokenPath)
}

func (a *Auth) notify() {
	if a.OnChange != nil {
		a.OnChange()
	}
}

// persistingSource saves refreshed tokens and detects revoked/expired grants.
type persistingSource struct {
	a    *Auth
	base oauth2.TokenSource
}

func (p persistingSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client") {
			p.a.MarkExpired()
			return nil, ErrReauth
		}
		return nil, err
	}
	p.a.mu.Lock()
	changed := tok.AccessToken != p.a.saved
	if changed {
		p.a.saved = tok.AccessToken
	}
	p.a.mu.Unlock()
	if changed {
		_ = p.a.save(tok)
	}
	return tok, nil
}

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func callbackPage(title, text string) string {
	return fmt.Sprintf(`<!doctype html><meta charset="utf-8"><title>SaveSync Ally</title>
<body style="font-family:system-ui;background:#0f1115;color:#e8eaf0;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><h2 style="margin:0 0 8px">%s</h2><p style="color:#9aa3b2">%s</p></div></body>`,
		htmlEscape(title), htmlEscape(text))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

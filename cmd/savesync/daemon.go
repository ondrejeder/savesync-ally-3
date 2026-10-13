package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/app"
	"github.com/ondrejeder/savesync-ally-3/internal/drive"
	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	demoMode := fs.Bool("demo", false, "show sample data instead of real games")
	if err := fs.Parse(args); err != nil {
		return err
	}

	configDir, err := platform.ConfigDir()
	if err != nil {
		return err
	}
	dataDir, err := platform.DataDir()
	if err != nil {
		return err
	}
	for _, dir := range []string{configDir, dataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	logger, closeLog, err := openLog(dataDir)
	if err != nil {
		return err
	}
	defer closeLog()

	token, err := sessionToken(configDir)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", platform.Port))
	if err != nil {
		if _, ok := ping(); ok {
			logger.Println("already running")
			return nil
		}
		return fmt.Errorf("listen on port %d: %w", platform.Port, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &web.Server{
		AppID:   platform.AppID,
		Version: version,
		Port:    platform.Port,
		Token:   token,
		Icon:    platform.IconSVG,
		Log:     logger,
		Quit:    stop,
	}
	if *demoMode {
		d := newDemo(deviceName())
		d.onChange = srv.Notify
		srv.Backend = d
		go d.run(ctx)
	} else {
		st, err := store.Open(configDir, dataDir, deviceName())
		if err != nil {
			return fmt.Errorf("open config: %w", err)
		}
		auth, err := drive.NewAuth(configDir)
		if err != nil {
			logger.Printf("google drive disabled: %v", err)
			auth = nil
		}
		a := app.New(st, auth, dataDir, version, logger)
		a.OnChange = srv.Notify
		srv.Backend = a
		go a.Run(ctx)
	}

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http server: %v", err)
			stop()
		}
	}()

	logger.Printf("SaveSync Ally %s running on http://127.0.0.1:%d", version, platform.Port)
	<-ctx.Done()
	logger.Println("stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// demo serves sample data (`run --demo`) for working on the UI.
type demo struct {
	web.Unimplemented
	mu       sync.Mutex
	state    web.State
	onChange func()
}

func newDemo(device string) *demo {
	d := &demo{state: web.DemoState(version, device, time.Now())}
	d.state.Autostart = platform.AutostartEnabled()
	return d
}

func (a *demo) State() web.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.Games = append([]web.Game(nil), a.state.Games...)
	return s
}

func (a *demo) update(fn func(*web.State)) {
	a.mu.Lock()
	fn(&a.state)
	a.mu.Unlock()
	if a.onChange != nil {
		a.onChange()
	}
}

func (a *demo) Action(kind, gameID string) (string, error) {
	return "Demo režim: akce " + kind + " se neprovádí.", nil
}

// run animates the syncing game so live updates can be checked in the UI.
func (a *demo) run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.update(func(s *web.State) {
				for i := range s.Games {
					if p := s.Games[i].Progress; p != nil {
						p.Done = (p.Done + 1) % (p.Total + 1)
					}
				}
			})
		}
	}
}

func deviceName() string {
	if name := strings.TrimSpace(os.Getenv("SAVESYNC_DEVICE_NAME")); name != "" {
		return name
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "Toto zařízení"
}

// sessionToken is created once and kept, so open windows survive daemon restarts.
func sessionToken(configDir string) (string, error) {
	path := filepath.Join(configDir, "session")
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); len(token) >= 32 {
			return token, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func readSessionToken() (string, error) {
	configDir, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(configDir, "session"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// openLog writes to <data>/logs/savesync.log (reset when over 5 MB) and stdout.
func openLog(dataDir string) (*log.Logger, func(), error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "savesync.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	// File first: on Windows GUI builds stdout may be invalid and MultiWriter stops at the first error.
	logger := log.New(io.MultiWriter(f, os.Stdout), "", log.LstdFlags)
	return logger, func() { f.Close() }, nil
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/platform"
)

var client = &http.Client{Timeout: 2 * time.Second}

func apiURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", platform.Port, path)
}

// ping returns the running daemon's version, if any.
func ping() (string, bool) {
	resp, err := client.Get(apiURL("/api/ping"))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var body struct{ App, Version string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.App != platform.AppID {
		return "", false
	}
	return body.Version, true
}

func waitFor(up bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := ping(); ok == up {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// daemonExe prefers the installed binary so autostart and manual starts match.
func daemonExe() (string, error) {
	if installed, err := platform.InstalledExe(); err == nil {
		if _, err := os.Stat(installed); err == nil {
			return installed, nil
		}
	}
	return platform.CurrentExe()
}

func cmdOpen() error {
	if _, ok := ping(); !ok {
		exe, err := daemonExe()
		if err != nil {
			return err
		}
		if err := platform.StartDaemon(exe); err != nil {
			return fmt.Errorf("start daemon: %w", err)
		}
		if !waitFor(true, 10*time.Second) {
			return errors.New("daemon se nespustil, podívej se do logu v datové složce")
		}
	}
	token, err := readSessionToken()
	if err != nil {
		return err
	}
	return platform.OpenAppWindow(apiURL("/?t=" + token))
}

// stopDaemon asks a running daemon to quit and waits until it is gone.
func stopDaemon() error {
	platform.StopService()
	if _, ok := ping(); !ok {
		return nil
	}
	token, err := readSessionToken()
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost, apiURL("/api/quit"), nil)
	req.Header.Set("X-SaveSync-Token", token)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	if !waitFor(false, 5*time.Second) {
		return errors.New("běžící daemon se nepodařilo zastavit")
	}
	return nil
}

func cmdInstall() error {
	current, err := platform.CurrentExe()
	if err != nil {
		return err
	}
	target, err := platform.InstalledExe()
	if err != nil {
		return err
	}
	if err := stopDaemon(); err != nil {
		return err
	}
	if !platform.SamePath(current, target) {
		if err := platform.CopyFile(current, target); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
	}
	if err := platform.InstallIntegration(target); err != nil {
		return err
	}
	fmt.Println("Nainstalováno do", target)
	if err := platform.StartDaemon(target); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	if !waitFor(true, 10*time.Second) {
		return errors.New("daemon se po instalaci nespustil")
	}
	token, err := readSessionToken()
	if err != nil {
		return err
	}
	return platform.OpenAppWindow(apiURL("/?t=" + token))
}

func cmdUninstall() error {
	if err := stopDaemon(); err != nil {
		return err
	}
	if err := platform.UninstallIntegration(); err != nil {
		return err
	}
	data, _ := platform.DataDir()
	config, _ := platform.ConfigDir()
	fmt.Println("Autostart a zástupci odebrány.")
	fmt.Println("Data zůstala v:", data)
	fmt.Println("Konfigurace zůstala v:", config)
	return nil
}

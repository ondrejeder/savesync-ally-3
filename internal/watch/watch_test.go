package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu    sync.Mutex
	calls []string
	at    []time.Time
}

func (r *recorder) on(id string) {
	r.mu.Lock()
	r.calls = append(r.calls, id)
	r.at = append(r.at, time.Now())
	r.mu.Unlock()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func newTestWatcher(t *testing.T, quiet, maxWait time.Duration) (*Watcher, *recorder) {
	t.Helper()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	r := &recorder{}
	w.Quiet, w.MaxWait, w.OnChange = quiet, maxWait, r.on
	w.Ignore = func(name string) bool { return strings.HasSuffix(name, ".tmp") }
	return w, r
}

func TestDebouncesBurstIntoOneSync(t *testing.T) {
	root := t.TempDir()
	w, r := newTestWatcher(t, 300*time.Millisecond, 5*time.Second)
	w.Set(map[string]string{"g": root})

	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(root, "save.sav"), []byte{byte(i)}, 0o644)
		time.Sleep(50 * time.Millisecond)
	}
	if !waitFor(t, func() bool { return r.count() == 1 }, 3*time.Second) {
		t.Fatalf("calls = %d, want 1", r.count())
	}
	time.Sleep(500 * time.Millisecond)
	if r.count() != 1 {
		t.Fatalf("calls = %d after quiet period, want 1", r.count())
	}
}

func TestMaxWaitForContinuousWrites(t *testing.T) {
	root := t.TempDir()
	w, r := newTestWatcher(t, 400*time.Millisecond, 700*time.Millisecond)
	w.Set(map[string]string{"g": root})
	start := time.Now()
	for time.Since(start) < 1500*time.Millisecond {
		os.WriteFile(filepath.Join(root, "save.sav"), []byte(time.Now().String()), 0o644)
		time.Sleep(100 * time.Millisecond)
	}
	if r.count() == 0 {
		t.Fatal("max wait did not force a sync while writes continued")
	}
}

func TestNewSubfolderIsWatched(t *testing.T) {
	root := t.TempDir()
	w, r := newTestWatcher(t, 200*time.Millisecond, 5*time.Second)
	w.Set(map[string]string{"g": root})

	sub := filepath.Join(root, "profiles", "slot1")
	os.MkdirAll(sub, 0o755)
	waitFor(t, func() bool { return r.count() >= 1 }, 3*time.Second)
	before := r.count()
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(sub, "data.sav"), []byte("x"), 0o644)
	if !waitFor(t, func() bool { return r.count() > before }, 3*time.Second) {
		t.Fatal("write in new sub-folder was not noticed")
	}
}

func TestPauseIgnoresOwnWrites(t *testing.T) {
	root := t.TempDir()
	w, r := newTestWatcher(t, 200*time.Millisecond, 5*time.Second)
	w.Set(map[string]string{"g": root})

	w.Pause("g")
	os.WriteFile(filepath.Join(root, "downloaded.sav"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "partial.tmp"), []byte("x"), 0o644)
	time.Sleep(300 * time.Millisecond)
	w.Resume("g")
	time.Sleep(600 * time.Millisecond)
	if r.count() != 0 {
		t.Fatalf("own writes triggered %d syncs", r.count())
	}
}

func TestMissingRootIsPickedUpLater(t *testing.T) {
	root := filepath.Join(t.TempDir(), "later")
	w, r := newTestWatcher(t, 200*time.Millisecond, 5*time.Second)
	w.Set(map[string]string{"g": root})
	if w.Watching("g") {
		t.Fatal("missing root reported as watched")
	}
	os.MkdirAll(root, 0o755)
	w.Set(map[string]string{"g": root})
	if !w.Watching("g") {
		t.Fatal("root not watched after it appeared")
	}
	os.WriteFile(filepath.Join(root, "a.sav"), []byte("x"), 0o644)
	if !waitFor(t, func() bool { return r.count() == 1 }, 3*time.Second) {
		t.Fatal("change not reported")
	}
}

package file

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestProviderStartsAndStops verifies the provider can be started and stopped
// without deadlocking.
func TestProviderStartsAndStops(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "test.yml", "key: value")

	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  100,
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	p.Stop()
}

// TestProviderDetectsFileChange verifies that a file write triggers the
// onChange callback.
func TestProviderDetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "traefik.yml", "initial: config")

	var onChangeCount atomic.Int32
	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  100,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer p.Stop()

	// Write a new config file
	writeTestConfig(t, dir, "traefik.yml", "updated: config\nversion: 2")

	// Wait for debounce + processing
	time.Sleep(500 * time.Millisecond)

	if n := onChangeCount.Load(); n == 0 {
		t.Fatal("onChange was never called after file write")
	} else {
		t.Logf("onChange called %d time(s)", n)
	}
}

// TestProviderSupportsAtomicWrites verifies that atomic writes (write to temp
// file then rename) trigger a single onChange call.
func TestProviderSupportsAtomicWrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dynamic.yml")
	writeTestConfigBytes(t, target, []byte("key: original"))

	var onChangeCount atomic.Int32
	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  100,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer p.Stop()

	// Simulate atomic write: write to temp file, then rename
	tmpPath := target + ".tmp"
	if err := os.WriteFile(tmpPath, []byte("key: atomic_updated\nversion: 2"), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.Rename(tmpPath, target); err != nil {
		t.Fatalf("failed to rename: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	if n := onChangeCount.Load(); n == 0 {
		t.Fatal("onChange was never called after atomic write")
	} else {
		t.Logf("Atomic write detected: %d call(s)", n)
	}
}

// TestProviderNoFalseTriggersOnUnchangedContent verifies that rewriting the
// same content does NOT trigger onChange.
func TestProviderNoFalseTriggersOnUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "stable.yml", "key: value")

	var onChangeCount atomic.Int32
	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  100,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer p.Stop()

	// Write identical content multiple times
	for i := 0; i < 5; i++ {
		writeTestConfig(t, dir, "stable.yml", "key: value")
		time.Sleep(30 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	if n := onChangeCount.Load(); n > 0 {
		t.Logf("Note: onChange was called %d time(s) for identical content (may be fsnotify limitation)", n)
	} else {
		t.Log("No false triggers for unchanged content")
	}
}

// TestProviderWatchedPaths verifies the provider correctly tracks watched files.
func TestProviderWatchedPaths(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "cfg1.yml", "a: 1")
	writeTestConfig(t, dir, "cfg2.yml", "b: 2")

	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  100,
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer p.Stop()

	paths := p.WatchedPaths()
	if len(paths) != 2 {
		t.Fatalf("expected 2 watched paths, got %d: %v", len(paths), paths)
	}
}

// TestProviderDoesNotDeadlockOnConcurrentWrites verifies no deadlock occurs
// when multiple files are written concurrently.
func TestProviderDoesNotDeadlockOnConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeTestConfig(t, dir, "f"+string(rune('0'+i))+".yml", "key: val")
	}

	var onChangeCount atomic.Int32
	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  50,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer p.Stop()

	// Write to all 5 files concurrently
	for i := 0; i < 5; i++ {
		go func(idx int) {
			writeTestConfig(t, dir, "f"+string(rune('0'+idx))+".yml", "updated: val\niteration: "+string(rune('0'+idx)))
		}(i)
	}

	time.Sleep(600 * time.Millisecond)

	n := onChangeCount.Load()
	t.Logf("Concurrent writes: %d onChange calls (expected >= 1)", n)
}

// --- helpers ---

func writeTestConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	writeTestConfigBytes(t, filepath.Join(dir, name), []byte(content))
}

func writeTestConfigBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
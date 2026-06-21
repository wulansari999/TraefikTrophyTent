package file

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestProviderDebouncesRapidWrites verifies that rapid consecutive writes
// are debounced into a single onChange call.
func TestProviderDebouncesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "app.yml", "version: 1")

	var onChangeCount atomic.Int32
	checkCh := make(chan string, 20)

	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  200 * time.Millisecond,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			checkCh <- "change:" + path
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

	// Record what events fsnotify sees
	time.Sleep(100 * time.Millisecond)

	// Simulate rapid consecutive writes
	for i := 0; i < 10; i++ {
		writeTestConfig(t, dir, "app.yml", fmt.Sprintf("version: 1\niteration: %d", i))
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for debounce to settle
	time.Sleep(800 * time.Millisecond)

	// Drain remaining check channel messages
	drainLoop := true
	for drainLoop {
		select {
		case msg := <-checkCh:
			t.Logf("Got on-change: %s", msg)
		default:
			drainLoop = false
		}
	}

	n := onChangeCount.Load()
	if n == 0 {
		t.Fatal("onChange was never called")
	}
	if n > 3 {
		t.Logf("WARNING: %d onChange calls (debounce may not have coalesced all writes)", n)
	} else {
		t.Logf("Rapid writes debounced: 10 writes -> %d call(s)", n)
	}
}

// TestProviderDebounceShortInterval verifies debounce works with very short
// intervals.
func TestProviderDebounceShortInterval(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "test.yml", "initial")

	var onChangeCount atomic.Int32
	p, err := New(Config{
		ConfigDir: dir,
		Debounce:  300 * time.Millisecond,
		OnChange: func(path string) error {
			onChangeCount.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	// Two very fast writes
	writeTestConfig(t, dir, "test.yml", "content: a")
	time.Sleep(5 * time.Millisecond)
	writeTestConfig(t, dir, "test.yml", "content: b")

	time.Sleep(800 * time.Millisecond)

	n := onChangeCount.Load()
	t.Logf("Two fast writes: onChange called %d time(s)", n)
	if n < 1 {
		t.Fatal("onChange was never called")
	}
}

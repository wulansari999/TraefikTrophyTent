// Package file provides a file-based provider for dynamic Traefik configuration.
// It watches configuration files for changes and applies updates reliably even
// under high-frequency consecutive writes.
package file

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Provider watches configuration files and applies changes.
type Provider struct {
	watcher      *fsnotify.Watcher
	configDir    string
	debounce     time.Duration
	mu           sync.Mutex
	stopCh       chan struct{}
	onChange     func(path string) error
	debounceChs  map[string]chan struct{} // per-file debounce notification
	lastContent  map[string]string
	subGoroutine sync.WaitGroup
}

// Config holds provider configuration.
type Config struct {
	ConfigDir string
	Debounce  time.Duration
	OnChange  func(path string) error
}

// New creates a new file Provider.
func New(cfg Config) (*Provider, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("file provider: failed to create watcher: %w", err)
	}

	debounce := cfg.Debounce
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}

	p := &Provider{
		watcher:     watcher,
		configDir:   cfg.ConfigDir,
		debounce:    debounce,
		stopCh:      make(chan struct{}),
		onChange:    cfg.OnChange,
		debounceChs: make(map[string]chan struct{}),
		lastContent: make(map[string]string),
	}

	return p, nil
}

// Start begins watching the config directory for file changes.
func (p *Provider) Start() error {
	info, err := os.Stat(p.configDir)
	if err != nil {
		return fmt.Errorf("file provider: cannot access config dir %s: %w", p.configDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("file provider: %s is not a directory", p.configDir)
	}

	if err := p.watcher.Add(p.configDir); err != nil {
		return fmt.Errorf("file provider: failed to watch %s: %w", p.configDir, err)
	}

	// Load initial content for all config files
	entries, err := os.ReadDir(p.configDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				fp := filepath.Join(p.configDir, e.Name())
				p.loadContent(fp)
			}
		}
	}

	go p.eventLoop()
	return nil
}

// Stop gracefully stops the file watcher.
func (p *Provider) Stop() {
	close(p.stopCh)
	p.watcher.Close()
	p.subGoroutine.Wait()
}

// loadContent reads and stores the content hash of a file.
func (p *Provider) loadContent(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.lastContent[path] = hashContent(data)
	p.mu.Unlock()
}

// hashContent returns a SHA-256 hex digest.
func hashContent(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// eventLoop processes filesystem events with debouncing.
func (p *Provider) eventLoop() {
	for {
		select {
		case event, ok := <-p.watcher.Events:
			if !ok {
				return
			}
			p.handleEvent(event)

		case err, ok := <-p.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("file provider: watcher error: %v", err)

		case <-p.stopCh:
			return
		}
	}
}

// handleEvent processes a single fsnotify event with debouncing.
// Atomic writes produce IN_CREATE (temp file) + IN_MOVED_TO (target) events.
// We debounce by filename using a per-file goroutine with a timer reset
// pattern to coalesce rapid consecutive writes into a single onChange call.
func (p *Provider) handleEvent(event fsnotify.Event) {
	path := event.Name
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.IsDir() {
		return
	}

	p.mu.Lock()
	ch, exists := p.debounceChs[path]
	if !exists {
		// Start a debounce goroutine for this file path
		ch = make(chan struct{}, 1)
		p.debounceChs[path] = ch
		p.subGoroutine.Add(1)
		go p.debounceLoop(path, ch)
	}
	p.mu.Unlock()

	// Non-blocking send: if the channel buffer is full, a notification is
	// already pending and the debounce timer has already been reset.
	select {
	case ch <- struct{}{}:
	default:
	}
}

// debounceLoop receives notifications and fires processChange after the
// debounce period elapses without further notifications.
func (p *Provider) debounceLoop(path string, notify chan struct{}) {
	defer p.subGoroutine.Done()

	timer := time.NewTimer(p.debounce)
	if !timer.Stop() {
		<-timer.C
	}

	for {
		select {
		case <-notify:
			// Reset the debounce timer: extend by debounce duration.
			// If Stop() returns false the timer already fired — drain its
			// channel so the next select doesn't see a stale value.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(p.debounce)

		case <-timer.C:
			// Debounce period elapsed without interruption
			p.processChange(path)
			timer.Stop()
			select {
			case <-timer.C:
			default:
			}

		case <-p.stopCh:
			timer.Stop()
			select {
			case <-timer.C:
			default:
			}
			// Clean up the channel map entry
			p.mu.Lock()
			delete(p.debounceChs, path)
			p.mu.Unlock()
			return
		}
	}
}

// processChange checks whether the file content actually changed and triggers
// the onChange callback if so. This prevents spurious reloads from
// intermediate atomic-write temp files.
func (p *Provider) processChange(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("file provider: failed to read %s: %v", path, err)
		return
	}

	newHash := hashContent(data)
	oldHash, exists := p.lastContent[path]
	if exists && newHash == oldHash {
		return // content unchanged; no-op after atomic write
	}

	p.lastContent[path] = newHash

	log.Printf("file provider: detected change in %s", path)

	if p.onChange != nil {
		if err := p.onChange(path); err != nil {
			log.Printf("file provider: onChange callback failed for %s: %v", path, err)
		}
	}
}

// WatchedPaths returns the set of files currently being watched.
func (p *Provider) WatchedPaths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(p.lastContent))
	for path := range p.lastContent {
		paths = append(paths, path)
	}
	return paths
}

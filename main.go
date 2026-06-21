package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/wulansari999/TraefikTrophyTent/pkg/provider/file"
)

func main() {
	// Create a temporary config directory for demonstration
	configDir := "./config"
	if err := os.MkdirAll(configDir, 0755); err != nil {
		log.Fatalf("failed to create config dir: %v", err)
	}

	// Write an initial config file
	initialConfig := `# Traefik dynamic configuration
[api]
  dashboard = true
`
	if err := os.WriteFile(filepath.Join(configDir, "traefik.yml"), []byte(initialConfig), 0644); err != nil {
		log.Fatalf("failed to write initial config: %v", err)
	}

	// Create the file provider
	provider, err := file.New(file.Config{
		ConfigDir: configDir,
		Debounce:  500, // 500ms debounce for rapid consecutive writes
		OnChange: func(path string) error {
			fmt.Printf("Configuration change detected: %s\n", path)
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", path, err)
			}
			fmt.Printf("New configuration:\n%s\n", string(data))
			return nil
		},
	})
	if err != nil {
		log.Fatalf("failed to create file provider: %v", err)
	}

	if err := provider.Start(); err != nil {
		log.Fatalf("failed to start file provider: %v", err)
	}

	fmt.Println("File provider started. Watching", configDir, "for changes...")
	fmt.Println("Watched files:", provider.WatchedPaths())

	// Wait for termination signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	provider.Stop()
	fmt.Println("File provider stopped.")
}

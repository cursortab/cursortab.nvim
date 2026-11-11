// Command cursortab is the completion server of cursortab.nvim.
//
// One daemon process per state directory serves every Neovim instance. Each
// instance starts the binary without arguments as an RPC relay that connects
// its stdin and stdout to the daemon socket, and the daemon runs one
// independent session per connection.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cursortab/logger"
)

// Version is the cursortab server version. It is updated automatically by the release workflow.
var Version = "0.8.1" // AUTO-UPDATED by release workflow

// DaemonConfig holds the settings shared by all sessions of a daemon. They
// come from the CURSORTAB_CONFIG environment variable; per-editor settings
// arrive with each session (see SessionConfig).
type DaemonConfig struct {
	StateDir          string `json:"state_dir"`
	LogLevel          string `json:"log_level"`
	ImmediateShutdown bool   `json:"immediate_shutdown"`
}

func (c DaemonConfig) Validate() error {
	if c.StateDir == "" {
		return fmt.Errorf("invalid state_dir: must be non-empty")
	}
	return validateEnum(c.LogLevel, "log_level", []string{"trace", "debug", "info", "warn", "error"})
}

func loadDaemonConfig() DaemonConfig {
	var config DaemonConfig
	if err := json.Unmarshal([]byte(os.Getenv("CURSORTAB_CONFIG")), &config); err != nil {
		log.Fatalf("invalid config JSON: %v", err)
	}
	if err := config.Validate(); err != nil {
		log.Fatalf("config validation failed: %v", err)
	}
	return config
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println(Version)
			return
		}
	}

	config := loadDaemonConfig()
	if len(os.Args) > 1 && os.Args[1] == "--daemon" {
		runDaemon(config)
		return
	}
	if err := relay(config.StateDir); err != nil {
		log.Fatalf("error connecting to daemon: %v", err)
	}
}

func runDaemon(config DaemonConfig) {
	if err := os.MkdirAll(config.StateDir, 0755); err != nil {
		log.Fatalf("error creating state directory: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(config.StateDir, "cursortab.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}
	ll := logger.NewLimitedLogger(f, logger.ParseLogLevel(config.LogLevel))
	defer ll.Close()

	if err := NewDaemon(config).Run(); err != nil {
		logger.Fatal("daemon: %v", err)
	}
}

// relay connects stdin and stdout to the daemon, started when none runs,
// until either side closes. The editor closing stdin is the normal end.
func relay(stateDir string) error {
	conn, err := connectDaemon(stateDir)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		io.Copy(conn, os.Stdin)
		conn.Close()
	}()
	if _, err := io.Copy(os.Stdout, conn); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// validateEnum checks that value is one of the valid options for the named field.
func validateEnum(value, field string, valid []string) error {
	if slices.Contains(valid, value) {
		return nil
	}
	return fmt.Errorf("invalid %s %q: must be one of %s", field, value, strings.Join(valid, ", "))
}

package main

import (
	"cursortab/logger"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// CursorPredictionConfig holds cursor prediction settings
type CursorPredictionConfig struct {
	Enabled            bool `json:"enabled"`
	AutoAdvance        bool `json:"auto_advance"`
	ProximityThreshold int  `json:"proximity_threshold"`
}

// BehaviorConfig holds timing and behavior settings
type BehaviorConfig struct {
	IdleCompletionDelay int                    `json:"idle_completion_delay"` // in milliseconds
	TextChangeDebounce  int                    `json:"text_change_debounce"`  // in milliseconds
	MaxVisibleLines     int                    `json:"max_visible_lines"`     // max visible lines per completion (0 to disable)
	CursorPrediction    CursorPredictionConfig `json:"cursor_prediction"`
}

// FIMTokensConfig holds FIM token settings
type FIMTokensConfig struct {
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
	Middle string `json:"middle"`
}

// ProviderConfig holds provider-specific settings
type ProviderConfig struct {
	Type                 string          `json:"type"` // "inline", "fim", "sweep", "sweepapi", "zeta", "copilot", "mercuryapi"
	URL                  string          `json:"url"`
	ApiKeyEnv            string          `json:"api_key_env"` // Environment variable name for API key
	Model                string          `json:"model"`
	Temperature          float64         `json:"temperature"`
	MaxTokens            int             `json:"max_tokens"` // Max tokens to generate (also drives input trimming)
	TopK                 int             `json:"top_k"`
	CompletionTimeout    int             `json:"completion_timeout"` // in milliseconds
	MaxDiffHistoryTokens int             `json:"max_diff_history_tokens"`
	CompletionPath       string          `json:"completion_path"`
	FIMTokens            FIMTokensConfig `json:"fim_tokens"`
	PrivacyMode          bool            `json:"privacy_mode"`
}

// DebugConfig holds debug settings
type DebugConfig struct {
	ImmediateShutdown bool `json:"immediate_shutdown"`
}

// Config is the main configuration structure
type Config struct {
	NsID          int            `json:"ns_id"`
	LogLevel      string         `json:"log_level"`
	StateDir      string         `json:"state_dir"`
	EditorVersion string         `json:"editor_version"`
	EditorOS      string         `json:"editor_os"`
	Behavior      BehaviorConfig `json:"behavior"`
	Provider      ProviderConfig `json:"provider"`
	Debug         DebugConfig    `json:"debug"`
}

// validateEnum checks that value is one of the valid options for the named field.
func validateEnum(value, field string, valid []string) error {
	if slices.Contains(valid, value) {
		return nil
	}
	return fmt.Errorf("invalid %s %q: must be one of %s", field, value, strings.Join(valid, ", "))
}

// Validate checks that the config has valid values.
// All config must come from the Lua client - no defaults are applied here.
func (c *Config) Validate() error {
	if err := validateEnum(c.Provider.Type, "provider.type", []string{"inline", "fim", "sweep", "sweepapi", "zeta", "copilot", "mercuryapi"}); err != nil {
		return err
	}
	if err := validateEnum(c.LogLevel, "log_level", []string{"trace", "debug", "info", "warn", "error"}); err != nil {
		return err
	}

	// Validate numeric ranges
	if c.Behavior.IdleCompletionDelay < -1 {
		return fmt.Errorf("invalid behavior.idle_completion_delay %d: must be >= -1", c.Behavior.IdleCompletionDelay)
	}
	if c.Behavior.TextChangeDebounce < -1 {
		return fmt.Errorf("invalid behavior.text_change_debounce %d: must be >= -1", c.Behavior.TextChangeDebounce)
	}
	if c.Behavior.MaxVisibleLines < 0 {
		return fmt.Errorf("invalid behavior.max_visible_lines %d: must be >= 0", c.Behavior.MaxVisibleLines)
	}
	if c.Provider.MaxTokens < 0 {
		return fmt.Errorf("invalid provider.max_tokens %d: must be >= 0", c.Provider.MaxTokens)
	}
	if c.Provider.CompletionTimeout < 0 {
		return fmt.Errorf("invalid provider.completion_timeout %d: must be >= 0", c.Provider.CompletionTimeout)
	}
	if c.Provider.MaxDiffHistoryTokens < 0 {
		return fmt.Errorf("invalid provider.max_diff_history_tokens %d: must be >= 0", c.Provider.MaxDiffHistoryTokens)
	}

	// Validate completion_path starts with /
	if !strings.HasPrefix(c.Provider.CompletionPath, "/") {
		return fmt.Errorf("invalid provider.completion_path %q: must start with /", c.Provider.CompletionPath)
	}

	// Validate fim_tokens fields are all non-empty
	if c.Provider.FIMTokens.Prefix == "" {
		return fmt.Errorf("invalid provider.fim_tokens.prefix: must be non-empty")
	}
	if c.Provider.FIMTokens.Suffix == "" {
		return fmt.Errorf("invalid provider.fim_tokens.suffix: must be non-empty")
	}
	if c.Provider.FIMTokens.Middle == "" {
		return fmt.Errorf("invalid provider.fim_tokens.middle: must be non-empty")
	}

	return nil
}

type ServerMode string

const (
	ModeDaemon ServerMode = "daemon"
	ModeClient ServerMode = "client"
)

// ensureStateDir creates the state directory if it doesn't exist
func ensureStateDir(stateDir string) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		logger.Fatal("error creating state directory: %v", err)
	}
}

// Setup logger to log to a file in the state directory
// Caller must defer logger.Close()
func setupLogger(stateDir, logLevel string) *logger.LimitedLogger {
	ensureStateDir(stateDir)
	logPath := filepath.Join(stateDir, "cursortab.log")

	f, err := os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		logger.Fatal("error opening file: %v", err)
	}

	level := logger.ParseLogLevel(logLevel)
	return logger.NewLimitedLogger(f, level)
}

func getSocketPath(stateDir string) string {
	return filepath.Join(stateDir, "cursortab.sock")
}

func getPidPath(stateDir string) string {
	return filepath.Join(stateDir, "cursortab.pid")
}

func isDaemonRunning(stateDir string) (bool, int) {
	pidPath := getPidPath(stateDir)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return false, 0
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return false, 0
	}

	// Check if process is still running
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}

	// On Unix, Signal(0) checks if process exists
	err = process.Signal(syscall.Signal(0))
	return err == nil, pid
}

// loadConfig parses config from CURSORTAB_CONFIG env var.
// Uses standard log package since this runs before our logger is initialized.
func loadConfig() Config {
	var config Config
	if err := json.Unmarshal([]byte(os.Getenv("CURSORTAB_CONFIG")), &config); err != nil {
		log.Fatalf("invalid config JSON: %v", err)
	}

	if err := config.Validate(); err != nil {
		log.Fatalf("config validation failed: %v", err)
	}

	return config
}

func runDaemon() {
	// Load config first to get state_dir
	config := loadConfig()

	// Setup logger with state_dir from config
	ll := setupLogger(config.StateDir, config.LogLevel)
	defer ll.Close()

	daemon, err := NewDaemon(config)
	if err != nil {
		logger.Fatal("error creating daemon: %v", err)
	}

	if err := daemon.Start(); err != nil {
		logger.Fatal("error starting daemon: %v", err)
	}
}

func runClient() {
	config := loadConfig()
	client := NewClient(config.StateDir)

	if err := client.EnsureDaemonRunning(config.StateDir); err != nil {
		logger.Fatal("error ensuring daemon is running: %v", err)
	}

	if err := client.Connect(); err != nil {
		logger.Fatal("error connecting to daemon: %v", err)
	}
}

func main() {
	var mode ServerMode = ModeClient

	// Check command line arguments
	if len(os.Args) > 1 && os.Args[1] == "--daemon" {
		mode = ModeDaemon
	}

	switch mode {
	case ModeDaemon:
		runDaemon()
	case ModeClient:
		runClient()
	}
}

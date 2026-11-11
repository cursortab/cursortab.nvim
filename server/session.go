package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursortab/buffer"
	"cursortab/engine"
	"cursortab/metrics"
	"cursortab/provider/copilot"
	"cursortab/provider/dataset"
	"cursortab/provider/fim"
	"cursortab/provider/inline"
	"cursortab/provider/mercuryapi"
	"cursortab/provider/sweep"
	"cursortab/provider/windsurf"
	"cursortab/provider/zeta"
	"cursortab/provider/zeta2"
	"cursortab/types"

	"github.com/neovim/go-client/nvim"
)

// SessionConfig is the configuration one Neovim instance sends when its
// session starts. All values come from the Lua client; none are defaulted here.
type SessionConfig struct {
	NsID           int            `json:"ns_id"`
	EditorVersion  string         `json:"editor_version"`
	EditorOS       string         `json:"editor_os"`
	ContributeData bool           `json:"contribute_data"`
	Behavior       BehaviorConfig `json:"behavior"`
	Provider       ProviderConfig `json:"provider"`
}

// BehaviorConfig holds timing and behavior settings
type BehaviorConfig struct {
	IdleCompletionDelay int                    `json:"idle_completion_delay"` // in milliseconds
	TextChangeDebounce  int                    `json:"text_change_debounce"`  // in milliseconds
	MaxVisibleLines     int                    `json:"max_visible_lines"`     // max visible lines per completion (0 to disable)
	CursorPrediction    CursorPredictionConfig `json:"cursor_prediction"`
	DisabledIn          []string               `json:"disabled_in"`
	CompleteInInsert    bool                   `json:"complete_in_insert"`
	CompleteInNormal    bool                   `json:"complete_in_normal"`
}

// CursorPredictionConfig holds cursor prediction settings
type CursorPredictionConfig struct {
	Enabled            bool `json:"enabled"`
	AutoAdvance        bool `json:"auto_advance"`
	ProximityThreshold int  `json:"proximity_threshold"`
}

// ProviderConfig holds provider-specific settings
type ProviderConfig struct {
	Type                 string                `json:"type"`
	URL                  string                `json:"url"`
	APIKey               string                `json:"api_key"` // Resolved by the editor from api_key_env
	Model                string                `json:"model"`
	Temperature          float64               `json:"temperature"`
	ContextSize          int                   `json:"context_size"` // Max input context size in tokens (0 = use max_tokens)
	MaxTokens            int                   `json:"max_tokens"`   // Max tokens to generate
	TopK                 int                   `json:"top_k"`
	TopP                 float64               `json:"top_p"`              // 0 leaves the provider default
	CompletionTimeout    int                   `json:"completion_timeout"` // in milliseconds
	MaxDiffHistoryTokens int                   `json:"max_diff_history_tokens"`
	CompletionPath       string                `json:"completion_path"`
	FIMTokens            *types.FIMTokenConfig `json:"fim_tokens,omitempty"`
	PrivacyMode          bool                  `json:"privacy_mode"`
}

// Validate checks that the config has valid values.
func (c *SessionConfig) Validate() error {
	checks := []struct {
		bad   bool
		field string
		value any
		rule  string
	}{
		{c.Behavior.IdleCompletionDelay < -1, "behavior.idle_completion_delay", c.Behavior.IdleCompletionDelay, "must be >= -1"},
		{c.Behavior.TextChangeDebounce < -1, "behavior.text_change_debounce", c.Behavior.TextChangeDebounce, "must be >= -1"},
		{c.Behavior.MaxVisibleLines < 0, "behavior.max_visible_lines", c.Behavior.MaxVisibleLines, "must be >= 0"},
		{c.Provider.ContextSize < 0, "provider.context_size", c.Provider.ContextSize, "must be >= 0"},
		{c.Provider.MaxTokens < 0, "provider.max_tokens", c.Provider.MaxTokens, "must be >= 0"},
		{c.Provider.CompletionTimeout < 0, "provider.completion_timeout", c.Provider.CompletionTimeout, "must be >= 0"},
		{c.Provider.MaxDiffHistoryTokens < 0, "provider.max_diff_history_tokens", c.Provider.MaxDiffHistoryTokens, "must be >= 0"},
		{c.Provider.TopP < 0 || c.Provider.TopP > 1, "provider.top_p", c.Provider.TopP, "must be between 0 and 1"},
		{!strings.HasPrefix(c.Provider.CompletionPath, "/"), "provider.completion_path", c.Provider.CompletionPath, "must start with /"},
	}
	for _, check := range checks {
		if check.bad {
			return fmt.Errorf("invalid %s %v: %s", check.field, check.value, check.rule)
		}
	}

	// When fim_tokens is configured, prefix/suffix/middle must all be non-empty.
	// Absence (nil) signals prompt+suffix mode and requires no validation.
	if t := c.Provider.FIMTokens; t != nil {
		for _, token := range []struct{ name, value string }{{"prefix", t.Prefix}, {"suffix", t.Suffix}, {"middle", t.Middle}} {
			if token.value == "" {
				return fmt.Errorf("invalid provider.fim_tokens.%s: must be non-empty", token.name)
			}
		}
	}
	return nil
}

// parseSessionConfig decodes and validates the config sent by the editor.
func parseSessionConfig(raw string) (SessionConfig, error) {
	var config SessionConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return config, fmt.Errorf("invalid session config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return config, err
	}
	return config, nil
}

// newSessionEngine builds the provider, buffer, and engine of one editor
// connection.
func newSessionEngine(client *nvim.Nvim, config SessionConfig, deviceID string) (*engine.Engine, error) {
	buf := buffer.New(client, config.NsID)
	providerConfig := &types.ProviderConfig{
		URL:               config.Provider.URL,
		APIKey:            config.Provider.APIKey,
		Model:             config.Provider.Model,
		Temperature:       config.Provider.Temperature,
		ContextSize:       config.Provider.ContextSize,
		MaxTokens:         config.Provider.MaxTokens,
		TopK:              config.Provider.TopK,
		TopP:              config.Provider.TopP,
		CompletionPath:    config.Provider.CompletionPath,
		FIMTokens:         config.Provider.FIMTokens,
		CompletionTimeout: config.Provider.CompletionTimeout,
		PrivacyMode:       config.Provider.PrivacyMode,
		Version:           Version,
		EditorVersion:     config.EditorVersion,
		EditorOS:          config.EditorOS,
	}

	var prov engine.Provider
	switch types.ProviderType(config.Provider.Type) {
	case types.ProviderTypeInline:
		prov = inline.NewProvider(providerConfig)
	case types.ProviderTypeFIM:
		prov = fim.NewProvider(providerConfig)
	case types.ProviderTypeSweep:
		prov = sweep.NewProvider(providerConfig)
	case types.ProviderTypeZeta:
		prov = zeta.NewProvider(providerConfig)
	case types.ProviderTypeZeta2:
		prov = zeta2.NewProvider(providerConfig)
	case types.ProviderTypeZeta21:
		prov = zeta2.NewProvider21(providerConfig)
	case types.ProviderTypeCopilot:
		prov = copilot.NewProvider(buf)
	case types.ProviderTypeWindsurf:
		prov = windsurf.NewProvider(buf)
	case types.ProviderTypeMercuryAPI:
		prov = mercuryapi.NewProvider(providerConfig)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", config.Provider.Type)
	}

	var community metrics.Sender
	if config.ContributeData {
		community = dataset.NewSender(deviceID, Version, "") // Default: api.cursortab.com
	}

	behavior := config.Behavior
	return engine.New(engine.Deps{
		Provider:      prov,
		Buffer:        buf,
		Clock:         engine.SystemClock,
		Metrics:       community,
		StateNotifier: buf.NotifyState,
	}, engine.Config{
		ProviderName:        config.Provider.Type,
		CompletionTimeout:   time.Duration(config.Provider.CompletionTimeout) * time.Millisecond,
		IdleCompletionDelay: time.Duration(behavior.IdleCompletionDelay) * time.Millisecond,
		TextChangeDebounce:  time.Duration(behavior.TextChangeDebounce) * time.Millisecond,
		CursorPrediction: engine.CursorPredictionConfig{
			Enabled:            behavior.CursorPrediction.Enabled,
			AutoAdvance:        behavior.CursorPrediction.AutoAdvance,
			ProximityThreshold: behavior.CursorPrediction.ProximityThreshold,
		},
		MaxDiffTokens:    config.Provider.MaxDiffHistoryTokens,
		MaxVisibleLines:  behavior.MaxVisibleLines,
		DisabledIn:       behavior.DisabledIn,
		CompleteInInsert: behavior.CompleteInInsert,
		CompleteInNormal: behavior.CompleteInNormal,
	}), nil
}

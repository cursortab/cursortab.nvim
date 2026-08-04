package harness

import (
	"fmt"
	"net/http"
	"strings"

	"cursortab/engine"
	"cursortab/eval/cassette"
	"cursortab/provider/copilot"
	"cursortab/provider/fim"
	"cursortab/provider/mercuryapi"
	"cursortab/provider/sweep"
	"cursortab/provider/windsurf"
	"cursortab/provider/zeta"
	"cursortab/provider/zeta2"
	"cursortab/types"
)

// BuildProviderForTarget constructs a real provider for the given target,
// wired with the given HTTP transport. baseCfg is merged on top of the
// target's own Model/URL overrides.
//
// cs is only used by type=copilot (LSP replay). HTTP providers ignore it.
// For copilot, if cs is non-nil, a new cassetteCopilotLSP is created;
// otherwise the caller should pass in a pre-created one via the copilotLSP param.
func BuildProviderForTarget(t Target, baseCfg *types.ProviderConfig, transport http.RoundTripper, cs *cassette.Cassette, copilotLSP *cassetteCopilotLSP) (engine.Provider, error) {
	if t.Type == "" {
		return nil, fmt.Errorf("harness: target %q has empty type", t.Name)
	}
	cfg := mergeConfig(baseCfg, t)

	switch t.Type {
	case "sweep":
		p := sweep.NewProvider(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "mercuryapi":
		if t.URL != "" {
			return nil, fmt.Errorf("harness: target %q has URL override but mercuryapi only supports the hosted endpoint", t.Name)
		}
		p := mercuryapi.NewProvider(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "zeta":
		p := zeta.NewProvider(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "zeta-2":
		p := zeta2.NewProvider(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "zeta-2.1":
		p := zeta2.NewProvider21(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "fim":
		if cfg.ProviderContextSize == 0 {
			cfg.ProviderContextSize = 1024
		}
		if cfg.ProviderMaxTokens == 0 || cfg.ProviderMaxTokens > 128 {
			cfg.ProviderMaxTokens = 128
		}
		// Qwen models (and Zeta, which is Qwen-based) use the standard FIM
		// tokens; inject them when targets haven't configured FIMTokens. Without
		// this, eval FIM falls back to prompt+suffix mode and Qwen completions
		// regress.
		isQwen := strings.Contains(strings.ToLower(cfg.ProviderModel), "qwen")
		if cfg.FIMTokens == nil && isQwen {
			cfg.FIMTokens = &types.FIMTokenConfig{
				Prefix: "<|fim_prefix|>",
				Suffix: "<|fim_suffix|>",
				Middle: "<|fim_middle|>",
			}
		}
		// Qwen also supports repo-level cross-file context.
		if cfg.FIMTokens != nil && isQwen {
			if cfg.FIMTokens.RepoName == "" {
				cfg.FIMTokens.RepoName = "<|repo_name|>"
			}
			if cfg.FIMTokens.FileSep == "" {
				cfg.FIMTokens.FileSep = "<|file_sep|>"
			}
		}
		p := fim.NewProvider(cfg)
		p.SetHTTPTransport(transport)
		return p, nil
	case "copilot":
		if cs == nil {
			return nil, fmt.Errorf("harness: target %q (copilot) requires a cassette; copilot cannot be recorded from the standalone harness", t.Name)
		}
		if copilotLSP == nil {
			copilotLSP = newCassetteCopilotLSP(cs)
		}
		return copilot.NewProvider(copilotLSP), nil
	case "windsurf":
		wsInfo := newCassetteWindsurfInfo()
		p := windsurf.NewProvider(wsInfo)
		p.SetHTTPTransport(transport)
		return p, nil
	default:
		return nil, fmt.Errorf("harness: unknown provider type %q (target %q)", t.Type, t.Name)
	}
}

// mergeConfig applies target-level overrides on top of baseCfg.
func mergeConfig(base *types.ProviderConfig, t Target) *types.ProviderConfig {
	out := &types.ProviderConfig{}
	if base != nil {
		*out = *base
	}
	if t.URL != "" {
		out.ProviderURL = t.URL
	}
	if t.Model != "" {
		out.ProviderModel = t.Model
	}
	if out.CompletionPath == "" {
		out.CompletionPath = "/v1/completions"
	}
	if out.ProviderMaxTokens == 0 {
		out.ProviderMaxTokens = 2048
	}
	if out.CompletionTimeout == 0 {
		out.CompletionTimeout = 30_000
	}
	if out.APIKey == "" {
		out.APIKey = "eval-placeholder"
	}
	return out
}

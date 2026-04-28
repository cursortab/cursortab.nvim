package harness

import "os"

// DefaultTargets returns the built-in target definitions. Targets that need
// a URL read it from the CURSORTAB_EVAL_URL environment variable.
func DefaultTargets() map[string]Target {
	url := os.Getenv("CURSORTAB_EVAL_URL")

	return map[string]Target{
		"mercuryapi": {Name: "mercuryapi", Type: "mercuryapi"},
		"sweepapi":   {Name: "sweepapi", Type: "sweepapi"},
		"copilot":    {Name: "copilot", Type: "copilot"},

		"sweep-next-edit-0.5B": {Name: "sweep-next-edit-0.5B", Type: "sweep", Model: "sweep-next-edit-0.5B", URL: url},
		"sweep-next-edit-1.5B": {Name: "sweep-next-edit-1.5B", Type: "sweep", Model: "sweep-next-edit-1.5B", URL: url},
		"sweep-next-edit-7B":   {Name: "sweep-next-edit-7B", Type: "sweep", Model: "sweep-next-edit-7B", URL: url},

		"zeta":   {Name: "zeta", Type: "zeta", Model: "zeta", URL: url},
		"zeta-2": {Name: "zeta-2", Type: "zeta-2", Model: "zeta-2", URL: url},

		"qwen3.5-0.8B":    {Name: "qwen3.5-0.8B", Type: "fim", Model: "Qwen/Qwen3.5-0.8B", URL: url},
		"qwen3.5-2B":      {Name: "qwen3.5-2B", Type: "fim", Model: "Qwen/Qwen3.5-2B", URL: url},
		"qwen3.5-4B":      {Name: "qwen3.5-4B", Type: "fim", Model: "Qwen/Qwen3.5-4B", URL: url},
		"qwen3.6-27B":     {Name: "qwen3.6-27B", Type: "fim", Model: "Qwen/Qwen3.6-27B", URL: url},
		"qwen3.6-35B-A3B": {Name: "qwen3.6-35B-A3B", Type: "fim", Model: "Qwen/Qwen3.6-35B-A3B", URL: url},
	}
}

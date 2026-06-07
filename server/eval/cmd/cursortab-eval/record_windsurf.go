package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursortab/eval/cassette"
	"cursortab/eval/harness"
	windsurfprovider "cursortab/provider/windsurf"

	"github.com/neovim/go-client/nvim"
)

// recordWindsurfCmd captures Windsurf responses by driving an
// already-running Neovim session that has the Codeium extension active.
//
// Usage:
//
//	# In a terminal, start nvim with a listening socket:
//	nvim --listen /tmp/nvim-eval.sock
//	# (make sure Codeium/Windsurf is installed and running in that nvim)
//
//	# In another terminal:
//	just eval-record-windsurf /tmp/nvim-eval.sock
//
// The recorder connects to that nvim instance, discovers the Windsurf server
// port and API key via Lua, builds the completion request body from each
// scenario, and posts it to the local Windsurf HTTP endpoint with a
// cassette.Recorder capturing the exchange.
func recordWindsurfCmd(args []string) error {
	fs := flag.NewFlagSet("record-windsurf", flag.ContinueOnError)
	var (
		dir         = fs.String("scenarios", "eval/scenarios", "directory of .txtar scenario fixtures")
		sock        = fs.String("nvim", "", "path to a running nvim listen socket (required; start nvim with --listen <path>)")
		scenario    = fs.String("scenario", "", "scenario id filter (empty = all that declare target=windsurf)")
		onlyMissing = fs.Bool("missing", false, "only record scenarios that don't yet have a windsurf cassette")
		targetName  = fs.String("target", "windsurf", "target name to write cassettes under")
		timeout     = fs.Duration("timeout", 15*time.Second, "HTTP request timeout per scenario")
	)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sock == "" {
		if env := os.Getenv("NVIM"); env != "" {
			*sock = env
		}
	}
	if *sock == "" {
		return fmt.Errorf("--nvim <socket> required (or set $NVIM). Start nvim with: nvim --listen /tmp/nvim-eval.sock")
	}

	scenarios, err := loadScenarios(*dir)
	if err != nil {
		return err
	}

	n, closeNvim, err := dialNvim(*sock)
	if err != nil {
		return fmt.Errorf("connect nvim: %w", err)
	}
	defer closeNvim()

	// Discover Windsurf server info from the running Neovim.
	info, err := getWindsurfInfo(n)
	if err != nil {
		return fmt.Errorf("get windsurf info: %w", err)
	}
	if !info.Healthy {
		return fmt.Errorf("windsurf server not healthy (is the Codeium extension running?)")
	}

	recorded := 0
	skipped := 0
	missing := 0
	for _, sc := range scenarios {
		if *scenario != "" && sc.ID != *scenario {
			continue
		}
		if sc.TargetByName(*targetName) == nil {
			missing++
			continue
		}
		if *onlyMissing {
			if _, ok := sc.Cassettes[*targetName]; ok {
				skipped++
				continue
			}
		}

		fmt.Printf("recording %s / %s...\n", sc.ID, *targetName)
		cs, err := captureWindsurfForScenario(info, sc, *timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			continue
		}
		if len(cs.Interactions) == 0 {
			fmt.Fprintf(os.Stderr, "  skipped: no request-completion steps\n")
			continue
		}
		if err := writeCassette(sc, *targetName, cs); err != nil {
			return fmt.Errorf("write cassette: %w", err)
		}
		fmt.Printf("  captured %d interaction(s), %dms total\n",
			len(cs.Interactions), cs.TotalDurationMs())
		recorded++
	}

	fmt.Printf("\nrecord-windsurf: %d recorded, %d skipped, %d scenarios do not declare target %q\n",
		recorded, skipped, missing, *targetName)
	return nil
}

func getWindsurfInfo(n *nvim.Nvim) (*windsurfServerInfo, error) {
	var result map[string]any
	if err := n.ExecLua(`return require('cursortab.bridge').windsurf_get_info()`, &result, nil); err != nil {
		return nil, fmt.Errorf("lua call failed: %w", err)
	}
	if result == nil {
		return &windsurfServerInfo{}, nil
	}
	healthy, _ := result["healthy"].(bool)
	port := numberFromLua(result["port"])
	apiKey, _ := result["api_key"].(string)

	// Read the Codeium extension's current request counter so our IDs don't
	// collide with requests the extension has already sent.
	var lastReqID int
	_ = n.ExecLua(`
		local ok, codeium = pcall(require, 'codeium')
		if ok and codeium.s and codeium.s.pending_request then
			return codeium.s.pending_request[1] or 0
		end
		return 0
	`, &lastReqID, nil)

	return &windsurfServerInfo{Healthy: healthy, Port: port, APIKey: apiKey, LastReqID: lastReqID}, nil
}

func numberFromLua(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

type windsurfServerInfo struct {
	Healthy   bool
	Port      int
	APIKey    string
	LastReqID int
}

// captureWindsurfForScenario walks the scenario's steps. For each
// request-completion step, it builds a Windsurf HTTP request from the scenario's
// buffer state and captures the response via a cassette.Recorder.
func captureWindsurfForScenario(info *windsurfServerInfo, sc *harness.Scenario, timeout time.Duration) (*cassette.Cassette, error) {
	client := &http.Client{Timeout: timeout}
	recorder := cassette.NewRecorder(http.DefaultTransport)
	recorder.RecordHeaders = true
	client.Transport = recorder

	language := sc.Language
	lineEnding := "\n"
	text := strings.Join(sc.Buffer.Lines, lineEnding)
	if len(sc.Buffer.Lines) > 0 {
		text += lineEnding
	}

	absFilePath := filepath.Join("/tmp", sc.FilePath)
	absWorkspacePath := "/tmp"

	// Start well above the extension's last request ID to avoid "stale" errors.
	reqCounter := info.LastReqID + 100

	for _, step := range sc.Steps {
		switch step.Action {
		case harness.ActionRequestCompletion:
			reqCounter++
			reqBody := map[string]any{
				"metadata": map[string]any{
					"api_key":           info.APIKey,
					"ide_name":          "neovim",
					"ide_version":       "0.10.0",
					"extension_name":    "neovim",
					"extension_version": "1.20.9",
					"request_id":        reqCounter,
				},
				"editor_options": map[string]any{
					"tab_size":      4,
					"insert_spaces": true,
				},
				"document": map[string]any{
					"text":            text,
					"editor_language": language,
					"language":        windsurfprovider.LanguageEnum(language),
					"cursor_position": map[string]int{
						"row": sc.Buffer.Row - 1,
						"col": sc.Buffer.Col,
					},
					"absolute_uri":  "file://" + absFilePath,
					"workspace_uri": "file://" + absWorkspacePath,
					"line_ending":   lineEnding,
				},
			}

			body, err := json.Marshal(reqBody)
			if err != nil {
				return nil, fmt.Errorf("marshal request: %w", err)
			}

			url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/GetCompletions", info.Port)
			httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(httpReq)
			if err != nil {
				return nil, fmt.Errorf("http request: %w", err)
			}
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("non-200 response %d: %s", resp.StatusCode, string(respBody))
			}

		case harness.ActionAccept:
			// No buffer mutation needed for recording — we only capture the
			// HTTP exchange. Multi-step scenarios with accepts would need the
			// provider's response conversion to update the buffer, but for
			// simple recording this is sufficient.
		}
	}

	cs := recorder.Cassette("windsurf", "codeium-windsurf")
	cs.Meta.Notes = fmt.Sprintf("recorded from nvim for %s", sc.ID)
	if err := redactWindsurfCassette(cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func redactWindsurfCassette(cs *cassette.Cassette) error {
	for i := range cs.Interactions {
		body, err := cassette.DecodeBody(cs.Interactions[i].Request.BodyB64)
		if err != nil {
			return fmt.Errorf("decode windsurf request body: %w", err)
		}
		redacted, err := redactWindsurfRequestBody(body)
		if err != nil {
			return err
		}
		cs.Interactions[i].Request.BodyB64 = cassette.EncodeBody(redacted)
	}
	return nil
}

func redactWindsurfRequestBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode windsurf request body: %w", err)
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		metadata["api_key"] = "<REDACTED>"
	}
	return json.Marshal(payload)
}

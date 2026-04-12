package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursortab/eval/cassette"
	"cursortab/eval/harness"

	"github.com/neovim/go-client/nvim"
)

// recordCopilotCmd captures Copilot NES responses by driving an already-running
// Neovim session that has Copilot attached and authenticated.
//
// Usage:
//
//	# In a terminal, start nvim with a listening socket:
//	nvim --listen /tmp/nvim-eval.sock
//	# (make sure Copilot is installed, authenticated, and attaches in that nvim)
//
//	# In another terminal:
//	just eval-record-copilot --nvim /tmp/nvim-eval.sock
//
// The recorder connects to that nvim instance, creates a scratch buffer per
// scenario, positions the cursor, waits briefly for Copilot LSP to attach,
// fires a synchronous `textDocument/copilotInlineEdit` request via Lua, and
// writes the captured edits back into the scenario as a `cassette/copilot.ndjson`
// section.
func recordCopilotCmd(args []string) error {
	fs := flag.NewFlagSet("record-copilot", flag.ContinueOnError)
	var (
		dir         = fs.String("scenarios", "eval/scenarios", "directory of .txtar scenario fixtures")
		sock        = fs.String("nvim", "", "path to a running nvim listen socket (required; start nvim with --listen <path>)")
		scenario    = fs.String("scenario", "", "scenario id filter (empty = all that declare target=copilot)")
		onlyMissing = fs.Bool("missing", false, "only record scenarios that don't yet have a copilot cassette")
		attachWait  = fs.Duration("attach-wait", 5*time.Second, "how long to wait for Copilot LSP to attach to a buffer before giving up")
		reqTimeout  = fs.Duration("timeout", 15*time.Second, "LSP request timeout per scenario")
		targetName  = fs.String("target", "copilot", "target name to write cassettes under")
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
		cs, err := captureCopilotForScenario(n, sc, *attachWait, *reqTimeout)
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

	fmt.Printf("\nrecord-copilot: %d recorded, %d skipped, %d scenarios do not declare target %q\n",
		recorded, skipped, missing, *targetName)
	return nil
}

// dialNvim connects to a running Neovim over a unix socket.
func dialNvim(path string) (*nvim.Nvim, func(), error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, nil, err
	}
	n, err := nvim.New(conn, conn, conn, func(format string, args ...any) {})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	// Spin up the RPC reader in the background so requests can be served.
	go func() { _ = n.Serve() }()
	closer := func() { _ = n.Close() }
	return n, closer, nil
}

// captureCopilotForScenario walks through the scenario's steps. For each
// request-completion it fires a synchronous copilotInlineEdit against the
// nvim buffer. For each accept it applies the last captured edits to the
// buffer so subsequent requests see the updated state. Returns a cassette
// with one interaction per request-completion step.
func captureCopilotForScenario(n *nvim.Nvim, sc *harness.Scenario, attachWait, reqTimeout time.Duration) (*cassette.Cassette, error) {
	buf, err := n.CreateBuffer(true, false)
	if err != nil {
		return nil, fmt.Errorf("create buffer: %w", err)
	}
	defer func() {
		_ = n.ExecLua(`
			local bufnr = ...
			if vim.api.nvim_buf_is_valid(bufnr) then
				pcall(vim.api.nvim_buf_delete, bufnr, {force = true})
			end
		`, nil, int(buf))
	}()

	tmpName := filepath.Join(os.TempDir(), fmt.Sprintf("cursortab-eval-%s-%d-%s",
		sc.ID, time.Now().UnixNano(), filepath.Base(sc.FilePath)))
	if err := n.SetBufferName(buf, tmpName); err != nil {
		return nil, fmt.Errorf("set buffer name: %w", err)
	}

	bufLines := make([][]byte, len(sc.Buffer.Lines))
	for i, l := range sc.Buffer.Lines {
		bufLines[i] = []byte(l)
	}
	if err := n.SetBufferLines(buf, 0, -1, true, bufLines); err != nil {
		return nil, fmt.Errorf("set buffer lines: %w", err)
	}

	ft := filetypeFor(sc.Language)
	if ft != "" {
		_ = n.SetBufferOption(buf, "filetype", ft)
	}
	if err := n.SetCurrentBuffer(buf); err != nil {
		return nil, fmt.Errorf("focus buffer: %w", err)
	}

	row, col := clampCursor(sc.Buffer.Row, sc.Buffer.Col, len(sc.Buffer.Lines))
	if err := n.Call("nvim_win_set_cursor", nil, 0, []int{row, col}); err != nil {
		return nil, fmt.Errorf("set cursor: %w", err)
	}
	if err := waitForCopilot(n, buf, attachWait); err != nil {
		return nil, err
	}

	cs := cassette.New("copilot", "github-copilot-nes")
	cs.Meta.Notes = fmt.Sprintf("recorded from nvim for %s", sc.ID)

	var lastEditsJSON string
	interactionIdx := 0

	for _, step := range sc.Steps {
		switch step.Action {
		case harness.ActionRequestCompletion:
			start := time.Now()
			var out string
			if err := n.ExecLua(copilotSyncLua, &out, reqTimeout.Milliseconds()); err != nil {
				return nil, fmt.Errorf("step %d copilotInlineEdit: %w", interactionIdx, err)
			}
			elapsed := time.Since(start)
			if strings.HasPrefix(out, "ERROR:") {
				return nil, fmt.Errorf("step %d copilot: %s", interactionIdx, strings.TrimPrefix(out, "ERROR:"))
			}
			var probe []json.RawMessage
			if err := json.Unmarshal([]byte(out), &probe); err != nil {
				return nil, fmt.Errorf("step %d: response not edits array: %w", interactionIdx, err)
			}

			reqBody, _ := json.Marshal(map[string]any{"req_id": interactionIdx + 1})
			cs.Interactions = append(cs.Interactions, cassette.Interaction{
				Request: cassette.RecordedRequest{
					Method:  "LSP",
					URL:     "<REDACTED>",
					BodyB64: cassette.EncodeBody(reqBody),
				},
				Response: cassette.RecordedResponse{
					Status:     200,
					BodyB64:    cassette.EncodeBody([]byte(out)),
					DurationMs: elapsed.Milliseconds(),
				},
				DurationMs: elapsed.Milliseconds(),
			})
			lastEditsJSON = out
			interactionIdx++

		case harness.ActionAccept:
			if lastEditsJSON == "" || lastEditsJSON == "[]" {
				continue
			}
			// Apply the last edits to the nvim buffer so the next
			// request-completion sees the updated state.
			if err := n.ExecLua(applyEditsLua, nil, lastEditsJSON, int(buf)); err != nil {
				return nil, fmt.Errorf("apply edits: %w", err)
			}
			lastEditsJSON = ""
		}
	}
	return cs, nil
}

func clampCursor(row, col, lineCount int) (int, int) {
	if row < 1 {
		row = 1
	}
	if row > lineCount {
		row = lineCount
	}
	if col < 0 {
		col = 0
	}
	return row, col
}

// applyEditsLua applies Copilot NES edits to the buffer via
// vim.lsp.util.apply_text_edits, matching how the real plugin applies them.
const applyEditsLua = `
local edits_json, bufnr = ...
local edits = vim.json.decode(edits_json)
local text_edits = {}
for _, e in ipairs(edits) do
	table.insert(text_edits, {newText = e.text, range = e.range})
end
vim.lsp.util.apply_text_edits(text_edits, bufnr, "utf-16")
`

// waitForCopilot polls until a copilot client is attached to the buffer,
// or attachWait elapses.
func waitForCopilot(n *nvim.Nvim, buf nvim.Buffer, attachWait time.Duration) error {
	deadline := time.Now().Add(attachWait)
	for time.Now().Before(deadline) {
		var attached bool
		err := n.ExecLua(`
			local bufnr = ...
			local function find()
				local cs = vim.lsp.get_clients({name = "copilot"})
				if #cs > 0 then return cs[1] end
				cs = vim.lsp.get_clients({name = "GitHub Copilot"})
				if #cs > 0 then return cs[1] end
				return nil
			end
			local c = find()
			if not c then return false end
			if not vim.lsp.buf_is_attached(bufnr, c.id) then
				pcall(vim.lsp.buf_attach_client, bufnr, c.id)
			end
			return vim.lsp.buf_is_attached(bufnr, c.id)
		`, &attached, int(buf))
		if err == nil && attached {
			// Extra beat so Copilot can send its didOpen / initial sync.
			time.Sleep(200 * time.Millisecond)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("copilot LSP did not attach within %s (is Copilot installed & authenticated in this nvim?)", attachWait)
}

// copilotSyncLua issues a synchronous copilotInlineEdit request against the
// Copilot LSP client attached to the current buffer and returns the edits
// as a JSON string (or "ERROR: ..." on failure).
const copilotSyncLua = `
local timeout_ms = ...
local function find_client()
	local cs = vim.lsp.get_clients({name = "copilot"})
	if #cs > 0 then return cs[1] end
	cs = vim.lsp.get_clients({name = "GitHub Copilot"})
	if #cs > 0 then return cs[1] end
	return nil
end
local client = find_client()
if not client then return "ERROR: no copilot client" end
local bufnr = vim.api.nvim_get_current_buf()
if not vim.lsp.buf_is_attached(bufnr, client.id) then
	vim.lsp.buf_attach_client(bufnr, client.id)
end
-- Force a full-document didChange so Copilot sees the buffer we just filled.
local version = vim.lsp.util.buf_versions[bufnr] or vim.b[bufnr].changedtick
local lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
client:notify("textDocument/didChange", {
	textDocument = {
		uri = vim.uri_from_bufnr(bufnr),
		version = version,
	},
	contentChanges = {{
		range = {
			start = { line = 0, character = 0 },
			["end"] = { line = 2147483647, character = 0 },
		},
		text = table.concat(lines, "\n"),
	}},
})
local params = vim.lsp.util.make_position_params(0, "utf-16")
params.textDocument.version = version
local result, err = client:request_sync("textDocument/copilotInlineEdit", params, timeout_ms, bufnr)
if err then return "ERROR: " .. tostring(err) end
if not result then return "[]" end
if result.err then return "ERROR: " .. vim.json.encode(result.err) end
if result.result and result.result.edits then
	return vim.json.encode(result.result.edits)
end
return "[]"
`

// filetypeFor maps a scenario language to a vim filetype.
func filetypeFor(lang string) string {
	switch lang {
	case "go":
		return "go"
	case "python":
		return "python"
	case "typescript", "ts":
		return "typescript"
	case "javascript", "js":
		return "javascript"
	case "rust":
		return "rust"
	}
	return ""
}

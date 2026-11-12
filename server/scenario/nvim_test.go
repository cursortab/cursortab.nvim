package scenario

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neovim/go-client/nvim"
)

// TestScenariosInNeovim runs the sweep behavior scenarios in a headless
// Neovim with the plugin, the real daemon, and a fake sweep model.
func TestScenariosInNeovim(t *testing.T) {
	nvimPath, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not found")
	}
	if testing.Short() {
		t.Skip("skipped in short mode")
	}
	plugin := buildPlugin(t)

	paths, err := filepath.Glob("testdata/*.txtar")
	must(t, err, "glob")
	for _, path := range paths {
		scs, err := LoadScenario(path, nil)
		must(t, err, "load "+path)
		for _, sc := range scs {
			if sc.Provider != ProviderSweep {
				continue
			}
			t.Run(sc.ID, func(t *testing.T) {
				t.Parallel()
				to := runInNeovim(t, nvimPath, plugin, sc)
				for _, f := range to.Failures() {
					t.Error(f)
				}
			})
		}
	}
}

// buildPlugin returns a plugin directory with the Lua sources of the
// repository and a freshly built daemon.
func buildPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := filepath.Abs("../..")
	must(t, err, "repository path")
	must(t, os.Symlink(filepath.Join(repo, "lua"), filepath.Join(dir, "lua")), "link lua")
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "server", "cursortab"), ".")
	build.Dir = filepath.Join(repo, "server")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}
	return dir
}

// nvimSession is one Neovim running a scenario.
type nvimSession struct {
	t         *testing.T
	client    *nvim.Nvim
	responses *responses
	synced    chan struct{}
	state     string
	inInsert  bool
}

func runInNeovim(t *testing.T, nvimPath, plugin string, sc *Scenario) *TargetOutcome {
	queue := &responses{}
	model := httptest.NewServer(&sweepModel{responses: queue})
	t.Cleanup(model.Close)

	// The daemon socket lives in the state directory, and socket paths are
	// short: the test directory can be too long.
	stateDir, err := os.MkdirTemp("", "cursortab")
	must(t, err, "state directory")
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	workspace := t.TempDir()
	file := filepath.Join(workspace, sc.FilePath)
	content := strings.Join(sc.Buffer.Lines, "\n")
	if len(sc.Buffer.Lines) > 1 || content != "" {
		content += "\n"
	}
	must(t, os.WriteFile(file, []byte(content), 0o644), "write buffer file")

	client, err := nvim.NewChildProcess(
		nvim.ChildProcessCommand(nvimPath),
		nvim.ChildProcessArgs("--embed", "--headless", "--clean", "-n"),
		nvim.ChildProcessDir(workspace),
		nvim.ChildProcessServe(false),
	)
	must(t, err, "start nvim")
	t.Cleanup(func() { client.Close() })
	s := &nvimSession{t: t, client: client, responses: queue, synced: make(chan struct{}, 1)}
	must(t, client.RegisterHandler("cursortab_test_synced", func(state string) {
		s.state = state
		s.synced <- struct{}{}
	}), "register sync handler")
	go func() { _ = client.Serve() }()

	height := sc.Buffer.ViewportBottom - sc.Buffer.ViewportTop + 1
	var connected bool
	must(t, client.ExecLua(nvimHarness, &connected, plugin, model.URL, stateDir, file, client.ChannelID(), height), "set up plugin")
	if !connected {
		t.Fatalf("session did not start, daemon log:\n%s", readLog(stateDir))
	}

	to := &TargetOutcome{Target: Target{Name: "neovim", Type: sc.Provider}}
	// Start in insert mode at the scenario cursor, like the Go runner.
	s.keys(fmt.Sprintf("i<Cmd>lua _G.cursortab_test.start(%d, %d, %d)<CR>",
		sc.Buffer.Row, sc.Buffer.Col, max(1, sc.Buffer.ViewportTop)))
	s.inInsert = true
	for _, step := range sc.Steps {
		to.Steps = append(to.Steps, s.step(step))
	}
	to.RequestCount = queue.count()
	return to
}

func must(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func readLog(stateDir string) string {
	data, _ := os.ReadFile(filepath.Join(stateDir, "cursortab.log"))
	return string(data)
}

func (s *nvimSession) step(step Step) StepOutcome {
	out := StepOutcome{Step: step}
	switch step.Action {
	case ActionRequestCompletion:
		event := "text_changed"
		if step.Manual {
			event = "trigger_completion"
		}
		s.keys(fmt.Sprintf(`<Cmd>lua require("cursortab.daemon").send_event(%q)<CR>`, event))
	case ActionRespond:
		s.responses.push(step.Edit)
	case ActionAccept:
		s.keys("<Tab>")
	case ActionPartialAccept:
		s.keys("<S-Tab>")
	case ActionReject:
		s.keys("<Esc>")
		s.inInsert = false
	case ActionType:
		keys := strings.NewReplacer("<", "<lt>", "\n", "<CR>").Replace(step.Text)
		if !s.inInsert {
			keys = "i" + keys
			s.inInsert = true
		}
		s.keys(keys)
	case ActionMove:
		s.keys(fmt.Sprintf("<Cmd>lua vim.api.nvim_win_set_cursor(0, {%d, %d})<CR>", step.Row, step.Col))
	case ActionWait:
		time.Sleep(step.Wait)
		s.keys("")
	case ActionExpect:
		out.Observation = s.observe()
		out.Failures = step.Expect.Check(out.Observation)
	}
	return out
}

// keys types keys as a user would, then waits until the plugin handled them
// and the engine settled.
func (s *nvimSession) keys(keys string) {
	s.t.Helper()
	_, err := s.client.Input(keys + "<Cmd>lua _G.cursortab_test.sync()<CR>")
	must(s.t, err, "input "+keys)
	select {
	case <-s.synced:
	case <-time.After(30 * time.Second):
		s.t.Fatalf("no sync after %q", keys)
	}
}

func (s *nvimSession) observe() Observation {
	s.t.Helper()
	var view struct {
		Buffer []string `msgpack:"buffer"`
		Row    int      `msgpack:"row"`
		Col    int      `msgpack:"col"`
		Target int      `msgpack:"target"`
		Groups *[]struct {
			Type       string `msgpack:"type"`
			BufferLine int    `msgpack:"buffer_line"`
			StartLine  int    `msgpack:"start_line"`
			EndLine    int    `msgpack:"end_line"`
			RenderHint string `msgpack:"render_hint"`
		} `msgpack:"groups"`
	}
	must(s.t, s.client.ExecLua(`return _G.cursortab_test.observe()`, &view), "observe")
	o := Observation{
		Buffer:       view.Buffer,
		Row:          view.Row,
		Col:          view.Col,
		CursorTarget: view.Target,
		State:        s.state,
		Requests:     s.responses.count(),
	}
	if view.Groups != nil {
		o.Display = []string{}
		for _, g := range *view.Groups {
			o.Display = append(o.Display, GroupSummary(g.Type, g.BufferLine, g.EndLine-g.StartLine+1, g.RenderHint))
		}
	}
	return o
}

// nvimHarness sets up the plugin and the test helpers, opens the scenario
// file, and returns whether the session started. The helpers record what the
// UI shows from the calls the plugin makes to its UI module.
const nvimHarness = `
local plugin, url, state_dir, file, chan, height = ...
vim.opt.rtp:prepend(plugin)
vim.o.swapfile = false
vim.o.autoindent = false
vim.o.laststatus = 0
vim.o.cmdheight = 1
vim.o.lines = height + 1

local ui = require("cursortab.ui")
local daemon = require("cursortab.daemon")
local T = { target = 0 }
_G.cursortab_test = T

local show_completion, show_cursor_prediction, ensure_close_all =
	ui.show_completion, ui.show_cursor_prediction, ui.ensure_close_all
ui.ensure_close_all = function()
	ensure_close_all()
	T.groups, T.target = nil, 0
end
ui.show_completion = function(diff_result)
	show_completion(diff_result)
	if ui.has_completion() then
		T.groups = {}
		for _, g in ipairs(diff_result.groups or {}) do
			table.insert(T.groups, {
				type = g.type,
				buffer_line = g.buffer_line,
				start_line = g.start_line,
				end_line = g.end_line,
				render_hint = g.render_hint or "",
			})
		end
	end
end
ui.show_cursor_prediction = function(line)
	show_cursor_prediction(line)
	if ui.has_cursor_prediction() then
		T.target = line
	end
end

function T.start(row, col, top)
	vim.api.nvim_win_set_cursor(0, { row, col })
	vim.fn.winrestview({ topline = top })
end

-- Sync once the events of the keys typed so far ran: the scheduled callback
-- runs when Neovim waits for input again.
function T.sync()
	vim.schedule(function()
		daemon.sync(function(state)
			vim.rpcnotify(chan, "cursortab_test_synced", state)
		end)
	end)
end

function T.observe()
	local cursor = vim.api.nvim_win_get_cursor(0)
	return {
		buffer = vim.api.nvim_buf_get_lines(0, 0, -1, false),
		row = cursor[1],
		col = cursor[2],
		target = T.target,
		groups = T.groups,
	}
end

require("cursortab").setup({
	state_dir = state_dir,
	log_level = "debug",
	provider = { type = "sweep", url = url },
	behavior = { idle_completion_delay = -1, ignore_gitignored = false },
	debug = { immediate_shutdown = true },
})
vim.cmd.edit(vim.fn.fnameescape(file))
return vim.wait(10000, function()
	return daemon.get_channel_status().daemon_pid ~= nil
end, 5)
`

package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/types"
)

// responses holds the edits of the respond steps. Each provider request
// takes the oldest one; a request without one gets no edit.
type responses struct {
	mu       sync.Mutex
	edits    []*Edit
	requests int
}

func (r *responses) push(edit *Edit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, edit)
}

// next counts a request and returns its edit, or nil.
func (r *responses) next() *Edit {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	if len(r.edits) == 0 {
		return nil
	}
	edit := r.edits[0]
	r.edits = r.edits[1:]
	return edit
}

func (r *responses) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

// scriptedProvider answers each request with the next respond edit as an
// engine completion. It declares the facts of the sweep provider.
type scriptedProvider struct {
	responses *responses
}

func (p *scriptedProvider) CompletionKind() engine.CompletionKind  { return engine.CompletionEdit }
func (p *scriptedProvider) CanPrefetchFromSyntheticCurrent() bool  { return true }
func (p *scriptedProvider) RequiredMaterials() sourcectx.Materials { return nil }

func (p *scriptedProvider) Complete(context.Context, sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	edit := p.responses.next()
	if edit == nil {
		return &types.CompletionResponse{}, nil
	}
	return &types.CompletionResponse{Completion: &types.Completion{
		StartLine:  edit.StartLine,
		EndLineInc: edit.EndLineInc,
		Lines:      slices.Clone(edit.Lines),
	}}, nil
}

// sweepModel is a fake sweep model server. It answers each completion
// request with the request window rewritten by the next respond edit, so
// the real sweep provider parses it.
type sweepModel struct {
	responses *responses
}

var sweepWindow = regexp.MustCompile(`<\|file_sep\|>current/[^\n]*:(\d+):(\d+)\n`)

func (m *sweepModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
		Stream bool   `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text, err := sweepAnswer(req.Prompt, m.responses.next())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"text": text, "finish_reason": "stop"}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range []map[string]any{
		{"choices": []map[string]any{{"text": text, "finish_reason": nil}}},
		{"choices": []map[string]any{{"text": "", "finish_reason": "stop"}}},
	} {
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// sweepAnswer returns the model text for prompt: the request window with
// edit applied, after the prefill the prompt ends with.
func sweepAnswer(prompt string, edit *Edit) (string, error) {
	loc := sweepWindow.FindAllStringSubmatchIndex(prompt, -1)
	if len(loc) == 0 {
		return "", fmt.Errorf("sweep model: no window in prompt")
	}
	last := loc[len(loc)-1]
	start, _ := strconv.Atoi(prompt[last[2]:last[3]])
	end, _ := strconv.Atoi(prompt[last[4]:last[5]])
	body := prompt[last[1]:]
	body = body[:strings.Index(body, "<|file_sep|>")]
	window := strings.Split(strings.TrimSuffix(strings.Replace(body, "<|cursor|>", "", 1), "\n"), "\n")

	if edit != nil {
		if edit.StartLine < start || edit.EndLineInc > end {
			return "", fmt.Errorf("sweep model: edit %d-%d is outside the window %d-%d", edit.StartLine, edit.EndLineInc, start, end)
		}
		from, to := edit.StartLine-start, edit.EndLineInc-start+1
		window = slices.Concat(window[:from], edit.Lines, window[to:])
	}

	answer := strings.Join(window, "\n") + "\n"
	updated := strings.LastIndex(prompt, "<|file_sep|>updated/")
	prefill := prompt[updated:]
	prefill = prefill[strings.Index(prefill, "\n")+1:]
	if !strings.HasPrefix(answer, prefill) {
		return "", fmt.Errorf("sweep model: answer does not start with the prefill %q", prefill)
	}
	return answer[len(prefill):], nil
}

// handlerTransport serves HTTP requests with a handler in process.
type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	res := rec.Result()
	res.Request = req
	return res, nil
}

// recordedProvider replays an evaluation target as its cassette recorded
// it: whole responses, so it never streams, and only the requests of
// request-completion steps, so it never prefetches.
type recordedProvider struct {
	engine.Provider
}

func (p recordedProvider) CanPrefetchFromSyntheticCurrent() bool { return false }

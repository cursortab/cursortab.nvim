package zeta2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursortab/assert"
	"cursortab/client/openai"
	"cursortab/types"
)

func TestStreamReplacementPreservesLinesBeyondViewport(t *testing.T) {
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("value_%02d = %d", i, i)
	}
	want := append([]string(nil), lines...)
	want[19] = "value_19 = 42"
	// Cursor row 20 selects lines 5..35, wider than the eight-line viewport.
	response := strings.Join(want[4:35], "\n") + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"text": response, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	p := NewProvider(&types.ProviderConfig{ProviderURL: server.URL, ProviderMaxTokens: 512, CompletionTimeout: 5000})
	input := testInput("settings.py", lines, 20, 10)
	input.Current.ViewportHeight = 8
	stream, err := p.StreamCompletion(context.Background(), input)
	assert.NoError(t, err, "start stream")
	if err != nil {
		return
	}
	for range stream.Lines() {
	}
	resp, err := stream.Finish()
	assert.NoError(t, err, "finish stream")
	if err != nil {
		return
	}
	assert.NotNil(t, resp, "complete response")
	if resp == nil {
		return
	}
	assert.NotNil(t, resp.Completion, "complete replacement")
	if resp.Completion == nil {
		return
	}
	edit := resp.Completion
	got := append(append(append([]string(nil), lines[:edit.StartLine-1]...), edit.Lines...), lines[edit.EndLineInc:]...)
	assert.Equal(t, want, got, "accepting an edit preserves every unrelated line beyond the viewport")
}

func TestIncompleteRegionIsNotAnApplicableDeletion(t *testing.T) {
	cases := []struct {
		name   string
		result openai.CompletionResult
	}{
		{"token limit", openai.CompletionResult{Text: "keep\n", FinishReason: "length"}},
		{"early stop", openai.CompletionResult{Text: "keep\n", FinishReason: "stop", StoppedEarly: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvider()
			state := stateForLines("settings.py", []string{"keep", "unrelated"}, 1, 0)
			resp, err := p.Parse(state, &tc.result)
			assert.Error(t, err, "incomplete region fails finalization")
			assert.Nil(t, resp, "missing tail cannot become a deletion")
		})
	}
	t.Run("complete deletion", func(t *testing.T) {
		p := newTestProvider()
		state := stateForLines("settings.py", []string{"keep", "remove"}, 1, 0)
		resp, err := p.Parse(state, &openai.CompletionResult{Text: "keep", FinishReason: "stop"})
		assert.NoError(t, err, "complete intentional deletion")
		if err != nil {
			return
		}
		assert.NotNil(t, resp, "complete response")
		if resp == nil {
			return
		}
		assert.NotNil(t, resp.Completion, "intentional deletion remains available")
		if resp.Completion != nil {
			assert.Equal(t, []string{"keep"}, resp.Completion.Lines, "only the requested line is deleted")
		}
	})
}

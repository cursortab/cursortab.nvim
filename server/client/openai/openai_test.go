package openai

import (
	"context"
	"cursortab/assert"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method, "HTTP method")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "Content-Type header")

		body, _ := io.ReadAll(r.Body)
		var req CompletionRequest
		json.Unmarshal(body, &req)

		assert.False(t, req.Stream, "Stream should be false")

		resp := CompletionResponse{
			ID:    "test-id",
			Model: req.Model,
			Choices: []struct {
				Index        int    `json:"index"`
				Text         string `json:"text"`
				Logprobs     any    `json:"logprobs"`
				FinishReason string `json:"finish_reason"`
			}{
				{Index: 0, Text: "completion text", FinishReason: "stop"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	resp, err := client.DoCompletion(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	})

	assert.NoError(t, err, "DoCompletion")
	assert.Equal(t, "test-id", resp.ID, "ID")
	assert.Equal(t, 1, len(resp.Choices), "Choices length")
	assert.Equal(t, "completion text", resp.Choices[0].Text, "Text")
}

func TestDoCompletion_DoesNotMutateRequestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := CompletionResponse{
			Choices: []struct {
				Index        int    `json:"index"`
				Text         string `json:"text"`
				Logprobs     any    `json:"logprobs"`
				FinishReason string `json:"finish_reason"`
			}{{Index: 0, Text: "completion text", FinishReason: "stop"}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	req := &CompletionRequest{Model: "test-model", Prompt: "hello", Stream: true}

	_, err := client.DoCompletion(context.Background(), req)

	assert.NoError(t, err, "DoCompletion")
	assert.True(t, req.Stream, "caller request stream flag should stay unchanged")
}

func TestDoCompletion_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	_, err := client.DoCompletion(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	})

	assert.Error(t, err, "Expected error for HTTP 500")
	assert.True(t, strings.Contains(err.Error(), "500"), "Error should mention status code")
}

func TestDoCompletion_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	_, err := client.DoCompletion(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	})

	assert.Error(t, err, "Expected error for invalid JSON")
}

func TestDoCompletion_WithAPIKey(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		resp := CompletionResponse{
			ID: "test-id",
			Choices: []struct {
				Index        int    `json:"index"`
				Text         string `json:"text"`
				Logprobs     any    `json:"logprobs"`
				FinishReason string `json:"finish_reason"`
			}{
				{Index: 0, Text: "completion", FinishReason: "stop"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "sk-test-api-key")
	ctx := context.Background()

	_, err := client.DoCompletion(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	})

	assert.NoError(t, err, "DoCompletion")
	assert.Equal(t, "Bearer sk-test-api-key", capturedAuth, "Authorization header")
}

func TestDoCompletion_WithoutAPIKey(t *testing.T) {
	var hasAuthHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasAuthHeader = r.Header.Get("Authorization") != ""
		resp := CompletionResponse{
			ID: "test-id",
			Choices: []struct {
				Index        int    `json:"index"`
				Text         string `json:"text"`
				Logprobs     any    `json:"logprobs"`
				FinishReason string `json:"finish_reason"`
			}{
				{Index: 0, Text: "completion", FinishReason: "stop"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	_, err := client.DoCompletion(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	})

	assert.NoError(t, err, "DoCompletion")
	assert.False(t, hasAuthHeader, "Authorization header should not be set")
}

func TestDoLineStream_Basic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"), "Accept header")

		flusher, ok := w.(http.Flusher)
		assert.True(t, ok, "ResponseWriter should support Flusher")

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send SSE events
		events := []string{
			`{"id":"1","choices":[{"text":"line 1\n","index":0}]}`,
			`{"id":"2","choices":[{"text":"line 2\n","index":0}]}`,
		}
		for _, evt := range events {
			w.Write([]byte("data: " + evt + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	result := <-stream.DoneChan()

	assert.Equal(t, 2, len(lines), "lines length")
	assert.Equal(t, "line 1", lines[0], "first line")
	assert.Equal(t, "line 2", lines[1], "second line")
	assert.Equal(t, "line 1\nline 2\n", result.Text, "result text")
}

func TestDoLineStream_MaxLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send more lines than maxLines
		for i := 1; i <= 10; i++ {
			evt := `{"id":"1","choices":[{"text":"line\n","index":0}]}`
			w.Write([]byte("data: " + evt + "\n\n"))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 3) // maxLines = 3

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	result := <-stream.DoneChan()

	assert.Equal(t, 3, len(lines), "lines length")
	assert.True(t, result.StoppedEarly, "StoppedEarly")
}

func TestDoLineStream_StopToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"id":"1","choices":[{"text":"hello","index":0}]}`,
			`{"id":"2","choices":[{"text":"<STOP>more","index":0}]}`,
		}
		for _, evt := range events {
			w.Write([]byte("data: " + evt + "\n\n"))
			flusher.Flush()
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
		Stop:   []string{"<STOP>"},
	}, 0)

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	result := <-stream.DoneChan()

	// Should stop at <STOP> token
	assert.Equal(t, "stop", result.FinishReason, "FinishReason")
	assert.Equal(t, "hello", result.Text, "Text")
}

func TestDoLineStream_StopTokenAcrossChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"id":"1","choices":[{"text":"hello<ST","index":0}]}`,
			`{"id":"2","choices":[{"text":"OP>more","index":0}]}`,
		}
		for _, evt := range events {
			w.Write([]byte("data: " + evt + "\n\n"))
			flusher.Flush()
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	stream := client.DoLineStream(context.Background(), &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
		Stop:   []string{"<STOP>"},
	}, 0)

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	result := <-stream.DoneChan()

	assert.Equal(t, 1, len(lines), "lines length")
	assert.Equal(t, "hello", lines[0], "line before split stop token")
	assert.Equal(t, "stop", result.FinishReason, "FinishReason")
	assert.Equal(t, "hello", result.Text, "Text")
}

func TestDoLineStream_DoesNotMutateRequestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"text\":\"line\\n\",\"index\":0}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	req := &CompletionRequest{Model: "test-model", Prompt: "hello", Stream: false}

	stream := client.DoLineStream(context.Background(), req, 0)
	for range stream.LinesChan() {
	}
	<-stream.DoneChan()

	assert.False(t, req.Stream, "caller request stream flag should stay unchanged")
}

func TestDoLineStream_Cancel(t *testing.T) {
	started := make(chan bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		close(started)
		for range 100 {
			evt := `{"id":"1","choices":[{"text":"x","index":0}]}`
			w.Write([]byte("data: " + evt + "\n\n"))
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	// Wait for server to start sending data
	<-started
	time.Sleep(50 * time.Millisecond)
	stream.Cancel()

	result := <-stream.DoneChan()

	// Result should indicate the stream was stopped (either cancelled or incomplete)
	assert.True(t, result.FinishReason == "cancelled" || result.FinishReason == "", "FinishReason should be cancelled or empty")
}

func TestDoLineStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	for range stream.LinesChan() {
		// Should be empty
	}

	result := <-stream.DoneChan()

	assert.Equal(t, "error", result.FinishReason, "FinishReason")
	assert.Error(t, result.Err, "stream HTTP error should be returned")
}

func TestDoLineStream_ReturnsInvalidJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		w.Write([]byte("data: not json\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"text\":\"valid\\n\",\"index\":0}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	result := <-stream.DoneChan()

	assert.Equal(t, 0, len(lines), "lines length")
	assert.Error(t, result.Err, "invalid JSON error")
}

func TestDoLineStream_SkipsComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		w.Write([]byte(": this is a comment\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"text\":\"text\\n\",\"index\":0}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	var lines []string
	for line := range stream.LinesChan() {
		lines = append(lines, line)
	}

	<-stream.DoneChan()

	assert.Equal(t, 1, len(lines), "lines length (comments skip)")
}

func TestDoLineStream_WithAPIKey(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"text\":\"line\\n\",\"index\":0}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "sk-line-stream-key")
	ctx := context.Background()

	stream := client.DoLineStream(ctx, &CompletionRequest{
		Model:  "test-model",
		Prompt: "hello",
	}, 0)

	for range stream.LinesChan() {
	}
	<-stream.DoneChan()

	assert.Equal(t, "Bearer sk-line-stream-key", capturedAuth, "Authorization header")
}

package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cursortab/logger"
)

// CompletionRequest matches the OpenAI Completion API format
type CompletionRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Suffix      string   `json:"suffix,omitempty"`
	Temperature float64  `json:"temperature"`
	MaxTokens   int      `json:"max_tokens"`
	TopK        int      `json:"top_k,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	N           int      `json:"n"`
	Echo        bool     `json:"echo"`
	Stream      bool     `json:"stream"`
}

// CompletionResponse matches the OpenAI Completion API response format
type CompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Text         string `json:"text"`
		Logprobs     any    `json:"logprobs"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// StreamChunk represents a single SSE chunk from streaming response
type StreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// CompletionResult is the raw text result shared by batch and line-stream calls.
type CompletionResult struct {
	Text         string
	FinishReason string
	StoppedEarly bool
	Err          error
}

// LineStream provides incremental line-by-line streaming
type LineStream struct {
	lines  <-chan string           // Complete lines (without trailing \n)
	done   <-chan CompletionResult // Completion signal with final result
	cancel func()                  // Cancel the stream early
}

// LinesChan returns the channel for receiving complete lines.
func (s *LineStream) LinesChan() <-chan string { return s.lines }

// DoneChan returns the final stream result.
func (s *LineStream) DoneChan() <-chan CompletionResult { return s.done }

// Cancel cancels the stream early.
func (s *LineStream) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

// DefaultCompletionPath is the default API endpoint path
const DefaultCompletionPath = "/v1/completions"

// Client is a reusable OpenAI-compatible API client
type Client struct {
	HTTPClient     *http.Client
	URL            string
	CompletionPath string
	APIKey         string
}

// NewClient creates a new OpenAI-compatible client
func NewClient(url, completionPath, apiKey string) *Client {
	return &Client{
		HTTPClient:     &http.Client{},
		URL:            url,
		CompletionPath: completionPath,
		APIKey:         apiKey,
	}
}

// SetHTTPTransport replaces the transport used for all outgoing requests.
// Used by the eval harness to intercept calls via a cassette transport.
func (c *Client) SetHTTPTransport(rt http.RoundTripper) {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	c.HTTPClient.Transport = rt
}

// DoCompletion sends a non-streaming completion request
func (c *Client) DoCompletion(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	defer logger.Trace("openai.DoCompletion")()
	req = completionRequestWithStream(req, false)

	body, err := c.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp CompletionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resp, nil
}

// DoLineStream sends a streaming completion request and returns lines as they complete.
// Lines are emitted when a newline is encountered. Stop tokens trigger stream completion.
// maxLines: stop after receiving this many lines (0 = no limit)
func (c *Client) DoLineStream(ctx context.Context, req *CompletionRequest, maxLines int) *LineStream {
	linesChan := make(chan string, 100)
	doneChan := make(chan CompletionResult, 1)

	ctx, cancel := context.WithCancel(ctx)

	stream := &LineStream{
		lines:  linesChan,
		done:   doneChan,
		cancel: cancel,
	}

	go func() {
		defer cancel()
		defer close(linesChan)
		defer close(doneChan)

		result := c.runLineStream(ctx, req, linesChan, maxLines)
		doneChan <- result
	}()

	return stream
}

// runLineStream executes the streaming request and sends lines to the channel
func (c *Client) runLineStream(ctx context.Context, req *CompletionRequest, lines chan<- string, maxLines int) CompletionResult {
	defer logger.Trace("openai.runLineStream")()
	req = completionRequestWithStream(req, true)

	// Marshal the request without HTML escaping
	var reqBodyBuf bytes.Buffer
	encoder := json.NewEncoder(&reqBodyBuf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(req); err != nil {
		logger.Error("line stream: failed to marshal request: %v", err)
		return CompletionResult{FinishReason: "error", StoppedEarly: true, Err: fmt.Errorf("marshal stream request: %w", err)}
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL+c.CompletionPath, &reqBodyBuf)
	if err != nil {
		logger.Error("line stream: failed to create request: %v", err)
		return CompletionResult{FinishReason: "error", StoppedEarly: true, Err: fmt.Errorf("create stream request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// Send the request
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return CompletionResult{FinishReason: "cancelled"}
		}
		logger.Error("line stream: failed to send request: %v", err)
		return CompletionResult{FinishReason: "error", StoppedEarly: true, Err: fmt.Errorf("send stream request: %w", err)}
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("line stream: request failed with status %d: %s", resp.StatusCode, string(body))
		return CompletionResult{FinishReason: "error", StoppedEarly: true, Err: fmt.Errorf("stream request failed with status %d: %s", resp.StatusCode, string(body))}
	}

	return c.processLineStream(ctx, resp.Body, lines, maxLines, req.Stop)
}

// processLineStream reads SSE events and emits complete lines
func (c *Client) processLineStream(ctx context.Context, body io.Reader, lines chan<- string, maxLines int, stopTokens []string) CompletionResult {
	var textBuilder strings.Builder
	var lineBuffer strings.Builder
	pending := ""
	var finishReason string
	lineCount := 0
	stoppedEarly := false

	longestStopToken := 0
	for _, token := range stopTokens {
		if len(token) > longestStopToken {
			longestStopToken = len(token)
		}
	}

	findStop := func(text string) (int, bool) {
		stopIdx := -1
		for _, token := range stopTokens {
			if token == "" {
				continue
			}
			idx := strings.Index(text, token)
			if idx != -1 && (stopIdx == -1 || idx < stopIdx) {
				stopIdx = idx
			}
		}
		return stopIdx, stopIdx != -1
	}

	commitText := func(text string) (CompletionResult, bool) {
		for _, ch := range text {
			textBuilder.WriteRune(ch)
			if ch != '\n' {
				lineBuffer.WriteRune(ch)
				continue
			}

			select {
			case lines <- lineBuffer.String():
				lineCount++
			case <-ctx.Done():
				return CompletionResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}, true
			}
			lineBuffer.Reset()

			if maxLines > 0 && lineCount >= maxLines {
				stoppedEarly = true
				logger.Debug("line stream: stopping early at %d lines (max: %d)", lineCount, maxLines)
				return CompletionResult{
					Text:         textBuilder.String(),
					FinishReason: "length",
					StoppedEarly: true,
				}, true
			}
		}
		return CompletionResult{}, false
	}

	flushLine := func(finishReason string, stoppedEarly bool) CompletionResult {
		if lineBuffer.Len() > 0 {
			select {
			case lines <- lineBuffer.String():
				lineCount++
			case <-ctx.Done():
				return CompletionResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}
			}
		}

		return CompletionResult{
			Text:         textBuilder.String(),
			FinishReason: finishReason,
			StoppedEarly: stoppedEarly,
		}
	}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return CompletionResult{
				Text:         textBuilder.String(),
				FinishReason: "cancelled",
				StoppedEarly: true,
			}
		default:
		}

		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Check for end of stream
		if line == "data: [DONE]" {
			break
		}

		// Parse SSE data line
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			return CompletionResult{
				Text:         textBuilder.String(),
				FinishReason: "error",
				StoppedEarly: true,
				Err:          fmt.Errorf("parse stream chunk: %w", err),
			}
		}

		// Extract text from chunk
		if len(chunk.Choices) > 0 {
			pending += chunk.Choices[0].Text

			if idx, ok := findStop(pending); ok {
				if result, done := commitText(pending[:idx]); done {
					return result
				}
				return flushLine("stop", false)
			}

			commitLen := len(pending)
			if longestStopToken > 0 {
				keep := longestStopToken - 1
				if commitLen > keep {
					commitLen -= keep
				} else {
					commitLen = 0
				}
			}
			if commitLen > 0 {
				if result, done := commitText(pending[:commitLen]); done {
					return result
				}
				pending = pending[commitLen:]
			}

			// Capture finish reason if present
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
		}
	}

	if pending != "" {
		if idx, ok := findStop(pending); ok {
			if result, done := commitText(pending[:idx]); done {
				return result
			}
			return flushLine("stop", false)
		}
		if result, done := commitText(pending); done {
			return result
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return CompletionResult{
				Text:         textBuilder.String(),
				FinishReason: "cancelled",
				StoppedEarly: true,
			}
		}
		return CompletionResult{
			Text:         textBuilder.String(),
			FinishReason: "error",
			StoppedEarly: true,
			Err:          fmt.Errorf("read stream: %w", err),
		}
	}

	return flushLine(finishReason, stoppedEarly)
}

func completionRequestWithStream(req *CompletionRequest, stream bool) *CompletionRequest {
	cloned := *req
	cloned.Stream = stream
	if req.Stop != nil {
		cloned.Stop = append([]string(nil), req.Stop...)
	}
	return &cloned
}

// doRequest sends an HTTP request and returns the response body
func (c *Client) doRequest(ctx context.Context, req *CompletionRequest) ([]byte, error) {
	// Marshal the request without HTML escaping
	var reqBodyBuf bytes.Buffer
	encoder := json.NewEncoder(&reqBodyBuf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL+c.CompletionPath, &reqBodyBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// Send the request
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return body, nil
}

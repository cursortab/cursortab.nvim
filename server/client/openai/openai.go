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

// StreamResult contains the result of a streaming completion
type StreamResult struct {
	Text         string
	FinishReason string
	StoppedEarly bool
}

// GetText returns the accumulated text (implements engine.StreamResult)
func (r *StreamResult) GetText() string { return r.Text }

// GetFinishReason returns the finish reason (implements engine.StreamResult)
func (r *StreamResult) GetFinishReason() string { return r.FinishReason }

// IsStoppedEarly returns whether the stream was stopped early (implements engine.StreamResult)
func (r *StreamResult) IsStoppedEarly() bool { return r.StoppedEarly }

// LineStream provides incremental line-by-line streaming
type LineStream struct {
	lines  <-chan string       // Complete lines (without trailing \n)
	done   <-chan StreamResult // Completion signal with final result
	cancel func()              // Cancel the stream early
}

// LinesChan returns the channel for receiving lines (implements engine.LineStream)
func (s *LineStream) LinesChan() <-chan string { return s.lines }

// DoneChan returns the channel for completion signal (implements engine.LineStream)
func (s *LineStream) DoneChan() <-chan StreamResult { return s.done }

// Cancel cancels the stream early (implements engine.LineStream)
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
	req.Stream = false

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
func (c *Client) DoLineStream(ctx context.Context, req *CompletionRequest, maxLines int, stopTokens []string) *LineStream {
	linesChan := make(chan string, 100)
	doneChan := make(chan StreamResult, 1)

	ctx, cancel := context.WithCancel(ctx)

	stream := &LineStream{
		lines:  linesChan,
		done:   doneChan,
		cancel: cancel,
	}

	go func() {
		defer close(linesChan)
		defer close(doneChan)

		result := c.runLineStream(ctx, req, linesChan, maxLines, stopTokens)
		doneChan <- result
	}()

	return stream
}

// runLineStream executes the streaming request and sends lines to the channel
func (c *Client) runLineStream(ctx context.Context, req *CompletionRequest, lines chan<- string, maxLines int, stopTokens []string) StreamResult {
	defer logger.Trace("openai.runLineStream")()
	req.Stream = true

	// Marshal the request without HTML escaping
	var reqBodyBuf bytes.Buffer
	encoder := json.NewEncoder(&reqBodyBuf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(req); err != nil {
		logger.Error("line stream: failed to marshal request: %v", err)
		return StreamResult{FinishReason: "error"}
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL+c.CompletionPath, &reqBodyBuf)
	if err != nil {
		logger.Error("line stream: failed to create request: %v", err)
		return StreamResult{FinishReason: "error"}
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
			return StreamResult{FinishReason: "cancelled"}
		}
		logger.Error("line stream: failed to send request: %v", err)
		return StreamResult{FinishReason: "error"}
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("line stream: request failed with status %d: %s", resp.StatusCode, string(body))
		return StreamResult{FinishReason: "error"}
	}

	return c.processLineStream(ctx, resp.Body, lines, maxLines, stopTokens)
}

// processLineStream reads SSE events and emits complete lines
func (c *Client) processLineStream(ctx context.Context, body io.Reader, lines chan<- string, maxLines int, stopTokens []string) StreamResult {
	var textBuilder strings.Builder
	var lineBuffer strings.Builder
	var finishReason string
	lineCount := 0
	stoppedEarly := false

	// Build stop token set for efficient lookup
	stopTokenSet := make(map[string]bool)
	for _, token := range stopTokens {
		stopTokenSet[token] = true
	}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return StreamResult{
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
			logger.Debug("line stream: failed to parse chunk: %v", err)
			continue
		}

		// Extract text from chunk
		if len(chunk.Choices) > 0 {
			text := chunk.Choices[0].Text

			// Check for stop tokens in the text
			for token := range stopTokenSet {
				if idx := strings.Index(text, token); idx != -1 {
					text = text[:idx]
					finishReason = "stop"
					// Process any remaining text before stop token
					if text != "" {
						lineBuffer.WriteString(text)
						textBuilder.WriteString(text)
					}
					// Flush any remaining content in buffer as final line
					if lineBuffer.Len() > 0 {
						select {
						case lines <- lineBuffer.String():
							lineCount++
						case <-ctx.Done():
							return StreamResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}
						}
					}
					return StreamResult{
						Text:         textBuilder.String(),
						FinishReason: finishReason,
						StoppedEarly: false,
					}
				}
			}

			textBuilder.WriteString(text)

			// Process text character by character for newlines
			for _, ch := range text {
				if ch == '\n' {
					// Emit complete line
					select {
					case lines <- lineBuffer.String():
						lineCount++
					case <-ctx.Done():
						return StreamResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}
					}
					lineBuffer.Reset()

					// Check line limit
					if maxLines > 0 && lineCount >= maxLines {
						stoppedEarly = true
						logger.Debug("line stream: stopping early at %d lines (max: %d)", lineCount, maxLines)
						return StreamResult{
							Text:         textBuilder.String(),
							FinishReason: "length",
							StoppedEarly: true,
						}
					}
				} else {
					lineBuffer.WriteRune(ch)
				}
			}

			// Capture finish reason if present
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Debug("line stream: scanner error: %v", err)
	}

	// Emit any remaining content as final line (handles truncation)
	if lineBuffer.Len() > 0 {
		select {
		case lines <- lineBuffer.String():
			lineCount++
		case <-ctx.Done():
		}
	}

	return StreamResult{
		Text:         textBuilder.String(),
		FinishReason: finishReason,
		StoppedEarly: stoppedEarly,
	}
}

// DoTokenStream sends a streaming completion request and emits cumulative text after each token.
// Each emission is the full accumulated text so far (idempotent for UI rendering).
// maxChars: stop after receiving this many characters (0 = no limit)
// stopTokens: stop tokens that terminate the stream (e.g., "\n" for inline completion)
func (c *Client) DoTokenStream(ctx context.Context, req *CompletionRequest, maxChars int, stopTokens []string) *LineStream {
	linesChan := make(chan string, 100)
	doneChan := make(chan StreamResult, 1)

	ctx, cancel := context.WithCancel(ctx)

	stream := &LineStream{
		lines:  linesChan,
		done:   doneChan,
		cancel: cancel,
	}

	go func() {
		defer close(linesChan)
		defer close(doneChan)

		result := c.runTokenStream(ctx, req, linesChan, maxChars, stopTokens)
		doneChan <- result
	}()

	return stream
}

// runTokenStream executes the streaming request and sends cumulative text to the channel
func (c *Client) runTokenStream(ctx context.Context, req *CompletionRequest, textChan chan<- string, maxChars int, stopTokens []string) StreamResult {
	defer logger.Trace("openai.runTokenStream")()
	req.Stream = true

	// Marshal the request without HTML escaping
	var reqBodyBuf bytes.Buffer
	encoder := json.NewEncoder(&reqBodyBuf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(req); err != nil {
		logger.Error("token stream: failed to marshal request: %v", err)
		return StreamResult{FinishReason: "error"}
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL+c.CompletionPath, &reqBodyBuf)
	if err != nil {
		logger.Error("token stream: failed to create request: %v", err)
		return StreamResult{FinishReason: "error"}
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
			return StreamResult{FinishReason: "cancelled"}
		}
		logger.Error("token stream: failed to send request: %v", err)
		return StreamResult{FinishReason: "error"}
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("token stream: request failed with status %d: %s", resp.StatusCode, string(body))
		return StreamResult{FinishReason: "error"}
	}

	return c.processTokenStream(ctx, resp.Body, textChan, maxChars, stopTokens)
}

// processTokenStream reads SSE events and emits cumulative text after each chunk
func (c *Client) processTokenStream(ctx context.Context, body io.Reader, textChan chan<- string, maxChars int, stopTokens []string) StreamResult {
	var textBuilder strings.Builder
	var finishReason string
	stoppedEarly := false

	// Build stop token set for efficient lookup
	stopTokenSet := make(map[string]bool)
	for _, token := range stopTokens {
		stopTokenSet[token] = true
	}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return StreamResult{
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
			logger.Debug("token stream: failed to parse chunk: %v", err)
			continue
		}

		// Extract text from chunk
		if len(chunk.Choices) > 0 {
			text := chunk.Choices[0].Text

			// Check for stop tokens in the text
			for token := range stopTokenSet {
				if idx := strings.Index(text, token); idx != -1 {
					// Only take text before the stop token
					text = text[:idx]
					finishReason = "stop"
					if text != "" {
						textBuilder.WriteString(text)
						// Emit final accumulated text
						select {
						case textChan <- textBuilder.String():
						case <-ctx.Done():
							return StreamResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}
						}
					}
					return StreamResult{
						Text:         textBuilder.String(),
						FinishReason: finishReason,
						StoppedEarly: false,
					}
				}
			}

			textBuilder.WriteString(text)

			// Check character limit
			if maxChars > 0 && textBuilder.Len() >= maxChars {
				stoppedEarly = true
				logger.Debug("token stream: stopping early at %d chars (max: %d)", textBuilder.Len(), maxChars)
				// Emit final accumulated text before stopping
				select {
				case textChan <- textBuilder.String():
				case <-ctx.Done():
				}
				return StreamResult{
					Text:         textBuilder.String(),
					FinishReason: "length",
					StoppedEarly: true,
				}
			}

			// Emit cumulative text after each chunk (idempotent for UI)
			select {
			case textChan <- textBuilder.String():
			case <-ctx.Done():
				return StreamResult{Text: textBuilder.String(), FinishReason: "cancelled", StoppedEarly: true}
			}

			// Capture finish reason if present
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Debug("token stream: scanner error: %v", err)
	}

	return StreamResult{
		Text:         textBuilder.String(),
		FinishReason: finishReason,
		StoppedEarly: stoppedEarly,
	}
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

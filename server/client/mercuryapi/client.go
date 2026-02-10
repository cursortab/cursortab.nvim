package mercuryapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursortab/logger"
)

// CompletionURL is the endpoint for completion requests
const CompletionURL = "https://api.inceptionlabs.ai/v1/edit/completions"

// FeedbackURL is the endpoint for feedback reporting
const FeedbackURL = "https://api-feedback.inceptionlabs.ai/feedback"

// Model is the Mercury coder model name
const Model = "mercury-coder"

// Request is the OpenAI-compatible request format for Mercury
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Response is the OpenAI-compatible response format from Mercury
type Response struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
}

// Choice represents a completion choice
type Choice struct {
	Message      MessageContent `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

// MessageContent represents the message content in a choice
type MessageContent struct {
	Content string `json:"content"`
}

// FeedbackAction represents the user action for feedback
type FeedbackAction string

const (
	FeedbackAccept FeedbackAction = "accept"
	FeedbackReject FeedbackAction = "reject"
	FeedbackIgnore FeedbackAction = "ignore"
)

// FeedbackRequest is the request format for the feedback API
type FeedbackRequest struct {
	RequestID       string         `json:"request_id"`
	ProviderName    string         `json:"provider_name"`
	UserAction      FeedbackAction `json:"user_action"`
	ProviderVersion string         `json:"provider_version"`
}

// Client is the HTTP client for the Mercury API
type Client struct {
	HTTPClient  *http.Client
	URL         string
	feedbackURL string
	AuthToken   string
}

// NewClient creates a new Mercury API client.
// If configURL points to a local server (http://127.0.0.1), it is used
// directly for both completion and feedback. Otherwise the production
// endpoints are used.
func NewClient(configURL, apiKey string, timeoutMs int) *Client {
	timeout := time.Duration(0)
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}

	url := CompletionURL
	feedbackURL := FeedbackURL
	if strings.HasPrefix(configURL, "http://127.0.0.1") {
		url = configURL
		feedbackURL = strings.TrimSuffix(configURL, "/v1/edit/completions") + "/feedback"
	}

	return &Client{
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
		URL:         url,
		feedbackURL: feedbackURL,
		AuthToken:   apiKey,
	}
}

// DoCompletion sends a completion request to the Mercury API
func (c *Client) DoCompletion(ctx context.Context, req *Request) (*Response, error) {
	defer logger.Trace("mercuryapi.DoCompletion")()

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connection", "keep-alive")
	if c.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp Response
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &apiResp, nil
}

// SendFeedback sends feedback about a completion to the Mercury API
func (c *Client) SendFeedback(ctx context.Context, req *FeedbackRequest) error {
	defer logger.Trace("mercuryapi.SendFeedback")()

	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal feedback request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.feedbackURL, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create feedback request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send feedback request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("feedback request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ExtractCompletion extracts the completion text from the response.
// Strips markdown code block wrapping if present.
func ExtractCompletion(resp *Response) string {
	if len(resp.Choices) == 0 {
		return ""
	}

	text := resp.Choices[0].Message.Content
	if text == "" {
		return ""
	}

	// Strip markdown code block if present
	text = strings.TrimPrefix(text, "```\n")
	text = strings.TrimSuffix(text, "\n```")

	// Handle "None" response which means no prediction
	if text == "None" {
		return ""
	}

	return text
}

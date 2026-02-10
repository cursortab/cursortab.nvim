package sweepapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursortab/logger"

	"github.com/andybalholm/brotli"
)

// AutocompleteRequest is the request format for the Sweep API
type AutocompleteRequest struct {
	RepoName             string       `json:"repo_name"`
	FilePath             string       `json:"file_path"`
	FileContents         string       `json:"file_contents"`
	OriginalFileContents string       `json:"original_file_contents"`
	CursorPosition       int          `json:"cursor_position"`
	RecentChanges        string       `json:"recent_changes"`
	ChangesAboveCursor   bool         `json:"changes_above_cursor"`
	MultipleSuggestions  bool         `json:"multiple_suggestions"`
	UseBytes             bool         `json:"use_bytes"`
	PrivacyModeEnabled   bool         `json:"privacy_mode_enabled"`
	FileChunks           []FileChunk  `json:"file_chunks"`
	RecentUserActions    []UserAction `json:"recent_user_actions"`
	RetrievalChunks      []FileChunk  `json:"retrieval_chunks"`
}

// FileChunk represents a chunk of file content
type FileChunk struct {
	FilePath  string  `json:"file_path"`
	Content   string  `json:"content"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Timestamp *uint64 `json:"timestamp,omitempty"`
}

// UserAction represents a user action for the Sweep API
type UserAction struct {
	ActionType string `json:"action_type"` // SCREAMING_SNAKE_CASE: INSERT_CHAR, DELETE_CHAR, etc.
	FilePath   string `json:"file_path"`
	LineNumber int    `json:"line_number"` // 1-indexed
	Offset     int    `json:"offset"`      // Byte offset in file
	Timestamp  int64  `json:"timestamp"`   // Unix epoch milliseconds
}

// AutocompleteResponse is the response format from the Sweep API
type AutocompleteResponse struct {
	AutocompleteID string `json:"autocomplete_id"`
	StartIndex     int    `json:"start_index"`
	EndIndex       int    `json:"end_index"`
	Completion     string `json:"completion"`
}

// CompletionURL is the endpoint for completion requests
const CompletionURL = "https://autocomplete.sweep.dev/backend/next_edit_autocomplete"

// MetricsURL is the endpoint for tracking acceptance metrics
const MetricsURL = "https://backend.app.sweep.dev/backend/track_autocomplete_metrics"

// EventType represents the type of metrics event
type EventType string

const (
	EventAccepted EventType = "autocomplete_suggestion_accepted"
	EventShown    EventType = "autocomplete_suggestion_shown"
	EventDisposed EventType = "autocomplete_suggestion_disposed"
)

// SuggestionType represents how the suggestion was displayed
type SuggestionType string

const (
	SuggestionGhostText SuggestionType = "GHOST_TEXT"
	SuggestionPopup     SuggestionType = "POPUP"
)

// MetricsRequest is the request format for tracking acceptance metrics
type MetricsRequest struct {
	EventType          EventType      `json:"event_type"`
	SuggestionType     SuggestionType `json:"suggestion_type"`
	Additions          int            `json:"additions"`
	Deletions          int            `json:"deletions"`
	AutocompleteID     string         `json:"autocomplete_id"`
	EditTracking       string         `json:"edit_tracking"`
	EditTrackingLine   *int           `json:"edit_tracking_line"`
	Lifespan           *int64         `json:"lifespan"`
	DebugInfo          string         `json:"debug_info"`
	DeviceID           string         `json:"device_id"`
	PrivacyModeEnabled bool           `json:"privacy_mode_enabled"`
}

// Client is the HTTP client for the Sweep API
type Client struct {
	HTTPClient *http.Client
	URL        string
	metricsURL string
	AuthToken  string
	UserAgent  string
}

// NewClient creates a new Sweep API client.
// If configURL points to a local server (http://127.0.0.1), it is used
// directly for both completion and metrics. Otherwise the production
// endpoints are used.
func NewClient(configURL, apiKey string, timeoutMs int) *Client {
	timeout := time.Duration(0)
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}

	url := CompletionURL
	metricsURL := MetricsURL
	if strings.HasPrefix(configURL, "http://127.0.0.1") {
		url = configURL
		metricsURL = strings.TrimSuffix(configURL, "/backend/next_edit_autocomplete") + "/backend/track_autocomplete_metrics"
	}

	return &Client{
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
		URL:        url,
		metricsURL: metricsURL,
		AuthToken:  apiKey,
	}
}

// DoCompletion sends a completion request to the Sweep API.
// The response is ndjson (newline-delimited JSON) when multiple_suggestions is true,
// returning one AutocompleteResponse per line. For single suggestions, returns a slice of one.
func (c *Client) DoCompletion(ctx context.Context, req *AutocompleteRequest) ([]*AutocompleteResponse, error) {
	defer logger.Trace("sweepapi.DoCompletion")()

	// Marshal request to JSON
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Compress with brotli (quality 1 for speed)
	var compressedBuf bytes.Buffer
	brotliWriter := brotli.NewWriterLevel(&compressedBuf, 1)
	if _, err := brotliWriter.Write(jsonData); err != nil {
		return nil, fmt.Errorf("failed to compress request: %w", err)
	}
	if err := brotliWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close brotli writer: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL, &compressedBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Content-Encoding", "br")
	if c.UserAgent != "" {
		httpReq.Header.Set("User-Agent", c.UserAgent)
	}
	if c.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	// Send request
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

	// Parse ndjson response (one JSON object per line)
	var results []*AutocompleteResponse
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var apiResp AutocompleteResponse
		if err := json.Unmarshal([]byte(line), &apiResp); err != nil {
			return nil, fmt.Errorf("failed to parse response line: %w", err)
		}
		results = append(results, &apiResp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return results, nil
}

// LineStream provides line-by-line streaming of completion results.
// The stream applies byte-range edits from ndjson responses to the original file
// content, then emits the resulting lines.
type LineStream struct {
	lines  chan string
	cancel context.CancelFunc
	// AutocompleteID from the first edit (for metrics)
	AutocompleteID string
}

// LinesChan returns the channel that emits complete lines.
func (s *LineStream) LinesChan() <-chan string {
	return s.lines
}

// Cancel stops the stream.
func (s *LineStream) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

// DoCompletionStream sends a completion request and returns a LineStream.
// The stream reads ndjson responses, applies all byte-range edits to fileContents,
// and emits the resulting lines of the modified file.
func (c *Client) DoCompletionStream(ctx context.Context, req *AutocompleteRequest, fileContents string) *LineStream {
	linesChan := make(chan string, 100)
	ctx, cancel := context.WithCancel(ctx)
	ls := &LineStream{lines: linesChan, cancel: cancel}

	go func() {
		defer close(linesChan)

		responses, err := c.DoCompletion(ctx, req)
		if err != nil {
			logger.Warn("sweepapi: stream error: %v", err)
			return
		}

		// Filter non-empty edits and capture autocomplete ID
		var edits []*AutocompleteResponse
		for _, r := range responses {
			if r.Completion != "" {
				edits = append(edits, r)
			}
		}
		if len(edits) == 0 {
			return
		}

		ls.AutocompleteID = edits[0].AutocompleteID

		// Apply all byte-range edits to produce the modified text
		modifiedText := ApplyByteRangeEdits(fileContents, edits)

		// Emit lines of the modified file
		for line := range strings.SplitSeq(modifiedText, "\n") {
			select {
			case linesChan <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ls
}

// CursorToByteOffset converts a cursor position (1-indexed row, 0-indexed col)
// to a byte offset within the text content.
func CursorToByteOffset(lines []string, row, col int) int {
	offset := 0
	for i := 0; i < row-1 && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	if row >= 1 && row <= len(lines) {
		offset += min(col, len(lines[row-1]))
	}
	return offset
}

// ApplyByteRangeEdits applies multiple byte-range edits to text sequentially,
// adjusting offsets as each edit changes the text length.
// Returns the final modified text.
func ApplyByteRangeEdits(text string, edits []*AutocompleteResponse) string {
	offset := 0
	for _, edit := range edits {
		start := edit.StartIndex + offset
		end := edit.EndIndex + offset
		if start < 0 {
			start = 0
		}
		if end > len(text) {
			end = len(text)
		}
		if start > end {
			start = end
		}
		text = text[:start] + edit.Completion + text[end:]
		offset += len(edit.Completion) - (edit.EndIndex - edit.StartIndex)
	}
	return text
}

// TrackMetrics sends acceptance/shown metrics to the Sweep API
func (c *Client) TrackMetrics(ctx context.Context, req *MetricsRequest) error {
	defer logger.Trace("sweepapi.TrackMetrics")()

	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal metrics request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.metricsURL, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create metrics request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send metrics request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("metrics request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

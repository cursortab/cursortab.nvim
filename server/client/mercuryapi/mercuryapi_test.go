package mercuryapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursortab/assert"
)

func TestExtractCompletion(t *testing.T) {
	tests := []struct {
		name     string
		response *Response
		expected string
	}{
		{
			name: "simple completion",
			response: &Response{
				Choices: []Choice{
					{Message: MessageContent{Content: "updated code"}},
				},
			},
			expected: "updated code",
		},
		{
			name: "completion with code block",
			response: &Response{
				Choices: []Choice{
					{Message: MessageContent{Content: "```\nfunc main() {}\n```"}},
				},
			},
			expected: "func main() {}",
		},
		{
			name: "none response",
			response: &Response{
				Choices: []Choice{
					{Message: MessageContent{Content: "None"}},
				},
			},
			expected: "",
		},
		{
			name:     "empty choices",
			response: &Response{Choices: []Choice{}},
			expected: "",
		},
		{
			name: "empty content",
			response: &Response{
				Choices: []Choice{
					{Message: MessageContent{Content: ""}},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractCompletion(tt.response)
			assert.Equal(t, tt.expected, result, "completion text")
		})
	}
}

func TestClientDoCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "Content-Type header")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "Authorization header")
		assert.Equal(t, "keep-alive", r.Header.Get("Connection"), "Connection header")

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err, "reading request body")

		var req Request
		err = json.Unmarshal(body, &req)
		assert.NoError(t, err, "parsing JSON")

		assert.Equal(t, "mercury-coder", req.Model, "model")
		assert.Equal(t, 1, len(req.Messages), "messages count")
		assert.Equal(t, "user", req.Messages[0].Role, "message role")

		resp := Response{
			ID: "test-id-123",
			Choices: []Choice{
				{
					Message:      MessageContent{Content: "new code"},
					FinishReason: "stop",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", 30000)
	req := &Request{
		Model: Model,
		Messages: []Message{
			{Role: "user", Content: "test prompt"},
		},
		Stream: false,
	}

	resp, err := client.DoCompletion(context.Background(), req)
	assert.NoError(t, err, "DoCompletion")
	assert.Equal(t, "test-id-123", resp.ID, "response ID")
	assert.Equal(t, "new code", resp.Choices[0].Message.Content, "completion content")
}

func TestClientSendFeedback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "Content-Type header")

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err, "reading request body")

		var req FeedbackRequest
		err = json.Unmarshal(body, &req)
		assert.NoError(t, err, "parsing JSON")

		assert.Equal(t, "req-123", req.RequestID, "request_id")
		assert.Equal(t, "cursortab-nvim", req.ProviderName, "provider_name")
		assert.Equal(t, FeedbackAccept, req.UserAction, "user_action")

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1/edit/completions", "", 30000)
	req := &FeedbackRequest{
		RequestID:       "req-123",
		ProviderName:    "cursortab-nvim",
		UserAction:      FeedbackAccept,
		ProviderVersion: "1.0.0",
	}

	err := client.SendFeedback(context.Background(), req)
	assert.NoError(t, err, "SendFeedback")
}

func TestClientErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid request"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", 30000)
	req := &Request{
		Model:    Model,
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	_, err := client.DoCompletion(context.Background(), req)
	assert.Error(t, err, "expected error")
	assert.Contains(t, err.Error(), "400", "error message should contain status code")
}

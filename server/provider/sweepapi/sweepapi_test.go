package sweepapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursortab/assert"
	"cursortab/client/sweepapi"
	"cursortab/types"

	"github.com/andybalholm/brotli"
)

func TestFormatRecentChanges(t *testing.T) {
	tests := []struct {
		name      string
		histories []*types.FileDiffHistory
		wantEmpty bool
		wantContains string
	}{
		{
			name:      "nil histories",
			histories: nil,
			wantEmpty: true,
		},
		{
			name:      "empty histories",
			histories: []*types.FileDiffHistory{},
			wantEmpty: true,
		},
		{
			name: "single history with entries",
			histories: []*types.FileDiffHistory{
				{
					FileName: "test.go",
					DiffHistory: []*types.DiffEntry{
						{Original: "old code", Updated: "new code"},
					},
				},
			},
			wantEmpty:    false,
			wantContains: "File: test.go:",
		},
		{
			name: "history with empty diff",
			histories: []*types.FileDiffHistory{
				{
					FileName:    "test.go",
					DiffHistory: []*types.DiffEntry{},
				},
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatRecentChanges(tt.histories)
			if tt.wantEmpty {
				assert.Equal(t, "", result, "should be empty")
			} else {
				assert.True(t, len(result) > 0, "should not be empty")
				if tt.wantContains != "" {
					assert.True(t, strings.Contains(result, tt.wantContains), "should contain expected string")
				}
			}
		})
	}
}

func TestFormatDiagnostics(t *testing.T) {
	tests := []struct {
		name         string
		linterErrors *types.LinterErrors
		wantLen      int
	}{
		{
			name:         "nil linter errors",
			linterErrors: nil,
			wantLen:      0,
		},
		{
			name: "empty errors slice",
			linterErrors: &types.LinterErrors{
				RelativeWorkspacePath: "test.go",
				Errors:                []*types.LinterError{},
			},
			wantLen: 0,
		},
		{
			name: "single error",
			linterErrors: &types.LinterErrors{
				RelativeWorkspacePath: "test.go",
				Errors: []*types.LinterError{
					{
						Message: "undefined variable",
						Source:  "go",
						Range:   &types.CursorRange{StartLine: 10},
					},
				},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDiagnostics(tt.linterErrors)
			assert.Equal(t, tt.wantLen, len(result), "chunk count")
		})
	}
}

func TestProviderGetCompletion(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and decompress request
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, _ := io.ReadAll(brotliReader)

		var req sweepapi.AutocompleteRequest
		json.Unmarshal(decompressed, &req)

		// Return a simple completion
		resp := sweepapi.AutocompleteResponse{
			StartIndex: 6,                   // Start of "world"
			EndIndex:   11,                  // End of "world"
			Completion: "universe is vast", // Replace "world" with longer text
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Set test env var
	t.Setenv("TEST_AUTH_TOKEN", "test-token")

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:           server.URL,
		AuthorizationTokenEnv: "TEST_AUTH_TOKEN",
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"hello world"},
		CursorRow: 1,
		CursorCol: 11,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Len(t, 1, resp.Completions, "completions")

	completion := resp.Completions[0]
	assert.Equal(t, 1, completion.StartLine, "StartLine")
	assert.Len(t, 1, completion.Lines, "lines")
	assert.Equal(t, "hello universe is vast", completion.Lines[0], "line content")
}

func TestProviderEmptyResponse(t *testing.T) {
	// Create a test server that returns empty completion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and decompress to avoid errors
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		resp := sweepapi.AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   0,
			Completion: "",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"hello"},
		CursorRow: 1,
		CursorCol: 5,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 0, len(resp.Completions), "completions count")
}

func TestProviderMultilineCompletion(t *testing.T) {
	// Create a test server that returns multiline completion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		// text is "func main() {\n\n}" (byte 14 is the empty line)
		// Replace the empty line with implementation
		resp := sweepapi.AutocompleteResponse{
			StartIndex: 14,
			EndIndex:   15,
			Completion: "\tfmt.Println(\"hello\")\n",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL: server.URL,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"func main() {", "", "}"},
		CursorRow: 2,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Len(t, 1, resp.Completions, "completions")

	completion := resp.Completions[0]
	assert.Equal(t, 2, completion.StartLine, "StartLine")
	// EndLineInc should be 2 (the original end line in the buffer being replaced)
	// The byte offset 15 is in line 2, so EndLineInc should be 2
	assert.Equal(t, 2, completion.EndLineInc, "EndLineInc")
}

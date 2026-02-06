package sweepapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursortab/assert"

	"github.com/andybalholm/brotli"
)

func TestCursorToByteOffset(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		row      int
		col      int
		expected int
	}{
		{
			name:     "first line, first col",
			lines:    []string{"hello", "world"},
			row:      1,
			col:      0,
			expected: 0,
		},
		{
			name:     "first line, middle col",
			lines:    []string{"hello", "world"},
			row:      1,
			col:      3,
			expected: 3,
		},
		{
			name:     "second line, first col",
			lines:    []string{"hello", "world"},
			row:      2,
			col:      0,
			expected: 6, // "hello\n" = 6 bytes
		},
		{
			name:     "second line, middle col",
			lines:    []string{"hello", "world"},
			row:      2,
			col:      2,
			expected: 8, // "hello\n" + "wo" = 8 bytes
		},
		{
			name:     "col beyond line length",
			lines:    []string{"hi", "world"},
			row:      1,
			col:      10,
			expected: 2, // clamped to line length
		},
		{
			name:     "empty lines",
			lines:    []string{"", "", "test"},
			row:      3,
			col:      2,
			expected: 4, // "\n" + "\n" + "te" = 4 bytes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CursorToByteOffset(tt.lines, tt.row, tt.col)
			assert.Equal(t, tt.expected, result, "byte offset")
		})
	}
}

func TestClientBrotliCompression(t *testing.T) {
	// Create a test server that verifies brotli encoding
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Content-Encoding header
		assert.Equal(t, "br", r.Header.Get("Content-Encoding"), "Content-Encoding header")

		// Read and decompress the request body
		compressedBody, err := io.ReadAll(r.Body)
		assert.NoError(t, err, "reading request body")

		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		decompressed, err := io.ReadAll(brotliReader)
		assert.NoError(t, err, "decompressing request")

		// Verify it's valid JSON
		var req AutocompleteRequest
		err = json.Unmarshal(decompressed, &req)
		assert.NoError(t, err, "parsing JSON")

		// Send back a valid ndjson response
		json.NewEncoder(w).Encode(AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   5,
			Completion: "hello",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", 30000)
	req := &AutocompleteRequest{
		FilePath:     "test.go",
		FileContents: "hello",
	}

	results, err := client.DoCompletion(context.Background(), req)
	assert.NoError(t, err, "DoCompletion")
	assert.Equal(t, 1, len(results), "should return 1 response")
}

func TestClientNdjsonMultipleResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		// Write two ndjson lines
		json.NewEncoder(w).Encode(AutocompleteResponse{
			AutocompleteID: "id-1",
			StartIndex:     0,
			EndIndex:       5,
			Completion:     "hello",
		})
		json.NewEncoder(w).Encode(AutocompleteResponse{
			AutocompleteID: "id-2",
			StartIndex:     6,
			EndIndex:       11,
			Completion:     "world",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", 30000)
	req := &AutocompleteRequest{
		FilePath:     "test.go",
		FileContents: "hello world",
	}

	results, err := client.DoCompletion(context.Background(), req)
	assert.NoError(t, err, "DoCompletion")
	assert.Equal(t, 2, len(results), "should return 2 responses")
	assert.Equal(t, "id-1", results[0].AutocompleteID, "first ID")
	assert.Equal(t, "id-2", results[1].AutocompleteID, "second ID")
}

func TestClientAuthorizationHeader(t *testing.T) {
	// Create a test server that verifies auth header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer my-secret-token", r.Header.Get("Authorization"), "Authorization header")

		// Read the brotli body (required for valid request)
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		json.NewEncoder(w).Encode(AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   0,
			Completion: "",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "my-secret-token", 30000)
	req := &AutocompleteRequest{
		FilePath:     "test.go",
		FileContents: "test",
	}

	_, err := client.DoCompletion(context.Background(), req)
	assert.NoError(t, err, "DoCompletion")
}

func TestApplyByteRangeEdits(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		edits    []*AutocompleteResponse
		expected string
	}{
		{
			name: "single edit",
			text: "hello world",
			edits: []*AutocompleteResponse{
				{StartIndex: 6, EndIndex: 11, Completion: "universe"},
			},
			expected: "hello universe",
		},
		{
			name: "two non-overlapping edits",
			text: "aaa bbb ccc",
			edits: []*AutocompleteResponse{
				{StartIndex: 0, EndIndex: 3, Completion: "AAA"},
				{StartIndex: 8, EndIndex: 11, Completion: "CCC"},
			},
			expected: "AAA bbb CCC",
		},
		{
			name: "edit that changes length then second edit",
			text: "ab cd ef",
			edits: []*AutocompleteResponse{
				{StartIndex: 0, EndIndex: 2, Completion: "ABCD"},
				{StartIndex: 6, EndIndex: 8, Completion: "GH"},
			},
			expected: "ABCD cd GH",
		},
		{
			name:     "empty edits",
			text:     "hello",
			edits:    []*AutocompleteResponse{},
			expected: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyByteRangeEdits(tt.text, tt.edits)
			assert.Equal(t, tt.expected, result, "result")
		})
	}
}

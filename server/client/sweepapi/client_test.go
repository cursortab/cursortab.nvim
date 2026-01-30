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

func TestByteOffsetToLineCol(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		offset      int
		expectedRow int
		expectedCol int
	}{
		{
			name:        "start of text",
			text:        "hello\nworld",
			offset:      0,
			expectedRow: 1,
			expectedCol: 0,
		},
		{
			name:        "middle of first line",
			text:        "hello\nworld",
			offset:      3,
			expectedRow: 1,
			expectedCol: 3,
		},
		{
			name:        "at newline",
			text:        "hello\nworld",
			offset:      5,
			expectedRow: 1,
			expectedCol: 5,
		},
		{
			name:        "start of second line",
			text:        "hello\nworld",
			offset:      6,
			expectedRow: 2,
			expectedCol: 0,
		},
		{
			name:        "middle of second line",
			text:        "hello\nworld",
			offset:      8,
			expectedRow: 2,
			expectedCol: 2,
		},
		{
			name:        "negative offset",
			text:        "hello",
			offset:      -1,
			expectedRow: 1,
			expectedCol: 0,
		},
		{
			name:        "offset beyond text",
			text:        "hi",
			offset:      100,
			expectedRow: 1,
			expectedCol: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, col := ByteOffsetToLineCol(tt.text, tt.offset)
			assert.Equal(t, tt.expectedRow, row, "row")
			assert.Equal(t, tt.expectedCol, col, "col")
		})
	}
}

func TestApplyByteRangeEdit(t *testing.T) {
	tests := []struct {
		name              string
		text              string
		startIdx          int
		endIdx            int
		completion        string
		expectedText      string
		expectedStartLine int
		expectedEndLine   int
	}{
		{
			name:              "insert in middle",
			text:              "hello world",
			startIdx:          5,
			endIdx:            5,
			completion:        " beautiful",
			expectedText:      "hello beautiful world",
			expectedStartLine: 1,
			expectedEndLine:   1,
		},
		{
			name:              "replace word",
			text:              "hello world",
			startIdx:          6,
			endIdx:            11,
			completion:        "universe",
			expectedText:      "hello universe",
			expectedStartLine: 1,
			expectedEndLine:   1,
		},
		{
			name:              "multiline replacement",
			text:              "line1\nline2\nline3",
			startIdx:          6,
			endIdx:            11,
			completion:        "new\nlines",
			expectedText:      "line1\nnew\nlines\nline3",
			expectedStartLine: 2,
			expectedEndLine:   3,
		},
		{
			name:              "delete (empty completion)",
			text:              "hello world",
			startIdx:          5,
			endIdx:            11,
			completion:        "",
			expectedText:      "hello",
			expectedStartLine: 1,
			expectedEndLine:   1,
		},
		{
			name:              "trailing newline single line",
			text:              "app.use(cors);",
			startIdx:          0,
			endIdx:            3,
			completion:        "application",
			expectedText:      "application.use(cors);",
			expectedStartLine: 1,
			expectedEndLine:   1,
		},
		{
			name:              "trailing newline should not add extra line",
			text:              "app.use(cors);",
			startIdx:          0,
			endIdx:            14,
			completion:        "application.use(cors);\n",
			expectedText:      "application.use(cors);\n",
			expectedStartLine: 1,
			expectedEndLine:   1,
		},
		{
			name:              "actual multiline with trailing newline",
			text:              "line1",
			startIdx:          0,
			endIdx:            5,
			completion:        "line1\nline2\n",
			expectedText:      "line1\nline2\n",
			expectedStartLine: 1,
			expectedEndLine:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newText, startLine, endLine := ApplyByteRangeEdit(tt.text, tt.startIdx, tt.endIdx, tt.completion)
			assert.Equal(t, tt.expectedText, newText, "newText")
			assert.Equal(t, tt.expectedStartLine, startLine, "startLine")
			assert.Equal(t, tt.expectedEndLine, endLine, "endLine")
		})
	}
}

func TestExtractLines(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		startLine int
		endLine   int
		expected  []string
	}{
		{
			name:      "single line",
			text:      "line1\nline2\nline3",
			startLine: 2,
			endLine:   2,
			expected:  []string{"line2"},
		},
		{
			name:      "multiple lines",
			text:      "line1\nline2\nline3",
			startLine: 1,
			endLine:   3,
			expected:  []string{"line1", "line2", "line3"},
		},
		{
			name:      "out of bounds",
			text:      "line1\nline2",
			startLine: 1,
			endLine:   10,
			expected:  []string{"line1", "line2"},
		},
		{
			name:      "start > end",
			text:      "line1\nline2",
			startLine: 3,
			endLine:   1,
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractLines(tt.text, tt.startLine, tt.endLine)
			assert.Equal(t, len(tt.expected), len(result), "length")
			for i := range result {
				assert.Equal(t, tt.expected[i], result[i], "line")
			}
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

		// Send back a valid response
		resp := AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   5,
			Completion: "hello",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", 30000)
	req := &AutocompleteRequest{
		FilePath:     "test.go",
		FileContents: "hello",
	}

	_, err := client.DoCompletion(context.Background(), req)
	assert.NoError(t, err, "DoCompletion")
}

func TestClientAuthorizationHeader(t *testing.T) {
	// Create a test server that verifies auth header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer my-secret-token", r.Header.Get("Authorization"), "Authorization header")

		// Read the brotli body (required for valid request)
		compressedBody, _ := io.ReadAll(r.Body)
		brotliReader := brotli.NewReader(bytes.NewReader(compressedBody))
		io.ReadAll(brotliReader)

		resp := AutocompleteResponse{
			StartIndex: 0,
			EndIndex:   0,
			Completion: "",
		}
		json.NewEncoder(w).Encode(resp)
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

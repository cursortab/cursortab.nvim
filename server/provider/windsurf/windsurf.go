// Package windsurf implements the Windsurf (Codeium) completion provider.
//
// This provider communicates with the Windsurf/Codeium local language server
// over HTTP. The port is assigned randomly at runtime when the Neovim Codeium
// extension starts; we discover it via lua/cursortab/lsp.lua which probes the
// extension's internal state (codeium.s.port, codeium.s.healthy, api_key).
//
// The server exposes a JSON API endpoint:
//
//	POST http://127.0.0.1:<port>/exa.language_server_pb.LanguageServerService/GetCompletions
//
// Example request:
//
//	{
//	  "metadata": {
//	    "api_key": "<api_key>",
//	    "ide_name": "neovim",
//	    "ide_version": "0.10.0",
//	    "extension_name": "neovim",
//	    "extension_version": "1.20.9",
//	    "request_id": 1
//	  },
//	  "editor_options": { "tab_size": 4, "insert_spaces": true },
//	  "document": {
//	    "text": "package main\n\nfunc main() {\n\tfmt.Println(",
//	    "editor_language": "go",
//	    "language": 9,
//	    "cursor_position": { "row": 3, "col": 13 },
//	    "absolute_uri": "file:///tmp/main.go",
//	    "workspace_uri": "file:///tmp",
//	    "line_ending": "\n"
//	  }
//	}
//
// Example response:
//
//	{
//	  "state": { "state": "CODEIUM_STATE_SUCCESS", "message": "Generated 3 completions" },
//	  "completionItems": [{
//	    "completion": {
//	      "completionId": "7decfce5-...",
//	      "text": "\tfmt.Println(\"Hello, World!\")",
//	      "stop": "\n",
//	      "score": -0.97,
//	      "tokens": ["1","9906","11","4435","23849"],
//	      "decodedTokens": ["\"","Hello",","," World","!\")\n"],
//	      "probabilities": [0.85,0.60,0.46,0.70,0.66],
//	      "adjustedProbabilities": [0,0,0,0,0],
//	      "generatedLength": "5",
//	      "stopReason": "STOP_REASON_STOP_PATTERN",
//	      "originalText": "\"Hello, World!\")\n"
//	    },
//	    "range": {
//	      "startOffset": "28",
//	      "endOffset": "41",
//	      "startPosition": { "row": "3" },
//	      "endPosition": { "row": "3", "col": "13" }
//	    },
//	    "source": "COMPLETION_SOURCE_TYPING_AS_SUGGESTED",
//	    "completionParts": [{
//	      "text": "\"Hello, World!\")",
//	      "offset": "41",
//	      "type": "COMPLETION_PART_TYPE_INLINE",
//	      "prefix": "\tfmt.Println(",
//	      "line": "3"
//	    }]
//	  }],
//	  "filteredCompletionItems": [...],
//	  "promptId": "8418cdd3-...",
//	  "codeRanges": [{ "source": "CODE_SOURCE_BASE", "endOffset": "42" }]
//	}
package windsurf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cursortab/buffer"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/types"
)

var languageEnum = map[string]int{
	"unspecified":  0,
	"c":            1,
	"clojure":      2,
	"coffeescript": 3,
	"cpp":          4,
	"csharp":       5,
	"css":          6,
	"cudacpp":      7,
	"dockerfile":   8,
	"go":           9,
	"groovy":       10,
	"handlebars":   11,
	"haskell":      12,
	"hcl":          13,
	"html":         14,
	"ini":          15,
	"java":         16,
	"javascript":   17,
	"json":         18,
	"julia":        19,
	"kotlin":       20,
	"latex":        21,
	"less":         22,
	"lua":          23,
	"makefile":     24,
	"markdown":     25,
	"objectivec":   26,
	"objectivecpp": 27,
	"perl":         28,
	"php":          29,
	"plaintext":    30,
	"protobuf":     31,
	"pbtxt":        32,
	"python":       33,
	"r":            34,
	"ruby":         35,
	"rust":         36,
	"sass":         37,
	"scala":        38,
	"scss":         39,
	"shell":        40,
	"sql":          41,
	"starlark":     42,
	"swift":        43,
	"tsx":          44,
	"typescript":   45,
	"visualbasic":  46,
	"vue":          47,
	"xml":          48,
	"xsl":          49,
	"yaml":         50,
	"svelte":       51,
}

var filetypeAliases = map[string]string{
	"bash":   "shell",
	"coffee": "coffeescript",
	"cs":     "csharp",
	"cuda":   "cudacpp",
	"dosini": "ini",
	"make":   "makefile",
	"objc":   "objectivec",
	"objcpp": "objectivecpp",
	"proto":  "protobuf",
	"raku":   "perl",
	"sh":     "shell",
	"text":   "plaintext",
}

type windsurfPos struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type windsurfResponseRange struct {
	StartOffset   string `json:"startOffset"`
	EndOffset     string `json:"endOffset"`
	StartPosition struct {
		Row string `json:"row"`
	} `json:"startPosition"`
	EndPosition struct {
		Row string `json:"row"`
		Col string `json:"col"`
	} `json:"endPosition"`
}

type windsurfCompletionPart struct {
	Text   string `json:"text"`
	Offset string `json:"offset"`
	Type   string `json:"type"`
	Prefix string `json:"prefix"`
	Line   string `json:"line"`
}

type windsurfCompletion struct {
	CompletionID          string    `json:"completionId"`
	Text                  string    `json:"text"`
	Stop                  string    `json:"stop"`
	Score                 float64   `json:"score"`
	Tokens                []string  `json:"tokens"`
	DecodedTokens         []string  `json:"decodedTokens"`
	Probabilities         []float64 `json:"probabilities"`
	AdjustedProbabilities []float64 `json:"adjustedProbabilities"`
	GeneratedLength       string    `json:"generatedLength"`
	StopReason            string    `json:"stopReason"`
	OriginalText          string    `json:"originalText"`
}

type windsurfCompletionItem struct {
	Completion      windsurfCompletion       `json:"completion"`
	Range           windsurfResponseRange    `json:"range"`
	Source          string                   `json:"source"`
	CompletionParts []windsurfCompletionPart `json:"completionParts"`
}

type windsurfState struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type windsurfResponse struct {
	State                   windsurfState            `json:"state"`
	CompletionItems         []windsurfCompletionItem `json:"completionItems"`
	FilteredCompletionItems []windsurfCompletionItem `json:"filteredCompletionItems"`
	PromptID                string                   `json:"promptId"`
}

type windsurfMetadata struct {
	APIKey           string `json:"api_key"`
	IDEName          string `json:"ide_name"`
	IDEVersion       string `json:"ide_version"`
	ExtensionName    string `json:"extension_name"`
	ExtensionVersion string `json:"extension_version"`
	RequestID        int    `json:"request_id"`
}

type windsurfEditorOptions struct {
	TabSize      int  `json:"tab_size"`
	InsertSpaces bool `json:"insert_spaces"`
}

type windsurfDocument struct {
	Text           string      `json:"text"`
	EditorLanguage string      `json:"editor_language"`
	Language       int         `json:"language"`
	CursorPosition windsurfPos `json:"cursor_position"`
	AbsoluteURI    string      `json:"absolute_uri"`
	WorkspaceURI   string      `json:"workspace_uri"`
	LineEnding     string      `json:"line_ending"`
}

type windsurfRequest struct {
	Metadata      windsurfMetadata      `json:"metadata"`
	EditorOptions windsurfEditorOptions `json:"editor_options"`
	Document      windsurfDocument      `json:"document"`
}

type windsurfAcceptRequest struct {
	Metadata     windsurfMetadata `json:"metadata"`
	CompletionID string           `json:"completion_id"`
}

type Provider struct {
	buffer     *buffer.NvimBuffer
	httpClient *http.Client
	reqCounter int
}

func NewProvider(buf *buffer.NvimBuffer) *Provider {
	return &Provider{
		buffer: buf,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (p *Provider) GetContextLimits() engine.ContextLimits {
	return engine.ContextLimits{
		MaxUserActions:     -1,
		FileChunkLines:     -1,
		MaxRecentSnapshots: -1,
		MaxDiffBytes:       -1,
		MaxChangedSymbols:  -1,
		MaxSiblings:        -1,
		MaxInputLines:      -1,
		MaxInputBytes:      -1,
	}
}

func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("windsurf.GetCompletion")()

	info, err := p.buffer.GetWindsurfInfo()
	if err != nil {
		logger.Error("failed to get windsurf info: %v", err)
		return p.emptyResponse(), nil
	}
	if info == nil || !info.Healthy {
		logger.Debug("windsurf: server not healthy")
		return p.emptyResponse(), nil
	}

	lineEnding := "\n"
	language := resolveLanguage(req.FilePath)

	text := strings.Join(req.Lines, lineEnding)
	if len(req.Lines) > 0 {
		text += lineEnding
	}

	p.reqCounter++
	wsReq := windsurfRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        p.reqCounter,
		},
		EditorOptions: windsurfEditorOptions{
			TabSize:      4,
			InsertSpaces: true,
		},
		Document: windsurfDocument{
			Text:           text,
			EditorLanguage: language,
			Language:       languageEnum[language],
			CursorPosition: windsurfPos{
				Row: req.CursorRow - 1,
				Col: req.CursorCol,
			},
			AbsoluteURI:  "file://" + req.FilePath,
			WorkspaceURI: "file://" + req.WorkspacePath,
			LineEnding:   lineEnding,
		},
	}

	body, err := json.Marshal(wsReq)
	if err != nil {
		logger.Error("windsurf: failed to marshal request: %v", err)
		return p.emptyResponse(), nil
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/GetCompletions", info.Port)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Error("windsurf: failed to create request: %v", err)
		return p.emptyResponse(), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		logger.Debug("windsurf: request failed: %v", err)
		return p.emptyResponse(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Debug("windsurf: non-200 response %d: %s", resp.StatusCode, string(respBody))
		return p.emptyResponse(), nil
	}

	var wsResp windsurfResponse
	if err := json.NewDecoder(resp.Body).Decode(&wsResp); err != nil {
		logger.Error("windsurf: failed to decode response: %v", err)
		return p.emptyResponse(), nil
	}

	if wsResp.State.State != "CODEIUM_STATE_SUCCESS" {
		logger.Debug("windsurf: non-success state: %v", wsResp.State)
		return p.emptyResponse(), nil
	}

	return p.convertResponse(&wsResp, req)
}

func (p *Provider) SendMetric(ctx context.Context, event metrics.Event) {
	if event.Type != metrics.EventAccepted {
		return
	}
	if event.Info.ID == "" {
		return
	}

	info, err := p.buffer.GetWindsurfInfo()
	if err != nil || info == nil || !info.Healthy {
		return
	}

	p.reqCounter++
	acceptReq := windsurfAcceptRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        p.reqCounter,
		},
		CompletionID: event.Info.ID,
	}

	body, err := json.Marshal(acceptReq)
	if err != nil {
		logger.Debug("windsurf: failed to marshal accept request: %v", err)
		return
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/AcceptCompletion", info.Port)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logger.Debug("windsurf: failed to create accept request: %v", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		logger.Debug("windsurf: accept request failed: %v", err)
		return
	}
	resp.Body.Close()
}

func (p *Provider) convertResponse(wsResp *windsurfResponse, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(wsResp.CompletionItems) == 0 {
		return p.emptyResponse(), nil
	}

	var completions []*types.Completion
	var metricsInfo *types.MetricsInfo

	for i, item := range wsResp.CompletionItems {
		completion := p.convertSingleItem(item, req, i)
		if completion != nil {
			completions = append(completions, completion)
			if metricsInfo == nil && item.Completion.CompletionID != "" {
				metricsInfo = &types.MetricsInfo{
					ID: item.Completion.CompletionID,
				}
			}
		}
	}

	if len(completions) == 0 {
		return p.emptyResponse(), nil
	}

	return &types.CompletionResponse{
		Completions: completions,
		MetricsInfo: metricsInfo,
	}, nil
}

func (p *Provider) convertSingleItem(item windsurfCompletionItem, req *types.CompletionRequest, idx int) *types.Completion {
	startRow, _ := strconv.Atoi(item.Range.StartPosition.Row)
	endRow, _ := strconv.Atoi(item.Range.EndPosition.Row)

	startLine := startRow + 1
	endLine := endRow + 1

	if startLine < 1 || startLine > len(req.Lines)+1 {
		logger.Debug("windsurf: item %d start line %d out of bounds", idx, startLine)
		return nil
	}

	if endLine > len(req.Lines) {
		endLine = len(req.Lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	completionText := item.Completion.Text
	if completionText == "" {
		logger.Debug("windsurf: item %d has empty completion text", idx)
		return nil
	}

	newLines := strings.Split(completionText, "\n")

	endCol, _ := strconv.Atoi(item.Range.EndPosition.Col)
	if endCol > 0 && endLine >= 1 && endLine <= len(req.Lines) {
		lastOrigLine := req.Lines[endLine-1]
		if endCol < len(lastOrigLine) {
			lineSuffix := lastOrigLine[endCol:]
			if len(newLines) > 0 {
				newLines[len(newLines)-1] += lineSuffix
			}
		}
	}

	origLines := req.Lines[startLine-1 : endLine]
	if slices.Equal(newLines, origLines) {
		logger.Debug("windsurf: item %d is no-op", idx)
		return nil
	}

	logger.Debug("windsurf: converted item %d startLine=%d endLine=%d newLines=%d", idx, startLine, endLine, len(newLines))

	return &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLine,
		Lines:      newLines,
	}
}

func resolveLanguage(filePath string) string {
	parts := strings.Split(filePath, ".")
	if len(parts) < 2 {
		return "plaintext"
	}
	ext := parts[len(parts)-1]

	ftMap := map[string]string{
		"go": "go", "py": "python", "js": "javascript", "ts": "typescript",
		"tsx": "tsx", "jsx": "javascript", "java": "java", "rs": "rust",
		"c": "c", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "h": "c",
		"hpp": "cpp", "lua": "lua", "rb": "ruby", "php": "php",
		"sh": "shell", "bash": "shell", "sql": "sql", "md": "markdown",
		"html": "html", "css": "css", "scss": "scss", "less": "less",
		"vue": "vue", "svelte": "svelte", "kt": "kotlin", "swift": "swift",
		"scala": "scala", "r": "r", "json": "json", "yaml": "yaml",
		"yml": "yaml", "xml": "xml", "toml": "ini", "dockerfile": "dockerfile",
		"makefile": "makefile", "ex": "elixir", "exs": "elixir",
		"erl": "erlang", "clj": "clojure", "hs": "haskell",
		"dart": "dart", "proto": "protobuf", "tex": "latex",
	}

	lang := ftMap[ext]
	if lang == "" {
		base := strings.ToLower(parts[len(parts)-2] + "." + ext)
		if base == "dockerfile" || base == "makefile" {
			lang = base
		}
	}
	if lang == "" {
		lang = "plaintext"
	}

	if alias, ok := filetypeAliases[lang]; ok {
		lang = alias
	}

	return lang
}

func (p *Provider) emptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}

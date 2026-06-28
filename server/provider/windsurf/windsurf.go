// Package windsurf implements the Windsurf completion provider.
//
// This provider communicates with the Windsurf local language server
// over HTTP. The port is assigned randomly at runtime when the Neovim Codeium
// extension starts; we discover it via lua/cursortab/bridge.lua which probes the
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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/provider"
	"cursortab/types"
)

// InfoProvider is the minimum interface the Windsurf provider needs from the
// buffer. *buffer.NvimBuffer satisfies this via duck typing. The eval harness
// provides a cassette-backed implementation that returns static info.
type InfoProvider interface {
	GetWindsurfInfo() (*buffer.WindsurfInfo, error)
}

const windsurfBlockPartType = "COMPLETION_PART_TYPE_BLOCK"
const windsurfInlineMaskPartType = "COMPLETION_PART_TYPE_INLINE_MASK"

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

var extensionLanguages = map[string]string{
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
	provider.Base
	buffer     InfoProvider
	httpClient *http.Client
	reqCounter int64
}

var _ engine.Provider = (*Provider)(nil)
var _ provider.CompletionFlow[*windsurfRequestPayload, *windsurfRawResult] = (*Provider)(nil)

type windsurfRequestPayload struct {
	ready bool
	url   string
	body  []byte
}

type windsurfRawResult struct {
	ready    bool
	response *windsurfResponse
}

func NewProvider(buf InfoProvider) *Provider {
	return &Provider{
		Base:   provider.NewBase(engine.CompletionEdit, nil, provider.SyntheticPrefetchEnabled),
		buffer: buf,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (p *Provider) nextRequestID() int {
	return int(atomic.AddInt64(&p.reqCounter, 1))
}

func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	p.httpClient.Transport = rt
}

func (p *Provider) Complete(reqCtx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(reqCtx, input, nil, p)
}

func (p *Provider) Build(state *provider.RequestState) (*windsurfRequestPayload, error) {
	info, ready, err := p.windsurfInfo()
	if err != nil {
		return nil, err
	}
	if !ready {
		return &windsurfRequestPayload{}, nil
	}
	return p.build(state, info)
}

func (p *Provider) windsurfInfo() (*buffer.WindsurfInfo, bool, error) {
	info, err := p.buffer.GetWindsurfInfo()
	if err != nil {
		return nil, false, fmt.Errorf("windsurf info: %w", err)
	}
	if info == nil || !info.Healthy {
		logger.Debug("windsurf: server not healthy")
		return nil, false, nil
	}
	return info, true, nil
}

func (p *Provider) build(state *provider.RequestState, info *buffer.WindsurfInfo) (*windsurfRequestPayload, error) {
	current := state.Input.Current

	wsReq := buildWindsurfRequest(info, current, p.nextRequestID())

	body, err := json.Marshal(wsReq)
	if err != nil {
		return nil, fmt.Errorf("marshal windsurf request: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/GetCompletions", info.Port)
	return &windsurfRequestPayload{ready: true, url: url, body: body}, nil
}

func (p *Provider) Call(reqCtx context.Context, payload *windsurfRequestPayload) (*windsurfRawResult, error) {
	if payload == nil {
		return nil, fmt.Errorf("windsurf: nil request")
	}
	if !payload.ready {
		return &windsurfRawResult{}, nil
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, payload.url, bytes.NewReader(payload.body))
	if err != nil {
		return nil, fmt.Errorf("create windsurf request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("windsurf request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("windsurf response %d: %s", resp.StatusCode, string(respBody))
	}

	var wsResp windsurfResponse
	if err := json.NewDecoder(resp.Body).Decode(&wsResp); err != nil {
		return nil, fmt.Errorf("decode windsurf response: %w", err)
	}
	return &windsurfRawResult{ready: true, response: &wsResp}, nil
}

func (p *Provider) Parse(state *provider.RequestState, result *windsurfRawResult) (*types.CompletionResponse, error) {
	if result == nil {
		return nil, fmt.Errorf("windsurf: nil response")
	}
	if !result.ready {
		return p.emptyResponse(), nil
	}
	if result.response == nil {
		return nil, fmt.Errorf("windsurf: nil response")
	}
	if result.response.State.State != "CODEIUM_STATE_SUCCESS" {
		logger.Debug("windsurf: non-success state: %v", result.response.State)
		return p.emptyResponse(), nil
	}

	return p.convertResponse(result.response, state.Input.Current)
}

func buildWindsurfRequest(info *buffer.WindsurfInfo, current ctx.CurrentSnapshot, reqID int) windsurfRequest {
	lineEnding := "\n"
	language := resolveLanguage(current.File.Path)
	absFilePath, _ := filepath.Abs(current.File.Path)
	absWorkspacePath, _ := filepath.Abs(current.WorkspacePath)

	return windsurfRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        reqID,
		},
		EditorOptions: windsurfEditorOptions{
			TabSize:      4,
			InsertSpaces: true,
		},
		Document: windsurfDocument{
			Text:           buildDocumentText(current.File.Lines),
			EditorLanguage: language,
			Language:       LanguageEnum(language),
			CursorPosition: windsurfPos{
				Row: current.Cursor.Row - 1,
				Col: current.Cursor.Col,
			},
			AbsoluteURI:  "file://" + absFilePath,
			WorkspaceURI: "file://" + absWorkspacePath,
			LineEnding:   lineEnding,
		},
	}
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

	reqID := p.nextRequestID()
	acceptReq := windsurfAcceptRequest{
		Metadata: windsurfMetadata{
			APIKey:           info.APIKey,
			IDEName:          "neovim",
			IDEVersion:       "0.10.0",
			ExtensionName:    "neovim",
			ExtensionVersion: "1.20.9",
			RequestID:        reqID,
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

func (p *Provider) convertResponse(wsResp *windsurfResponse, current ctx.CurrentSnapshot) (*types.CompletionResponse, error) {
	if len(wsResp.CompletionItems) == 0 {
		return p.emptyResponse(), nil
	}

	for i, item := range wsResp.CompletionItems {
		completion := p.convertSingleItem(item, current, i)
		if completion != nil {
			var metricsInfo *types.MetricsInfo
			if item.Completion.CompletionID != "" {
				metricsInfo = &types.MetricsInfo{
					ID: item.Completion.CompletionID,
				}
			}
			return &types.CompletionResponse{
				Completion:  completion,
				MetricsInfo: metricsInfo,
			}, nil
		}
	}

	return p.emptyResponse(), nil
}

func (p *Provider) convertSingleItem(item windsurfCompletionItem, current ctx.CurrentSnapshot, idx int) *types.Completion {
	lines := current.File.Lines
	documentText := buildDocumentText(lines)
	startOffset, endOffset, ok := p.resolveItemOffsets(item, idx, len(documentText))
	if !ok {
		return nil
	}

	startLine, endLine, ok := p.resolveReplacementLines(lines, startOffset, endOffset, documentText, idx)
	if !ok {
		return nil
	}

	completionText := p.buildCompletionText(item, documentText, startOffset, endOffset)
	if completionText == "" {
		logger.Debug("windsurf: item %d has empty completion text", idx)
		return nil
	}

	newLines := strings.Split(completionText, "\n")

	// Always append the suffix from the original line after the completion
	// range. When completion parts are present we skip any INLINE_MASK parts
	// above, so the suffix must be preserved here to avoid truncation of the
	// line tail.
	endCol, _ := strconv.Atoi(item.Range.EndPosition.Col)
	if endCol > 0 && endLine >= 1 && endLine <= len(lines) {
		lastOrigLine := lines[endLine-1]
		if endCol < len(lastOrigLine) && len(newLines) > 0 {
			newLines[len(newLines)-1] += lastOrigLine[endCol:]
		}
	}

	origLines := lines[startLine-1 : endLine]
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

func buildDocumentText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}

func (p *Provider) resolveItemOffsets(item windsurfCompletionItem, idx, documentLen int) (int, int, bool) {
	startOffset, startErr := strconv.Atoi(item.Range.StartOffset)
	endOffset, endErr := strconv.Atoi(item.Range.EndOffset)
	if startErr != nil || endErr != nil {
		logger.Debug("windsurf: item %d missing valid offsets start=%q end=%q", idx, item.Range.StartOffset, item.Range.EndOffset)
		return 0, 0, false
	}

	if startOffset < 0 || endOffset < startOffset || endOffset > documentLen {
		logger.Debug("windsurf: item %d offsets out of bounds start=%d end=%d len=%d", idx, startOffset, endOffset, documentLen)
		return 0, 0, false
	}

	return startOffset, endOffset, true
}

func (p *Provider) resolveReplacementLines(lines []string, startOffset, endOffset int, documentText string, idx int) (int, int, bool) {
	startLine, _ := byteOffsetToLineCol(documentText, startOffset)
	endLine, endCol := byteOffsetToLineCol(documentText, endOffset)

	if endOffset > startOffset && endCol == 0 && endLine > startLine {
		endLine--
	}

	if startLine < 1 || startLine > len(lines)+1 {
		logger.Debug("windsurf: item %d start line %d out of bounds", idx, startLine)
		return 0, 0, false
	}

	if len(lines) == 0 {
		return 1, 1, true
	}

	if startLine > len(lines) {
		startLine = len(lines)
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	return startLine, endLine, true
}

func (p *Provider) buildCompletionText(item windsurfCompletionItem, documentText string, startOffset, endOffset int) string {
	if len(item.CompletionParts) == 0 {
		return item.Completion.Text
	}

	var b strings.Builder
	cur := startOffset

	for _, part := range item.CompletionParts {
		// Skip inline-mask parts: these are display-only masks that show the
		// complete visible line (insert + suffix). They should not be treated as
		// additional inserted text, otherwise the preview duplicates content.
		if part.Type == windsurfInlineMaskPartType {
			curOff, _ := strconv.Atoi(part.Offset)
			if curOff > cur {
				// advance cur to avoid re-including replaced content
				cur = curOff
			}
			continue
		}
		offset, err := strconv.Atoi(part.Offset)
		if err != nil {
			continue
		}
		if offset < cur {
			offset = cur
		}
		if offset > endOffset {
			offset = endOffset
		}

		b.WriteString(documentText[cur:offset])
		if part.Type == windsurfBlockPartType {
			b.WriteByte('\n')
		}
		b.WriteString(part.Text)
		cur = offset
	}

	b.WriteString(documentText[cur:endOffset])
	return b.String()
}

func byteOffsetToLineCol(text string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}

	line := 1
	lineStart := 0
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}

	return line, offset - lineStart
}

// LanguageEnum returns the numeric language ID expected by the Windsurf API.
func LanguageEnum(language string) int {
	return languageEnum[language]
}

func resolveLanguage(filePath string) string {
	parts := strings.Split(filePath, ".")
	if len(parts) < 2 {
		return "plaintext"
	}
	ext := parts[len(parts)-1]

	lang := extensionLanguages[ext]
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

func (p *Provider) emptyResponse() *types.CompletionResponse { return &types.CompletionResponse{} }

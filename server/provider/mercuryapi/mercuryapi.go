// Package mercuryapi implements CursorTab-hosted edit prediction.
package mercuryapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"cursortab/client/mercuryapi"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/provider"
	"cursortab/types"
	"cursortab/utils"
)

// Token limits (characters, approximating 1 token ~= 3 chars)
const (
	MaxRewriteChars = 450    // ~150 tokens for editable region
	MaxFileChars    = 150000 // trim files larger than this
)

// Prompt format constants
const (
	RecentlyViewedSnippetsStart = "<|recently_viewed_code_snippets|>\n"
	RecentlyViewedSnippetsEnd   = "<|/recently_viewed_code_snippets|>\n"
	RecentlyViewedSnippetStart  = "<|recently_viewed_code_snippet|>\n"
	RecentlyViewedSnippetEnd    = "<|/recently_viewed_code_snippet|>\n"
	CurrentFileContentStart     = "<|current_file_content|>\n"
	CurrentFileContentEnd       = "<|/current_file_content|>\n"
	CodeToEditStart             = "<|code_to_edit|>\n"
	CodeToEditEnd               = "<|/code_to_edit|>\n"
	EditDiffHistoryStart        = "<|edit_diff_history|>\n"
	EditDiffHistoryEnd          = "<|/edit_diff_history|>\n"
	CursorTag                   = "<|cursor|>"
	CodeSnippetFilePathPrefix   = "code_snippet_file_path: "
	CurrentFilePathPrefix       = "current_file_path: "
)

type Provider struct {
	provider.Base
	config *types.ProviderConfig
	client *mercuryapi.Client
}

var _ engine.Provider = (*Provider)(nil)
var _ provider.CompletionFlow[*mercuryapi.Request, *mercuryapi.Response] = (*Provider)(nil)

func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		Base: provider.NewBase(engine.CompletionEdit, sourcectx.Materials{
			sourcectx.Diagnostics{}, sourcectx.Treesitter{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		}, provider.SyntheticPrefetchEnabled),
		config: config,
		client: mercuryapi.NewClient(config.ProviderURL, config.APIKey, config.CompletionTimeout),
	}
}

// SetHTTPTransport forwards the transport override to the underlying client.
// Used by the eval harness for cassette record/replay.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	p.client.SetHTTPTransport(rt)
}

func (p *Provider) SendMetric(ctx context.Context, event metrics.Event) {
	var action mercuryapi.FeedbackAction
	switch event.Type {
	case metrics.EventShown:
		// Mercury doesn't have a "shown" event, only accept/reject/ignore
		return
	case metrics.EventAccepted:
		action = mercuryapi.FeedbackAccept
	case metrics.EventRejected:
		action = mercuryapi.FeedbackReject
	case metrics.EventIgnored:
		action = mercuryapi.FeedbackIgnore
	default:
		return
	}

	req := &mercuryapi.FeedbackRequest{
		RequestID:       event.Info.ID,
		ProviderName:    "cursortab-nvim",
		UserAction:      action,
		ProviderVersion: p.config.Version,
	}
	if err := p.client.SendFeedback(ctx, req); err != nil {
		logger.Warn("mercuryapi: failed to send %s feedback: %v", event.Type, err)
	}
}

func (p *Provider) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	if len(input.Current.File.Lines) == 0 {
		return provider.EmptyResponse(), nil
	}
	return provider.StartBatch(ctx, input, nil, p)
}

func (p *Provider) Build(state *provider.RequestState) (*mercuryapi.Request, error) {
	current := state.Input.Current
	lines := current.File.Lines

	if len(lines) == 0 {
		return nil, fmt.Errorf("mercuryapi: empty current file")
	}

	editableStart, editableEnd, contextStart, contextEnd := computeRegionsForState(state)

	var diffHistories []*types.FileDiffHistory
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](state.Input.Materials); ok {
		diffHistories = editHistory.Files
	}
	var recentSnapshots []*types.RecentBufferSnapshot
	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](state.Input.Materials); ok {
		recentSnapshots = recentFiles.Files
	}
	var diagnostics *types.Diagnostics
	if diagnosticsMaterial, ok := sourcectx.Find[sourcectx.Diagnostics](state.Input.Materials); ok {
		diagnostics = diagnosticsMaterial.Data
	}
	var treesitter *types.TreesitterContext
	if treesitterMaterial, ok := sourcectx.Find[sourcectx.Treesitter](state.Input.Materials); ok {
		treesitter = treesitterMaterial.Data
	}
	var gitDiff *types.GitDiffContext
	if gitDiffMaterial, ok := sourcectx.Find[sourcectx.GitDiff](state.Input.Materials); ok {
		gitDiff = gitDiffMaterial.Data
	}

	prompt := buildPrompt(
		current.File.Path,
		lines,
		editableStart, editableEnd,
		contextStart, contextEnd,
		current.Cursor.Row, current.Cursor.Col,
		diffHistories,
		recentSnapshots,
		diagnostics,
		treesitter,
		gitDiff,
	)

	model := p.config.ProviderModel
	if model == "" {
		model = mercuryapi.Model
	}

	apiReq := &mercuryapi.Request{
		Model: model,
		Messages: []mercuryapi.Message{
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	p.logRequest(apiReq, editableStart, editableEnd, contextStart, contextEnd)
	return apiReq, nil
}

func (p *Provider) Call(ctx context.Context, req *mercuryapi.Request) (*mercuryapi.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("mercuryapi: nil request")
	}
	apiResp, err := p.client.DoCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	return apiResp, nil
}

func (p *Provider) Parse(state *provider.RequestState, response *mercuryapi.Response) (*types.CompletionResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("mercuryapi: nil response")
	}
	completionText := mercuryapi.ExtractCompletion(response)

	p.logResponse(response, completionText)

	if completionText == "" {
		return &types.CompletionResponse{}, nil
	}

	newLines := strings.Split(completionText, "\n")
	editableStart, editableEnd, _, _ := computeRegionsForState(state)
	lines := state.Input.Current.File.Lines

	originalEditable := lines[editableStart-1 : editableEnd]
	if slices.Equal(newLines, originalEditable) {
		return &types.CompletionResponse{}, nil
	}

	additions, deletions := countChanges(editableEnd-editableStart+1, len(newLines))

	return &types.CompletionResponse{
		Completion: &types.Completion{
			StartLine:  editableStart,
			EndLineInc: editableEnd,
			Lines:      newLines,
		},
		MetricsInfo: &types.MetricsInfo{
			ID:        response.ID,
			Additions: additions,
			Deletions: deletions,
		},
	}, nil
}

func computeRegionsForState(state *provider.RequestState) (editableStart, editableEnd, contextStart, contextEnd int) {
	var syntaxRanges []*types.LineRange
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](state.Input.Materials); ok && treesitter.Data != nil {
		syntaxRanges = treesitter.Data.SyntaxRanges
	}
	current := state.Input.Current
	return computeRegions(current.File.Lines, current.Cursor.Row, syntaxRanges)
}

func (p *Provider) logRequest(req *mercuryapi.Request, editableStart, editableEnd, contextStart, contextEnd int) {
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Content
	}
	logger.Debug("mercuryapi request:\n  URL: %s\n  Model: %s\n  Editable: [%d:%d]\n  Context: [%d:%d]\n  Prompt length: %d chars\n  Prompt:\n%s",
		p.client.URL,
		req.Model,
		editableStart, editableEnd,
		contextStart, contextEnd,
		len(prompt),
		prompt)
}

func (p *Provider) logResponse(resp *mercuryapi.Response, completionText string) {
	finishReason := ""
	if len(resp.Choices) > 0 {
		finishReason = resp.Choices[0].FinishReason
	}
	logger.Debug("mercuryapi response:\n  ID: %s\n  FinishReason: %s\n  Text length: %d chars\n  Text:\n%s",
		resp.ID,
		finishReason,
		len(completionText),
		completionText)
}

func countChanges(oldLineCount, newLineCount int) (additions, deletions int) {
	return max(newLineCount, 1), max(oldLineCount, 1)
}

// computeRegions calculates the editable and context regions around the cursor.
// Returns 1-indexed line numbers: editableStart, editableEnd, contextStart, contextEnd.
// Context defaults to the entire file; only trimmed for extremely large files.
// When syntax ranges are available, the editable region snaps to AST boundaries.
func computeRegions(lines []string, cursorRow int, syntaxRanges []*types.LineRange) (int, int, int, int) {
	if len(lines) == 0 {
		return 1, 1, 1, 1
	}

	// Clamp cursor to valid range
	if cursorRow < 1 {
		cursorRow = 1
	}
	if cursorRow > len(lines) {
		cursorRow = len(lines)
	}

	cursorIdx := cursorRow - 1 // 0-indexed

	editableStart, editableEnd := expandRegion(lines, cursorIdx, MaxRewriteChars)

	// Snap editable region to syntax boundaries if available
	editableStart, editableEnd = utils.SnapToSyntaxBoundaries(lines, editableStart, editableEnd, MaxRewriteChars, syntaxRanges)

	// Context defaults to the entire file
	contextStart := 0
	contextEnd := len(lines) - 1

	// For extremely large files, trim distant regions while preserving the editable area
	totalChars := 0
	for _, l := range lines {
		totalChars += len(l) + 1
	}
	if totalChars > MaxFileChars {
		contextStart, contextEnd = expandRegionAround(lines, editableStart, editableEnd, MaxFileChars)
	}

	return editableStart + 1, editableEnd + 1, contextStart + 1, contextEnd + 1
}

// expandRegion expands a region around the cursor within a character budget.
// Returns 0-indexed start and end (inclusive).
func expandRegion(lines []string, cursorIdx int, maxChars int) (int, int) {
	if len(lines) == 0 {
		return 0, 0
	}

	start := cursorIdx
	end := cursorIdx
	chars := len(lines[cursorIdx]) + 1 // +1 for newline

	// Expand alternating up and down
	for {
		expandedUp := false
		expandedDown := false

		// Try expanding up
		if start > 0 {
			newChars := len(lines[start-1]) + 1
			if chars+newChars <= maxChars {
				start--
				chars += newChars
				expandedUp = true
			}
		}

		// Try expanding down
		if end < len(lines)-1 {
			newChars := len(lines[end+1]) + 1
			if chars+newChars <= maxChars {
				end++
				chars += newChars
				expandedDown = true
			}
		}

		if !expandedUp && !expandedDown {
			break
		}
	}

	return start, end
}

// expandRegionAround expands context around an existing region.
// Returns 0-indexed start and end (inclusive).
func expandRegionAround(lines []string, regionStart, regionEnd int, maxChars int) (int, int) {
	if len(lines) == 0 {
		return 0, 0
	}

	start := regionStart
	end := regionEnd

	chars := 0
	for i := start; i <= end && i < len(lines); i++ {
		chars += len(lines[i]) + 1
	}

	// Expand alternating up and down
	for {
		expandedUp := false
		expandedDown := false

		// Try expanding up
		if start > 0 {
			newChars := len(lines[start-1]) + 1
			if chars+newChars <= maxChars {
				start--
				chars += newChars
				expandedUp = true
			}
		}

		// Try expanding down
		if end < len(lines)-1 {
			newChars := len(lines[end+1]) + 1
			if chars+newChars <= maxChars {
				end++
				chars += newChars
				expandedDown = true
			}
		}

		if !expandedUp && !expandedDown {
			break
		}
	}

	return start, end
}

// buildPrompt constructs the Mercury prompt format.
func buildPrompt(
	filePath string,
	lines []string,
	editableStart, editableEnd int, // 1-indexed
	contextStart, contextEnd int, // 1-indexed
	cursorRow, cursorCol int, // 1-indexed row, 0-indexed col
	diffHistories []*types.FileDiffHistory,
	recentSnapshots []*types.RecentBufferSnapshot,
	diagnostics *types.Diagnostics,
	treesitter *types.TreesitterContext,
	gitDiff *types.GitDiffContext,
) string {
	var sb strings.Builder

	// Recently viewed code snippets
	sb.WriteString(RecentlyViewedSnippetsStart)
	for _, snap := range recentSnapshots {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString(snap.FilePath)
		sb.WriteString("\n")
		sb.WriteString(strings.Join(snap.Lines, "\n"))
		if len(snap.Lines) > 0 && !strings.HasSuffix(snap.Lines[len(snap.Lines)-1], "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	if diagText := provider.FormatDiagnosticsText(diagnostics); diagText != "" {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString("diagnostics\n")
		sb.WriteString(diagText)
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	if treesitter != nil {
		var tsContent strings.Builder
		if treesitter.EnclosingSignature != "" {
			fmt.Fprintf(&tsContent, "Enclosing scope: %s\n", treesitter.EnclosingSignature)
		}
		for _, s := range treesitter.Siblings {
			fmt.Fprintf(&tsContent, "Sibling: line %d: %s\n", s.Line, s.Signature)
		}
		for _, imp := range treesitter.Imports {
			fmt.Fprintf(&tsContent, "Import: %s\n", imp)
		}
		if tsContent.Len() > 0 {
			sb.WriteString(RecentlyViewedSnippetStart)
			sb.WriteString(CodeSnippetFilePathPrefix)
			sb.WriteString("treesitter_context\n")
			sb.WriteString(tsContent.String())
			sb.WriteString(RecentlyViewedSnippetEnd)
		}
	}
	if gitDiff != nil && gitDiff.Diff != "" {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString("staged_git_diff\n")
		sb.WriteString(gitDiff.Diff)
		if !strings.HasSuffix(gitDiff.Diff, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	sb.WriteString(RecentlyViewedSnippetsEnd)
	sb.WriteString("\n")

	// Current file content with editable region
	sb.WriteString(CurrentFileContentStart)
	sb.WriteString(CurrentFilePathPrefix)
	sb.WriteString(filePath)
	sb.WriteString("\n")

	// Content before editable region (within context)
	for i := contextStart; i < editableStart; i++ {
		sb.WriteString(lines[i-1])
		sb.WriteString("\n")
	}

	// Editable region with cursor marker
	sb.WriteString(CodeToEditStart)
	for i := editableStart; i <= editableEnd; i++ {
		line := lines[i-1]
		if i == cursorRow {
			// Insert cursor marker at cursor column
			col := min(cursorCol, len(line))
			sb.WriteString(line[:col])
			sb.WriteString(CursorTag)
			sb.WriteString(line[col:])
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(CodeToEditEnd)

	// Content after editable region (within context)
	for i := editableEnd + 1; i <= contextEnd; i++ {
		sb.WriteString(lines[i-1])
		sb.WriteString("\n")
	}
	sb.WriteString(CurrentFileContentEnd)
	sb.WriteString("\n")

	// Edit diff history
	sb.WriteString(EditDiffHistoryStart)
	sb.WriteString(formatDiffHistories(diffHistories))
	sb.WriteString(EditDiffHistoryEnd)

	return sb.String()
}

func formatDiffHistories(histories []*types.FileDiffHistory) string {
	if len(histories) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, h := range histories {
		for _, entry := range h.DiffHistory {
			diff := provider.DiffEntryToUnifiedDiff(entry)
			if diff == "" {
				continue
			}
			sb.WriteString("--- ")
			sb.WriteString(h.FileName)
			sb.WriteString("\n")
			sb.WriteString("+++ ")
			sb.WriteString(h.FileName)
			sb.WriteString("\n")
			sb.WriteString(diff)
			sb.WriteString("\n")
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

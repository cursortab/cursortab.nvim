// Package zeta2 implements Zed SeedCoder edit prediction with provider-owned
// cursor-marker parsing and editable-region stream windowing.
package zeta2

import (
	"fmt"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/provider"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// SeedCoder format tokens. Match Zed's crates/zeta_prompt/src/zeta_prompt.rs
// lines 3119-3128 exactly.
const (
	fimSuffix     = "<[fim-suffix]>"
	fimPrefix     = "<[fim-prefix]>"
	fimMiddle     = "<[fim-middle]>"
	fileMarker    = "<filename>"
	currentMarker = "<<<<<<< CURRENT\n"
	separator     = "=======\n"
	endMarker     = ">>>>>>> UPDATED\n"
	noEditsMarker = "NO_EDITS"
	cursorMarker  = "<|user_cursor|>"
)

// Editable region sizing. Zed uses token budgets (350 editable, 150 context)
// for the cloud endpoint. We approximate with line counts inside the shared
// request window bounded by ProviderMaxTokens.
const (
	editableLinesBefore  = 15
	editableLinesAfter   = 15
	maxEditableChars     = 3000 // ~1000 tokens; upper bound when snapping to AST
	maxEditHistoryEvents = 6
)

var stopTokens = []string{endMarker, strings.TrimSuffix(endMarker, "\n")}

// NewProvider creates a new Zeta2 provider (Zed's SeedCoder-8B model).
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return provider.NewProvider(
		"zeta-2",
		config,
		openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		engine.CompletionEdit,
		sourcectx.Materials{
			sourcectx.Diagnostics{}, sourcectx.Treesitter{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		},
		buildRequest,
		parseResult,
		provider.WithLineStream(
			provider.LineStreamWindow(streamWindow),
			provider.LineStreamLineTransform(visibleStreamLine),
			provider.LineStreamParser(parseStreamResult),
		),
	)
}

func buildRequest(p *provider.Provider, ctx *provider.RequestState) *openai.CompletionRequest {
	prompt := assemblePrompt(p, ctx)

	return &openai.CompletionRequest{
		Model:       p.Config().ProviderModel,
		Prompt:      prompt,
		Temperature: p.Config().ProviderTemperature,
		MaxTokens:   p.Config().ProviderMaxTokens,
		TopK:        p.Config().ProviderTopK,
		Stop:        stopTokens,
		N:           1,
		Echo:        false,
	}
}

func assemblePrompt(p *provider.Provider, ctx *provider.RequestState) string {
	trimmed := ctx.TrimmedLines
	input := ctx.Input
	current := input.Current
	if len(trimmed) == 0 {
		var b strings.Builder
		b.WriteString(fimSuffix)
		b.WriteString("\n")
		b.WriteString(fimPrefix)
		b.WriteString(fileMarker)
		b.WriteString(current.File.Path)
		b.WriteString("\n")
		b.WriteString(currentMarker)
		b.WriteString(cursorMarker)
		b.WriteString("\n")
		b.WriteString(separator)
		b.WriteString(fimMiddle)
		return b.String()
	}

	editableStart, editableEnd := computeEditableRange(trimmed, ctx.CursorLine, ctx.WindowStart, treesitterRanges(input.Materials))

	beforeLines := trimmed[:editableStart]
	editLines := trimmed[editableStart:editableEnd]
	suffixLines := trimmed[editableEnd:]

	var b strings.Builder

	b.WriteString(fimSuffix)
	suffixText := ""
	if len(suffixLines) > 0 {
		suffixText = strings.Join(suffixLines, "\n")
		b.WriteString(suffixText)
	}
	ensureTrailingNewline(&b, suffixText)

	b.WriteString(fimPrefix)

	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		writeRecentFilesPseudoFiles(&b, recentFiles.Files)
	}
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		writeDiagnosticsPseudoFile(&b, diagnostics.Data)
	}
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		writeTreesitterPseudoFile(&b, treesitter.Data)
	}
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		writeGitDiffPseudoFile(&b, gitDiff.Data)
	}

	if editHistoryMaterial, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		editHistory := buildEditHistory(editHistoryMaterial.Files)
		if editHistory != "" {
			b.WriteString(fileMarker)
			b.WriteString("edit_history\n")
			b.WriteString(editHistory)
			if !strings.HasSuffix(editHistory, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(fileMarker)
	b.WriteString(current.File.Path)
	b.WriteString("\n")

	if len(beforeLines) > 0 {
		b.WriteString(strings.Join(beforeLines, "\n"))
		b.WriteString("\n")
	}

	b.WriteString(currentMarker)
	editableText := formatEditableWithCursor(editLines, ctx.CursorLine-editableStart, current.Cursor.Col)
	b.WriteString(editableText)
	ensureTrailingNewline(&b, editableText)
	b.WriteString(separator)
	b.WriteString(fimMiddle)

	return b.String()
}

func streamWindow(ctx *provider.RequestState) (int, []string) {
	if len(ctx.TrimmedLines) == 0 {
		return 0, nil
	}
	editableStart, editableEnd := computeEditableRange(ctx.TrimmedLines, ctx.CursorLine, ctx.WindowStart, treesitterRanges(ctx.Input.Materials))
	oldLines := ctx.TrimmedLines[editableStart:editableEnd]
	for len(oldLines) > 0 && strings.TrimSpace(oldLines[len(oldLines)-1]) == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	return ctx.WindowStart + editableStart, oldLines
}

// computeEditableRange returns [start, end) line indices within trimmed lines
// for the editable region centered on cursorLine. When syntaxRanges is
// non-empty, the range is snapped to AST node boundaries (within a char
// budget) so the editable region lands on complete syntactic units rather
// than mid-expression. windowStart is the offset of trimmed within the full
// buffer, used to translate syntax ranges into trimmed-window coordinates.
func computeEditableRange(trimmed []string, cursorLine, windowStart int, syntaxRanges []*types.LineRange) (int, int) {
	if len(trimmed) == 0 {
		return 0, 0
	}
	if cursorLine < 0 {
		cursorLine = 0
	}
	if cursorLine >= len(trimmed) {
		cursorLine = len(trimmed) - 1
	}

	start := cursorLine - editableLinesBefore
	if start < 0 {
		start = 0
	}
	end := cursorLine + 1 + editableLinesAfter
	if end > len(trimmed) {
		end = len(trimmed)
	}

	if len(syntaxRanges) > 0 {
		shifted := make([]*types.LineRange, 0, len(syntaxRanges))
		for _, sr := range syntaxRanges {
			shifted = append(shifted, &types.LineRange{
				StartLine: sr.StartLine - windowStart,
				EndLine:   sr.EndLine - windowStart,
			})
		}
		// SnapToSyntaxBoundaries takes inclusive end; convert and back.
		snapStart, snapEnd := utils.SnapToSyntaxBoundaries(trimmed, start, end-1, maxEditableChars, shifted)
		if snapStart < 0 {
			snapStart = 0
		}
		if snapEnd >= len(trimmed) {
			snapEnd = len(trimmed) - 1
		}
		start = snapStart
		end = snapEnd + 1
	}

	return start, end
}

// treesitterRanges extracts syntax ranges from collected context, returning nil
// when treesitter context is unavailable.
func treesitterRanges(materials sourcectx.Materials) []*types.LineRange {
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](materials); ok && treesitter.Data != nil {
		return treesitter.Data.SyntaxRanges
	}
	return nil
}

// formatEditableWithCursor renders the editable region with <|user_cursor|>
// inserted at the cursor position.
func formatEditableWithCursor(editLines []string, cursorRelLine, cursorCol int) string {
	if len(editLines) == 0 {
		return cursorMarker
	}
	if cursorRelLine < 0 {
		cursorRelLine = 0
	}
	if cursorRelLine >= len(editLines) {
		cursorRelLine = len(editLines) - 1
		cursorCol = len(editLines[cursorRelLine])
	}

	lines := make([]string, len(editLines))
	copy(lines, editLines)
	line := lines[cursorRelLine]
	col := cursorCol
	if col > len(line) {
		col = len(line)
	}
	if col < 0 {
		col = 0
	}
	lines[cursorRelLine] = line[:col] + cursorMarker + line[col:]

	return strings.Join(lines, "\n")
}

// ensureTrailingNewline appends a newline only when the builder's last byte
// isn't one already. It avoids strings.Builder.String() (which copies the
// entire buffer) by tracking length before and after the last write.
func ensureTrailingNewline(b *strings.Builder, lastWrite string) {
	if len(lastWrite) == 0 || lastWrite[len(lastWrite)-1] != '\n' {
		b.WriteString("\n")
	}
}

// writePseudoFile writes one <filename>{path}\n{content}\n block followed by
// a blank separator line, matching the shape of a Zed V0211SeedCoder related
// file block.
func writePseudoFile(b *strings.Builder, path, content string) {
	if content == "" {
		return
	}
	b.WriteString(fileMarker)
	b.WriteString(path)
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// writeRecentFilesPseudoFiles renders each recent buffer snapshot as its own
// <filename>{path} block. This fills the slot Zed reserves for LSP-driven
// related files with the best proxy we have: recently accessed buffers.
func writeRecentFilesPseudoFiles(b *strings.Builder, snapshots []*types.RecentBufferSnapshot) {
	for _, snap := range snapshots {
		if len(snap.Lines) == 0 {
			continue
		}
		writePseudoFile(b, snap.FilePath, strings.Join(snap.Lines, "\n"))
	}
}

// writeDiagnosticsPseudoFile renders LSP diagnostics for the current buffer
// as a <filename>diagnostics block, one line per diagnostic.
//
// Zed's V0211SeedCoder deliberately drops diagnostics from the prompt, trusting
// the model to infer errors from the code alone. We include them anyway —
// our mercuryapi provider does the same thing with similar
// pseudo-file tricks, and the format is self-explanatory enough that the
// SeedCoder fine-tune should parse it even without seeing it in training.
func writeDiagnosticsPseudoFile(b *strings.Builder, diag *types.Diagnostics) {
	text := provider.FormatDiagnosticsText(diag)
	if text == "" {
		return
	}
	writePseudoFile(b, "diagnostics", text)
}

// writeTreesitterPseudoFile renders enclosing scope + sibling symbols + imports
// as a <filename>context/treesitter block. Zed replaced this with LSP-driven
// related files in Zeta2, but since we don't have LSP definition resolution,
// treesitter scope is the best structural context we can offer.
func writeTreesitterPseudoFile(b *strings.Builder, ts *types.TreesitterContext) {
	if ts == nil {
		return
	}

	var content strings.Builder
	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&content, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}
	for _, s := range ts.Siblings {
		fmt.Fprintf(&content, "Sibling: line %d: %s\n", s.Line, s.Signature)
	}
	for _, imp := range ts.Imports {
		fmt.Fprintf(&content, "Import: %s\n", imp)
	}

	if content.Len() == 0 {
		return
	}
	writePseudoFile(b, "context/treesitter", content.String())
}

// writeGitDiffPseudoFile renders the staged git diff as a
// <filename>context/staged_diff block. Populated only for COMMIT_EDITMSG;
// The collector only populates this for COMMIT_EDITMSG.
func writeGitDiffPseudoFile(b *strings.Builder, gd *types.GitDiffContext) {
	if gd == nil || gd.Diff == "" {
		return
	}
	writePseudoFile(b, "context/staged_diff", gd.Diff)
}

// buildEditHistory formats file diff histories as git-style unified diffs,
// newest-first, capped at maxEditHistoryEvents. Each event is preceded by
// "--- a/path\n+++ b/path\n" headers and separated by blank lines.
//
// Matches Zed's write_event in crates/zeta_prompt/src/zeta_prompt.rs:169-195.
func buildEditHistory(history []*types.FileDiffHistory) string {
	if len(history) == 0 {
		return ""
	}

	// Flatten events with their file path, newest-first by timestamp.
	type event struct {
		path      string
		diff      string
		predicted bool
		tsNs      int64
	}
	var events []event
	for _, fh := range history {
		for _, de := range fh.DiffHistory {
			unified := provider.DiffEntryToUnifiedDiff(de)
			if unified == "" {
				continue
			}
			events = append(events, event{
				path:      fh.FileName,
				diff:      unified,
				predicted: de.Source == types.DiffSourcePredicted,
				tsNs:      de.TimestampNs,
			})
		}
	}
	if len(events) == 0 {
		return ""
	}

	// Sort newest-first by timestamp.
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].tsNs > events[j-1].tsNs; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}

	// Cap to maxEditHistoryEvents most recent.
	if len(events) > maxEditHistoryEvents {
		events = events[:maxEditHistoryEvents]
	}

	// Reverse to oldest-first for prompt output.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	var b strings.Builder
	for i, ev := range events {
		if i > 0 {
			b.WriteString("\n")
		}
		if ev.predicted {
			b.WriteString("// User accepted prediction:\n")
		}
		path := strings.ReplaceAll(ev.path, "\\", "/")
		b.WriteString("--- a/")
		b.WriteString(path)
		b.WriteString("\n+++ b/")
		b.WriteString(path)
		b.WriteString("\n")
		b.WriteString(ev.diff)
		if !strings.HasSuffix(ev.diff, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func parseResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	text := result.Text
	if resp, done := provider.RejectEmptyText(p, text); done {
		return resp
	}
	if stripped, resp, done := provider.StripRepetitionText(p, text); done {
		return resp
	} else {
		text = stripped
	}
	return parseResultText(p, ctx, text)
}

// stripCursorMarker removes the cursor marker from the response text. Lines
// that consist solely of the marker (with optional surrounding whitespace)
// are dropped entirely so they don't produce phantom empty lines in the
// completion output. Inline markers within content lines are stripped
// normally.
func stripCursorMarker(text, marker string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, marker) {
			out = append(out, line)
			continue
		}
		stripped := strings.ReplaceAll(line, marker, "")
		if strings.TrimSpace(stripped) == "" {
			continue // marker-only line — drop it
		}
		out = append(out, stripped)
	}
	return strings.Join(out, "\n")
}

func buildCursorTarget(ctx *provider.RequestState, editableStart, markerLine int, newLines []string) *types.CursorPredictionTarget {
	lineIdx := markerLine
	if lineIdx < 0 {
		lineIdx = 0
	}
	if lineIdx >= len(newLines) {
		lineIdx = len(newLines) - 1
	}
	if lineIdx < 0 {
		return nil
	}

	bufferRow := ctx.WindowStart + editableStart + lineIdx + 1

	return &types.CursorPredictionTarget{
		LineNumber:      int32(bufferRow),
		ShouldRetrigger: true,
	}
}

func parseStreamResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	return parseResult(p, ctx, result)
}

func parseResultText(p *provider.Provider, ctx *provider.RequestState, text string) *types.CompletionResponse {
	cursorMarkerLine, cursorMarkerSeen := cursorMarkerPosition(text)
	return parseCompletionWithCursorMarker(p, ctx, text, cursorMarkerSeen, cursorMarkerLine)
}

func parseCompletionWithCursorMarker(
	p *provider.Provider,
	ctx *provider.RequestState,
	rawText string,
	cursorMarkerSeen bool,
	cursorMarkerLine int,
) *types.CompletionResponse {
	raw := rawText

	raw = strings.TrimSuffix(raw, endMarker)
	raw = strings.TrimSuffix(raw, strings.TrimSuffix(endMarker, "\n"))

	if strings.HasPrefix(strings.TrimSpace(raw), noEditsMarker) {
		return p.EmptyResponse()
	}

	raw = stripCursorMarker(raw, cursorMarker)

	if raw == "" {
		return p.EmptyResponse()
	}

	newLines := text.SplitLines(raw)
	if len(newLines) == 0 {
		return p.EmptyResponse()
	}

	editableStart, editableEnd := computeEditableRange(ctx.TrimmedLines, ctx.CursorLine, ctx.WindowStart, treesitterRanges(ctx.Input.Materials))
	startLine := ctx.WindowStart + editableStart + 1
	endLineInc := ctx.WindowStart + editableEnd

	resp := p.BuildCompletion(ctx, startLine, endLineInc, newLines)
	if resp != nil && resp.Completion != nil && cursorMarkerSeen {
		resp.CursorTarget = buildCursorTarget(ctx, editableStart, cursorMarkerLine, newLines)
	}
	return resp
}

func visibleStreamLine(line string) (string, bool) {
	if !strings.Contains(line, cursorMarker) {
		return line, true
	}
	stripped := strings.ReplaceAll(line, cursorMarker, "")
	return stripped, strings.TrimSpace(stripped) != ""
}

func cursorMarkerPosition(raw string) (int, bool) {
	for i, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, cursorMarker) {
			return i, true
		}
	}
	return 0, false
}

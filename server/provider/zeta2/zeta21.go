package zeta2

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/provider"
	"cursortab/text"
	"cursortab/types"
)

const (
	zeta21ProviderName = "zeta-2.1"
	zeta21EOSMarker    = "<[end▁of▁sentence]>"
)

var zeta21StopTokens = []string{zeta21EOSMarker}

type Provider21 struct {
	provider.Base
	provider.OpenAI
}

var _ engine.Provider = (*Provider21)(nil)
var _ provider.CompletionFlow[*openai.CompletionRequest, *openai.CompletionResult] = (*Provider21)(nil)

func NewProvider21(config *types.ProviderConfig) *Provider21 {
	return &Provider21{
		Base: provider.NewBase(engine.CompletionEdit, sourcectx.Materials{
			sourcectx.Diagnostics{}, sourcectx.Treesitter{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		}, provider.SyntheticPrefetchEnabled),
		OpenAI: provider.NewOpenAI(zeta21ProviderName, config),
	}
}

func (p *Provider21) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(ctx, input, p.ProviderConfig(), p)
}

func (p *Provider21) Build(ctx *provider.RequestState) (*openai.CompletionRequest, error) {
	prompt := assembleZeta21Prompt(ctx)
	req := p.Request(prompt, zeta21StopTokens)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func (p *Provider21) Parse(ctx *provider.RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	return parseZeta21Result(ctx, result), nil
}

// Zeta 2.1 uses the SeedCoder SPM envelope from Zeta-2 with V0318 numbered
// boundaries inside one contiguous editable excerpt. The model returns the
// two boundaries surrounding the smaller span it chose to rewrite.
func assembleZeta21Prompt(ctx *provider.RequestState) string {
	trimmed := ctx.Window.Lines
	input := ctx.Input
	current := input.Current
	editableStart, editableEnd := computeEditableRange(trimmed, ctx.Window.CursorLine, ctx.Window.Start, treesitterRanges(input.Materials))

	var b strings.Builder
	b.WriteString(fimSuffix)
	suffixText := ""
	if editableEnd < len(trimmed) {
		suffixText = strings.Join(trimmed[editableEnd:], "\n")
		b.WriteString(suffixText)
	}
	ensureTrailingNewline(&b, suffixText)
	b.WriteString(fimPrefix)

	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		writeRecentFilesPseudoFiles(&b, recentFiles.Files)
	}
	if editHistoryMaterial, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		editHistory := buildEditHistory(editHistoryMaterial.Files)
		if editHistory != "" {
			writePseudoFile(&b, "edit_history", editHistory)
		}
	}
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		writeTreesitterPseudoFile(&b, treesitter.Data)
	}
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		writeGitDiffPseudoFile(&b, gitDiff.Data)
	}
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		writeZeta21DiagnosticsPseudoFile(
			&b,
			diagnostics.Data,
			ctx.Window.Start+editableStart+1,
			ctx.Window.Start+editableEnd,
		)
	}

	b.WriteString(fileMarker)
	b.WriteString(current.File.Path)
	b.WriteString("\n")
	if editableStart > 0 {
		b.WriteString(strings.Join(trimmed[:editableStart], "\n"))
		b.WriteString("\n")
	}

	rendered := renderZeta21Editable(trimmed, editableStart, editableEnd, ctx.Window.CursorLine, current.Cursor.Col)
	b.WriteString(rendered.text)
	ensureTrailingNewline(&b, rendered.text)
	b.WriteString(fimMiddle)
	return b.String()
}

const (
	zeta21MinBlockLines = 6
	zeta21MaxBlockLines = 16
	zeta21MaxNudgeLines = 5
)

func writeZeta21DiagnosticsPseudoFile(b *strings.Builder, diagnostics *types.Diagnostics, editableStartLine, editableEndLine int) {
	if diagnostics == nil || editableStartLine > editableEndLine {
		return
	}

	filtered := &types.Diagnostics{FilePath: diagnostics.FilePath}
	for _, diagnostic := range diagnostics.Items {
		if diagnostic == nil || diagnostic.Range == nil || diagnostic.Range.StartLine <= 0 {
			continue
		}

		diagnosticEndLine := diagnostic.Range.EndLine
		if diagnosticEndLine < diagnostic.Range.StartLine {
			diagnosticEndLine = diagnostic.Range.StartLine
		}
		if diagnostic.Range.StartLine > editableEndLine || diagnosticEndLine < editableStartLine {
			continue
		}

		filtered.Items = append(filtered.Items, diagnostic)
	}

	writeDiagnosticsPseudoFile(b, filtered)
}

type zeta21LineInfo struct {
	startByte   int
	isBlank     bool
	isGoodStart bool
}

type renderedZeta21Editable struct {
	text          string
	boundaryLines []int
}

func zeta21MarkerBoundaryLines(ctx *provider.RequestState) []int {
	start, end := computeEditableRange(ctx.Window.Lines, ctx.Window.CursorLine, ctx.Window.Start, treesitterRanges(ctx.Input.Materials))
	rendered := renderZeta21Editable(ctx.Window.Lines, start, end, ctx.Window.CursorLine, ctx.Input.Current.Cursor.Col)
	return rendered.boundaryLines
}

func renderZeta21Editable(lines []string, editableStart, editableEnd, cursorLine, cursorCol int) renderedZeta21Editable {
	editableText := textForLineRange(lines, editableStart, editableEnd)
	markerOffsets := computeZeta21MarkerOffsets(editableText)
	lineInfo := collectZeta21LineInfo(editableText)
	lineByStartByte := make(map[int]int, len(lineInfo))
	for i, line := range lineInfo {
		lineByStartByte[line.startByte] = i
	}

	boundaryLines := make([]int, len(markerOffsets))
	for i, offset := range markerOffsets {
		if offset == len(editableText) {
			boundaryLines[i] = editableStart + len(lineInfo)
			continue
		}
		boundaryLines[i] = editableStart + lineByStartByte[offset]
	}

	cursorOffset := relativeCursorByte(lines, editableStart, cursorLine, cursorCol)
	cursorOffset = max(0, min(cursorOffset, len(editableText)))
	var b strings.Builder
	cursorPlaced := false
	endsWithNewline := true
	for i, offset := range markerOffsets {
		if b.Len() > 0 && !endsWithNewline {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "<|marker_%d|>", i+1)
		endsWithNewline = false

		if i+1 >= len(markerOffsets) {
			continue
		}
		nextOffset := markerOffsets[i+1]
		b.WriteString("\n")
		endsWithNewline = true
		block := editableText[offset:nextOffset]
		if !cursorPlaced && cursorOffset >= offset && cursorOffset <= nextOffset {
			cursorPlaced = true
			relativeOffset := cursorOffset - offset
			b.WriteString(block[:relativeOffset])
			b.WriteString(cursorMarker)
			b.WriteString(block[relativeOffset:])
			endsWithNewline = relativeOffset < len(block) && strings.HasSuffix(block, "\n")
		} else {
			b.WriteString(block)
			if block != "" {
				endsWithNewline = strings.HasSuffix(block, "\n")
			}
		}
	}

	return renderedZeta21Editable{text: b.String(), boundaryLines: boundaryLines}
}

func computeZeta21MarkerOffsets(editableText string) []int {
	if editableText == "" {
		return []int{0, 0}
	}

	lines := collectZeta21LineInfo(editableText)
	offsets := []int{0}
	lastBoundaryLine := 0
	for i := 0; i < len(lines); {
		gap := i - lastBoundaryLine
		line := lines[i]
		if gap >= zeta21MinBlockLines && !line.isBlank && i > 0 && lines[i-1].isBlank {
			target := i
			if !line.isGoodStart {
				if goodStart, ok := skipToGoodZeta21Start(lines, i); ok {
					target = goodStart
				}
			}
			if len(lines)-target >= zeta21MinBlockLines && lines[target].startByte > offsets[len(offsets)-1] {
				offsets = append(offsets, lines[target].startByte)
				lastBoundaryLine = target
				i = target + 1
				continue
			}
		}

		if gap >= zeta21MaxBlockLines {
			target := i
			if goodStart, ok := skipToGoodZeta21Start(lines, i); ok {
				target = goodStart
			}
			if lines[target].startByte > offsets[len(offsets)-1] {
				offsets = append(offsets, lines[target].startByte)
				lastBoundaryLine = target
				i = target + 1
				continue
			}
		}
		i++
	}

	if offsets[len(offsets)-1] != len(editableText) {
		offsets = append(offsets, len(editableText))
	}
	return offsets
}

func collectZeta21LineInfo(text string) []zeta21LineInfo {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	lines := make([]zeta21LineInfo, 0, len(parts))
	startByte := 0
	for _, line := range parts {
		trimmed := strings.TrimSpace(line)
		isBlank := trimmed == ""
		lines = append(lines, zeta21LineInfo{
			startByte:   startByte,
			isBlank:     isBlank,
			isGoodStart: !isBlank && !isZeta21StructuralTail(trimmed),
		})
		startByte += len(line) + 1
	}
	return lines
}

func skipToGoodZeta21Start(lines []zeta21LineInfo, from int) (int, bool) {
	end := min(len(lines), from+zeta21MaxNudgeLines)
	for i := from; i < end; i++ {
		if lines[i].isGoodStart {
			return i, true
		}
	}
	return 0, false
}

func isZeta21StructuralTail(line string) bool {
	if strings.HasPrefix(line, "}") || strings.HasPrefix(line, "]") || strings.HasPrefix(line, ")") {
		return true
	}
	line = strings.TrimSuffix(line, ";")
	switch line {
	case "break", "continue", "return", "throw", "end":
		return true
	default:
		return false
	}
}

func textForLineRange(lines []string, start, end int) string {
	text := strings.Join(lines[start:end], "\n")
	if end < len(lines) && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

func relativeCursorByte(lines []string, startLine, cursorLine, cursorCol int) int {
	offset := 0
	for i := startLine; i < cursorLine && i < len(lines); i++ {
		offset += len(lines[i]) + 1
	}
	return offset + cursorCol
}

var zeta21NumberedMarkerPattern = regexp.MustCompile(`<\|marker_(\d+)\|>`)

type zeta21MarkerHit struct {
	number int
	start  int
	end    int
}

type zeta21MarkerSpan struct {
	replacement string
	startLine   int
	endLine     int
	noEdits     bool
}

func parseZeta21Result(ctx *provider.RequestState, result *openai.CompletionResult) *types.CompletionResponse {
	if result.FinishReason == "length" || result.StoppedEarly {
		return provider.EmptyResponse()
	}

	span, ok := decodeZeta21MarkerSpan(ctx, result.Text)
	if !ok || span.noEdits {
		return provider.EmptyResponse()
	}
	replacement := span.replacement
	if eos := strings.Index(replacement, zeta21EOSMarker); eos >= 0 {
		replacement = replacement[:eos]
	}
	if stripped, resp, done := provider.StripRepetitionText(replacement); done {
		return resp
	} else {
		replacement = stripped
	}

	cursorMarkerLine, cursorMarkerSeen := cursorMarkerPosition(replacement)
	replacement = stripCursorMarker(replacement, cursorMarker)
	newLines := text.SplitLines(replacement)
	resp := provider.BuildCompletion(ctx, ctx.Window.Start+span.startLine+1, ctx.Window.Start+span.endLine, newLines)
	if resp.Completion != nil && cursorMarkerSeen {
		resp.CursorTarget = buildCursorTarget(ctx, span.startLine, cursorMarkerLine, newLines)
	}
	return resp
}

func decodeZeta21MarkerSpan(ctx *provider.RequestState, output string) (zeta21MarkerSpan, bool) {
	matches := zeta21NumberedMarkerPattern.FindAllStringSubmatchIndex(output, -1)
	if len(matches) != 2 {
		return zeta21MarkerSpan{}, false
	}

	hits := make([]zeta21MarkerHit, 0, len(matches))
	for _, match := range matches {
		number, err := strconv.Atoi(output[match[2]:match[3]])
		if err != nil || number < 1 {
			return zeta21MarkerSpan{}, false
		}
		hits = append(hits, zeta21MarkerHit{number: number, start: match[0], end: match[1]})
	}
	if hits[1].number < hits[0].number {
		return zeta21MarkerSpan{}, false
	}

	boundaries := zeta21MarkerBoundaryLines(ctx)
	startMarkerIndex := hits[0].number - 1
	endMarkerIndex := hits[1].number - 1
	if startMarkerIndex >= len(boundaries) || endMarkerIndex >= len(boundaries) {
		return zeta21MarkerSpan{}, false
	}
	startLine := boundaries[startMarkerIndex]
	endLine := boundaries[endMarkerIndex]
	if endLine < startLine {
		return zeta21MarkerSpan{}, false
	}

	replacement := output[hits[0].end:hits[1].start]
	if hits[0].number == hits[1].number {
		if strings.TrimSpace(replacement) != "" {
			return zeta21MarkerSpan{}, false
		}
		return zeta21MarkerSpan{startLine: startLine, endLine: endLine, noEdits: true}, true
	}
	if strings.HasPrefix(replacement, "\r\n") {
		replacement = strings.TrimPrefix(replacement, "\r\n")
	} else {
		replacement = strings.TrimPrefix(replacement, "\n")
	}
	if strings.HasSuffix(replacement, "\r\n") {
		replacement = strings.TrimSuffix(replacement, "\r\n")
	} else {
		replacement = strings.TrimSuffix(replacement, "\n")
	}
	return zeta21MarkerSpan{replacement: replacement, startLine: startLine, endLine: endLine}, true
}

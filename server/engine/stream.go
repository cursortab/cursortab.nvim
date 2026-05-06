package engine

import (
	"context"
	"strings"

	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// requestStreamingCompletion handles line-by-line streaming completions
func (e *Engine) requestStreamingCompletion(provider LineStreamProvider, req *types.CompletionRequest) {
	e.state = stateStreamingCompletion

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.streamingCancel = cancel

	// Prepare the stream
	stream, providerCtx, err := provider.PrepareLineStream(ctx, req)
	if err != nil {
		cancel()
		e.state = stateIdle
		return
	}

	// Get old lines for incremental diff building.
	// StreamContext (FIM) takes priority — provides cursor-relative old lines.
	// TrimmedContext is the default for full-file replacement providers.
	windowStart := 0
	var oldLines []string
	if sc, ok := providerCtx.(StreamContext); ok && sc.GetStreamOldLines() != nil {
		windowStart = sc.GetStreamBaseOffset()
		oldLines = sc.GetStreamOldLines()
	} else if tc, ok := providerCtx.(TrimmedContext); ok && len(tc.GetTrimmedLines()) > 0 {
		// Provider trimmed the content - use trimmed lines and offset
		windowStart = tc.GetWindowStart()
		oldLines = tc.GetTrimmedLines()
	} else {
		// No trimming - use full buffer
		oldLines = req.Lines
	}

	viewportTop, viewportBottom := e.buffer.ViewportBounds()

	// Initialize streaming state
	e.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			oldLines,
			windowStart+1, // baseLineOffset (1-indexed)
			e.config.CursorPrediction.ProximityThreshold,
			e.config.MaxVisibleLines,
			viewportTop,
			viewportBottom,
			e.buffer.Row(),
			e.buffer.Col(),
			req.FilePath,
			e.buffer.AvailableWidth(),
		),
		ProviderContext: providerCtx,
		Request:         req,
	}

	// Inject prefill lines through the normal streaming pipeline so the stage
	// builder, accumulated text, and validation all see a complete line sequence
	// starting from the top of the window.
	if pc, ok := providerCtx.(PrefillContext); ok {
		if prefill := pc.GetPrefill(); prefill != "" {
			for _, line := range strings.Split(strings.TrimSuffix(prefill, "\n"), "\n") {
				e.handleStreamLine(line)
			}
		}
	}

	// Set stream channel directly - event loop will select on it
	e.streamLinesChan = stream.LinesChan()
	e.streamLineNum = 0
}

// requestTokenStreamingCompletion handles token-by-token streaming completions (inline)
func (e *Engine) requestTokenStreamingCompletion(provider TokenStreamProvider, req *types.CompletionRequest) {
	e.state = stateStreamingCompletion

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.streamingCancel = cancel

	// Prepare the stream
	stream, providerCtx, err := provider.PrepareTokenStream(ctx, req)
	if err != nil {
		cancel()
		e.state = stateIdle
		return
	}

	// Get line prefix (text before cursor on current line)
	linePrefix := ""
	if req.CursorRow >= 1 && req.CursorRow <= len(req.Lines) {
		currentLine := req.Lines[req.CursorRow-1]
		cursorCol := min(req.CursorCol, len(currentLine))
		linePrefix = currentLine[:cursorCol]
	}

	// Initialize token streaming state
	e.tokenStreamingState = &TokenStreamingState{
		AccumulatedText: "",
		ProviderContext: providerCtx,
		Request:         req,
		LinePrefix:      linePrefix,
		LineNum:         req.CursorRow,
	}

	// Set token stream channel - event loop will select on it
	e.tokenStreamChan = stream.LinesChan()
}

// cancelStreaming cancels an in-progress streaming completion (both line and token streaming)
func (e *Engine) cancelStreaming() {
	// Clear channels first - this immediately stops event loop from reading
	e.streamLinesChan = nil
	e.streamLineNum = 0
	e.tokenStreamChan = nil
	// Then cancel the HTTP request
	if e.streamingCancel != nil {
		e.streamingCancel()
		e.streamingCancel = nil
	}
	e.streamingState = nil
	e.tokenStreamingState = nil
	e.acceptedDuringStreaming = false
}

// cancelTokenStreamingKeepPartial cancels token streaming but preserves the partial
// completion state (completions and completionOriginalLines) for typing match validation.
// Used when user types during token streaming to check if typing matches partial result.
func (e *Engine) cancelTokenStreamingKeepPartial() {
	// Clear channel first - stops event loop from reading
	e.tokenStreamChan = nil
	// Cancel the HTTP request
	if e.streamingCancel != nil {
		e.streamingCancel()
		e.streamingCancel = nil
	}
	// Clear streaming state but keep completions and completionOriginalLines
	// These were populated by handleTokenChunk and are needed for checkTypingMatchesPrediction
	e.tokenStreamingState = nil
}

// cancelLineStreamingKeepPartial cancels line streaming but preserves the partial
// completion state (completions and completionOriginalLines) for typing match validation.
// Used when user types during line streaming after first stage was rendered.
func (e *Engine) cancelLineStreamingKeepPartial() {
	// Clear channel first - stops event loop from reading
	e.streamLinesChan = nil
	e.streamLineNum = 0
	// Cancel the HTTP request
	if e.streamingCancel != nil {
		e.streamingCancel()
		e.streamingCancel = nil
	}
	// Clear streaming state but keep completions and completionOriginalLines
	// These were populated by renderStreamedStage and are needed for checkTypingMatchesPrediction
	e.streamingState = nil
}

// handleStreamLine processes a line received from the streaming provider.
// Caller must verify stream ID matches before calling.
func (e *Engine) handleStreamLine(line string) {
	ss := e.streamingState
	if ss == nil {
		return
	}

	// Strip any provider-configured in-band markers (e.g. Zeta2's
	// <|user_cursor|>) before the line flows into validation, accumulation,
	// or the stage builder. The default implementation on provider.Context is
	// a no-op counter bump, so this is safe for all providers.
	//
	// When the marker was the entire line content (e.g. the model emitted
	// <|user_cursor|> on its own line), ShouldSkipLine signals that the
	// resulting empty line must be dropped. Accumulating it would add a
	// phantom trailing line that the stage builder diffs against the old
	// lines, producing a spurious deletion/addition stage.
	if sc, ok := ss.ProviderContext.(StreamContext); ok {
		line = sc.TransformLine(line)
		if sc.ShouldSkipLine() {
			return
		}
	}

	// Accumulate text for postprocessing
	ss.AccumulatedText.WriteString(line)
	ss.AccumulatedText.WriteString("\n")

	// First line validation
	if !ss.Validated {
		if sp, ok := e.provider.(LineStreamProvider); ok {
			if err := sp.ValidateFirstLine(ss.ProviderContext, line); err != nil {
				e.cancelStreaming()
				e.state = stateIdle
				return
			}
		}
		ss.Validated = true
	}

	// If user accepted during streaming, skip stage building (diffs would be wrong).
	// Just accumulate text for cursor prediction computation when streaming completes.
	if e.acceptedDuringStreaming {
		return
	}

	// Process pending line through stage builder (if any)
	if ss.HasPendingLine {
		finalized := ss.StageBuilder.AddLine(ss.PendingLine)
		if finalized != nil && !ss.FirstStageRendered {
			// Check if this stage is close enough to render immediately
			viewportTop, viewportBottom := e.buffer.ViewportBounds()
			needsNav := text.StageNeedsNavigation(
				finalized,
				e.buffer.Row(),
				viewportTop, viewportBottom,
				e.config.CursorPrediction.ProximityThreshold,
			)
			if !needsNav {
				// Stage is close to cursor - render it immediately
				if e.renderStreamedStage(finalized) {
					e.recordMetricsShown(nil)
					ss.FirstStageRendered = true
				}
			}
			// If needsNav, don't render - let Finalize() handle it with cursor prediction
		}
	}

	// Buffer current line (will be processed on next line or completion).
	// For FIM streams, transform the first line by prepending the cursor prefix.
	pendingLine := line
	if !ss.HasPendingLine {
		if sc, ok := ss.ProviderContext.(StreamContext); ok && sc.GetStreamOldLines() != nil {
			pendingLine = sc.TransformFirstLine(pendingLine)
		}
	}
	ss.PendingLine = pendingLine
	ss.HasPendingLine = true
}

// handleStreamCompleteSimple processes stream completion when lines channel closes.
// Called directly from event loop.
func (e *Engine) handleStreamCompleteSimple() {
	// Clear stream channel first
	e.streamLinesChan = nil
	e.streamLineNum = 0

	if e.streamingState == nil {
		return
	}

	ss := e.streamingState

	// Handle case where user accepted during streaming
	// We need to recompute diff from accumulated text against current buffer
	if e.acceptedDuringStreaming {
		e.acceptedDuringStreaming = false
		e.handleStreamCompleteAfterAccept(ss)
		e.streamingState = nil
		e.streamingCancel = nil
		return
	}

	firstStageRendered := ss.FirstStageRendered

	// Process pending line if not truncated.
	// For FIM streams, transform the last line by appending the cursor suffix.
	if ss.HasPendingLine {
		lastLine := ss.PendingLine
		if sc, ok := ss.ProviderContext.(StreamContext); ok && sc.GetStreamOldLines() != nil {
			lastLine = sc.TransformLastLine(lastLine)
		}
		ss.StageBuilder.AddLine(lastLine)
		ss.HasPendingLine = false
	}

	// Log response via provider postprocessing (this also runs postprocessors)
	sp, ok := e.provider.(LineStreamProvider)
	if ok {
		accumulatedText := ss.AccumulatedText.String()
		_, _ = sp.FinishLineStream(ss.ProviderContext, accumulatedText, "stop", false)
	}

	// Finalize remaining stages
	stagingResult := ss.StageBuilder.Finalize()

	// Clear streaming state
	e.streamingState = nil
	e.streamingCancel = nil

	if stagingResult == nil || len(stagingResult.Stages) == 0 {
		e.state = stateIdle
		return
	}

	e.stagedCompletion = &text.StagedCompletion{
		Stages:     stagingResult.Stages,
		CurrentIdx: 0,
	}

	// If the first stage matches a recent rejection, drop everything.
	// Important: if we already rendered a stage during streaming, ghost text
	// and applyBatch are live; a plain state flip would leave them visible
	// with no (idle, accept) transition, so Tab would do nothing. Route
	// through reject() so ClearUI and completion/applyBatch cleanup happen.
	firstStage := stagingResult.Stages[0]
	if e.suppressRejectedCompletionForStage(firstStage) {
		e.reject()
		return
	}

	// If we already rendered a stage during streaming, keep it as-is.
	// Re-rendering would cause visible flicker since Finalize() diffs against
	// full old lines (vs partial during streaming), producing different groups.
	// The accept path reconciles any boundary mismatch (accept.go:54-63).
	if firstStageRendered {
		e.state = stateHasCompletion
		return
	}

	// Clear any UI (nothing was rendered during streaming)
	e.buffer.ClearUI()

	// Transition to appropriate state
	if stagingResult.FirstNeedsNavigation {
		e.showStageCursorTarget(stagingResult.Stages[0])
	} else {
		e.showCurrentStage()
	}
}

// handleStreamCompleteAfterAccept handles stream completion when user accepted during streaming.
// It recomputes diff from accumulated text against current buffer and shows cursor prediction.
func (e *Engine) handleStreamCompleteAfterAccept(ss *StreamingState) {
	// Get the line stream provider to run postprocessing
	sp, ok := e.provider.(LineStreamProvider)
	if !ok {
		return
	}

	// Run postprocessing to get completions from accumulated text
	accumulatedText := ss.AccumulatedText.String()
	resp, err := sp.FinishLineStream(ss.ProviderContext, accumulatedText, "stop", false)
	if err != nil {
		return
	}

	if resp == nil || len(resp.Completions) == 0 {
		return
	}

	// Sync buffer to get current state after accept
	e.syncBuffer()

	// Get first completion and compute diff against current buffer
	comp := resp.Completions[0]
	bufferLines := e.buffer.Lines()

	// Extract old lines (current buffer content in the completion range)
	var oldLines []string
	endLine := max(comp.EndLineInc, comp.StartLine+len(comp.Lines)-1)
	for i := comp.StartLine; i <= endLine && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}

	// Find first changed line
	targetLine := text.FindFirstChangedLine(oldLines, comp.Lines, comp.StartLine-1)
	if targetLine <= 0 {
		return
	}

	// Check distance to determine if show completion or cursor prediction
	distance := utils.Abs(targetLine - e.buffer.Row())

	if distance <= e.config.CursorPrediction.ProximityThreshold {
		// Close enough - show completion
		e.prefetchedCompletions = resp.Completions
		e.prefetchedCursorTarget = resp.CursorTarget
		e.prefetchState = prefetchReady
		e.tryShowPrefetchedCompletion()
	} else {
		// Far away - show cursor prediction
		e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
			RelativePath:    e.buffer.Path(),
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}, e.rejectedCompletionFor(comp))
		// Store the completions for when user jumps to target
		e.prefetchedCompletions = resp.Completions
		e.prefetchedCursorTarget = resp.CursorTarget
		e.prefetchState = prefetchReady
	}
}

// renderStreamedStage renders a finalized stage during streaming.
// Returns true only when the stage was actually rendered.
func (e *Engine) renderStreamedStage(stage *text.Stage) bool {
	if stage == nil || len(stage.Groups) == 0 {
		return false
	}

	// Suppress before rendering so cached rejections do not flash during
	// streaming and then disappear when the full stream finalizes.
	if e.suppressRejectedCompletionForStage(stage) {
		e.cancelStreaming()
		e.reject()
		return false
	}

	// Prepare completion for this stage and render it
	e.applyBatch = e.buffer.PrepareCompletion(
		stage.BufferStart,
		stage.BufferEnd,
		stage.Lines,
		stage.Groups,
	)

	// Store for partial typing optimization
	bufferLines := e.buffer.Lines()
	e.completionOriginalLines = nil
	for i := stage.BufferStart; i <= stage.BufferEnd && i-1 < len(bufferLines); i++ {
		e.completionOriginalLines = append(e.completionOriginalLines, bufferLines[i-1])
	}

	e.completions = []*types.Completion{{
		StartLine:  stage.BufferStart,
		EndLineInc: stage.BufferEnd,
		Lines:      stage.Lines,
	}}
	e.cursorTarget = stage.CursorTarget
	e.currentRejectedCompletion = e.currentRejectedCompletionCandidate()

	// Store groups for partial accept
	e.currentGroups = stage.Groups

	return true
}

// handleTokenChunk processes a cumulative text chunk from token streaming.
// The text parameter contains the full accumulated text so far (not a delta).
func (e *Engine) handleTokenChunk(accumulatedText string) {
	ts := e.tokenStreamingState
	if ts == nil {
		return
	}

	// Update accumulated text
	ts.AccumulatedText = accumulatedText

	// Build the full line content
	fullLineText := ts.LinePrefix + accumulatedText
	lineNum := ts.LineNum
	colStart := len(ts.LinePrefix)

	// Get original line content
	bufferLines := e.buffer.Lines()
	var oldLine string
	if lineNum >= 1 && lineNum <= len(bufferLines) {
		oldLine = bufferLines[lineNum-1]
	}

	// Create a group with append_chars render hint
	group := &text.Group{
		Type:       "modification",
		StartLine:  1,
		EndLine:    1,
		BufferLine: lineNum,
		Lines:      []string{fullLineText},
		OldLines:   []string{oldLine},
		RenderHint: "append_chars",
		ColStart:   colStart,
		ColEnd:     len(fullLineText),
	}

	// Call PrepareCompletion to render the ghost text
	e.applyBatch = e.buffer.PrepareCompletion(lineNum, lineNum, []string{fullLineText}, []*text.Group{group})

	// Store completion state for partial typing optimization
	e.completions = []*types.Completion{{
		StartLine:  lineNum,
		EndLineInc: lineNum,
		Lines:      []string{fullLineText},
	}}
	e.completionOriginalLines = []string{oldLine}

	// Don't capture currentRejectedCompletion here: each token chunk would
	// store an incomplete fragment that won't match a future full completion.
	// handleTokenStreamComplete re-runs through processCompletion, which
	// captures the final candidate via showCurrentStage.

	// Store groups for partial accept
	e.currentGroups = []*text.Group{group}
}

// handleTokenStreamComplete processes token stream completion when channel closes.
// Called directly from event loop.
func (e *Engine) handleTokenStreamComplete() {
	// Clear token stream channel first
	e.tokenStreamChan = nil

	if e.tokenStreamingState == nil {
		e.state = stateIdle
		return
	}

	ts := e.tokenStreamingState
	finalText := ts.AccumulatedText
	providerCtx := ts.ProviderContext
	req := ts.Request

	// Clear token streaming state
	e.tokenStreamingState = nil
	e.streamingCancel = nil

	goIdle := func() {
		e.buffer.ClearUI()
		e.state = stateIdle
	}

	if finalText == "" {
		goIdle()
		return
	}

	tokenProvider, ok := e.provider.(TokenStreamProvider)
	if !ok {
		goIdle()
		return
	}

	resp, err := tokenProvider.FinishTokenStream(providerCtx, finalText)
	if err != nil || resp == nil || len(resp.Completions) == 0 {
		goIdle()
		return
	}

	// For inline provider, there's always just one completion
	completion := resp.Completions[0]
	if completion.StartLine < 1 || completion.StartLine > len(req.Lines) {
		goIdle()
		return
	}

	// Process through normal completion flow (handles staging etc.)
	if e.processCompletion(completion) == completionShown {
		e.state = stateHasCompletion
	} else {
		goIdle()
	}
}

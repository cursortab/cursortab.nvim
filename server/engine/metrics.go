package engine

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"cursortab/ctx"
	"cursortab/history"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/types"
	"cursortab/utils"
)

var actionAbbrev = map[types.UserActionType]string{
	types.ActionInsertChar:      "IC",
	types.ActionInsertSelection: "IS",
	types.ActionDeleteChar:      "DC",
	types.ActionDeleteSelection: "DS",
	types.ActionCursorMovement:  "CM",
}

// Metrics tracking

// recordMetricsShown records that a completion was shown. Pass nil for info
// when no provider metrics ID is available (e.g. streaming completions). A
// completion shown again, after typing, a partial accept or the end of its
// stream, keeps the metrics of its first show.
func (e *Engine) recordMetricsShown(info *types.MetricsInfo, manual bool) {
	if !e.currentMetrics.ShownAt.IsZero() {
		return
	}
	now := e.clock.Now()
	e.currentMetrics = metrics.CompletionInfo{ShownAt: now}

	if info != nil && info.ID != "" {
		e.currentMetrics.ID = info.ID
		e.currentMetrics.Additions = info.Additions
		e.currentMetrics.Deletions = info.Deletions
	} else if e.display.hasCompletion() {
		// Estimate additions/deletions from completion line counts
		comp := e.display.current()
		bufferLines := e.buffer.Lines()
		origCount := 0
		for i := comp.StartLine; i <= comp.EndLineInc && i-1 < len(bufferLines); i++ {
			origCount++
		}
		newCount := len(comp.Lines)
		if newCount > origCount {
			e.currentMetrics.Additions = newCount - origCount
		} else if origCount > newCount {
			e.currentMetrics.Deletions = origCount - newCount
		}
	}

	e.currentSnapshot = e.captureSnapshot(manual)
	e.sendMetric(metrics.EventShown)
}

// sendMetric sends eventType for the shown completion. It does nothing when
// no completion was shown since the last outcome, so callers may end a
// completion that was never shown.
func (e *Engine) sendMetric(eventType metrics.EventType) {
	if e.metricSender == nil {
		return
	}
	// Need either a provider ID or a snapshot to send anything useful
	if e.currentMetrics.ID == "" && e.currentSnapshot == nil {
		return
	}

	event := metrics.Event{
		Type:     eventType,
		Info:     e.currentMetrics,
		Snapshot: e.currentSnapshot,
	}

	if eventType != metrics.EventShown {
		e.currentMetrics = metrics.CompletionInfo{}
		e.currentSnapshot = nil
		if eventType == metrics.EventAccepted {
			e.completionsSinceAccept = 0
		} else {
			e.completionsSinceAccept++
		}
	}

	select {
	case e.metricsCh <- event:
	default:
		logger.Warn("metrics: event queue full, dropping %s event for %s", eventType, event.Info.ID)
	}
}

// classifyScope maps a treesitter enclosing signature to a coarse scope bucket.
func classifyScope(signature string) string {
	if signature == "" {
		return "top_level"
	}
	sig := strings.ToLower(signature)
	switch {
	case strings.Contains(sig, "func") || strings.Contains(sig, "function") ||
		strings.Contains(sig, "method") || strings.Contains(sig, "def "):
		return "function"
	case strings.Contains(sig, "class") || strings.Contains(sig, "struct") ||
		strings.Contains(sig, "impl") || strings.Contains(sig, "interface"):
		return "class"
	case strings.Contains(sig, "comment"):
		return "comment"
	case strings.Contains(sig, "string") || strings.Contains(sig, "template"):
		return "string"
	default:
		return "other"
	}
}

func (e *Engine) captureSnapshot(manual bool) *metrics.Snapshot {
	current := e.buildCurrentSnapshot(completionInputOptions{})
	fileContext := e.buildFileContextSnapshot(ctx.Materials{ctx.EditHistory{}, ctx.UserActions{}})
	lines := current.File.Lines
	row := current.Cursor.Row

	line, col := currentLine(lines, row, current.Cursor.Col)
	prefix := line[:col]
	trimmedPrefix := strings.TrimRight(prefix, " \t")

	docLen := documentByteLength(lines)
	cursorOffset := byteOffset(lines, row, col)
	relativePosition := 0.0
	if docLen > 0 {
		relativePosition = (float64(cursorOffset) + 0.5) / (1.0 + float64(docLen))
	}

	lastChar := ""
	if len(prefix) > 0 {
		lastChar = string(prefix[len(prefix)-1])
	}
	lastNWS := ""
	if nwc, ok := lastNonWSChar(line, col); ok {
		lastNWS = string(nwc)
	}

	// Count leading whitespace characters (raw count, not indent units)
	leadingWS := 0
	for _, ch := range line {
		if ch != ' ' && ch != '\t' {
			break
		}
		leadingWS++
	}

	fileExt := strings.ToLower(filepath.Ext(current.File.Path))
	language := extToLanguage[fileExt]
	if language == "" {
		language = "unknown"
	}

	source := "typing"
	if e.lastCompletionSource == types.CompletionSourceIdle {
		source = "idle"
	}

	completionLines := 0
	if e.display.hasCompletion() {
		completionLines = len(e.display.current().Lines)
	}

	editCount, predictedEditRatio, timeSinceLastEditMs := metricsDiffStatsFromSnapshot(fileContext, e.config.MaxDiffTokens)
	typingSpeed := metricsTypingSpeed(fileContext.UserActions)
	recentActions := metricsRecentActions(fileContext.UserActions)
	hasDiagnostics := false
	if diagnostics := e.buffer.Diagnostics(); diagnostics != nil && len(diagnostics.Items) > 0 {
		hasDiagnostics = true
	}
	treesitterScope := "other"
	if treesitter := e.buffer.TreesitterSymbols(row, current.Cursor.Col, defaultMaxSiblings); treesitter != nil {
		treesitterScope = classifyScope(treesitter.EnclosingSignature)
	}

	stageIndex := 0
	if e.stagedCompletion != nil {
		stageIndex = e.stagedCompletion.CurrentIdx
	}

	cursorTargetDistance := 0
	if e.cursorTarget != nil {
		dist := int(e.cursorTarget.LineNumber) - row
		if dist < 0 {
			dist = -dist
		}
		cursorTargetDistance = dist
	}

	snapshot := &metrics.Snapshot{
		FileExt:                fileExt,
		Language:               language,
		PrefixLength:           len(prefix),
		TrimmedPrefixLength:    len(trimmedPrefix),
		LineCount:              len(lines),
		RelativePosition:       relativePosition,
		AfterCursorWS:          afterCursorIsWhitespace(lines, row, col),
		LastChar:               lastChar,
		LastNonWSChar:          lastNWS,
		IndentationLevel:       leadingWS,
		CompletionLines:        completionLines,
		CompletionAdditions:    e.currentMetrics.Additions,
		CompletionDeletions:    e.currentMetrics.Deletions,
		CompletionSource:       source,
		ManuallyTriggered:      manual,
		Provider:               e.config.ProviderName,
		StageIndex:             stageIndex,
		CursorTargetDistance:   cursorTargetDistance,
		IsPrefetched:           e.readyPrefetch() != nil,
		TimeSinceLastEditMs:    timeSinceLastEditMs,
		TypingSpeed:            typingSpeed,
		RecentActions:          recentActions,
		HasDiagnostics:         hasDiagnostics,
		TreesitterScope:        treesitterScope,
		EditCount:              editCount,
		PredictedEditRatio:     predictedEditRatio,
		CompletionsSinceAccept: e.completionsSinceAccept,
	}
	return snapshot
}

func metricsDiffStatsFromSnapshot(snapshot ctx.FileContextSnapshot, maxDiffTokens int) (int, float64, int) {
	editCount := 0
	predictedCount := 0
	latestTimestampNs := int64(0)
	addDiffStats := func(diffs []*types.DiffEntry) {
		for _, d := range diffs {
			if d == nil {
				continue
			}
			editCount++
			if d.Source == types.DiffSourcePredicted {
				predictedCount++
			}
			if d.TimestampNs > latestTimestampNs {
				latestTimestampNs = d.TimestampNs
			}
		}
	}

	for _, file := range snapshot.RecentFiles {
		addDiffStats(history.Process(file.DiffHistories, snapshot.NowNs))
	}
	currentDiffs := history.Process(snapshot.CurrentDiffHistories, snapshot.NowNs)
	if maxDiffTokens > 0 {
		currentDiffs = utils.TrimDiffEntries(currentDiffs, maxDiffTokens)
	}
	addDiffStats(currentDiffs)

	predictedEditRatio := 0.0
	if editCount > 0 {
		predictedEditRatio = float64(predictedCount) / float64(editCount)
	}
	timeSinceLastEditMs := 0
	if latestTimestampNs > 0 {
		timeSinceLastEditMs = int(snapshot.NowNs/1_000_000 - latestTimestampNs/1_000_000)
	}
	return editCount, predictedEditRatio, timeSinceLastEditMs
}

func metricsTypingSpeed(actions []*types.UserAction) float64 {
	if len(actions) < 2 {
		return 0
	}
	insertCount := 0
	for _, action := range actions {
		if action != nil && action.ActionType == types.ActionInsertChar {
			insertCount++
		}
	}
	first := actions[0]
	last := actions[len(actions)-1]
	if first == nil || last == nil {
		return 0
	}
	if durationSec := float64(last.TimestampMs-first.TimestampMs) / 1000.0; durationSec > 0 {
		return float64(insertCount) / durationSec
	}
	return 0
}

func metricsRecentActions(actions []*types.UserAction) []string {
	recentActions := make([]string, 0, 5)
	start := len(actions) - 5
	if start < 0 {
		start = 0
	}
	for _, action := range actions[start:] {
		if action == nil {
			continue
		}
		if abbr, ok := actionAbbrev[action.ActionType]; ok {
			recentActions = append(recentActions, abbr)
		}
	}
	return recentActions
}

// metricSendTimeout bounds the delivery of one metrics event.
const metricSendTimeout = 10 * time.Second

// metricsWorker delivers metrics events in order. It outlives the engine:
// shutdown closes the queue, and the worker sends what is left, so outcomes
// of the last completion still arrive after the editor disconnects.
func (e *Engine) metricsWorker() {
	for event := range e.metricsCh {
		ctx, cancel := context.WithTimeout(context.Background(), metricSendTimeout)
		e.metricSender.SendMetric(ctx, event)
		cancel()
	}
}

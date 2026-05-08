package engine

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
)

// inertSuffixPattern matches cursor suffixes where insertion-only completions
// are still useful: whitespace, closing brackets, trailing punctuation.
// Matches Copilot's heuristic: /^\s*[)>}\]"'`]*\s*[:{;,]?\s*$/
var inertSuffixPattern = regexp.MustCompile(`^\s*[)>}\]"'` + "`" + `]*\s*[:{;,]?\s*$`)

const (
	// consecutiveDeletionThreshold is the number of consecutive deletion actions
	// after which completions are re-enabled (user is rewriting, not correcting).
	consecutiveDeletionThreshold = 3

	rejectedCompletionTTL                 = 30 * time.Second
	rejectedCompletionLineProximity       = 3
	rejectedCompletionContextThreshold    = 0.9
	rejectedCompletionOldLinesThreshold   = 0.9
	rejectedCompletionSimilarityThreshold = 0.85
	// rejectedCompletionMinLineSimilarity prevents a single very-different
	// line from being averaged away by surrounding identical lines.
	rejectedCompletionMinLineSimilarity = 0.5
	rejectedCompletionMaxPerFile        = 8
)

type rejectedCompletion struct {
	filePath   string
	startLine  int
	endLineInc int
	beforeLine string
	afterLine  string
	oldLines   []string
	lines      []string
	expiresAt  time.Time
}

// suppressForSingleDeletion returns true if the last action was a single
// deletion (typo correction) without a streak of deletions (rewrite).
func (e *Engine) suppressForSingleDeletion() bool {
	if len(e.userActions) == 0 {
		return false
	}

	last := e.userActions[len(e.userActions)-1]
	if !isDeletion(last.ActionType) {
		return false
	}

	// Count consecutive deletions from the end
	consecutive := 0
	for i := len(e.userActions) - 1; i >= 0; i-- {
		if isDeletion(e.userActions[i].ActionType) {
			consecutive++
		} else {
			break
		}
	}

	// A streak of deletions means the user is rewriting → allow completions
	return consecutive < consecutiveDeletionThreshold
}

// suppressForMidLine returns true if the cursor is in the middle of a line
// with meaningful code to the right, and the provider is inline (not FIM or edit).
func (e *Engine) suppressForMidLine() bool {
	if e.config.EditCompletionProvider || e.config.ProviderName == string(types.ProviderTypeFIM) {
		return false
	}

	lines := e.buffer.Lines()
	row := e.buffer.Row() // 1-indexed
	col := e.buffer.Col() // 0-indexed

	if row < 1 || row > len(lines) {
		return false
	}

	line := lines[row-1]
	if col >= len(line) {
		return false // cursor at end of line
	}

	suffix := line[col:]
	return !inertSuffixPattern.MatchString(suffix)
}

// suppressForNoEdits returns true if the buffer hasn't changed since the last
// save (or initial open). Files that skip history (e.g. COMMIT_EDITMSG) are
// never suppressed.
func (e *Engine) suppressForNoEdits() bool {
	if e.buffer.SkipHistory() {
		return false
	}
	return !e.buffer.IsModified()
}

// suppressForDisabledScope returns the matched scope name if the cursor is
// inside a treesitter scope listed in DisabledIn, or "" if not suppressed.
func (e *Engine) suppressForDisabledScope() string {
	if len(e.config.DisabledIn) == 0 {
		return ""
	}

	scopes := e.buffer.CursorScopes()
	if len(scopes) == 0 {
		return ""
	}

	disabled := make(map[string]bool, len(e.config.DisabledIn))
	for _, s := range e.config.DisabledIn {
		disabled[s] = true
	}

	for _, scope := range scopes {
		if disabled[scope] {
			return scope
		}
	}
	return ""
}

// suppressRejectedCompletionForStage returns true when the staged completion
// is similar to one the user recently rejected in this file. Suppression is
// always done at stage granularity because that's also how cache entries are
// stored — the user only sees one stage at a time. It emits exactly one
// debug log per call describing the outcome.
func (e *Engine) suppressRejectedCompletionForStage(stage *text.Stage) bool {
	if e.manuallyTriggered || stage == nil {
		return false
	}
	entry := e.rejectedCompletionForStage(stage)
	if entry == nil {
		return false
	}

	entries := e.pruneRejectedCompletions(entry.filePath)
	if len(entries) == 0 {
		return false
	}

	for _, cached := range entries {
		if matched, reason := cached.matches(entry); matched {
			logger.Debug("rejection cache: suppressed (%d entries, %s)", len(entries), reason)
			return true
		}
	}
	logger.Debug("rejection cache: allowed (%d entries, no match)", len(entries))
	return false
}

// rememberRejectedCompletion caches the current completion so future similar
// completions are suppressed. Called only when the user explicitly rejects.
func (e *Engine) rememberRejectedCompletion() {
	if e.currentRejectedCompletion == nil {
		return
	}

	entry := e.currentRejectedCompletion.clone()
	entry.expiresAt = e.clock.Now().Add(rejectedCompletionTTL)
	entries := e.pruneRejectedCompletions(entry.filePath)
	if len(entries) >= rejectedCompletionMaxPerFile {
		entries = entries[len(entries)-rejectedCompletionMaxPerFile+1:]
	}
	entries = append(entries, entry)
	e.rejectedCompletions[entry.filePath] = entries
	e.currentRejectedCompletion = nil
}

// forgetRejectedCompletions drops the rejection cache for the given file.
// Called on accept/partial-accept: the user moved forward, any cached
// entries are now stale (line numbers may have shifted, context changed).
func (e *Engine) forgetRejectedCompletions(filePath string) {
	if filePath == "" {
		return
	}
	delete(e.rejectedCompletions, filePath)
}

func (e *Engine) currentRejectedCompletionCandidate() *rejectedCompletion {
	if len(e.completions) == 0 {
		return nil
	}
	return e.rejectedCompletionFor(e.completions[0])
}

// rejectedCompletionForStage builds a rejection-cache candidate from a stage.
// Used when no completion has been pushed into e.completions yet (e.g. a
// cursor-target-only render that didn't show ghost text).
func (e *Engine) rejectedCompletionForStage(stage *text.Stage) *rejectedCompletion {
	if stage == nil {
		return nil
	}
	return e.rejectedCompletionFor(&types.Completion{
		StartLine:  stage.BufferStart,
		EndLineInc: stage.BufferEnd,
		Lines:      stage.Lines,
	})
}

func (e *Engine) rejectedCompletionFor(comp *types.Completion) *rejectedCompletion {
	if comp == nil {
		return nil
	}

	beforeLine, afterLine := surroundingCompletionContext(e.buffer.Lines(), comp.StartLine, comp.EndLineInc)
	oldLines := currentCompletionOldLines(e.buffer.Lines(), comp.StartLine, comp.EndLineInc)

	// A completion with no new lines and no old lines isn't a real edit; nothing
	// to compare against later, so don't bother caching it.
	if len(comp.Lines) == 0 && len(oldLines) == 0 {
		return nil
	}

	return &rejectedCompletion{
		filePath:   e.buffer.Path(),
		startLine:  comp.StartLine,
		endLineInc: comp.EndLineInc,
		beforeLine: beforeLine,
		afterLine:  afterLine,
		oldLines:   oldLines,
		lines:      normalizeCompletionLines(comp.Lines),
	}
}

func (e *Engine) pruneRejectedCompletions(filePath string) []*rejectedCompletion {
	if filePath == "" || e.rejectedCompletions == nil {
		return nil
	}

	now := e.clock.Now()
	entries := e.rejectedCompletions[filePath]
	if len(entries) == 0 {
		delete(e.rejectedCompletions, filePath)
		return nil
	}

	kept := entries[:0]
	for _, entry := range entries {
		if entry != nil && now.Before(entry.expiresAt) {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		delete(e.rejectedCompletions, filePath)
		return nil
	}
	trimmed := append([]*rejectedCompletion(nil), kept...)
	e.rejectedCompletions[filePath] = trimmed
	return trimmed
}

func (r *rejectedCompletion) matches(other *rejectedCompletion) (bool, string) {
	if r == nil || other == nil {
		return false, "missing comparison entry"
	}
	if r.filePath != other.filePath {
		return false, fmt.Sprintf("file mismatch cached=%s incoming=%s", r.filePath, other.filePath)
	}
	if dist := absInt(r.startLine - other.startLine); dist > rejectedCompletionLineProximity {
		return false, fmt.Sprintf("start line distance %d > %d", dist, rejectedCompletionLineProximity)
	}
	beforeSim, beforeOK := contextLineSimilar(r.beforeLine, other.beforeLine)
	if !beforeOK {
		return false, fmt.Sprintf("before-line similarity %.2f < %.2f", beforeSim, rejectedCompletionContextThreshold)
	}
	afterSim, afterOK := contextLineSimilar(r.afterLine, other.afterLine)
	if !afterOK {
		return false, fmt.Sprintf("after-line similarity %.2f < %.2f", afterSim, rejectedCompletionContextThreshold)
	}
	oldAvg, oldMin := completionLinesSimilarityStats(r.oldLines, other.oldLines)
	if oldAvg < rejectedCompletionOldLinesThreshold {
		return false, fmt.Sprintf("old-lines avg similarity %.2f < %.2f", oldAvg, rejectedCompletionOldLinesThreshold)
	}
	if oldMin < rejectedCompletionMinLineSimilarity {
		return false, fmt.Sprintf("old-lines min line similarity %.2f < %.2f", oldMin, rejectedCompletionMinLineSimilarity)
	}
	completionAvg, completionMin := completionLinesSimilarityStats(r.lines, other.lines)
	if completionAvg < rejectedCompletionSimilarityThreshold {
		return false, fmt.Sprintf("completion avg similarity %.2f < %.2f", completionAvg, rejectedCompletionSimilarityThreshold)
	}
	if completionMin < rejectedCompletionMinLineSimilarity {
		return false, fmt.Sprintf("completion min line similarity %.2f < %.2f", completionMin, rejectedCompletionMinLineSimilarity)
	}
	return true, fmt.Sprintf("before=%.2f after=%.2f old(avg=%.2f,min=%.2f) completion(avg=%.2f,min=%.2f)",
		beforeSim, afterSim, oldAvg, oldMin, completionAvg, completionMin)
}

func (r *rejectedCompletion) clone() *rejectedCompletion {
	if r == nil {
		return nil
	}
	copyOldLines := append([]string(nil), r.oldLines...)
	copyLines := append([]string(nil), r.lines...)
	return &rejectedCompletion{
		filePath:   r.filePath,
		startLine:  r.startLine,
		endLineInc: r.endLineInc,
		beforeLine: r.beforeLine,
		afterLine:  r.afterLine,
		oldLines:   copyOldLines,
		lines:      copyLines,
		expiresAt:  r.expiresAt,
	}
}

// completionLinesSimilarityStats returns both the average and minimum
// per-line similarity. The minimum guards against a single very-different
// line being averaged away by surrounding identical lines. The minimum is
// computed over the overlapping prefix only — trailing positions in the
// longer slice would always count as 0 similarity and trivially fail the
// min gate, so length differences are penalized through the average alone
// (total summed over the overlap, divided by the longer length).
func completionLinesSimilarityStats(a, b []string) (avg, minSim float64) {
	if len(a) == 0 && len(b) == 0 {
		return 1.0, 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	overlap := min(len(a), len(b))
	maxLines := max(len(a), len(b))

	total := 0.0
	minSim = 1.0
	for i := 0; i < overlap; i++ {
		sim := text.LineSimilarity(a[i], b[i])
		total += sim
		if sim < minSim {
			minSim = sim
		}
	}
	return total / float64(maxLines), minSim
}

// contextLineSimilar reports whether two surrounding-context lines should
// be treated as the same context. For lines longer than 3 chars after
// trimming, the strict similarity ratio applies. For shorter lines the
// ratio is dominated by single-char punctuation differences ("}" vs "};")
// so the gate is relaxed — the oldLines / completion-lines gates carry the
// real signal.
func contextLineSimilar(a, b string) (float64, bool) {
	sim := text.LineSimilarity(a, b)
	if sim >= rejectedCompletionContextThreshold {
		return sim, true
	}
	if max(len(a), len(b)) <= 3 {
		return sim, true
	}
	return sim, false
}

func normalizeCompletionLines(lines []string) []string {
	normalized := make([]string, len(lines))
	allEmpty := true
	for i, line := range lines {
		normalized[i] = strings.TrimRight(line, " \t")
		if normalized[i] != "" {
			allEmpty = false
		}
	}
	if allEmpty {
		return normalized
	}
	for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
		normalized = normalized[:len(normalized)-1]
	}
	return normalized
}

func currentCompletionOldLines(lines []string, startLine, endLineInc int) []string {
	if len(lines) == 0 || startLine < 1 || endLineInc < startLine {
		return nil
	}
	var oldLines []string
	for i := startLine; i <= endLineInc && i-1 < len(lines); i++ {
		oldLines = append(oldLines, lines[i-1])
	}
	return normalizeCompletionLines(oldLines)
}

func surroundingCompletionContext(lines []string, startLine, endLineInc int) (string, string) {
	if len(lines) == 0 {
		return "", ""
	}
	before := ""
	if startLine > 1 && startLine-2 < len(lines) {
		before = strings.TrimSpace(lines[startLine-2])
	}
	after := ""
	if endLineInc >= 0 && endLineInc < len(lines) {
		after = strings.TrimSpace(lines[endLineInc])
	}
	return before, after
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func isDeletion(action types.UserActionType) bool {
	return action == types.ActionDeleteChar || action == types.ActionDeleteSelection
}

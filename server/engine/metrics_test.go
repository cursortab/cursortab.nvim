package engine

import (
	"math"
	"strings"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

func TestCaptureSnapshot_CollectsDiagnosticsAndTreesitter(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "main.go"
	buf.row = 1
	buf.col = 4
	buf.diagnostics = &types.Diagnostics{
		FilePath: "main.go",
		Items: []*types.Diagnostic{
			{Message: "problem", Severity: types.SeverityWarning},
		},
	}
	buf.treesitter = &types.TreesitterContext{EnclosingSignature: "func main()"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.mainCtx = t.Context()

	sourceInput := eng.buildMetricsSourceInput()
	snapshot := eng.captureSnapshot(sourceInput)

	assert.True(t, snapshot.HasDiagnostics, "diagnostics from collector")
	assert.Equal(t, "function", snapshot.TreesitterScope, "treesitter scope from collector")
}

func TestCaptureSnapshot_DiffStatsFromFileContextSnapshot(t *testing.T) {
	now := time.Unix(1000, 0)
	nowNs := now.UnixNano()
	buf := newMockBuffer()
	buf.path = "current.go"
	buf.diffHistories = []*types.DiffEntry{
		{Original: strings.Repeat("a", 10), Updated: strings.Repeat("b", 10), Source: types.DiffSourceManual, TimestampNs: nowNs - int64(4*time.Second), StartLine: 1},
		{Original: strings.Repeat("c", 10), Updated: strings.Repeat("d", 10), Source: types.DiffSourcePredicted, TimestampNs: nowNs - int64(2*time.Second), StartLine: 20},
	}
	prov := newMockProvider()
	clock := newMockClock()
	clock.now = now
	eng := createTestEngine(buf, prov, clock)
	eng.config.MaxDiffTokens = 1
	eng.fileStateStore["recent.go"] = &FileState{
		DiffHistories: []*types.DiffEntry{
			{Original: "dup", Updated: "changed", Source: types.DiffSourceManual, TimestampNs: nowNs - int64(6*time.Second), StartLine: 1},
			{Original: "dup", Updated: "changed", Source: types.DiffSourceManual, TimestampNs: nowNs - int64(5*time.Second), StartLine: 1},
			{Original: strings.Repeat("x", 10), Updated: strings.Repeat("y", 10), Source: types.DiffSourcePredicted, TimestampNs: nowNs - int64(3*time.Second), StartLine: 30},
		},
		LastAccessNs: nowNs - int64(time.Second),
	}

	sourceInput := eng.buildMetricsSourceInput()
	buf.diffHistories = nil
	eng.fileStateStore = map[string]*FileState{}
	snapshot := eng.captureSnapshot(sourceInput)

	assert.Equal(t, 3, snapshot.EditCount, "processed cross-file plus trimmed current edit count")
	assert.True(t, math.Abs(snapshot.PredictedEditRatio-(2.0/3.0)) < 0.0001, "predicted edit ratio")
	assert.Equal(t, 2000, snapshot.TimeSinceLastEditMs, "time since latest edit from snapshot now")
}

func TestCaptureSnapshot_UserActionStatsFromFileContextSnapshot(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar, FilePath: "current.go", TimestampMs: 0},
		{ActionType: types.ActionDeleteChar, FilePath: "other.go", TimestampMs: 1000},
		{ActionType: types.ActionInsertChar, FilePath: "other.go", TimestampMs: 2000},
	}

	sourceInput := eng.buildMetricsSourceInput()
	eng.userActions = nil
	snapshot := eng.captureSnapshot(sourceInput)

	assert.Equal(t, 1.0, snapshot.TypingSpeed, "typing speed from frozen full action ring")
	assert.Equal(t, []string{"IC", "DC", "IC"}, snapshot.RecentActions, "recent actions include cross-file actions")
}

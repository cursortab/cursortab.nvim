package text

import (
	"crypto/sha256"
	"cursortab/assert"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"
)

var updateAll = flag.Bool("update", false, "update all expected golden files")
var update multiStringFlag
var verifyAll = flag.Bool("verify-all", false, "mark all passing test cases as verified")
var verify multiStringFlag

func init() {
	flag.Var(&update, "update-only", "update expected for specific test cases (can be repeated)")
	flag.Var(&verify, "verify", "mark a test case as verified by name (can be repeated)")
}

type multiStringFlag []string

func (f *multiStringFlag) String() string     { return strings.Join(*f, ",") }
func (f *multiStringFlag) Set(v string) error { *f = append(*f, v); return nil }
func (f multiStringFlag) contains(v string) bool {
	return slices.Contains(f, v)
}

type fixtureParams struct {
	CursorRow      int
	CursorCol      int
	ViewportTop    int
	ViewportBottom int
}

// parseTxtarFixture parses a txtar archive into its fixture components.
// The header contains key: value metadata, and the archive sections contain
// old.txt, new.txt, and expected (DSL format).
func parseTxtarFixture(ar *txtar.Archive) (params fixtureParams, oldBytes, newBytes []byte, expected []map[string]any, err error) {
	// Parse header metadata
	for _, line := range strings.Split(string(ar.Comment), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		n, _ := strconv.Atoi(val)
		switch key {
		case "cursorRow":
			params.CursorRow = n
		case "cursorCol":
			params.CursorCol = n
		case "viewportTop":
			params.ViewportTop = n
		case "viewportBottom":
			params.ViewportBottom = n
		}
	}

	var expectedDSL string
	for _, f := range ar.Files {
		data := strings.TrimSuffix(string(f.Data), "\n")
		switch f.Name {
		case "old.txt":
			oldBytes = []byte(data)
		case "new.txt":
			newBytes = []byte(data)
		case "expected":
			expectedDSL = data
		}
	}

	expected, err = parseExpected(expectedDSL)
	return
}

// writeTxtarFixture writes a fixture back to txtar format.
func writeTxtarFixture(path string, params fixtureParams, oldBytes, newBytes []byte, expected []map[string]any) error {
	header := fmt.Sprintf("cursorRow: %d\ncursorCol: %d\nviewportTop: %d\nviewportBottom: %d\n",
		params.CursorRow, params.CursorCol, params.ViewportTop, params.ViewportBottom)

	dsl := formatExpected(expected)

	ar := &txtar.Archive{
		Comment: []byte(header),
		Files: []txtar.File{
			{Name: "old.txt", Data: append(oldBytes, '\n')},
			{Name: "new.txt", Data: append(newBytes, '\n')},
			{Name: "expected", Data: []byte(dsl + "\n")},
		},
	}
	return os.WriteFile(path, txtar.Format(ar), 0644)
}

type maxLinesResult struct {
	MaxLines           int
	ApplyPass          bool
	ApplyLines         []string
	PartialAcceptPass  bool
	PartialAcceptLines []string
}

type fixtureResult struct {
	Name              string
	OldText           string
	NewText           string
	Params            fixtureParams
	Expected          []map[string]any
	BatchActual       []map[string]any
	IncrementalActual []map[string]any
	BatchPass         bool
	IncrementalPass   bool
	MaxLinesResults   []maxLinesResult
	Verified          bool
}

// stageIsPureInsertion checks if a stage is a pure insertion (insert without
// replacing any old lines). Mirrors computeReplaceEnd in buffer.go.
func stageIsPureInsertion(stage *Stage) bool {
	if stage.BufferStart != stage.BufferEnd || len(stage.Groups) == 0 {
		return false
	}
	groupLines := 0
	for _, g := range stage.Groups {
		if g.Type != "addition" {
			return false
		}
		groupLines += g.EndLine - g.StartLine + 1
	}
	return len(stage.Lines) == groupLines
}

// testBuffer simulates Neovim's buffer for apply verification.
type testBuffer struct {
	lines []string
}

// applyStage simulates nvim_buf_set_lines for a stage.
func (b *testBuffer) applyStage(stage *Stage) {
	isPureInsertion := stageIsPureInsertion(stage)

	start := stage.BufferStart - 1 // 0-indexed
	if isPureInsertion {
		// Insert without replacing: splice at start
		newLines := make([]string, 0, len(b.lines)+len(stage.Lines))
		newLines = append(newLines, b.lines[:start]...)
		newLines = append(newLines, stage.Lines...)
		newLines = append(newLines, b.lines[start:]...)
		b.lines = newLines
	} else {
		// Replace [start, end] inclusive with stage.Lines
		end := stage.BufferEnd // 1-indexed inclusive → 0-indexed exclusive
		newLines := make([]string, 0, len(b.lines)-end+start+len(stage.Lines))
		newLines = append(newLines, b.lines[:start]...)
		newLines = append(newLines, stage.Lines...)
		if end < len(b.lines) {
			newLines = append(newLines, b.lines[end:]...)
		}
		// Neovim buffers always have at least one line
		if len(newLines) == 0 {
			newLines = []string{""}
		}
		b.lines = newLines
	}
}

// partialAcceptStage simulates Ctrl+Right partial acceptance for a stage.
// Mirrors the engine's partialAcceptCompletion → rerenderPartial loop.
// Stages with deletions fall back to full apply since partial accept cannot
// delete lines (the user would press Tab instead of Ctrl+Right).
func (b *testBuffer) partialAcceptStage(stage *Stage) {
	// Partial accept only works for same-line-count modifications and append_chars.
	// Stages that add or remove lines require full batch apply (Tab).
	isPureInsertion := stageIsPureInsertion(stage)
	var oldLineCount int
	if isPureInsertion {
		oldLineCount = 0
	} else {
		oldLineCount = stage.BufferEnd - stage.BufferStart + 1
	}
	if len(stage.Lines) != oldLineCount {
		b.applyStage(stage)
		return
	}

	startLine := stage.BufferStart
	completionLines := append([]string{}, stage.Lines...)
	groups := make([]*Group, len(stage.Groups))
	for i, g := range stage.Groups {
		cp := *g
		groups[i] = &cp
	}

	maxIter := len(completionLines)*20 + 100
	for iter := 0; iter < maxIter && len(completionLines) > 0 && len(groups) > 0; iter++ {
		firstGroup := groups[0]

		if firstGroup.RenderHint == "append_chars" {
			lineIdx := firstGroup.BufferLine - 1
			if lineIdx < 0 || lineIdx >= len(b.lines) {
				break
			}
			currentLine := b.lines[lineIdx]
			targetLine := completionLines[0]

			if len(currentLine) >= len(targetLine) {
				if len(completionLines) <= 1 {
					return
				}
				completionLines = completionLines[1:]
				startLine++
			} else {
				remainingGhost := targetLine[len(currentLine):]
				acceptLen := FindNextWordBoundary(remainingGhost)
				b.lines[lineIdx] = currentLine + remainingGhost[:acceptLen]

				if len(b.lines[lineIdx]) >= len(targetLine) {
					if len(completionLines) <= 1 {
						return
					}
					completionLines = completionLines[1:]
					startLine++
				}
			}
		} else {
			firstLine := completionLines[0]
			if startLine > len(b.lines) {
				newLines := make([]string, 0, len(b.lines)+1)
				newLines = append(newLines, b.lines[:startLine-1]...)
				newLines = append(newLines, firstLine)
				newLines = append(newLines, b.lines[startLine-1:]...)
				b.lines = newLines
			} else {
				b.lines[startLine-1] = firstLine
			}

			if len(completionLines) <= 1 {
				return
			}
			completionLines = completionLines[1:]
			startLine++
		}

		// Recompute diff and groups (mirrors rerenderPartial)
		endLineInc := startLine + len(completionLines) - 1
		var originalLines []string
		for i := startLine; i <= endLineInc && i-1 < len(b.lines); i++ {
			originalLines = append(originalLines, b.lines[i-1])
		}

		diffResult := ComputeDiff(JoinLines(originalLines), JoinLines(completionLines))
		groups = GroupChanges(diffResult.ChangesMap())
		for _, g := range groups {
			g.BufferLine = startLine + g.StartLine - 1
		}
	}
}

// advanceOffsets applies the offset from the applied stage to remaining stages
// that are at or after the applied stage's buffer position.
// Stages before the applied stage's position are unaffected.
func advanceOffsets(stages []*Stage, appliedIdx int) {
	stage := stages[appliedIdx]

	isPureInsertion := stageIsPureInsertion(stage)

	var oldLineCount int
	if isPureInsertion {
		oldLineCount = 0
	} else {
		oldLineCount = stage.BufferEnd - stage.BufferStart + 1
	}
	offset := len(stage.Lines) - oldLineCount

	if offset != 0 {
		for i := appliedIdx + 1; i < len(stages); i++ {
			if stages[i].BufferStart >= stage.BufferStart {
				stages[i].BufferStart += offset
				stages[i].BufferEnd += offset
				for _, g := range stages[i].Groups {
					g.BufferLine += offset
				}
			}
		}
	}
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func loadVerifiedManifest(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return map[string]string{}
	}
	return m
}

func saveVerifiedManifest(path string, m map[string]string) error {
	// Sort keys for stable output
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]struct{ k, v string }, len(keys))
	for i, k := range keys {
		ordered[i] = struct{ k, v string }{k, m[k]}
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func TestE2E(t *testing.T) {
	e2eDir := filepath.Join("testdata")
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		t.Fatalf("failed to read e2e directory: %v", err)
	}

	manifestPath := filepath.Join(e2eDir, "verified.json")
	manifest := loadVerifiedManifest(manifestPath)
	manifestDirty := false

	var fixtures []fixtureResult

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".txtar") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".txtar")
		fixturePath := filepath.Join(e2eDir, entry.Name())

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(fixturePath)
			assert.NoError(t, err, "read fixture")

			ar := txtar.Parse(data)
			params, oldBytes, newBytes, expected, err := parseTxtarFixture(ar)
			assert.NoError(t, err, "parse fixture")

			oldLines := strings.Split(string(oldBytes), "\n")
			newLines := strings.Split(string(newBytes), "\n")

			if params.CursorRow < 1 || params.CursorRow > len(oldLines) {
				t.Fatalf("cursorRow %d is out of bounds for old.txt (%d lines)", params.CursorRow, len(oldLines))
			}

			// --- Batch pipeline ---
			oldText := JoinLines(oldLines)
			newText := JoinLines(newLines)
			diff := ComputeDiff(oldText, newText)

			batchResult := CreateStages(&StagingParams{
				Diff:               diff,
				CursorRow:          params.CursorRow,
				CursorCol:          params.CursorCol,
				ViewportTop:        params.ViewportTop,
				ViewportBottom:     params.ViewportBottom,
				BaseLineOffset:     1,
				ProximityThreshold: 10,
				NewLines:           newLines,
				OldLines:           oldLines,
				FilePath:           "test.txt",
			})

			var batchLua []map[string]any
			if batchResult != nil {
				for _, stage := range batchResult.Stages {
					batchLua = append(batchLua, ToLuaFormat(stage, stage.BufferStart))
				}
			}

			// --- Incremental pipeline ---
			builder := NewIncrementalStageBuilder(
				oldLines,
				1,
				10,
				0,
				params.ViewportTop, params.ViewportBottom,
				params.CursorRow, params.CursorCol,
				"test.txt",
			)
			for _, line := range newLines {
				builder.AddLine(line)
			}
			incResult := builder.Finalize()

			var incLua []map[string]any
			if incResult != nil {
				for _, stage := range incResult.Stages {
					incLua = append(incLua, ToLuaFormat(stage, stage.BufferStart))
				}
			}

			// --- Apply verification with multiple MaxLines values ---
			maxLinesValues := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 1000}
			var mlResults []maxLinesResult

			for _, maxLines := range maxLinesValues {
				mlResult := verifyApplyWithMaxLines(t, oldLines, newLines, oldText, newText, params, maxLines)
				mlResults = append(mlResults, mlResult)
			}

			// --- Update or compare ---
			if *updateAll || update.contains(name) {
				newDSL := formatExpected(batchLua)
				oldDSL := formatExpected(expected)
				if newDSL == oldDSL {
					t.Logf("skipped %s (unchanged)", fixturePath)
				} else {
					assert.NoError(t, writeTxtarFixture(fixturePath, params, oldBytes, newBytes, batchLua), "write fixture")
					delete(manifest, name)
					manifestDirty = true
					t.Logf("updated %s (unverified)", fixturePath)
					expected = batchLua
				}
			}

			batchJSON := toJSON(t, batchLua)
			incJSON := toJSON(t, incLua)
			expectedJSON := toJSON(t, expected)

			// Check verification status
			currentHash := sha256Hex([]byte(formatExpected(expected)))
			verified := manifest[name] == currentHash

			if *verifyAll || verify.contains(name) {
				if batchJSON == expectedJSON && incJSON == expectedJSON {
					manifest[name] = currentHash
					manifestDirty = true
					verified = true
					t.Logf("verified %s", name)
				} else {
					t.Errorf("cannot verify %s: batch or incremental output does not match expected", name)
				}
			}

			fr := fixtureResult{
				Name:              name,
				OldText:           string(oldBytes),
				NewText:           string(newBytes),
				Params:            params,
				Expected:          expected,
				BatchActual:       batchLua,
				IncrementalActual: incLua,
				BatchPass:         batchJSON == expectedJSON,
				IncrementalPass:   incJSON == expectedJSON,
				MaxLinesResults:   mlResults,
				Verified:          verified,
			}
			fixtures = append(fixtures, fr)

			if !verified {
				t.Errorf("unverified: run with -verify after reviewing expected")
			}
			assert.Equal(t, expectedJSON, batchJSON, "batch output mismatch")
			assert.Equal(t, expectedJSON, incJSON, "incremental output mismatch")
		})
	}

	// Save manifest if changed
	if manifestDirty {
		if err := saveVerifiedManifest(manifestPath, manifest); err != nil {
			t.Logf("failed to save verified manifest: %v", err)
		} else {
			t.Logf("saved %s", manifestPath)
		}
	}

	// Generate HTML report
	reportPath := filepath.Join(e2eDir, "report.html")
	if err := generateReport(fixtures, reportPath); err != nil {
		t.Logf("failed to generate report: %v", err)
	} else {
		t.Logf("report: %s", reportPath)
	}
}

// copyStages deep-copies a slice of stages for apply simulation.
func copyStages(stages []*Stage) []*Stage {
	copies := make([]*Stage, len(stages))
	for i, s := range stages {
		cp := *s
		cp.Groups = make([]*Group, len(s.Groups))
		for j, g := range s.Groups {
			gCopy := *g
			cp.Groups[j] = &gCopy
		}
		copies[i] = &cp
	}
	return copies
}

// verifyApplyWithMaxLines runs apply and partial-accept verification for a given MaxLines value.
func verifyApplyWithMaxLines(t *testing.T, oldLines, newLines []string, oldText, newText string, params fixtureParams, maxLines int) maxLinesResult {
	t.Helper()

	diff := ComputeDiff(oldText, newText)
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          params.CursorRow,
		CursorCol:          params.CursorCol,
		ViewportTop:        params.ViewportTop,
		ViewportBottom:     params.ViewportBottom,
		BaseLineOffset:     1,
		ProximityThreshold: 10,
		MaxLines:           maxLines,
		NewLines:           newLines,
		OldLines:           oldLines,
		FilePath:           "test.txt",
	})

	mlr := maxLinesResult{
		MaxLines:          maxLines,
		ApplyPass:         true,
		PartialAcceptPass: true,
	}

	if result == nil || len(result.Stages) == 0 {
		return mlr
	}

	label := "default"
	if maxLines > 0 {
		label = fmt.Sprintf("maxLines=%d", maxLines)
	}

	// Apply verification
	{
		buf := &testBuffer{lines: append([]string{}, oldLines...)}
		stages := copyStages(result.Stages)
		for i := range stages {
			buf.applyStage(stages[i])
			advanceOffsets(stages, i)
		}
		mlr.ApplyLines = buf.lines
		if !slicesEqual(mlr.ApplyLines, newLines) {
			mlr.ApplyPass = false
			t.Errorf("apply result mismatch (%s, %d stages):\n  got:  %v\n  want: %v", label, len(result.Stages), mlr.ApplyLines, newLines)
		}
	}

	// Partial accept verification
	{
		buf := &testBuffer{lines: append([]string{}, oldLines...)}
		stages := copyStages(result.Stages)
		for i := range stages {
			buf.partialAcceptStage(stages[i])
			advanceOffsets(stages, i)
		}
		mlr.PartialAcceptLines = buf.lines
		if !slicesEqual(mlr.PartialAcceptLines, newLines) {
			mlr.PartialAcceptPass = false
			t.Errorf("partial accept result mismatch (%s, %d stages):\n  got:  %v\n  want: %v", label, len(result.Stages), mlr.PartialAcceptLines, newLines)
		}
	}

	return mlr
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	assert.NoError(t, err, "marshal json")
	return string(data)
}

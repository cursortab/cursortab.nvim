package text

import (
	"crypto/sha256"
	"cursortab/assert"
	"cursortab/e2e"
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
	AvailableWidth int
}

// parseTxtarFixture parses a txtar archive into its fixture components.
func parseTxtarFixture(ar *txtar.Archive) (params fixtureParams, oldBytes, newBytes []byte, expected []map[string]any, err error) {
	hdr := e2e.ParseHeader(ar.Comment)
	params.CursorRow, _ = strconv.Atoi(hdr["cursorRow"])
	params.CursorCol, _ = strconv.Atoi(hdr["cursorCol"])
	params.ViewportTop, _ = strconv.Atoi(hdr["viewportTop"])
	params.ViewportBottom, _ = strconv.Atoi(hdr["viewportBottom"])
	params.AvailableWidth, _ = strconv.Atoi(hdr["availableWidth"])

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
	if params.AvailableWidth > 0 {
		header += fmt.Sprintf("availableWidth: %d\n", params.AvailableWidth)
	}

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

// --- Shared helpers ---

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

// --- Verified manifest ---

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
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// --- Fixture loading ---

type fixture struct {
	Name     string
	Path     string
	Params   fixtureParams
	Old      []byte
	New      []byte
	OldLines []string
	NewLines []string
	Expected []map[string]any
}

func loadFixtures(t *testing.T, e2eDir string) []fixture {
	t.Helper()
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		t.Fatalf("failed to read e2e directory: %v", err)
	}

	var fixtures []fixture
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".txtar") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".txtar")
		fixturePath := filepath.Join(e2eDir, entry.Name())

		data, err := os.ReadFile(fixturePath)
		assert.NoError(t, err, "read fixture "+name)

		ar := txtar.Parse(data)
		params, oldBytes, newBytes, expected, err := parseTxtarFixture(ar)
		assert.NoError(t, err, "parse fixture "+name)

		oldLines := strings.Split(string(oldBytes), "\n")
		newLines := strings.Split(string(newBytes), "\n")

		fixtures = append(fixtures, fixture{
			Name: name, Path: fixturePath,
			Params: params, Old: oldBytes, New: newBytes,
			OldLines: oldLines, NewLines: newLines, Expected: expected,
		})
	}
	return fixtures
}

// --- Pipeline runners ---

func runBatchPipeline(oldLines, newLines []string, params fixtureParams) []map[string]any {
	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          params.CursorRow,
		CursorCol:          params.CursorCol,
		ViewportTop:        params.ViewportTop,
		ViewportBottom:     params.ViewportBottom,
		BaseLineOffset:     1,
		ProximityThreshold: 10,
		AvailableWidth:     params.AvailableWidth,
		NewLines:           newLines,
		OldLines:           oldLines,
		FilePath:           "test.txt",
	})

	var lua []map[string]any
	if result != nil {
		for _, stage := range result.Stages {
			lua = append(lua, ToLuaFormat(stage, stage.BufferStart))
		}
	}
	return lua
}

func runIncrementalPipeline(oldLines, newLines []string, params fixtureParams) []map[string]any {
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,
		10,
		0,
		params.ViewportTop, params.ViewportBottom,
		params.CursorRow, params.CursorCol,
		"test.txt",
		params.AvailableWidth,
	)
	for _, line := range newLines {
		builder.AddLine(line)
	}
	result := builder.Finalize()

	var lua []map[string]any
	if result != nil {
		for _, stage := range result.Stages {
			lua = append(lua, ToLuaFormat(stage, stage.BufferStart))
		}
	}
	return lua
}

// --- Tests ---

func TestBatchPipeline(t *testing.T) {
	for _, f := range loadFixtures(t, "testdata") {
		t.Run(f.Name, func(t *testing.T) {
			if f.Params.CursorRow < 1 || f.Params.CursorRow > len(f.OldLines) {
				t.Fatalf("cursorRow %d out of bounds (%d lines)", f.Params.CursorRow, len(f.OldLines))
			}

			actual := runBatchPipeline(f.OldLines, f.NewLines, f.Params)
			assert.Equal(t, toJSON(t, f.Expected), toJSON(t, actual), "batch output mismatch")
		})
	}
}

func TestIncrementalPipeline(t *testing.T) {
	for _, f := range loadFixtures(t, "testdata") {
		t.Run(f.Name, func(t *testing.T) {
			if f.Params.CursorRow < 1 || f.Params.CursorRow > len(f.OldLines) {
				t.Fatalf("cursorRow %d out of bounds (%d lines)", f.Params.CursorRow, len(f.OldLines))
			}

			actual := runIncrementalPipeline(f.OldLines, f.NewLines, f.Params)
			assert.Equal(t, toJSON(t, f.Expected), toJSON(t, actual), "incremental output mismatch")
		})
	}
}

func TestApplyWithMaxLines(t *testing.T) {
	maxLinesValues := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 1000}

	for _, f := range loadFixtures(t, "testdata") {
		t.Run(f.Name, func(t *testing.T) {
			if f.Params.CursorRow < 1 || f.Params.CursorRow > len(f.OldLines) {
				t.Fatalf("cursorRow %d out of bounds (%d lines)", f.Params.CursorRow, len(f.OldLines))
			}

			for _, maxLines := range maxLinesValues {
				verifyApplyWithMaxLines(t, f.OldLines, f.NewLines,
					JoinLines(f.OldLines), JoinLines(f.NewLines), f.Params, maxLines)
			}
		})
	}
}

func TestE2EUpdate(t *testing.T) {
	if !*updateAll && len(update) == 0 {
		t.Skip("no -update or -update-only flag")
	}

	manifestPath := filepath.Join("testdata", "verified.json")
	manifest := loadVerifiedManifest(manifestPath)
	manifestDirty := false

	for _, f := range loadFixtures(t, "testdata") {
		if !*updateAll && !update.contains(f.Name) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			actual := runBatchPipeline(f.OldLines, f.NewLines, f.Params)
			newDSL := formatExpected(actual)
			oldDSL := formatExpected(f.Expected)
			if newDSL == oldDSL {
				t.Logf("skipped %s (unchanged)", f.Path)
				return
			}
			assert.NoError(t, writeTxtarFixture(f.Path, f.Params, f.Old, f.New, actual), "write fixture")
			if _, ok := manifest[f.Name]; ok {
				delete(manifest, f.Name)
				manifestDirty = true
			}
			t.Logf("updated %s (unverified)", f.Path)
		})
	}

	if manifestDirty {
		assert.NoError(t, saveVerifiedManifest(manifestPath, manifest), "save manifest")
	}
}

func TestE2EVerify(t *testing.T) {
	manifestPath := filepath.Join("testdata", "verified.json")

	if *verifyAll || len(verify) > 0 {
		manifest := loadVerifiedManifest(manifestPath)
		for _, f := range loadFixtures(t, "testdata") {
			if !*verifyAll && !verify.contains(f.Name) {
				continue
			}
			t.Run(f.Name, func(t *testing.T) {
				batchLua := runBatchPipeline(f.OldLines, f.NewLines, f.Params)
				incLua := runIncrementalPipeline(f.OldLines, f.NewLines, f.Params)
				if toJSON(t, batchLua) != toJSON(t, f.Expected) || toJSON(t, incLua) != toJSON(t, f.Expected) {
					t.Errorf("cannot verify %s: output does not match expected", f.Name)
					return
				}
				hash := sha256Hex([]byte(formatExpected(f.Expected)))
				manifest[f.Name] = hash
				t.Logf("verified %s", f.Name)
			})
		}
		assert.NoError(t, saveVerifiedManifest(manifestPath, manifest), "save manifest")
		return
	}

	if *updateAll || len(update) > 0 {
		t.Skip("skipping verification check during update")
	}

	manifest := loadVerifiedManifest(manifestPath)
	for _, f := range loadFixtures(t, "testdata") {
		t.Run(f.Name, func(t *testing.T) {
			hash := sha256Hex([]byte(formatExpected(f.Expected)))
			if manifest[f.Name] != hash {
				t.Errorf("unverified: run 'just verify-e2e %s' after reviewing", f.Name)
			}
		})
	}
}

func TestE2EReport(t *testing.T) {
	e2eDir := "testdata"
	manifestPath := filepath.Join(e2eDir, "verified.json")
	manifest := loadVerifiedManifest(manifestPath)
	var fixtures []fixtureResult

	for _, f := range loadFixtures(t, e2eDir) {
		if f.Params.CursorRow < 1 || f.Params.CursorRow > len(f.OldLines) {
			continue
		}

		batchLua := runBatchPipeline(f.OldLines, f.NewLines, f.Params)
		incLua := runIncrementalPipeline(f.OldLines, f.NewLines, f.Params)

		batchJSON := toJSON(t, batchLua)
		incJSON := toJSON(t, incLua)
		expectedJSON := toJSON(t, f.Expected)

		hash := sha256Hex([]byte(formatExpected(f.Expected)))
		verified := manifest[f.Name] == hash

		maxLinesValues := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 1000}
		var mlResults []maxLinesResult
		for _, ml := range maxLinesValues {
			mlResults = append(mlResults, verifyApplyWithMaxLines(t,
				f.OldLines, f.NewLines,
				JoinLines(f.OldLines), JoinLines(f.NewLines),
				f.Params, ml))
		}

		fixtures = append(fixtures, fixtureResult{
			Name:              f.Name,
			OldText:           string(f.Old),
			NewText:           string(f.New),
			Params:            f.Params,
			Expected:          f.Expected,
			BatchActual:       batchLua,
			IncrementalActual: incLua,
			BatchPass:         batchJSON == expectedJSON,
			IncrementalPass:   incJSON == expectedJSON,
			MaxLinesResults:   mlResults,
			Verified:          verified,
		})
	}

	reportPath := filepath.Join(e2eDir, "report.html")
	if err := generateReport(fixtures, reportPath); err != nil {
		t.Logf("failed to generate report: %v", err)
	} else {
		t.Logf("report: %s", reportPath)
	}
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
		AvailableWidth:     params.AvailableWidth,
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

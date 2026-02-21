package text

import (
	"crypto/sha256"
	"cursortab/assert"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update expected.json golden files")
var verify = flag.Bool("verify", false, "mark current expected.json files as verified")

type fixtureParams struct {
	CursorRow      int `json:"cursorRow"`
	CursorCol      int `json:"cursorCol"`
	ViewportTop    int `json:"viewportTop"`
	ViewportBottom int `json:"viewportBottom"`
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
	Verified          bool
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
	e2eDir := filepath.Join("e2e")
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		t.Fatalf("failed to read e2e directory: %v", err)
	}

	manifestPath := filepath.Join(e2eDir, "verified.json")
	manifest := loadVerifiedManifest(manifestPath)

	var fixtures []fixtureResult

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(e2eDir, name)

		t.Run(name, func(t *testing.T) {
			oldBytes, err := os.ReadFile(filepath.Join(dir, "old.txt"))
			assert.NoError(t, err, "read old.txt")
			newBytes, err := os.ReadFile(filepath.Join(dir, "new.txt"))
			assert.NoError(t, err, "read new.txt")
			paramsBytes, err := os.ReadFile(filepath.Join(dir, "params.json"))
			assert.NoError(t, err, "read params.json")

			var params fixtureParams
			assert.NoError(t, json.Unmarshal(paramsBytes, &params), "parse params.json")

			oldLines := strings.Split(string(oldBytes), "\n")
			newLines := strings.Split(string(newBytes), "\n")

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

			// --- Update or compare ---
			expectedPath := filepath.Join(dir, "expected.json")

			if *update {
				data, err := json.MarshalIndent(batchLua, "", "  ")
				assert.NoError(t, err, "marshal expected")
				assert.NoError(t, os.WriteFile(expectedPath, append(data, '\n'), 0644), "write expected.json")
				t.Logf("updated %s (unverified)", expectedPath)
			}

			expectedBytes, err := os.ReadFile(expectedPath)
			assert.NoError(t, err, "read expected.json")

			var expected []map[string]any
			assert.NoError(t, json.Unmarshal(expectedBytes, &expected), "parse expected.json")

			// Check verification status
			currentHash := sha256Hex(expectedBytes)
			verified := manifest[name] == currentHash

			if *verify {
				manifest[name] = currentHash
				verified = true
				t.Logf("verified %s", name)
			}

			batchJSON := toJSON(t, batchLua)
			incJSON := toJSON(t, incLua)
			expectedJSON := toJSON(t, expected)

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
				Verified:          verified,
			}
			fixtures = append(fixtures, fr)

			if !verified {
				t.Errorf("unverified: run with -verify after reviewing expected.json")
			}
			assert.Equal(t, expectedJSON, batchJSON, "batch output mismatch")
			assert.Equal(t, expectedJSON, incJSON, "incremental output mismatch")
		})
	}

	// Save manifest if verifying
	if *verify {
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

func toJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	assert.NoError(t, err, "marshal json")
	return string(data)
}

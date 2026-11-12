package engine

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestReconcileRange(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}

	tests := []struct {
		name       string
		completion *types.Completion
		wantOld    []string
		wantNew    []string
	}{
		{
			name:       "replaces the declared range",
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{"B"}},
			wantOld:    []string{"b"},
			wantNew:    []string{"B"},
		},
		{
			name:       "extends over lines already applied",
			completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"A", "b", "C"}},
			wantOld:    []string{"a", "b"},
			wantNew:    []string{"A", "b", "C"},
		},
		{
			name:       "drops trailing lines that repeat the buffer",
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{"B", "x", "d"}},
			wantOld:    []string{"b"},
			wantNew:    []string{"B", "x"},
		},
		{
			name:       "pure insertion",
			completion: &types.Completion{StartLine: 2, EndLineInc: 1, Lines: []string{"new"}},
			wantOld:    []string{},
			wantNew:    []string{"new"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.completion.Lines...)
			oldLines, newLines := reconcileRange(lines, tt.completion)
			assert.Equal(t, tt.wantOld, oldLines, "old lines")
			assert.Equal(t, tt.wantNew, newLines, "new lines")
			assert.Equal(t, original, tt.completion.Lines, "completion unchanged")
		})
	}
}

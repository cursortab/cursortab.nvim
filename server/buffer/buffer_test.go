package buffer

import (
	"cursortab/assert"
	"cursortab/text"
	"testing"
)

func TestTextEditForCharGroupAppend(t *testing.T) {
	edit, ok := textEditForCharGroup(&text.Group{
		RenderHint: text.HintAppendChars,
		StartLine:  1,
		EndLine:    1,
		BufferLine: 3,
		OldLines:   []string{"hello"},
		Lines:      []string{"hello world"},
	})

	assert.True(t, ok, "append should use text edit")
	assert.Equal(t, 2, edit.row, "row")
	assert.Equal(t, 5, edit.startCol, "start col")
	assert.Equal(t, 5, edit.endCol, "end col")
	assert.Equal(t, " world", string(edit.replacement), "replacement")
}

func TestTextEditForCharGroupReplace(t *testing.T) {
	edit, ok := textEditForCharGroup(&text.Group{
		RenderHint: text.HintReplaceChars,
		StartLine:  1,
		EndLine:    1,
		BufferLine: 1,
		OldLines:   []string{"hello world"},
		Lines:      []string{"hello there"},
	})

	assert.True(t, ok, "replace should use text edit")
	assert.Equal(t, 0, edit.row, "row")
	assert.Equal(t, 6, edit.startCol, "start col")
	assert.Equal(t, 11, edit.endCol, "end col")
	assert.Equal(t, "there", string(edit.replacement), "replacement")
}

func TestTextEditForCharGroupDelete(t *testing.T) {
	edit, ok := textEditForCharGroup(&text.Group{
		RenderHint: text.HintDeleteChars,
		StartLine:  1,
		EndLine:    1,
		BufferLine: 1,
		OldLines:   []string{"hello world"},
		Lines:      []string{"hello"},
	})

	assert.True(t, ok, "delete should use text edit")
	assert.Equal(t, 0, edit.row, "row")
	assert.Equal(t, 5, edit.startCol, "start col")
	assert.Equal(t, 11, edit.endCol, "end col")
	assert.Equal(t, "", string(edit.replacement), "replacement")
}

func TestTextEditForCharGroupReplaceUsesUTF8Boundaries(t *testing.T) {
	edit, ok := textEditForCharGroup(&text.Group{
		RenderHint: text.HintReplaceChars,
		StartLine:  1,
		EndLine:    1,
		BufferLine: 1,
		OldLines:   []string{"Hello 🎉 world"},
		Lines:      []string{"Hello 🚀 world"},
	})

	assert.True(t, ok, "replace should use text edit")
	assert.Equal(t, len("Hello "), edit.startCol, "start col")
	assert.Equal(t, len("Hello 🎉"), edit.endCol, "end col")
	assert.Equal(t, "🚀", string(edit.replacement), "replacement")
}

func TestCharLevelTextEditsRejectsStructuralGroups(t *testing.T) {
	_, ok := charLevelTextEdits([]*text.Group{{
		Type:      text.GroupAddition,
		StartLine: 1,
		EndLine:   1,
		Lines:     []string{"new line"},
	}})

	assert.False(t, ok, "structural groups should use line replacement")
}

func TestRelativeToWorkspace(t *testing.T) {
	tests := []struct {
		name          string
		absolutePath  string
		workspacePath string
		want          string
	}{
		{
			name:          "file in workspace",
			absolutePath:  "/home/user/project/src/main.go",
			workspacePath: "/home/user/project",
			want:          "src/main.go",
		},
		{
			name:          "file outside workspace",
			absolutePath:  "/other/path/file.go",
			workspacePath: "/home/user/project",
			want:          "/other/path/file.go",
		},
		{
			name:          "file at workspace root",
			absolutePath:  "/home/user/project/main.go",
			workspacePath: "/home/user/project",
			want:          "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeToWorkspace(tt.absolutePath, tt.workspacePath)
			assert.Equal(t, tt.want, got, "relative path mismatch")
		})
	}
}

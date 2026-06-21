package ctx

import (
	"context"

	"cursortab/types"
)

type RequestKind string

const (
	RequestCompletion RequestKind = "completion"
	RequestPrefetch   RequestKind = "prefetch"
	RequestEval       RequestKind = "eval"
)

type ContextSourceID string

const (
	SourceDiagnostics ContextSourceID = "diagnostics"
	SourceTreesitter  ContextSourceID = "treesitter"
	SourceGitDiff     ContextSourceID = "git_diff"
	SourceRecentFiles ContextSourceID = "recent_files"
	SourceEditHistory ContextSourceID = "edit_history"
	SourceUserActions ContextSourceID = "user_actions"
)

type ContextMaterial interface {
	SourceID() ContextSourceID
	contextMaterial()
}

type ContextRequirements []ContextMaterial
type CollectedContext []ContextMaterial

func Find[T ContextMaterial](materials CollectedContext) (T, bool) {
	for _, material := range materials {
		if typed, ok := material.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

type collectableMaterial interface {
	ContextMaterial
	collect(context.Context, ContextSourceInput) (ContextMaterial, error)
}

type CompletionInput struct {
	Kind    RequestKind
	Trigger types.CompletionSource
	Current CurrentSnapshot
	Context CollectedContext
}

type CurrentSnapshot struct {
	Workspace WorkspaceRef
	File      FileSnapshot
	Cursor    CursorPosition
	View      ViewConstraints
}

type WorkspaceRef struct {
	Path string
	ID   string
}

type FileSnapshot struct {
	Path    string
	Lines   []string
	Version int
}

type CursorPosition struct {
	Row int
	Col int
}

type ViewConstraints struct {
	ViewportHeight  int
	MaxVisibleLines int
}

package ctx

import "context"

// Materials is a set of context material values. The concrete Go type is the
// material identity; zero-value materials are also collection requests.
type Materials []material

func Find[T material](materials Materials) (T, bool) {
	for _, material := range materials {
		if typed, ok := material.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

type material interface {
	collect(context.Context, ContextSourceInput) (material, error)
}

// CompletionInput is the provider-visible request shape: current editor state
// plus the materials the provider asked the collector to gather.
type CompletionInput struct {
	Current   CurrentSnapshot
	Materials Materials
}

type CurrentSnapshot struct {
	WorkspacePath  string
	File           FileSnapshot
	Cursor         CursorPosition
	ViewportHeight int
}

type FileSnapshot struct {
	Path  string
	Lines []string
}

type CursorPosition struct {
	// Row is 1-indexed.
	Row int
	// Col is a 0-indexed byte column.
	Col int
}

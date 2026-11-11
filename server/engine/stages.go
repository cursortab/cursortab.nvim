package engine

import (
	"slices"

	"cursortab/text"
)

// applyAllStages returns the buffer with every staged completion stage
// applied in order. Stages are deep-copied so the engine's state isn't
// mutated.
func applyAllStages(bufLines []string, stagedStages []*text.Stage) []string {
	stages := make([]*text.Stage, len(stagedStages))
	for i, s := range stagedStages {
		cp := *s
		cp.Groups = text.CopyGroups(s.Groups)
		if s.CursorTarget != nil {
			target := *s.CursorTarget
			cp.CursorTarget = &target
		}
		stages[i] = &cp
	}
	out := slices.Clone(bufLines)
	for i, stage := range stages {
		out = stage.Apply(out)
		text.ShiftAfter(stages[i+1:], stage)
	}
	return out
}

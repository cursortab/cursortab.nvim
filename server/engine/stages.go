package engine

import (
	"slices"

	"cursortab/text"
)

// stageIsPureInsertion reports whether a stage is a pure insertion (no
// replacement — only addition groups at a single buffer line).
func stageIsPureInsertion(stage *text.Stage) bool {
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

// applyStageToLines returns lines with the given stage applied. It mutates
// nothing: input slices are copied.
func applyStageToLines(lines []string, stage *text.Stage) []string {
	isPure := stageIsPureInsertion(stage)
	start := stage.BufferStart - 1
	if start < 0 {
		start = 0
	}

	if isPure {
		if start > len(lines) {
			start = len(lines)
		}
		out := make([]string, 0, len(lines)+len(stage.Lines))
		out = append(out, lines[:start]...)
		out = append(out, stage.Lines...)
		out = append(out, lines[start:]...)
		return out
	}

	end := stage.BufferEnd
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		start = end
	}
	out := make([]string, 0, len(lines)-end+start+len(stage.Lines))
	out = append(out, lines[:start]...)
	out = append(out, stage.Lines...)
	if end < len(lines) {
		out = append(out, lines[end:]...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// advanceStageOffsets adjusts later stages' buffer positions after an earlier
// stage has been applied, so they refer to the right lines in the mutated
// buffer.
func advanceStageOffsets(stages []*text.Stage, appliedIdx int) {
	stage := stages[appliedIdx]
	var oldLineCount int
	if stageIsPureInsertion(stage) {
		oldLineCount = 0
	} else {
		oldLineCount = stage.BufferEnd - stage.BufferStart + 1
	}
	offset := len(stage.Lines) - oldLineCount
	if offset == 0 {
		return
	}
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

// applyAllStages returns the buffer with every staged completion stage
// applied in order. Stages are deep-copied so the engine's state isn't
// mutated.
func applyAllStages(bufLines []string, stagedStages []*text.Stage) []string {
	if len(stagedStages) == 0 {
		return slices.Clone(bufLines)
	}
	stages := make([]*text.Stage, len(stagedStages))
	for i, s := range stagedStages {
		cp := *s
		cp.Groups = make([]*text.Group, len(s.Groups))
		for j, g := range s.Groups {
			gCopy := *g
			cp.Groups[j] = &gCopy
		}
		stages[i] = &cp
	}
	out := slices.Clone(bufLines)
	for i := range stages {
		out = applyStageToLines(out, stages[i])
		advanceStageOffsets(stages, i)
	}
	return out
}

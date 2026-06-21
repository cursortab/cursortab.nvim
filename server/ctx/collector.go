package ctx

import (
	"context"
	"fmt"
	"time"
)

// GatherTimeout is the maximum time allowed for all context sources to complete.
const GatherTimeout = 200 * time.Millisecond

// Collect executes the requested materials with one shared timeout.
func Collect(parent context.Context, input ContextSourceInput, requirements ContextRequirements) (CollectedContext, error) {
	if len(requirements) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(parent, GatherTimeout)
	defer cancel()

	seen := make(map[ContextSourceID]struct{}, len(requirements))
	collected := make(CollectedContext, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement == nil {
			return nil, fmt.Errorf("context source <nil>: requirement is not collectable")
		}
		sourceID := requirement.SourceID()
		if _, ok := seen[sourceID]; ok {
			return nil, fmt.Errorf("context source %s: duplicate requirement", sourceID)
		}
		seen[sourceID] = struct{}{}

		collectable, ok := requirement.(collectableMaterial)
		if !ok {
			return nil, fmt.Errorf("context source %s: requirement is not collectable", sourceID)
		}

		material, err := collectable.collect(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("context source %s: %w", sourceID, err)
		}
		if material == nil {
			return nil, fmt.Errorf("context source %s: returned nil material", sourceID)
		}
		if material.SourceID() != sourceID {
			return nil, fmt.Errorf("context source %s: returned material from %s", sourceID, material.SourceID())
		}
		collected = append(collected, material)
	}

	if len(collected) == 0 {
		return nil, nil
	}
	return collected, nil
}

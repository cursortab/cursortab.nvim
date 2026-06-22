package ctx

import (
	"context"
	"fmt"
	"time"
)

// CollectTimeout is the maximum time allowed for all context materials to complete.
const CollectTimeout = 200 * time.Millisecond

// Collect executes the requested materials with one shared timeout.
func Collect(parent context.Context, input ContextSourceInput, requirements Materials) (Materials, error) {
	if len(requirements) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(parent, CollectTimeout)
	defer cancel()

	collected := make(Materials, 0, len(requirements))
	for _, requirement := range requirements {
		material, err := requirement.collect(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("context material %T: %w", requirement, err)
		}
		collected = append(collected, material)
	}
	return collected, nil
}

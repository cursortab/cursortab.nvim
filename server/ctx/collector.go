package ctx

import (
	"context"
	"fmt"
	"time"
)

// cooperativeCollectTimeout is passed to material collectors that honor context
// cancellation.
const cooperativeCollectTimeout = 200 * time.Millisecond

// Collect executes the requested materials in order with one shared context.
func Collect(parent context.Context, input ContextSourceInput, requirements Materials) (Materials, error) {
	if len(requirements) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(parent, cooperativeCollectTimeout)
	defer cancel()

	collected := make(Materials, len(requirements))
	for i, requirement := range requirements {
		material, err := requirement.collect(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("context material %T: %w", requirement, err)
		}
		collected[i] = material
	}
	return collected, nil
}

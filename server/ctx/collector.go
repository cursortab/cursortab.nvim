package ctx

import (
	"context"
	"fmt"
	"sync"
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

	collected := make(Materials, len(requirements))
	errs := make([]error, len(requirements))

	var wg sync.WaitGroup
	wg.Add(len(requirements))
	for i, requirement := range requirements {
		i, requirement := i, requirement
		go func() {
			defer wg.Done()
			material, err := requirement.collect(ctx, input)
			if err != nil {
				errs[i] = fmt.Errorf("context material %T: %w", requirement, err)
				return
			}
			collected[i] = material
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return collected, nil
}

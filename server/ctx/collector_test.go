package ctx

import (
	"context"
	"errors"
	"testing"
	"time"

	"cursortab/assert"
)

type testMaterial struct {
	name      string
	collectFn func(context.Context, ContextSourceInput) (material, error)
}

func (m testMaterial) collect(ctx context.Context, input ContextSourceInput) (material, error) {
	if m.collectFn != nil {
		return m.collectFn(ctx, input)
	}
	return collectedTestMaterial{name: m.name}, nil
}

type collectedTestMaterial struct {
	name string
}

func (m collectedTestMaterial) collect(context.Context, ContextSourceInput) (material, error) {
	return m, nil
}

func TestCollectRunsMaterialsInOrder(t *testing.T) {
	var calls []string

	collectOne := func(name string) func(context.Context, ContextSourceInput) (material, error) {
		return func(context.Context, ContextSourceInput) (material, error) {
			calls = append(calls, name)
			return collectedTestMaterial{name: name}, nil
		}
	}

	materials, err := Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{name: "first", collectFn: collectOne("first")},
		testMaterial{name: "second", collectFn: collectOne("second")},
	})

	assert.NoError(t, err, "collect")
	assert.Equal(t, []string{"first", "second"}, calls, "call order")
	assert.Len(t, 2, materials, "materials")
	first, ok := materials[0].(collectedTestMaterial)
	assert.True(t, ok, "first material type")
	second, ok := materials[1].(collectedTestMaterial)
	assert.True(t, ok, "second material type")
	assert.Equal(t, "first", first.name, "first material order")
	assert.Equal(t, "second", second.name, "second material order")
}

func TestCollectWrapsMaterialError(t *testing.T) {
	sourceErr := errors.New("source failed")
	_, err := Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{
			name: "broken",
			collectFn: func(context.Context, ContextSourceInput) (material, error) {
				return nil, sourceErr
			},
		},
	})

	assert.Error(t, err, "collect error")
	assert.Contains(t, err.Error(), "ctx.testMaterial", "material type in error")
	assert.True(t, errors.Is(err, sourceErr), "wrapped source error")
}

func TestCollectPassesSharedTimeoutToCooperativeMaterial(t *testing.T) {
	start := time.Now()
	_, err := Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{
			name: "slow",
			collectFn: func(ctx context.Context, _ ContextSourceInput) (material, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	})

	assert.Error(t, err, "timeout")
	assert.Contains(t, err.Error(), context.DeadlineExceeded.Error(), "deadline error")
	assert.Less(t, int(time.Since(start)), int(time.Second), "timeout bounded")
}

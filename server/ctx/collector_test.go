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

func TestCollectRunsMaterialsConcurrentlyAndPreservesOrder(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})

	collectOne := func(name string) func(context.Context, ContextSourceInput) (material, error) {
		return func(context.Context, ContextSourceInput) (material, error) {
			started <- name
			<-release
			return collectedTestMaterial{name: name}, nil
		}
	}

	done := make(chan struct {
		materials Materials
		err       error
	}, 1)
	go func() {
		materials, err := Collect(context.Background(), ContextSourceInput{}, Materials{
			testMaterial{name: "first", collectFn: collectOne("first")},
			testMaterial{name: "second", collectFn: collectOne("second")},
		})
		done <- struct {
			materials Materials
			err       error
		}{materials: materials, err: err}
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(200 * time.Millisecond):
			t.Fatal("collect did not start all materials before release")
		}
	}
	assert.True(t, seen["first"], "first material started")
	assert.True(t, seen["second"], "second material started")

	close(release)

	var result struct {
		materials Materials
		err       error
	}
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("collect did not finish after release")
	}

	assert.NoError(t, result.err, "collect")
	assert.Len(t, 2, result.materials, "materials")
	first, ok := result.materials[0].(collectedTestMaterial)
	assert.True(t, ok, "first material type")
	second, ok := result.materials[1].(collectedTestMaterial)
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

func TestCollectRespectsSharedTimeout(t *testing.T) {
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

package ctx

import (
	"context"
	"errors"
	"testing"
	"time"

	"cursortab/assert"
)

const sourceFake ContextSourceID = "fake"

type fakeMaterial struct {
	id        ContextSourceID
	collectFn func(context.Context, ContextSourceInput) (ContextMaterial, error)
}

func (f fakeMaterial) SourceID() ContextSourceID { return f.id }
func (f fakeMaterial) contextMaterial()          {}

func (f fakeMaterial) collect(ctx context.Context, input ContextSourceInput) (ContextMaterial, error) {
	if f.collectFn == nil {
		return f, nil
	}
	return f.collectFn(ctx, input)
}

type inertMaterial struct {
	id ContextSourceID
}

func (i inertMaterial) SourceID() ContextSourceID { return i.id }
func (i inertMaterial) contextMaterial()          {}

func TestCollectRejectsDuplicateSourceID(t *testing.T) {
	_, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		Diagnostics{},
		Diagnostics{MaxItems: 1},
	})

	assert.Error(t, err, "duplicate source id")
	assert.Contains(t, err.Error(), string(SourceDiagnostics), "duplicate error source")
}

func TestCollectRejectsNonCollectableMaterial(t *testing.T) {
	_, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		inertMaterial{id: sourceFake},
	})

	assert.Error(t, err, "non-collectable material")
	assert.Contains(t, err.Error(), string(sourceFake), "non-collectable source")
}

func TestCollectUsesSharedTimeout(t *testing.T) {
	start := time.Now()
	_, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		fakeMaterial{
			id: sourceFake,
			collectFn: func(ctx context.Context, _ ContextSourceInput) (ContextMaterial, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	})

	assert.Error(t, err, "timeout")
	assert.Contains(t, err.Error(), string(sourceFake), "timeout source")
	assert.Contains(t, err.Error(), context.DeadlineExceeded.Error(), "deadline error")
	assert.Less(t, int(time.Since(start)), int(time.Second), "timeout bounded")
}

func TestCollectRespectsCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Collect(parent, ContextSourceInput{}, ContextRequirements{
		fakeMaterial{
			id: sourceFake,
			collectFn: func(ctx context.Context, _ ContextSourceInput) (ContextMaterial, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	})

	assert.Error(t, err, "cancellation")
	assert.Contains(t, err.Error(), string(sourceFake), "cancellation source")
	assert.Contains(t, err.Error(), context.Canceled.Error(), "canceled error")
}

func TestCollectWrapsSourceErrorWithSourceID(t *testing.T) {
	sourceErr := errors.New("source failed")
	_, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		fakeMaterial{
			id: sourceFake,
			collectFn: func(context.Context, ContextSourceInput) (ContextMaterial, error) {
				return nil, sourceErr
			},
		},
	})

	assert.Error(t, err, "source error")
	assert.Contains(t, err.Error(), string(sourceFake), "source error id")
	assert.True(t, errors.Is(err, sourceErr), "wrapped source error")
}

func TestCollectRejectsReturnedSourceIDMismatch(t *testing.T) {
	_, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		fakeMaterial{
			id: sourceFake,
			collectFn: func(context.Context, ContextSourceInput) (ContextMaterial, error) {
				return Diagnostics{}, nil
			},
		},
	})

	assert.Error(t, err, "source id mismatch")
	assert.Contains(t, err.Error(), string(sourceFake), "expected source")
	assert.Contains(t, err.Error(), string(SourceDiagnostics), "returned source")
}

func TestCollectReturnsCollectedContextSlice(t *testing.T) {
	collected, err := Collect(context.Background(), ContextSourceInput{}, ContextRequirements{
		Diagnostics{},
		Treesitter{},
	})

	assert.NoError(t, err, "collect")
	assert.Len(t, 2, collected, "collected context")
	assert.Equal(t, SourceDiagnostics, collected[0].SourceID(), "first material")
	assert.Equal(t, SourceTreesitter, collected[1].SourceID(), "second material")
}

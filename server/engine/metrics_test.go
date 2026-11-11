package engine

import (
	"context"
	"sync"
	"testing"

	"cursortab/assert"
	"cursortab/metrics"
)

type recordingSender struct {
	mu     sync.Mutex
	events []metrics.EventType
	errs   []error
}

func (r *recordingSender) SendMetric(ctx context.Context, event metrics.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event.Type)
	r.errs = append(r.errs, ctx.Err())
}

func TestQueuedMetricsAreDeliveredAfterClose(t *testing.T) {
	sender := &recordingSender{}
	eng := New(Deps{Provider: newMockProvider(), Buffer: newMockBuffer(), Clock: newMockClock(), Metrics: sender}, Config{})
	eng.currentMetrics = metrics.CompletionInfo{ID: "completion-1"}
	eng.sendMetric(metrics.EventAccepted)

	done := make(chan struct{})
	go func() {
		eng.metricsWorker()
		close(done)
	}()
	eng.Close()
	<-done

	assert.Equal(t, []metrics.EventType{metrics.EventAccepted}, sender.events, "queued event delivered")
	assert.Nil(t, sender.errs[0], "delivery context is not cancelled by Close")
}

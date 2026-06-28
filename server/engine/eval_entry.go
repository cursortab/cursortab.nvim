package engine

import (
	"context"
	"fmt"
	"time"

	"cursortab/logger"
	"cursortab/types"
)

// EvalRequestResult reports the outcome of a synchronous eval request.
type EvalRequestResult struct {
	// Shown is true when gating passed, a non-empty completion came back,
	// and staging produced at least one stage.
	Shown bool
	// Suppressed is true when a gating layer rejected the request before
	// it reached the provider.
	Suppressed bool
	// SuppressReason identifies which layer fired: "no-edits",
	// "disabled-scope", "mid-line", "single-deletion", "unknown".
	SuppressReason string
	// ProviderLatency is the wall-clock duration of the provider call.
	// Under replay this reflects the recorded duration (when a latency-aware
	// transport is used) or zero if bypassed.
	ProviderLatency time.Duration
	// StageCount is the number of stages in the staged completion (0 if none).
	StageCount int
	// StagedLines is the buffer contents with all stages applied. Useful for
	// quality scoring when the scenario doesn't accept the completion.
	StagedLines []string
	// CursorTargetLine is non-zero when the engine is displaying a cursor jump
	// instead of a full completion.
	CursorTargetLine int
}

// EvalRequestCompletion runs gating, provider request, and staging
// synchronously. It is the single entry point used by the eval harness — the
// production Engine.requestCompletion spawns a goroutine and routes through
// the event loop, which is not friendly to deterministic evaluation.
//
// The manualTrigger parameter bypasses call-before suppression, matching
// production manual trigger semantics.
func (e *Engine) EvalRequestCompletion(ctx context.Context, manualTrigger bool) (*EvalRequestResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return nil, fmt.Errorf("engine stopped")
	}

	result := &EvalRequestResult{}
	e.syncBuffer()

	if reason := e.suppressCompletionRequest(types.CompletionSourceTyping, manualTrigger); reason != "" {
		result.Suppressed = true
		result.SuppressReason = reason
		return result, nil
	}
	e.lastCompletionSource = types.CompletionSourceTyping

	input, compatible, err := e.prepareCompletionInput(ctx, completionInputOptions{})
	if err != nil {
		return result, fmt.Errorf("context: %w", err)
	}
	if !compatible {
		return result, nil
	}
	start := time.Now()
	resp, err := e.provider.Complete(ctx, input)
	result.ProviderLatency = time.Since(start)
	if err != nil {
		return result, fmt.Errorf("provider: %w", err)
	}

	if resp == nil || resp.Completion == nil {
		if resp != nil && resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
			result.CursorTargetLine = int(resp.CursorTarget.LineNumber)
		}
		return result, nil
	}

	shown := e.processCompletionWithManual(resp, manualTrigger) == completionShown
	result.Shown = shown
	if !shown {
		e.pendingMetricsInfo = nil
	}
	if e.stagedCompletion != nil {
		result.StageCount = len(e.stagedCompletion.Stages)
		result.StagedLines = e.applyAllStagesToBufferCopy()
	}
	logger.Debug("eval request done: shown=%v stages=%d latency=%s",
		shown, result.StageCount, result.ProviderLatency)
	return result, nil
}

// EvalAccept accepts the current staged completion synchronously. Unlike the
// interactive event loop, eval never speculatively prefetches or retriggers
// provider requests from an accept step — scenarios request the next
// completion explicitly. That keeps cassette replay deterministic and ensures
// per-step latency only reflects the requests the scenario asked for.
func (e *Engine) EvalAccept() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stagedCompletion == nil {
		return
	}
	if e.cursorTarget != nil {
		e.cursorTarget.ShouldRetrigger = false
	}
	for _, stage := range e.stagedCompletion.Stages {
		if stage != nil && stage.CursorTarget != nil {
			stage.CursorTarget.ShouldRetrigger = false
		}
	}
	e.acceptCompletion()
}

// EvalReject rejects the current completion synchronously.
func (e *Engine) EvalReject() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rejectAndRemember()
}

// applyAllStagesToBufferCopy returns the buffer with all staged completion
// stages applied (deep-copied so engine state isn't mutated).
func (e *Engine) applyAllStagesToBufferCopy() []string {
	if e.stagedCompletion == nil {
		return e.buffer.Lines()
	}
	return applyAllStages(e.buffer.Lines(), e.stagedCompletion.Stages)
}

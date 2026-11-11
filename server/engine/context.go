package engine

import (
	"cursortab/logger"
	"cursortab/metrics"
)

// syncBuffer refreshes the editor snapshot. Switching to another buffer drops
// all completion work for the previous one and opens the new file's history.
// When the same buffer got a new path (:cd, :file, :saveas), its history
// moves along. It reports whether the buffer switched.
func (e *Engine) syncBuffer() bool {
	oldPath := e.buffer.Path()
	switched, err := e.buffer.Sync()
	if err != nil {
		logger.Debug("sync error: %v", err)
		return false
	}
	if switched {
		e.handleFileSwitch()
		return true
	}
	e.history.Rename(oldPath, e.buffer.Path())
	e.history.Observe(e.buffer.Path(), e.buffer.Lines())
	return false
}

// handleFileSwitch drops any in-flight or visible completion work: it refers
// to the old file's lines and would render against the wrong rows. Late
// responses for the cancelled requests are discarded by the request ID and
// state guards in events.go.
func (e *Engine) handleFileSwitch() {
	e.sendMetric(metrics.EventIgnored)
	e.cancelPrefetch()
	e.cancelInFlight()
	e.cursorTarget = nil
	e.stagedCompletion = nil
	e.resetCompletionFields()
	e.state = stateIdle
	e.lastBufferSize = -1
	e.buffer.ClearUI()
	e.history.Open(e.buffer.Path(), e.buffer.Lines(), e.clock.Now().UnixNano())
}

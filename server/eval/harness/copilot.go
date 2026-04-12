package harness

import (
	"sync"
	"sync/atomic"

	"cursortab/buffer"
	"cursortab/eval/cassette"
)

// latencyTracker is an interface for objects that can report cumulative
// latency from cassette replay.
type latencyTracker interface {
	TotalDurationMs() int64
}

// cassetteCopilotLSP replays a sequence of Copilot LSP exchanges against a
// cassette. Each exchange: the provider calls SendCopilotNESRequest, we look
// up the next interaction's recorded response, and fire the registered
// handler with the recorded edits JSON after a short synthetic delay that
// matches the recorded duration.
//
// This implements copilot.LSPBuffer so the same copilot.Provider that runs
// in production Neovim can run against recordings in the eval harness.
type cassetteCopilotLSP struct {
	mu       sync.Mutex
	cassette *cassette.Cassette
	idx      int
	handler  func(reqID int64, editsJSON string, errMsg string)
	clientID atomic.Int64
	totalMs  int64
}

func newCassetteCopilotLSP(cs *cassette.Cassette) *cassetteCopilotLSP {
	c := &cassetteCopilotLSP{cassette: cs}
	c.clientID.Store(1)
	return c
}

// GetCopilotClient implements copilot.LSPBuffer. The harness always reports
// a single stable client — there's no real LSP attached, and the provider
// uses the id only to detect reconnection.
func (c *cassetteCopilotLSP) GetCopilotClient() (*buffer.CopilotClientInfo, error) {
	return &buffer.CopilotClientInfo{ID: int(c.clientID.Load())}, nil
}

// SendCopilotDidFocus implements copilot.LSPBuffer — no-op.
func (c *cassetteCopilotLSP) SendCopilotDidFocus(uri string) error { return nil }

// SendCopilotNESRequest implements copilot.LSPBuffer. Instead of dispatching
// over Neovim RPC, it looks up the next recorded interaction and fires the
// registered handler with the recorded edits synchronously in a goroutine.
func (c *cassetteCopilotLSP) SendCopilotNESRequest(reqID int64, uri string) error {
	c.mu.Lock()
	handler := c.handler
	if c.cassette == nil || c.idx >= len(c.cassette.Interactions) {
		c.mu.Unlock()
		// Cassette exhausted — fire the handler with empty edits so the
		// provider's GetCompletion receives a response and doesn't deadlock
		// on the pendingResult channel.
		go func() {
			if handler != nil {
				handler(reqID, "[]", "")
			}
		}()
		return nil
	}
	it := c.cassette.Interactions[c.idx]
	c.idx++
	c.totalMs += it.DurationMs
	c.mu.Unlock()

	body, err := cassette.DecodeBody(it.Response.BodyB64)
	if err != nil {
		go func() {
			if handler != nil {
				handler(reqID, "[]", err.Error())
			}
		}()
		return nil
	}

	go func() {
		if handler != nil {
			handler(reqID, string(body), "")
		}
	}()
	return nil
}

// TotalDurationMs returns the sum of recorded durations for the interactions
// consumed so far.
func (c *cassetteCopilotLSP) TotalDurationMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalMs
}

// Used returns the number of interactions replayed so far.
func (c *cassetteCopilotLSP) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idx
}

// RegisterCopilotHandler implements copilot.LSPBuffer.
func (c *cassetteCopilotLSP) RegisterCopilotHandler(handler func(reqID int64, editsJSON string, errMsg string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = handler
	return nil
}

package cassette

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// Replayer is an http.RoundTripper that returns pre-recorded responses from a
// cassette, in the order they were captured. Requests beyond the recorded
// count fail loudly rather than falling back to the network — the whole point
// is that evaluation never touches real APIs.
type Replayer struct {
	cassette *Cassette
	mu       sync.Mutex
	idx      int
	totalMs  int64
}

// NewReplayer wraps a cassette.
func NewReplayer(c *Cassette) *Replayer {
	return &Replayer{cassette: c}
}

// RoundTrip implements http.RoundTripper.
func (r *Replayer) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.idx >= len(r.cassette.Interactions) {
		return nil, fmt.Errorf("cassette %q exhausted: request #%d but only %d interaction(s) recorded",
			r.cassette.Meta.Provider, r.idx+1, len(r.cassette.Interactions))
	}

	it := r.cassette.Interactions[r.idx]
	r.idx++
	r.totalMs += it.DurationMs

	// Consume request body so the provider code path runs as it would live.
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}

	body, err := DecodeBody(it.Response.BodyB64)
	if err != nil {
		return nil, fmt.Errorf("cassette: decode response body: %w", err)
	}

	header := http.Header{}
	for k, v := range it.Response.Headers {
		header.Set(k, v)
	}
	if header.Get("Content-Length") == "" {
		header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}

	return &http.Response{
		Status:        http.StatusText(it.Response.Status),
		StatusCode:    it.Response.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

// Used returns the number of interactions replayed so far.
func (r *Replayer) Used() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx
}

// TotalDurationMs returns the sum of recorded durations for the interactions
// consumed so far.
func (r *Replayer) TotalDurationMs() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalMs
}

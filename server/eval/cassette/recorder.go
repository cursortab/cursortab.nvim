package cassette

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"
)

// Redacted headers are replaced with this placeholder on record.
const redactedMarker = "<REDACTED>"

// DefaultAllowedHeaders is an allowlist of headers safe to persist in a
// committed cassette. Everything else is either redacted (if present in
// DefaultSensitiveHeaders) or dropped entirely. Whitelist-by-default is the
// conservative choice: a new auth header introduced upstream won't leak
// into git because it'll be silently dropped rather than accidentally
// captured by a denylist that doesn't know about it yet.
var DefaultAllowedHeaders = map[string]bool{
	"Content-Type":     true,
	"Content-Length":   true,
	"Content-Encoding": true,
	"Accept":           true,
	"Connection":       true,
}

// DefaultSensitiveHeaders are still surfaced in cassettes (replaced with
// <REDACTED>) so readers can see the server expected them. Dropping them
// entirely would hide protocol-relevant info; leaking values would hide
// credentials. Replacement is the middle ground.
var DefaultSensitiveHeaders = map[string]bool{
	"Authorization":   true,
	"Cookie":          true,
	"Set-Cookie":      true,
	"Proxy-Authorize": true,
	"X-Api-Key":       true,
}

// Recorder is an http.RoundTripper that captures all interactions passing
// through the wrapped transport. Call Cassette() to retrieve the recording.
type Recorder struct {
	Inner            http.RoundTripper
	AllowedHeaders   map[string]bool
	SensitiveHeaders map[string]bool
	RecordHeaders    bool // when false, request/response headers are not stored at all
	mu               sync.Mutex
	interactions     []Interaction
}

// NewRecorder wraps inner (use http.DefaultTransport if nil).
func NewRecorder(inner http.RoundTripper) *Recorder {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &Recorder{
		Inner:            inner,
		AllowedHeaders:   DefaultAllowedHeaders,
		SensitiveHeaders: DefaultSensitiveHeaders,
	}
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	resp, err := r.Inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	var respBody []byte
	if resp.Body != nil {
		b, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		respBody = b
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}

	rec := Interaction{
		Request: RecordedRequest{
			Method:  req.Method,
			URL:     redactedMarker,
			Headers: r.snapshotHeaders(req.Header),
			BodyB64: EncodeBody(reqBody),
		},
		Response: RecordedResponse{
			Status:  resp.StatusCode,
			Headers: r.snapshotHeaders(resp.Header),
			BodyB64: EncodeBody(respBody),
		},
		DurationMs: time.Since(start).Milliseconds(),
	}
	rec.Response.DurationMs = rec.DurationMs

	r.mu.Lock()
	r.interactions = append(r.interactions, rec)
	r.mu.Unlock()

	return resp, nil
}

// Cassette returns a snapshot of everything recorded so far, tagged with the
// given provider and model version.
func (r *Recorder) Cassette(provider, modelVersion string) *Cassette {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := New(provider, modelVersion)
	c.Interactions = append(c.Interactions, r.interactions...)
	return c
}

// Reset drops all recorded interactions.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.interactions = nil
	r.mu.Unlock()
}

// snapshotHeaders applies a whitelist-first policy: only headers in
// AllowedHeaders are captured verbatim; SensitiveHeaders are replaced with
// <REDACTED>; anything else is dropped. This keeps committed cassettes
// robust against accidental future auth header leaks.
func (r *Recorder) snapshotHeaders(h http.Header) map[string]string {
	if !r.RecordHeaders {
		return nil
	}
	if len(h) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, vs := range h {
		if len(vs) == 0 {
			continue
		}
		switch {
		case r.SensitiveHeaders[k]:
			out[k] = redactedMarker
		case r.AllowedHeaders[k]:
			out[k] = vs[0]
		}
		// Everything else is intentionally dropped.
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

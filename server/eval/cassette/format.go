// Package cassette implements HTTP record/replay for deterministic provider
// evaluation.
//
// A cassette is an ordered sequence of HTTP request/response pairs recorded
// from a real provider API. Cassettes are stored as NDJSON (one JSON object
// per line) so they diff cleanly in git and are easy to inspect by hand.
//
// Usage:
//
//	// Record:
//	rec := cassette.NewRecorder(http.DefaultTransport)
//	client.HTTPClient.Transport = rec
//	// ... make requests ...
//	c := rec.Cassette("mercuryapi", "mercury-edit")
//	_ = c.Save("path/to/cassette.ndjson")
//
//	// Replay:
//	c, _ := cassette.Load("path/to/cassette.ndjson")
//	client.HTTPClient.Transport = cassette.NewReplayer(c)
package cassette

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// SchemaVersion identifies the on-disk cassette format. Bump when the layout
// changes in a way that breaks parsers.
const SchemaVersion = 1

// Meta is the first line of a cassette file.
type Meta struct {
	Kind          string    `json:"kind"` // always "meta"
	SchemaVersion int       `json:"schema_version"`
	Provider      string    `json:"provider"`
	ModelVersion  string    `json:"model_version"`
	RecordedAt    time.Time `json:"recorded_at"`
	Notes         string    `json:"notes,omitempty"`
}

// Interaction captures one HTTP request and its response.
type Interaction struct {
	Request    RecordedRequest  `json:"-"`
	Response   RecordedResponse `json:"-"`
	DurationMs int64            `json:"-"`
}

// RecordedRequest captures the outbound request.
type RecordedRequest struct {
	Kind        string            `json:"kind"` // "request"
	Interaction int               `json:"interaction"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyB64     string            `json:"body_b64,omitempty"`
}

// RecordedResponse captures the response that came back.
type RecordedResponse struct {
	Kind        string            `json:"kind"` // "response"
	Interaction int               `json:"interaction"`
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyB64     string            `json:"body_b64,omitempty"`
	DurationMs  int64             `json:"duration_ms"`
}

// Cassette is an ordered sequence of interactions plus metadata.
type Cassette struct {
	Meta         Meta
	Interactions []Interaction
}

// TotalDurationMs returns the sum of recorded durations across all
// interactions.
func (c *Cassette) TotalDurationMs() int64 {
	var total int64
	for _, it := range c.Interactions {
		total += it.DurationMs
	}
	return total
}

// New returns an empty cassette with metadata filled in.
func New(provider, modelVersion string) *Cassette {
	return &Cassette{
		Meta: Meta{
			Kind:          "meta",
			SchemaVersion: SchemaVersion,
			Provider:      provider,
			ModelVersion:  modelVersion,
			RecordedAt:    time.Now().UTC(),
		},
	}
}

// Parse reads NDJSON from r into a Cassette.
func Parse(r io.Reader) (*Cassette, error) {
	c := &Cassette{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024) // up to 16MB per line
	seenMeta := false
	pending := make(map[int]*Interaction)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("cassette: invalid line: %w", err)
		}
		switch probe.Kind {
		case "meta":
			if seenMeta {
				return nil, fmt.Errorf("cassette: duplicate meta line")
			}
			if err := json.Unmarshal(line, &c.Meta); err != nil {
				return nil, fmt.Errorf("cassette: invalid meta: %w", err)
			}
			seenMeta = true
		case "request":
			var req RecordedRequest
			if err := json.Unmarshal(line, &req); err != nil {
				return nil, fmt.Errorf("cassette: invalid request: %w", err)
			}
			it := pending[req.Interaction]
			if it == nil {
				it = &Interaction{}
				pending[req.Interaction] = it
			}
			it.Request = req
		case "response":
			var resp RecordedResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				return nil, fmt.Errorf("cassette: invalid response: %w", err)
			}
			it := pending[resp.Interaction]
			if it == nil {
				it = &Interaction{}
				pending[resp.Interaction] = it
			}
			it.Response = resp
			it.DurationMs = resp.DurationMs
		default:
			return nil, fmt.Errorf("cassette: unknown kind %q", probe.Kind)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cassette: read error: %w", err)
	}
	if !seenMeta {
		return nil, fmt.Errorf("cassette: missing meta line")
	}

	ids := make([]int, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		it := pending[id]
		if it.Request.Kind == "" {
			return nil, fmt.Errorf("cassette: interaction %d missing request", id)
		}
		if it.Response.Kind == "" {
			return nil, fmt.Errorf("cassette: interaction %d missing response", id)
		}
		c.Interactions = append(c.Interactions, *it)
	}
	return c, nil
}

// Save writes the cassette to path as NDJSON.
func (c *Cassette) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create cassette: %w", err)
	}
	defer f.Close()
	return c.Write(f)
}

// Write serializes the cassette as NDJSON to w.
func (c *Cassette) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	meta := c.Meta
	meta.Kind = "meta"
	if meta.SchemaVersion == 0 {
		meta.SchemaVersion = SchemaVersion
	}
	if err := enc.Encode(meta); err != nil {
		return err
	}
	for i := range c.Interactions {
		req := c.Interactions[i].Request
		req.Kind = "request"
		req.Interaction = i
		if err := enc.Encode(req); err != nil {
			return err
		}
		resp := c.Interactions[i].Response
		resp.Kind = "response"
		resp.Interaction = i
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return nil
}

// EncodeBody base64-encodes a body for storage.
func EncodeBody(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeBody returns the raw bytes of a stored body.
func DecodeBody(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

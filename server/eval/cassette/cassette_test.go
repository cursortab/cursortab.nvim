package cassette

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursortab/assert"
)

func TestRecordThenReplay(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		payload, _ := json.Marshal(map[string]string{"echo": string(body)})
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	rec := NewRecorder(http.DefaultTransport)
	rec.RecordHeaders = true
	client := &http.Client{Transport: rec}

	req, _ := http.NewRequest("POST", upstream.URL+"/foo", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := client.Do(req)
	assert.NoError(t, err, "record roundtrip")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode, "status")
	if !strings.Contains(string(body), "hello") {
		t.Fatalf("expected echo body, got %q", body)
	}

	cs := rec.Cassette("test-provider", "v1")
	assert.Equal(t, 1, len(cs.Interactions), "interaction count")
	if got := cs.Interactions[0].Request.Headers["Authorization"]; got != redactedMarker {
		t.Errorf("authorization not redacted: got %q", got)
	}

	var buf bytes.Buffer
	err = cs.Write(&buf)
	assert.NoError(t, err, "write cassette")

	parsed, err := Parse(&buf)
	assert.NoError(t, err, "parse cassette")
	assert.Equal(t, 1, len(parsed.Interactions), "parsed interaction count")
	assert.Equal(t, "test-provider", parsed.Meta.Provider, "meta provider")

	replayer := NewReplayer(parsed)
	replayClient := &http.Client{Transport: replayer}
	req2, _ := http.NewRequest("POST", upstream.URL+"/foo", strings.NewReader(`{"hello":"world"}`))
	resp2, err := replayClient.Do(req2)
	assert.NoError(t, err, "replay roundtrip")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	assert.Equal(t, 200, resp2.StatusCode, "replay status")
	if string(body2) != string(body) {
		t.Errorf("replay body mismatch:\n got: %q\n want: %q", body2, body)
	}
}

func TestReplayerExhausted(t *testing.T) {
	cs := New("p", "m")
	replayer := NewReplayer(cs)
	client := &http.Client{Transport: replayer}

	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error for exhausted cassette")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("expected 'exhausted' in error, got %v", err)
	}
}

func TestParseRejectsMissingMeta(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"kind":"request","interaction":0,"method":"GET","url":"x"}`))
	if err == nil {
		t.Fatal("expected error for missing meta")
	}
}

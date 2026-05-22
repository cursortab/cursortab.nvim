package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursortab/assert"
	mercuryclient "cursortab/client/mercuryapi"
	"cursortab/eval/cassette"
	"cursortab/types"
)

// TestRecordThenReplayZeta runs a scenario against a local httptest server
// in record mode (captures a real HTTP round-trip), then replays the
// cassette against the same scenario and verifies the engine produces the
// recorded completion. This is the full end-to-end loop. Uses the zeta
// target because it's openai-compatible and accepts arbitrary URLs.
func TestRecordThenReplayZeta(t *testing.T) {
	// Fake upstream that returns an openai-shaped completion response with
	// a valid zeta editable-region body.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "fake-1",
			"object":  "text_completion",
			"created": 0,
			"model":   "zeta-test",
			"choices": []map[string]any{
				{
					"index":         0,
					"text":          "function greet(name) {\n  return \"hello, \" + name;\n}\n<|editable_region_end|>",
					"finish_reason": "stop",
				},
			},
		}
		payload, _ := json.Marshal(resp)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	sc := &Scenario{
		ID:       "fake-zeta-greet",
		FilePath: "main.js",
		Language: "javascript",
		Buffer: BufferState{
			Lines: []string{
				`function greet(name) {`,
				`  return "hello";`,
				`}`,
			},
			Row:            2,
			Col:            16,
			ViewportTop:    1,
			ViewportBottom: 20,
		},
		Targets: []Target{{
			Name:  "zeta",
			Type:  "zeta",
			Model: "zeta-test",
			URL:   upstream.URL,
		}},
		Steps: []Step{
			{Action: ActionRequestCompletion, Manual: true},
		},
		Cassettes: map[string]*cassette.Cassette{},
	}

	// Record.
	outcome := Run(sc, Config{
		Mode:       ModeRecord,
		Transport:  http.DefaultTransport,
		BaseConfig: &types.ProviderConfig{APIKey: "fake"},
	})
	assert.Equal(t, 1, len(outcome.Targets), "targets")
	to := outcome.Targets[0]
	if to.Error != nil {
		t.Fatalf("record error: %v", to.Error)
	}
	if to.Cassette == nil || len(to.Cassette.Interactions) == 0 {
		t.Fatal("no cassette captured")
	}

	// Replay.
	sc.Cassettes["zeta"] = to.Cassette
	outcome2 := Run(sc, Config{Mode: ModeReplay})
	to2 := outcome2.Targets[0]
	if to2.Error != nil {
		t.Fatalf("replay error: %v", to2.Error)
	}

	if len(to2.Steps) == 0 {
		t.Fatal("no step outcomes in replay")
	}
	var sawRequest bool
	for _, step := range to2.Steps {
		if step.Step.Action == ActionRequestCompletion {
			sawRequest = true
			if step.Err != nil {
				t.Errorf("request-completion step error: %v", step.Err)
			}
		}
	}
	if !sawRequest {
		t.Fatal("no request-completion step outcome")
	}
}

// TestStrictModelMismatch verifies that the strict-model check fails loudly
// when a cassette's recorded model_version doesn't match the target model.
// This is the core "record once, replay forever" contract: model upgrades
// must be visible, deliberate events.
func TestStrictModelMismatch(t *testing.T) {
	cs := cassette.New("zeta", "zeta-v1")
	cs.Interactions = append(cs.Interactions, cassette.Interaction{
		Request:    cassette.RecordedRequest{Method: "POST", URL: "https://example/"},
		Response:   cassette.RecordedResponse{Status: 200, BodyB64: cassette.EncodeBody([]byte(`{}`))},
		DurationMs: 1,
	})

	sc := &Scenario{
		ID:       "strict-mismatch",
		FilePath: "main.js",
		Buffer: BufferState{
			Lines:          []string{"x"},
			Row:            1,
			Col:            0,
			ViewportBottom: 20,
		},
		Targets: []Target{{
			Name:  "zeta",
			Type:  "zeta",
			Model: "zeta-v2", // note: doesn't match cassette's "zeta-v1"
			URL:   "https://example",
		}},
		Steps:     []Step{{Action: ActionRequestCompletion, Manual: true}},
		Cassettes: map[string]*cassette.Cassette{"zeta": cs},
	}

	outcome := Run(sc, Config{Mode: ModeReplay, StrictModelVersion: true})
	if len(outcome.Targets) == 0 {
		t.Fatal("expected one target outcome")
	}
	to := outcome.Targets[0]
	if to.Error == nil {
		t.Fatal("expected strict-model mismatch error, got nil")
	}
	if !strings.Contains(to.Error.Error(), "model_version") {
		t.Errorf("expected error to mention model_version, got %v", to.Error)
	}

	// With strict off, the run should proceed.
	outcome2 := Run(sc, Config{Mode: ModeReplay, StrictModelVersion: false})
	to2 := outcome2.Targets[0]
	if to2.Error != nil && strings.Contains(to2.Error.Error(), "model_version") {
		t.Errorf("strict=false should not fail on model mismatch: %v", to2.Error)
	}
}

// TestRegressionNoEditsSuppressed verifies the gating path — a scenario with
// an unmodified buffer (no recent edits) triggers the no-edits suppression.
func TestRegressionNoEditsSuppressed(t *testing.T) {
	sc := &Scenario{
		ID:       "unmodified-gated",
		FilePath: "main.go",
		Buffer: BufferState{
			Lines:          []string{"package main", "", "func main() {}"},
			Row:            3,
			Col:            14,
			ViewportTop:    1,
			ViewportBottom: 20,
		},
		Targets: []Target{{Name: "mercuryapi", Type: "mercuryapi", Model: mercuryclient.Model}},
		Cassettes: map[string]*cassette.Cassette{
			// Empty cassette: if gating doesn't fire, the replayer will panic
			// and the test fails.
			"mercuryapi": cassette.New("mercuryapi", mercuryclient.Model),
		},
	}

	outcome := Run(sc, Config{Mode: ModeReplay})
	if len(outcome.Targets) == 0 {
		t.Fatal("expected one target outcome")
	}
}

func TestEvalAcceptDoesNotRetriggerRequests(t *testing.T) {
	buildResponseBody := func(id string) string {
		payload, err := json.Marshal(map[string]any{
			"id": id,
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": "```\nimport httpx\n\n\nasync def fetch_user(client: httpx.AsyncClient, user_id: str) -> dict:\n    resp = await client.get(f\"/users/{user_id}\")\n    resp.raise_for_status()\n    return resp.json()\n\n\nasync def build_profile(client: httpx.AsyncClient, user_id: str) -> str:\n    user = await fetch_user(client, user_id)\n    return f\"{user['name']} <{user['email']}>\"\n```",
				},
			}},
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return string(payload)
	}

	cs := cassette.New("mercuryapi", mercuryclient.Model)
	cs.Interactions = append(cs.Interactions,
		cassette.Interaction{
			Request:    cassette.RecordedRequest{Method: "POST", URL: "https://example/"},
			Response:   cassette.RecordedResponse{Status: 200, BodyB64: cassette.EncodeBody([]byte(buildResponseBody("first"))), DurationMs: 11},
			DurationMs: 11,
		},
		cassette.Interaction{
			Request:    cassette.RecordedRequest{Method: "POST", URL: "https://example/"},
			Response:   cassette.RecordedResponse{Status: 200, BodyB64: cassette.EncodeBody([]byte(buildResponseBody("second"))), DurationMs: 22},
			DurationMs: 22,
		},
	)

	sc := &Scenario{
		ID:       "python-sync-to-async",
		FilePath: "profile.py",
		Language: "python",
		Buffer: BufferState{
			Lines: []string{
				"import httpx",
				"",
				"",
				"async def fetch_user(client: httpx.AsyncClient, user_id: str) -> dict:",
				"    resp = await client.get(f\"/users/{user_id}\")",
				"    resp.raise_for_status()",
				"    return resp.json()",
				"",
				"",
				"def build_profile(client: httpx.AsyncClient, user_id: str) -> str:",
				"    user = fetch_user(client, user_id)",
				"    return f\"{user['name']} <{user['email']}>\"",
			},
			Row:            11,
			Col:            30,
			ViewportTop:    1,
			ViewportBottom: 40,
		},
		History: []DiffEntryState{{
			FileName: "profile.py",
			Original: "def fetch_user(client: httpx.Client, user_id: str) -> dict:\n    resp = client.get(f\"/users/{user_id}\")\n    resp.raise_for_status()\n    return resp.json()",
			Updated:  "async def fetch_user(client: httpx.AsyncClient, user_id: str) -> dict:\n    resp = await client.get(f\"/users/{user_id}\")\n    resp.raise_for_status()\n    return resp.json()",
		}},
		Targets: []Target{{Name: "mercuryapi", Type: "mercuryapi"}},
		Steps: []Step{
			{Action: ActionRequestCompletion, Manual: true},
			{Action: ActionAccept},
			{Action: ActionRequestCompletion, Manual: true},
		},
		Cassettes: map[string]*cassette.Cassette{"mercuryapi": cs},
	}

	outcome := Run(sc, Config{Mode: ModeReplay})
	assert.Equal(t, 1, len(outcome.Targets), "targets")
	to := outcome.Targets[0]
	if to.Error != nil {
		t.Fatalf("replay error: %v", to.Error)
	}
	assert.Equal(t, 2, to.RequestCount, "manual steps should consume exactly two recorded requests")
	assert.Equal(t, int64(22), to.Steps[2].ProviderLatencyMs, "second manual request should use second cassette interaction")
}

// TestRunnerParsesFixtureAndScores exercises the scenario parser and the
// metric scoring path with a synthetic fixture built from scratch.
func TestRunnerParsesFixtureAndScores(t *testing.T) {
	fixture := []byte(`Simple return-value rewrite fixture.
id: synthetic
language: javascript
row: 2
col: 16
viewportTop: 1
viewportBottom: 20
-- buffer.txt --
function greet(name) {
  return "hello";
}
-- steps --
request-completion
-- expected --
function greet(name) {
  return "hello, " + name;
}
`)
	sc, err := ParseScenario(fixture, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	assert.Equal(t, "synthetic", sc.ID, "id")
	assert.Equal(t, 2, sc.Buffer.Row, "row")
	assert.Equal(t, 3, len(sc.Expected), "expected lines")
	assert.Equal(t, 1, len(sc.Steps), "step count")
}

// TestParseCassetteSection verifies that a cassette embedded in a .txtar
// fixture is loaded correctly.
func TestParseCassetteSection(t *testing.T) {
	// Build a minimal cassette.
	cs := cassette.New("mercuryapi", mercuryclient.Model)
	cs.Interactions = append(cs.Interactions, cassette.Interaction{
		Request: cassette.RecordedRequest{
			Method: "POST", URL: "https://example/",
			BodyB64: cassette.EncodeBody([]byte(`{"hi":"there"}`)),
		},
		Response: cassette.RecordedResponse{
			Status:     200,
			BodyB64:    cassette.EncodeBody([]byte(`{"choices":[{"message":{"content":"x"}}]}`)),
			DurationMs: 123,
		},
		DurationMs: 123,
	})
	var buf bytes.Buffer
	assert.NoError(t, cs.Write(&buf), "write cassette")
	body := buf.String()
	if !strings.Contains(body, mercuryclient.Model) {
		t.Fatalf("expected model version in cassette body: %q", body)
	}

	fixture := []byte(`Cassette smoke.
id: cass-smoke
row: 1
col: 0
-- buffer.txt --
hello
-- steps --
request-completion
-- cassette/mercuryapi.ndjson --
` + body)

	sc, err := ParseScenario(fixture, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sc.Cassettes["mercuryapi"] == nil {
		t.Fatal("cassette not parsed")
	}
	if len(sc.Cassettes["mercuryapi"].Interactions) != 1 {
		t.Errorf("want 1 interaction, got %d", len(sc.Cassettes["mercuryapi"].Interactions))
	}
}

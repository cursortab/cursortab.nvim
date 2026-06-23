package provider

import (
	"cursortab/assert"
	"testing"
)

func TestLineStreamRun_PrefillDoesNotConsumeValidator(t *testing.T) {
	var validated []string
	run := &lineStreamRun{
		lines:    make(chan string, 2),
		cancelCh: make(chan struct{}),
		mode: &lineStreamMode{
			firstLineValidator: func(_ *Provider, _ *RequestState, line string) error {
				validated = append(validated, line)
				return nil
			},
		},
	}

	assert.True(t, run.send("prefill"), "prefill sent")
	assert.Equal(t, 0, len(validated), "prefill does not validate")

	assert.True(t, run.emit("model"), "model line emitted")
	assert.Equal(t, []string{"model"}, validated, "model line validates")
}

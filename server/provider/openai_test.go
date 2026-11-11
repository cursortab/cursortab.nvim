package provider

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestRequestCarriesSamplingConfig(t *testing.T) {
	o := NewOpenAI("test", &types.ProviderConfig{
		Model:       "model",
		Temperature: 0.2,
		MaxTokens:   256,
		TopK:        0,
		TopP:        1.0,
	})

	req := o.Request("prompt", []string{"stop"})

	assert.Equal(t, "model", req.Model, "model")
	assert.Equal(t, "prompt", req.Prompt, "prompt")
	assert.Equal(t, 0.2, req.Temperature, "temperature")
	assert.Equal(t, 256, req.MaxTokens, "max tokens")
	assert.Equal(t, 0, req.TopK, "top k")
	assert.Equal(t, 1.0, req.TopP, "top p")
	assert.Equal(t, []string{"stop"}, req.Stop, "stop tokens")
}

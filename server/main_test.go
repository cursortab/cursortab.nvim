package main

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestValidateAcceptsSupportedProviderSource(t *testing.T) {
	config := Config{
		LogLevel: "info",
		Provider: ProviderConfig{
			Type:              string(types.ProviderSourceSweep),
			CompletionPath:    "/v1/completions",
			FIMTokens:         FIMTokensConfig{Prefix: "<PRE>", Suffix: "<SUF>", Middle: "<MID>"},
			MaxTokens:         1,
			CompletionTimeout: 1,
		},
	}

	assert.NoError(t, config.Validate(), "supported provider source should validate")
}

func TestValidateRejectsUnknownProviderSource(t *testing.T) {
	config := Config{
		LogLevel: "info",
		Provider: ProviderConfig{
			Type:           "unknown",
			CompletionPath: "/v1/completions",
			FIMTokens:      FIMTokensConfig{Prefix: "<PRE>", Suffix: "<SUF>", Middle: "<MID>"},
		},
	}

	err := config.Validate()
	assert.Error(t, err, "unknown provider source should fail")
	assert.Contains(t, err.Error(), "supported source/backend identity", "validation message should describe source identity")
}

func TestProviderCapabilityForSource(t *testing.T) {
	capabilityTests := []struct {
		source     types.ProviderSource
		capability types.ProviderCapability
	}{
		{types.ProviderSourceInline, types.ProviderCapabilityInsert},
		{types.ProviderSourceFIM, types.ProviderCapabilityInsert},
		{types.ProviderSourceSweep, types.ProviderCapabilityEdit},
		{types.ProviderSourceSweepAPI, types.ProviderCapabilityEdit},
		{types.ProviderSourceZeta, types.ProviderCapabilityEdit},
		{types.ProviderSourceCopilot, types.ProviderCapabilityEdit},
		{types.ProviderSourceMercuryAPI, types.ProviderCapabilityEdit},
	}

	for _, tt := range capabilityTests {
		assert.Equal(t, tt.capability, providerCapabilityForSource(tt.source), string(tt.source))
	}
}

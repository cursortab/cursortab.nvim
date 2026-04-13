package buffer

import (
	_ "embed"
	"fmt"

	"github.com/neovim/go-client/nvim"
)

type CopilotClientInfo struct {
	ID int
}

func (b *NvimBuffer) GetCopilotClient() (*CopilotClientInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("nvim client not set")
	}

	var result []map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(`return require('cursortab.bridge').get_lsp_client({'copilot', 'GitHub Copilot'})`, &result, nil)

	if err := batch.Execute(); err != nil {
		return nil, fmt.Errorf("failed to get Copilot client: %w", err)
	}

	if len(result) == 0 {
		return nil, nil
	}

	return &CopilotClientInfo{
		ID: getNumber(result[0], "id"),
	}, nil
}

func (b *NvimBuffer) SendCopilotDidFocus(uri string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	batch.ExecLua(`
		return require('cursortab.bridge').send_lsp_event(
			{ 'copilot', 'GitHub Copilot' },
			'textDocument/didFocus',
			{ textDocument = { uri = ... } }
		)
	`, nil, uri)

	return batch.Execute()
}

func (b *NvimBuffer) SendCopilotNESRequest(reqID int64, uri string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	chanID := b.client.ChannelID()

	batch := b.client.NewBatch()

	batch.ExecLua(`
		local chanID, reqID, uri = ...
		require('cursortab.bridge').send_copilot_nes_request(
			{'copilot', 'GitHub Copilot'},
			{ chanID = chanID, reqID = reqID, uri = uri }
		)
	`, nil, chanID, reqID, uri)

	return batch.Execute()
}

func (b *NvimBuffer) RegisterCopilotHandler(handler func(reqID int64, editsJSON string, errMsg string)) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	return b.client.RegisterHandler("cursortab_copilot_response", func(_ *nvim.Nvim, reqID int64, editsJSON string, errMsg string) {
		handler(reqID, editsJSON, errMsg)
	})
}

type WindsurfInfo struct {
	Healthy bool
	Port    int
	APIKey  string
}

func (b *NvimBuffer) GetWindsurfInfo() (*WindsurfInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("nvim client not set")
	}

	var result map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(`return require('cursortab.bridge').windsurf_get_info()`, &result, nil)

	if err := batch.Execute(); err != nil {
		return nil, fmt.Errorf("failed to get windsurf info: %w", err)
	}

	if result == nil {
		return &WindsurfInfo{Healthy: false}, nil
	}

	healthy, _ := result["healthy"].(bool)
	if !healthy {
		return &WindsurfInfo{Healthy: false}, nil
	}

	port := 0

	if n, ok := result["port"].(uint64); ok {
		port = int(n)
	}

	apiKey, _ := result["api_key"].(string)

	return &WindsurfInfo{
		Healthy: healthy,
		Port:    port,
		APIKey:  apiKey,
	}, nil
}

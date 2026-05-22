package harness

import "cursortab/buffer"

// cassetteWindsurfInfo implements windsurf.InfoProvider for eval replay.
// It always reports a healthy server with a dummy port and API key; the actual
// HTTP calls are intercepted by the cassette transport.
type cassetteWindsurfInfo struct{}

func newCassetteWindsurfInfo() *cassetteWindsurfInfo {
	return &cassetteWindsurfInfo{}
}

func (c *cassetteWindsurfInfo) GetWindsurfInfo() (*buffer.WindsurfInfo, error) {
	return &buffer.WindsurfInfo{
		Healthy: true,
		Port:    12345,
		APIKey:  "eval-placeholder",
	}, nil
}

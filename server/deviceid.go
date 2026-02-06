package main

import (
	"os"
	"path/filepath"
	"strings"

	"cursortab/logger"

	"github.com/google/uuid"
)

// loadOrCreateDeviceID reads a persistent device ID from stateDir/device_id,
// or generates and stores a new UUID if the file doesn't exist.
func loadOrCreateDeviceID(stateDir string) string {
	if stateDir == "" {
		return uuid.New().String()
	}

	path := filepath.Join(stateDir, "device_id")

	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}

	id := uuid.New().String()
	if err := os.WriteFile(path, []byte(id), 0644); err != nil {
		logger.Warn("failed to write device_id: %v", err)
	}
	return id
}

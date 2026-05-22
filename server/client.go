package main

import (
	"cursortab/logger"
	"fmt"
	"io"
	"os"
	"time"
)

type Client struct {
	stateDir string
}

func NewClient(stateDir string) *Client {
	return &Client{
		stateDir: stateDir,
	}
}

func (c *Client) Connect() error {
	conn, err := dialIPC(c.stateDir)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Relay between stdin/stdout and socket
	go func() {
		io.Copy(conn, os.Stdin)
		conn.Close()
	}()

	io.Copy(os.Stdout, conn)
	return nil
}

func (c *Client) EnsureDaemonRunning(stateDir string) error {
	running, pid := isDaemonRunning(stateDir)
	if running {
		logger.Debug("daemon already running with PID %d", pid)
		return nil
	}

	return c.startDaemon(stateDir)
}

func (c *Client) startDaemon(stateDir string) error {
	logger.Debug("starting daemon...")

	// Start daemon in background
	cmd := []string{os.Args[0], "--daemon"}
	env := os.Environ()

	// Start the daemon process
	_, err := os.StartProcess(os.Args[0], cmd, &os.ProcAttr{
		Env: env,
		Files: []*os.File{
			nil, // stdin
			nil, // stdout
			nil, // stderr
		},
	})
	if err != nil {
		return err
	}

	// Wait for daemon to start
	return c.waitForDaemon(stateDir)
}

func (c *Client) waitForDaemon(stateDir string) error {
	for range 50 { // Wait up to 5 seconds
		if running, _ := isDaemonRunning(stateDir); running {
			logger.Debug("daemon started successfully")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon failed to start within timeout")
}

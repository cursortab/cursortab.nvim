//go:build !windows

package main

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func getIPCAddress(stateDir string) string {
	return filepath.Join(stateDir, "cursortab.sock")
}

func listenIPC(stateDir string) (net.Listener, string, error) {
	path := getIPCAddress(stateDir)
	os.Remove(path)
	l, err := net.Listen("unix", path)
	return l, path, err
}

func dialIPC(stateDir string) (net.Conn, error) {
	return net.Dial("unix", getIPCAddress(stateDir))
}

func cleanupIPC(stateDir string) {
	os.Remove(getIPCAddress(stateDir))
}

func isProcessRunning(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func setupShutdownHandler(onShutdown func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		onShutdown()
	}()
}

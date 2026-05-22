//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

func getIPCAddress(stateDir string) string {
	return filepath.Join(stateDir, "cursortab.port")
}

func listenIPC(stateDir string) (net.Listener, string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(getIPCAddress(stateDir), []byte(strconv.Itoa(port)), 0644); err != nil {
		l.Close()
		return nil, "", err
	}
	return l, fmt.Sprintf("127.0.0.1:%d", port), nil
}

func dialIPC(stateDir string) (net.Conn, error) {
	portPath := getIPCAddress(stateDir)
	data, err := os.ReadFile(portPath)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(string(data))
	if err != nil {
		return nil, err
	}
	return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func cleanupIPC(stateDir string) {
	os.Remove(getIPCAddress(stateDir))
}

func isProcessRunning(pid int) bool {
	const processQueryInformation = 0x0400
	h, err := syscall.OpenProcess(processQueryInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var exitCode uint32
	err = syscall.GetExitCodeProcess(h, &exitCode)
	if err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

func setupShutdownHandler(onShutdown func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		onShutdown()
	}()
}

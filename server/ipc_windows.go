//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func getIPCAddress(stateDir string) string {
	return runtimePath(stateDir, "port")
}

// lockDaemon takes the daemon lock of stateDir, or returns errDaemonRunning
// when another daemon holds it. The lock is the lock file opened without
// sharing; the system releases it when the process exits, and unlock
// releases it earlier.
func lockDaemon(stateDir string) (unlock func(), err error) {
	const errorSharingViolation = syscall.Errno(32)
	name, err := syscall.UTF16PtrFromString(runtimePath(stateDir, "lock"))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errDaemonRunning
		}
		return nil, err
	}
	return func() { syscall.CloseHandle(h) }, nil
}

// listenIPC listens on a free local port and records it in the port file.
// The caller holds the daemon lock. The file is replaced in one step, so a
// relay never reads a partly written port.
func listenIPC(stateDir string) (net.Listener, string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	path := getIPCAddress(stateDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(port)), 0644); err != nil {
		l.Close()
		return nil, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		l.Close()
		return nil, "", err
	}
	return l, fmt.Sprintf("127.0.0.1:%d", port), nil
}

// dialIPC connects to the port in the port file. The file outlives a daemon
// that crashed, and another program may then use the port, so it is trusted
// only while a daemon holds the lock.
func dialIPC(stateDir string) (net.Conn, error) {
	if unlock, err := lockDaemon(stateDir); err == nil {
		unlock()
		return nil, errors.New("no daemon is running")
	} else if !errors.Is(err, errDaemonRunning) {
		return nil, err
	}
	data, err := os.ReadFile(getIPCAddress(stateDir))
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

func detachedProcAttr() *syscall.SysProcAttr {
	const detachedProcess, createNewProcessGroup = 0x00000008, 0x00000200
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
}

func setupShutdownHandler(onShutdown func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		onShutdown()
	}()
}

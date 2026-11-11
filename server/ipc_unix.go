//go:build !windows

package main

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func getIPCAddress(stateDir string) string {
	return runtimePath(stateDir, "sock")
}

// lockDaemon takes the daemon lock of stateDir, or returns errDaemonRunning
// when another daemon holds it. The system releases the lock when the
// process exits; unlock releases it earlier.
func lockDaemon(stateDir string) (unlock func(), err error) {
	f, err := os.OpenFile(runtimePath(stateDir, "lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errDaemonRunning
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// listenIPC creates the daemon socket. The caller holds the daemon lock, so
// a socket file left there belongs to a dead daemon.
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

func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func setupShutdownHandler(onShutdown func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		onShutdown()
	}()
}

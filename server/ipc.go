package main

import (
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// runtimePath returns the path of a daemon runtime file in stateDir. The name
// includes the runtime ID, so a binary only ever talks to a daemon started
// from the same build: an updated plugin starts a new daemon, and the old one
// exits once its editors leave.
func runtimePath(stateDir, ext string) string {
	return filepath.Join(stateDir, fmt.Sprintf("cursortab-%s.%s", runtimeID(), ext))
}

// runtimeID identifies this build: the version and a hash of the size and
// modification time of the executable, which a rebuild changes.
var runtimeID = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return Version
	}
	info, err := os.Stat(exe)
	if err != nil {
		return Version
	}
	h := fnv.New32a()
	fmt.Fprint(h, info.Size(), info.ModTime().UnixNano())
	return fmt.Sprintf("%s-%08x", Version, h.Sum32())
})

// connectDaemon dials the daemon of stateDir. When none answers, it starts
// one and keeps dialing for up to connectTimeout. It starts another daemon
// every second while dialing fails: a daemon that finds the previous one
// still shutting down exits at once.
func connectDaemon(stateDir string) (net.Conn, error) {
	const connectTimeout = 10 * time.Second
	deadline := time.Now().Add(connectTimeout)
	var started time.Time
	for {
		conn, err := dialIPC(stateDir)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no daemon answered within %s: %w", connectTimeout, err)
		}
		if time.Since(started) >= time.Second {
			if err := startDaemon(); err != nil {
				return nil, fmt.Errorf("starting the daemon: %w", err)
			}
			started = time.Now()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startDaemon starts this binary as a daemon, detached from the relay, with
// the same environment.
func startDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--daemon")
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // Reaps a daemon that exits while this relay runs.
	return nil
}

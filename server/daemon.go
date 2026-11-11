package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"cursortab/buffer"
	"cursortab/engine"
	"cursortab/logger"

	"github.com/neovim/go-client/nvim"
)

// errDaemonRunning means another daemon already serves the state directory.
var errDaemonRunning = errors.New("daemon already running")

// idleTimeout is how long the daemon keeps running without connected editors.
const idleTimeout = 30 * time.Second

// Daemon accepts editor connections and runs one session per connection.
type Daemon struct {
	config   DaemonConfig
	deviceID string

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions int
	idleFrom time.Time // When the last session ended
}

func NewDaemon(config DaemonConfig) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{
		config:   config,
		deviceID: loadOrCreateDeviceID(config.StateDir),
		ctx:      ctx,
		cancel:   cancel,
		idleFrom: time.Now(),
	}
}

// Run serves connections until a shutdown signal arrives or no editor has
// been connected for the idle timeout.
//
// Only the daemon that holds the lock of the state directory runs; it alone
// creates and removes the runtime files.
func (d *Daemon) Run() error {
	unlock, err := lockDaemon(d.config.StateDir)
	if errors.Is(err, errDaemonRunning) {
		logger.Info("another daemon is already running, exiting")
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()

	listener, addr, err := listenIPC(d.config.StateDir)
	if err != nil {
		return err
	}
	defer cleanupIPC(d.config.StateDir)

	pidPath := runtimePath(d.config.StateDir, "pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		logger.Warn("could not write PID file: %v", err)
	}
	defer os.Remove(pidPath)
	logger.Info("daemon %s listening on %s (PID %d)", Version, addr, os.Getpid())

	setupShutdownHandler(func() {
		logger.Info("received shutdown signal")
		d.cancel()
	})
	go d.shutdownWhenIdle()
	go func() {
		<-d.ctx.Done()
		listener.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			if d.ctx.Err() != nil {
				break
			}
			logger.Error("error accepting connection: %v", err)
			continue
		}
		d.sessionStarted()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.serve(conn)
		}()
	}
	// Sessions end once their connection closes; a session stuck in a call
	// that ignores cancellation must not keep the daemon alive.
	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
		logger.Info("daemon stopped")
	case <-time.After(5 * time.Second):
		logger.Warn("daemon stopped with sessions still ending")
	}
	return nil
}

// serve runs the session of one editor connection. The editor first sends
// the cursortab_init notification with its session config; events before it
// are dropped. go-client runs notifications in order on one goroutine; the
// lock only orders them with the end of the session, which closes the engine
// even when the init arrives late.
func (d *Daemon) serve(conn net.Conn) {
	defer conn.Close()
	defer d.sessionEnded()

	client, err := nvim.New(conn, conn, conn, logger.Debug)
	if err != nil {
		logger.Error("error creating nvim client: %v", err)
		return
	}
	defer client.Close()

	var (
		mu      sync.Mutex
		session *engine.Engine
		ended   bool
	)
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		ended = true
		if session != nil {
			session.Close()
		}
	}()

	handlers := map[string]any{
		"cursortab_init": func(raw string) {
			mu.Lock()
			defer mu.Unlock()
			if ended || session != nil {
				logger.Warn("ignoring session init")
				return
			}
			eng, err := initSession(client, raw, d.deviceID)
			if err != nil {
				logger.Error("session init failed: %v", err)
				notifyError(client, err)
				return
			}
			session = eng
			go eng.Run()
			logger.Info("session started")
			notifyStarted(client)
		},
		"cursortab_event": func(event string, pos [4]int) {
			mu.Lock()
			eng := session
			mu.Unlock()
			if eng != nil {
				eng.HandleEditorEvent(event, buffer.Position{Row: pos[0], Col: pos[1], Offset: pos[2], Size: pos[3]})
			}
		},
		"cursortab_shutdown": func() {
			logger.Info("shutdown requested by an editor")
			d.cancel()
		},
	}
	for name, handler := range handlers {
		if err := client.RegisterHandler(name, handler); err != nil {
			logger.Error("error registering %s handler: %v", name, err)
			return
		}
	}

	served := make(chan error, 1)
	go func() { served <- client.Serve() }()
	select {
	case err = <-served:
		logSessionEnd(err)
	case <-d.ctx.Done():
		// Close the connection before the engine, so its final RPCs fail fast
		// instead of waiting on an editor that may not respond. The editor
		// resets its UI when the connection ends.
		conn.Close()
	}
}

// initSession builds the engine of a session from the raw session config.
func initSession(client *nvim.Nvim, raw string, deviceID string) (*engine.Engine, error) {
	config, err := parseSessionConfig(raw)
	if err != nil {
		return nil, err
	}
	return newSessionEngine(client, config, deviceID)
}

// notifyStarted tells the editor that its session runs, with the PID of the
// daemon.
func notifyStarted(client *nvim.Nvim) {
	const started = `require("cursortab.daemon").on_session_started(...)`
	if err := client.ExecLua(started, nil, os.Getpid()); err != nil {
		logger.Debug("could not confirm session start: %v", err)
	}
}

// notifyError shows a session error in the editor.
func notifyError(client *nvim.Nvim, err error) {
	const show = `require("cursortab.daemon").on_session_error(...)`
	if execErr := client.ExecLua(show, nil, err.Error()); execErr != nil {
		logger.Debug("could not show session error: %v", execErr)
	}
}

func logSessionEnd(err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		logger.Error("connection error: %v", err)
	}
	logger.Info("session ended")
}

func (d *Daemon) sessionStarted() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions++
	logger.Info("client connected, sessions: %d", d.sessions)
}

func (d *Daemon) sessionEnded() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions--
	if d.sessions == 0 {
		d.idleFrom = time.Now()
	}
	logger.Info("client disconnected, sessions: %d", d.sessions)
}

// shutdownWhenIdle stops the daemon once no editor has been connected for the
// idle timeout, or right away in immediate-shutdown debug mode.
func (d *Daemon) shutdownWhenIdle() {
	timeout := idleTimeout
	if d.config.ImmediateShutdown {
		timeout = 0
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.mu.Lock()
			idle := d.sessions == 0 && time.Since(d.idleFrom) >= timeout
			d.mu.Unlock()
			if idle {
				logger.Info("no clients connected, shutting down daemon")
				d.cancel()
				return
			}
		}
	}
}

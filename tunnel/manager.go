package tunnel

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type Process struct {
	ID       string
	TunnelID string
	Cmd      *exec.Cmd
	RunID    string
	Stderr   *bytes.Buffer
}

type OnExit func(tunnelID string, runID string, exitCode int, stderr string)

type Manager struct {
	mu     sync.Mutex
	procs  map[string]*Process
	onExit OnExit
}

func NewManager(onExit OnExit) *Manager {
	return &Manager{
		procs:  make(map[string]*Process),
		onExit: onExit,
	}
}

func (m *Manager) Start(tunnelID, sshHost, password, identityFile string, localPort uint16, remoteHost string, remotePort uint16) (*Process, error) {
	m.mu.Lock()
	if _, exists := m.procs[tunnelID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("tunnel %q is already running", tunnelID)
	}

	forward := fmt.Sprintf("%d:%s:%d", localPort, remoteHost, remotePort)
	runID := uuid.New().String()
	stderrBuf := &bytes.Buffer{}

	args := []string{"-N", "-o", "ConnectTimeout=8", "-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3", "-L", forward}

	var askpassFile string
	if password != "" {
		// Create temp ASKPASS script
		tmpDir := os.TempDir()
		askpassFile = filepath.Join(tmpDir, fmt.Sprintf("td-askpass-%s.bat", runID[:8]))
		script := fmt.Sprintf("@echo off\r\necho %s", password)
		os.WriteFile(askpassFile, []byte(script), 0700)
	} else {
		args = append(args, "-o", "BatchMode=yes", "-o", "PasswordAuthentication=no")
	}

	if identityFile != "" {
		args = append(args, "-i", identityFile)
	}

	args = append(args, sshHost)
	cmd := exec.Command("ssh", args...)
	cmd.Stderr = stderrBuf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if askpassFile != "" {
		cmd.Env = append(os.Environ(),
			"SSH_ASKPASS="+askpassFile,
			"DISPLAY=dummy",
		)
	}

	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("start ssh for %q: %w", tunnelID, err)
	}

	proc := &Process{
		ID:       fmt.Sprintf("%d", cmd.Process.Pid),
		TunnelID: tunnelID,
		Cmd:      cmd,
		RunID:    runID,
		Stderr:   stderrBuf,
	}
	m.procs[tunnelID] = proc
	m.mu.Unlock()

	exited := make(chan struct {
		exitCode int
		stderr   string
	}, 1)

	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		stderr := stderrBuf.String()
		m.mu.Lock()
		delete(m.procs, tunnelID)
		m.mu.Unlock()
		if askpassFile != "" {
			_ = os.Remove(askpassFile)
		}
		if m.onExit != nil {
			m.onExit(tunnelID, runID, exitCode, stderr)
		}
		exited <- struct {
			exitCode int
			stderr   string
		}{exitCode: exitCode, stderr: stderr}
	}()

	select {
	case res := <-exited:
		return nil, fmt.Errorf("ssh for %q exited immediately (code %d): %s", tunnelID, res.exitCode, res.stderr)
	case <-time.After(800 * time.Millisecond):
	}

	return proc, nil
}

func (m *Manager) Stop(tunnelID string) (*Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	proc, exists := m.procs[tunnelID]
	if !exists {
		return nil, fmt.Errorf("tunnel %q is not running", tunnelID)
	}
	proc.Cmd.Process.Kill()
	delete(m.procs, tunnelID)
	return proc, nil
}

func (m *Manager) IsRunning(tunnelID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.procs[tunnelID]
	return exists
}

func (m *Manager) RunningIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.procs))
	for id := range m.procs {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, proc := range m.procs {
		proc.Cmd.Process.Kill()
		delete(m.procs, id)
	}
}

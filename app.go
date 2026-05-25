package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"tunnel-deck/config"
	"tunnel-deck/db"
	"tunnel-deck/health"
	"tunnel-deck/tunnel"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx     context.Context
	cfg     *config.AppConfig
	store   *db.Store
	manager *tunnel.Manager
}

func NewApp() *App {
	a := &App{}
	a.manager = tunnel.NewManager(a.onTunnelExit)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	var err error
	a.cfg, err = config.Load()
	if err != nil {
		runtime.LogErrorf(ctx, "config load: %v", err)
		a.cfg = config.Default()
	}

	a.store, err = db.Open()
	if err != nil {
		runtime.LogErrorf(ctx, "db open: %v", err)
	}

	a.store.MarkAllStopped()
}

func (a *App) shutdown(ctx context.Context) {
	a.manager.StopAll()
	if a.store != nil {
		a.store.Close()
	}
}

// --- Wails-bound methods (exposed to frontend) ---

type TunnelInfo struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	SSHHost          string  `json:"ssh_host"`
	LocalPort        uint16  `json:"local_port"`
	RemoteHost       string  `json:"remote_host"`
	RemotePort       uint16  `json:"remote_port"`
	AutoStart        bool    `json:"auto_start"`
	OpenURL          *string `json:"open_url"`
	HealthURL        *string `json:"health_url"`
	Status           string  `json:"status"`
	PID              *int64  `json:"pid"`
	LastStartedAt    *string `json:"last_started_at"`
	LastHealthStatus *string `json:"last_health_status"`
}

type AppInfo struct {
	Tunnels    []TunnelInfo   `json:"tunnels"`
	Settings   config.Settings `json:"settings"`
	ConfigPath string         `json:"config_path"`
}

func (a *App) GetAppInfo() (*AppInfo, error) {
	states, _ := a.store.AllStates()
	stateMap := map[string]db.TunnelState{}
	for _, s := range states {
		stateMap[s.TunnelID] = s
	}

	runningIDs := map[string]bool{}
	for _, id := range a.manager.RunningIDs() {
		runningIDs[id] = true
	}

	tunnels := make([]TunnelInfo, 0, len(a.cfg.Tunnels))
	for _, t := range a.cfg.Tunnels {
		s := stateMap[t.ID]
		status := s.Status
		if runningIDs[t.ID] {
			status = "running"
		}
		if status == "" {
			status = "stopped"
		}

		tunnels = append(tunnels, TunnelInfo{
			ID: t.ID, Name: t.Name, SSHHost: t.SSHHost,
			LocalPort: t.LocalPort, RemoteHost: t.RemoteHost, RemotePort: t.RemotePort,
			AutoStart: t.AutoStart, OpenURL: t.OpenURL, HealthURL: t.HealthURL,
			Status: status, PID: s.PID,
			LastStartedAt: s.LastStartedAt, LastHealthStatus: s.LastHealthStatus,
		})
	}

	cp, _ := config.ConfigPath()

	return &AppInfo{
		Tunnels:    tunnels,
		Settings:   a.cfg.Settings,
		ConfigPath: cp,
	}, nil
}

func (a *App) StartTunnel(tunnelID string) (*TunnelInfo, error) {
	t := a.findTunnel(tunnelID)
	if t == nil {
		return nil, fmt.Errorf("tunnel %q not found", tunnelID)
	}

	proc, err := a.manager.Start(t.ID, t.SSHHost, strPtr(t.Password), strPtr(t.IdentityFile), t.LocalPort, t.RemoteHost, t.RemotePort)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pid := int64(proc.Cmd.Process.Pid)

	a.store.RecordRunStart(proc.RunID, tunnelID)
	a.store.UpsertState(db.TunnelState{
		TunnelID: tunnelID, Status: "running", PID: &pid,
		LastStartedAt: &now, UpdatedAt: now,
	})

	return a.tunnelInfo(t, "running", &pid, &now, nil), nil
}

func (a *App) StopTunnel(tunnelID string) (*TunnelInfo, error) {
	t := a.findTunnel(tunnelID)
	if t == nil {
		return nil, fmt.Errorf("tunnel %q not found", tunnelID)
	}

	proc, err := a.manager.Stop(tunnelID)
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		// Mark as stopped anyway
		a.store.UpsertState(db.TunnelState{
			TunnelID: tunnelID, Status: "stopped",
			LastStoppedAt: &now, UpdatedAt: now,
		})
		return a.tunnelInfo(t, "stopped", nil, nil, nil), nil
	}

	a.store.RecordRunStop(proc.RunID, nil, nil)
	a.store.UpsertState(db.TunnelState{
		TunnelID: tunnelID, Status: "stopped",
		LastStoppedAt: &now, UpdatedAt: now,
	})

	return a.tunnelInfo(t, "stopped", nil, nil, nil), nil
}

func (a *App) RestartTunnel(tunnelID string) (*TunnelInfo, error) {
	a.manager.Stop(tunnelID)
	time.Sleep(300 * time.Millisecond)
	return a.StartTunnel(tunnelID)
}

func (a *App) StartAutoTunnels() ([]string, error) {
	var started []string
	for _, t := range a.cfg.Tunnels {
		if t.AutoStart && !a.manager.IsRunning(t.ID) {
			if _, err := a.StartTunnel(t.ID); err != nil {
				runtime.LogWarningf(a.ctx, "auto-start %q: %v", t.ID, err)
				continue
			}
			started = append(started, t.ID)
		}
	}
	return started, nil
}

func (a *App) StopAllTunnels() error {
	ids := a.manager.RunningIDs()
	a.manager.StopAll()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		a.store.UpsertState(db.TunnelState{
			TunnelID: id, Status: "stopped",
			LastStoppedAt: &now, UpdatedAt: now,
		})
	}
	return nil
}

func (a *App) CheckTunnelHealth(tunnelID string) (*health.Result, error) {
	t := a.findTunnel(tunnelID)
	if t == nil {
		return nil, fmt.Errorf("tunnel %q not found", tunnelID)
	}
	if t.HealthURL == nil {
		return nil, fmt.Errorf("no health_url for %q", tunnelID)
	}

	result := health.Check(*t.HealthURL, 5*time.Second)
	now := time.Now().UTC().Format(time.RFC3339)

	status := "unhealthy"
	healthStatus := "unhealthy"
	if result.OK {
		status = "running"
		healthStatus = "healthy"
	}

	a.store.UpsertState(db.TunnelState{
		TunnelID: tunnelID, Status: status,
		LastHealthStatus: &healthStatus,
		LastHealthAt:     &now,
		UpdatedAt:        now,
	})

	return &result, nil
}

func (a *App) ReloadConfig() (*AppInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return a.GetAppInfo()
}

func (a *App) OpenConfigFile() error {
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	return exec.Command("cmd", "/c", "start", "", path).Start()
}

func (a *App) OpenURL(url string) {
	runtime.BrowserOpenURL(a.ctx, url)
}

// --- helpers ---

func (a *App) onTunnelExit(tunnelID, runID string, exitCode int, stderr string) {
	runtime.LogWarningf(a.ctx, "tunnel %q exited (code %d): %s", tunnelID, exitCode, stderr)
	now := time.Now().UTC().Format(time.RFC3339)
	a.store.RecordRunStop(runID, &exitCode, &stderr)
	a.store.UpsertState(db.TunnelState{
		TunnelID: tunnelID, Status: "stopped",
		LastStoppedAt: &now, UpdatedAt: now,
	})
}

func (a *App) findTunnel(id string) *config.Tunnel {
	for _, t := range a.cfg.Tunnels {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

func (a *App) tunnelInfo(t *config.Tunnel, status string, pid *int64, startedAt *string, healthStatus *string) *TunnelInfo {
	return &TunnelInfo{
		ID: t.ID, Name: t.Name, SSHHost: t.SSHHost,
		LocalPort: t.LocalPort, RemoteHost: t.RemoteHost, RemotePort: t.RemotePort,
		AutoStart: t.AutoStart, OpenURL: t.OpenURL, HealthURL: t.HealthURL,
		Status: status, PID: pid,
		LastStartedAt: startedAt, LastHealthStatus: healthStatus,
	}
}

func strPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

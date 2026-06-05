package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tunnel-deck/config"
	d "tunnel-deck/db"
	"tunnel-deck/health"
	"tunnel-deck/orchestrator"
	"tunnel-deck/tunnel"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	goRuntime "runtime"
)

type App struct {
	ctx          context.Context
	store        *d.Store
	manager      *tunnel.Manager
	orch         *orchestrator.Orchestrator
	apiServer    *http.Server
	apiListener  net.Listener
	apiAddress   string
	bootstrapKey string
}

type TunnelInfo struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	SSHHost          string  `json:"ssh_host"`
	LocalPort        uint16  `json:"local_port"`
	RemoteHost       string  `json:"remote_host"`
	RemotePort       uint16  `json:"remote_port"`
	Sort             int     `json:"sort"`
	AutoStart        bool    `json:"auto_start"`
	OpenURL          *string `json:"open_url"`
	HealthURL        *string `json:"health_url"`
	Status           string  `json:"status"`
	PID              *int64  `json:"pid"`
	LastStartedAt    *string `json:"last_started_at"`
	LastHealthStatus *string `json:"last_health_status"`
}

type TunnelDraft struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	SSHHost      string `json:"ssh_host"`
	Password     string `json:"password"`
	IdentityFile string `json:"identity_file"`
	LocalPort    uint16 `json:"local_port"`
	RemoteHost   string `json:"remote_host"`
	RemotePort   uint16 `json:"remote_port"`
	AutoStart    bool   `json:"auto_start"`
	OpenURL      string `json:"open_url"`
	HealthURL    string `json:"health_url"`
	Enabled      bool   `json:"enabled"`
	Sort         int    `json:"sort"`
}

type ServerEntry struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Command     string `json:"command"`
	DockerImage string `json:"docker_image"`
	AutoStart   bool   `json:"auto_start"`
	Enabled     bool   `json:"enabled"`
	Sort        int    `json:"sort"`
}

type OpenPort struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	State    string `json:"state"`
	PID      int    `json:"pid"`
	Process  string `json:"process"`
}

type Settings struct {
	StartOnLogin             bool   `json:"start_on_login"`
	AutoStartTunnelsOnLaunch bool   `json:"auto_start_tunnels_on_launch"`
	ProcessComposeConfig     string `json:"process_compose_config"`
	ActiveProjectID          string `json:"active_project_id"`
}

type ProjectInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	SortOrder   int    `json:"sort_order"`
	Description string `json:"description"`
	Config      string `json:"config"`
}

type SystemInfo struct {
	DockerAvailable bool   `json:"docker_available"`
	DockerRunning   bool   `json:"docker_running"`
	DockerPath      string `json:"docker_path"`
	DockerVersion   string `json:"docker_version"`
	Error           string `json:"error,omitempty"`
}

type UITab struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Label     string `json:"label"`
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	Sort      int    `json:"sort"`
	Config    string `json:"config"`
}

type AppState struct {
	Settings           Settings      `json:"settings"`
	Projects           []ProjectInfo `json:"projects"`
	ActiveProjectID    string        `json:"active_project_id"`
	Tunnels            []TunnelInfo  `json:"tunnels"`
	Tabs               []UITab       `json:"tabs"`
	DbFile             string        `json:"db_file"`
	OrchestratorConfig string        `json:"orchestrator_config"`
	APIAddress         string        `json:"api_address"`
	MCPPath            string        `json:"mcp_path"`
	System             SystemInfo    `json:"system"`
	MainMode           bool          `json:"main_mode"`
}

type ActionFailure struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type StartResult struct {
	Started        []string        `json:"started"`
	AlreadyRunning []string        `json:"already_running"`
	Skipped        []string        `json:"skipped"`
	Failed         []ActionFailure `json:"failed"`
}

const (
	defaultAPIAddress = "127.0.0.1:8765"
	defaultMCPPath    = "/mcp"
	defaultProjectID  = "main"
)

var protectedProjectTabKeys = map[string]struct{}{
	"main":         {},
	"ports":        {},
	"tunnels":      {},
	"servers":      {},
	"orchestrator": {},
}

func isProtectedTabKey(key string) bool {
	_, ok := protectedProjectTabKeys[strings.TrimSpace(key)]
	return ok
}

func NewApp() *App {
	a := &App{}
	a.manager = tunnel.NewManager(a.onTunnelExit)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	var err error
	a.store, err = d.Open()
	if err != nil {
		wailsRuntime.LogErrorf(ctx, "open db: %v", err)
		return
	}
	if err := a.store.MarkAllStopped(); err != nil {
		wailsRuntime.LogErrorf(ctx, "mark old runs stopped: %v", err)
	}

	if cnt, err := a.store.TunnelCount(); err == nil && cnt == 0 {
		if err := a.seedLegacyConfig(); err != nil {
			wailsRuntime.LogWarningf(ctx, "legacy import skipped: %v", err)
		}
	}

	st, err := a.store.GetSettings()
	if err != nil {
		wailsRuntime.LogErrorf(ctx, "load settings: %v", err)
		st = d.Settings{}
	}

	pcConfig := strings.TrimSpace(st.ProcessComposeConfig)
	if pcConfig == "" {
		pcConfig = "process-compose.yaml"
	}
	a.orch = orchestrator.New([]string{pcConfig})

	activeProjectID := strings.TrimSpace(st.ActiveProjectID)
	if activeProjectID == "" {
		activeProjectID = defaultProjectID
	}
	if err := a.store.SetActiveProjectID(activeProjectID); err != nil {
		wailsRuntime.LogErrorf(ctx, "set active project failed: %v", err)
	}
	if err := a.ensureProjectSeed(activeProjectID); err != nil {
		wailsRuntime.LogErrorf(ctx, "project seed failed: %v", err)
	}
	activeProjectID = a.resolveProjectID("")

	if err := a.bootstrapAPIKey(); err != nil {
		wailsRuntime.LogErrorf(ctx, "bootstrap api keys failed: %v", err)
	}
	if err := a.startControlServer(); err != nil {
		wailsRuntime.LogErrorf(ctx, "start control server failed: %v", err)
	}

	if st.AutoStartTunnelsOnLaunch {
		go func() {
			_, _ = a.StartAutoTunnelsForProject(activeProjectID)
		}()
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.manager.StopAll()
	a.stopControlServer(ctx)
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *App) seedLegacyConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	for _, t := range cfg.Tunnels {
		if err := a.store.UpsertTunnel(d.Tunnel{
			ID: t.ID, ProjectID: defaultProjectID, Name: t.Name, SSHHost: t.SSHHost,
			Password: t.Password, IdentityFile: t.IdentityFile,
			LocalPort: t.LocalPort, RemoteHost: t.RemoteHost, RemotePort: t.RemotePort,
			AutoStart: t.AutoStart, OpenURL: t.OpenURL, HealthURL: t.HealthURL, Enabled: true,
		}); err != nil {
			return err
		}
	}

	settings := cfg.Settings
	_ = a.store.SetSettings(d.Settings{
		StartOnLogin:             settings.StartOnLogin,
		AutoStartTunnelsOnLaunch: settings.AutoStartTunnelsOnLaunch,
		ProcessComposeConfig:     settings.ProcessComposeConfig,
	})

	return nil
}

func (a *App) GetAppState() (*AppState, error) {
	return a.GetAppStateForProject(a.activeProjectID())
}

func (a *App) GetAppStateForProject(projectID string) (*AppState, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	projectID = a.resolveProjectID(projectID)
	tunnelsCfg, err := a.store.ListTunnelsByProject(projectID)
	if err != nil {
		return nil, err
	}
	states, _ := a.store.AllStates()
	statusMap := map[string]d.TunnelState{}
	for _, s := range states {
		statusMap[s.TunnelID] = s
	}

	running := map[string]bool{}
	for _, id := range a.manager.RunningIDs() {
		running[id] = true
	}

	infos := make([]TunnelInfo, 0, len(tunnelsCfg))
	for _, t := range tunnelsCfg {
		s := statusMap[t.ID]
		status := s.Status
		if running[t.ID] {
			status = "running"
		}
		if status == "" {
			status = "stopped"
		}
		infos = append(infos, TunnelInfo{
			ID: t.ID, Name: t.Name, SSHHost: t.SSHHost,
			LocalPort: t.LocalPort, RemoteHost: t.RemoteHost, RemotePort: t.RemotePort,
			Sort:      t.SortOrder,
			AutoStart: t.AutoStart, OpenURL: t.OpenURL, HealthURL: t.HealthURL,
			Status: status, PID: s.PID,
			LastStartedAt: s.LastStartedAt, LastHealthStatus: s.LastHealthStatus,
		})
	}

	tabsRaw, err := a.store.ListTabsByProject(projectID)
	if err != nil {
		return nil, err
	}
	settings, err := a.store.GetSettings()
	if err != nil {
		return nil, err
	}
	rawProjects, err := a.store.ListProjects()
	if err != nil {
		return nil, err
	}
	projects := make([]ProjectInfo, 0, len(rawProjects))
	for _, p := range rawProjects {
		projects = append(projects, projectToInfo(p))
	}
	path, _ := d.DataPath()
	tabs := make([]UITab, 0, len(tabsRaw))
	for _, t := range tabsRaw {
		if t.Enabled {
			tabs = append(tabs, UITab{
				ID: t.ID, ProjectID: t.ProjectID, Label: t.Label, Key: t.Key,
				Kind: t.Kind, Enabled: t.Enabled, Sort: t.Sort, Config: t.Config,
			})
		}
	}

	appSettings := Settings{
		StartOnLogin:             settings.StartOnLogin,
		AutoStartTunnelsOnLaunch: settings.AutoStartTunnelsOnLaunch,
		ProcessComposeConfig:     settings.ProcessComposeConfig,
		ActiveProjectID:          projectID,
	}

	return &AppState{
		Settings:           appSettings,
		Projects:           projects,
		ActiveProjectID:    projectID,
		Tunnels:            infos,
		Tabs:               tabs,
		DbFile:             path,
		OrchestratorConfig: settings.ProcessComposeConfig,
		APIAddress:         a.apiAddress,
		MCPPath:            defaultMCPPath,
		System:             a.systemInfo(),
		MainMode:           projectID == defaultProjectID,
	}, nil
}

func (a *App) GetTabs() ([]UITab, error) {
	return a.GetTabsForProject(a.activeProjectID())
}

func (a *App) GetTabsForProject(projectID string) ([]UITab, error) {
	projectID = a.resolveProjectID(projectID)
	rows, err := a.store.ListTabsByProject(projectID)
	if err != nil {
		return nil, err
	}
	tabs := make([]UITab, 0, len(rows))
	for _, r := range rows {
		tabs = append(tabs, UITab{
			ID:        r.ID,
			ProjectID: r.ProjectID,
			Label:     r.Label,
			Key:       r.Key,
			Kind:      r.Kind,
			Enabled:   r.Enabled,
			Sort:      r.Sort,
			Config:    r.Config,
		})
	}
	return tabs, nil
}

func (a *App) GetProjects() ([]ProjectInfo, error) {
	list, err := a.store.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectInfo, 0, len(list))
	for _, p := range list {
		out = append(out, projectToInfo(p))
	}
	return out, nil
}

func (a *App) GetProject(id string) (ProjectInfo, error) {
	p, err := a.store.GetProject(id)
	if err != nil {
		return ProjectInfo{}, err
	}
	return projectToInfo(p), nil
}

func (a *App) SetActiveProject(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = defaultProjectID
	}
	if err := a.store.EnsureProjectExists(projectID); err != nil {
		return err
	}
	if err := a.ensureProjectTabs(projectID); err != nil {
		return err
	}
	return a.store.SetActiveProjectID(projectID)
}

func (a *App) UpsertProject(p ProjectInfo) (ProjectInfo, error) {
	projectID := strings.TrimSpace(p.ID)
	if projectID == "" {
		projectID = fmt.Sprintf("project_%d", time.Now().UnixNano())
	}
	created, err := a.store.UpsertProject(projectID, p.Name, p.Slug, p.Kind, p.Description, p.Config, p.SortOrder, p.Enabled)
	if err != nil {
		return ProjectInfo{}, err
	}
	if strings.TrimSpace(projectID) == defaultProjectID {
		_ = a.store.SetActiveProjectID(defaultProjectID)
	}
	if err := a.ensureProjectTabs(projectID); err != nil {
		return ProjectInfo{}, err
	}
	return projectToInfo(created), nil
}

func (a *App) DeleteProject(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	if err := a.store.DeleteProject(projectID); err != nil {
		return err
	}
	if projectID == a.resolveProjectID("") {
		_ = a.store.SetActiveProjectID(defaultProjectID)
	}
	return nil
}

func (a *App) UpsertTab(tab UITab) (*UITab, error) {
	saved := d.Tab{
		ID:        tab.ID,
		ProjectID: strings.TrimSpace(tab.ProjectID),
		Label:     strings.TrimSpace(tab.Label),
		Key:       strings.TrimSpace(tab.Key),
		Kind:      strings.TrimSpace(tab.Kind),
		Enabled:   tab.Enabled,
		Sort:      tab.Sort,
		Config:    tab.Config,
	}
	if saved.ProjectID == "" {
		saved.ProjectID = a.activeProjectID()
	} else if err := a.store.EnsureProjectExists(saved.ProjectID); err != nil {
		return nil, err
	}
	if saved.ID == "" {
		saved.ID = strings.ToLower(strings.TrimSpace(tab.Key))
		if saved.ID == "" {
			saved.ID = fmt.Sprintf("tab_%d", time.Now().UnixNano())
		}
	}
	if saved.Key == "" {
		saved.Key = strings.TrimSpace(tab.ID)
	}
	if saved.Label == "" {
		saved.Label = saved.Key
	}
	if saved.Kind == "" {
		saved.Kind = "custom"
	}
	if err := a.store.UpsertTab(saved); err != nil {
		return nil, err
	}
	tab.ID = saved.ID
	tab.ProjectID = saved.ProjectID
	tab.Key = saved.Key
	if tab.Label == "" {
		tab.Label = saved.Label
	}
	if tab.Kind == "" {
		tab.Kind = saved.Kind
	}
	return &tab, nil
}

func (a *App) UpsertProjectTunnel(t TunnelDraft) (*TunnelInfo, error) {
	projectID := strings.TrimSpace(t.ProjectID)
	if projectID == "" {
		projectID = a.activeProjectID()
	}
	if err := a.store.EnsureProjectExists(projectID); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(t.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	sshHost := strings.TrimSpace(t.SSHHost)
	if sshHost == "" {
		return nil, fmt.Errorf("ssh_host is required")
	}
	remoteHost := strings.TrimSpace(t.RemoteHost)
	if remoteHost == "" {
		return nil, fmt.Errorf("remote_host is required")
	}
	if t.LocalPort == 0 {
		return nil, fmt.Errorf("local_port is required")
	}
	if t.RemotePort == 0 {
		return nil, fmt.Errorf("remote_port is required")
	}

	id := strings.TrimSpace(t.ID)
	if id == "" {
		id = fmt.Sprintf("tunnel_%d", time.Now().UnixNano())
	}

	var password *string
	if strings.TrimSpace(t.Password) != "" {
		p := strings.TrimSpace(t.Password)
		password = &p
	}
	var identity *string
	if strings.TrimSpace(t.IdentityFile) != "" {
		idFile := strings.TrimSpace(t.IdentityFile)
		identity = &idFile
	}

	// On edit (existing id), a blank password/identity must NOT wipe the stored
	// secret — TunnelInfo never exposes it to the UI for re-filling, so the edit
	// form legitimately submits these blank. Keep the current stored value.
	if strings.TrimSpace(t.ID) != "" && (password == nil || identity == nil) {
		if existing, err := a.store.ListTunnels(); err == nil {
			for _, ex := range existing {
				if ex.ID == id {
					if password == nil {
						password = ex.Password
					}
					if identity == nil {
						identity = ex.IdentityFile
					}
					break
				}
			}
		}
	}

	var openURL *string
	if strings.TrimSpace(t.OpenURL) != "" {
		s := strings.TrimSpace(t.OpenURL)
		openURL = &s
	}
	var healthURL *string
	if strings.TrimSpace(t.HealthURL) != "" {
		s := strings.TrimSpace(t.HealthURL)
		healthURL = &s
	}

	sort := t.Sort
	if sort == 0 {
		sort = 100
	}
	enabled := t.Enabled
	if strings.TrimSpace(t.ID) == "" {
		enabled = true
	}

	err := a.store.UpsertTunnel(d.Tunnel{
		ID:           id,
		ProjectID:    projectID,
		Name:         name,
		SSHHost:      sshHost,
		Password:     password,
		IdentityFile: identity,
		LocalPort:    t.LocalPort,
		RemoteHost:   remoteHost,
		RemotePort:   t.RemotePort,
		AutoStart:    t.AutoStart,
		OpenURL:      openURL,
		HealthURL:    healthURL,
		Enabled:      enabled,
		SortOrder:    sort,
	})
	if err != nil {
		return nil, err
	}

	info := &TunnelInfo{
		ID:         id,
		Name:       name,
		SSHHost:    sshHost,
		LocalPort:  t.LocalPort,
		RemoteHost: remoteHost,
		RemotePort: t.RemotePort,
		Sort:       sort,
		Status:     "stopped",
	}
	info.AutoStart = t.AutoStart
	if openURL != nil {
		info.OpenURL = openURL
	}
	if healthURL != nil {
		info.HealthURL = healthURL
	}
	if state, err := a.GetAppStateForProject(projectID); err == nil {
		for _, ti := range state.Tunnels {
			if ti.ID == id {
				if ti.Status != "" {
					info.Status = ti.Status
				}
				if ti.PID != nil {
					v := *ti.PID
					info.PID = &v
				}
				if ti.LastHealthStatus != nil {
					info.LastHealthStatus = ti.LastHealthStatus
				}
				if ti.LastStartedAt != nil {
					info.LastStartedAt = ti.LastStartedAt
				}
				break
			}
		}
	}
	return info, nil
}

func (a *App) DeleteProjectTunnel(projectID, tunnelID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = a.activeProjectID()
	}
	if strings.TrimSpace(tunnelID) == "" {
		return fmt.Errorf("tunnel id is required")
	}
	if a.findTunnelInProject(projectID, tunnelID) == nil {
		return fmt.Errorf("tunnel %q not found", tunnelID)
	}
	a.manager.Stop(tunnelID)
	return a.store.DeleteTunnel(tunnelID)
}

func (a *App) GetServers() ([]ServerEntry, error) {
	return a.GetServersForProject(a.activeProjectID())
}

func (a *App) GetServersForProject(projectID string) ([]ServerEntry, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	projectID = a.resolveProjectID(projectID)
	rows, err := a.store.ListDockerProcesses(projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ServerEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, ServerEntry{
			ID:          r.ID,
			ProjectID:   r.ProjectID,
			Name:        r.Name,
			DisplayName: r.DisplayName,
			Command:     r.Command,
			DockerImage: r.DockerImage,
			AutoStart:   r.AutoStart,
			Enabled:     r.Enabled,
			Sort:        r.SortOrder,
		})
	}
	return out, nil
}

func (a *App) UpsertServer(entry ServerEntry) (*ServerEntry, error) {
	projectID := strings.TrimSpace(entry.ProjectID)
	if projectID == "" {
		projectID = a.activeProjectID()
	}
	if err := a.store.EnsureProjectExists(projectID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		id = fmt.Sprintf("server_%d", time.Now().UnixNano())
	}
	sort := entry.Sort
	if sort == 0 {
		sort = 100
	}
	enabled := entry.Enabled
	if strings.TrimSpace(entry.ID) == "" {
		enabled = true
	}
	display := strings.TrimSpace(entry.DisplayName)
	if display == "" {
		display = name
	}
	rec := d.DockerProcess{
		ID:          id,
		ProjectID:   projectID,
		Name:        name,
		DisplayName: display,
		Command:     strings.TrimSpace(entry.Command),
		DockerImage: strings.TrimSpace(entry.DockerImage),
		AutoStart:   entry.AutoStart,
		Enabled:     enabled,
		SortOrder:   sort,
	}
	if err := a.store.UpsertDockerProcess(rec); err != nil {
		return nil, err
	}
	entry.ID = rec.ID
	entry.ProjectID = rec.ProjectID
	entry.Name = rec.Name
	entry.DisplayName = rec.DisplayName
	entry.Command = rec.Command
	entry.DockerImage = rec.DockerImage
	entry.AutoStart = rec.AutoStart
	entry.Enabled = rec.Enabled
	entry.Sort = rec.SortOrder
	return &entry, nil
}

func (a *App) DeleteServerForProject(projectID, id string) error {
	projectID = a.resolveProjectID(projectID)
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("server id is required")
	}
	list, err := a.GetServersForProject(projectID)
	if err != nil {
		return err
	}
	for _, item := range list {
		if item.ID == id {
			return a.store.DeleteDockerProcess(id)
		}
	}
	return fmt.Errorf("server %q not found", id)
}

func (a *App) DeleteTab(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("tab id is required")
	}
	tab, err := a.store.GetTab(id)
	if err != nil {
		return err
	}
	if isProtectedTabKey(tab.Key) {
		return fmt.Errorf("cannot delete protected tab: %s", tab.Key)
	}
	return a.store.DeleteTab(id)
}

func (a *App) UpdateSettings(st Settings) error {
	settings := d.Settings{
		StartOnLogin:             st.StartOnLogin,
		AutoStartTunnelsOnLaunch: st.AutoStartTunnelsOnLaunch,
		ProcessComposeConfig:     st.ProcessComposeConfig,
	}
	if strings.TrimSpace(st.ActiveProjectID) != "" {
		projectID := strings.TrimSpace(st.ActiveProjectID)
		if err := a.store.EnsureProjectExists(projectID); err != nil {
			return err
		}
		settings.ActiveProjectID = projectID
	}
	return a.store.SetSettings(settings)
}

func (a *App) ReloadOrchestratorConfig() error {
	settings, err := a.store.GetSettings()
	if err != nil {
		return err
	}
	cfg := strings.TrimSpace(settings.ProcessComposeConfig)
	if cfg == "" {
		cfg = "process-compose.yaml"
	}
	return a.orch.ReloadConfig([]string{cfg})
}

func (a *App) StartTunnel(tunnelID string) (*TunnelInfo, error) {
	return a.StartTunnelForProject(a.activeProjectID(), tunnelID)
}

func (a *App) StartTunnelForProject(projectID, tunnelID string) (*TunnelInfo, error) {
	projectID = a.resolveProjectID(projectID)
	t := a.findTunnelInProject(projectID, tunnelID)
	if t == nil {
		return nil, fmt.Errorf("tunnel %q not found", tunnelID)
	}

	proc, err := a.manager.Start(t.ID, t.SSHHost, strPtr(t.Password), strPtr(t.IdentityFile), t.LocalPort, t.RemoteHost, t.RemotePort)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pid := int64(proc.Cmd.Process.Pid)
	_ = a.store.RecordRunStart(proc.RunID, tunnelID)
	_ = a.store.UpsertState(d.TunnelState{
		TunnelID: tunnelID, Status: "running", PID: &pid,
		LastStartedAt: &now, UpdatedAt: now,
	})
	return a.tunnelInfo(t, "running", &pid, &now, nil), nil
}

func (a *App) StopTunnel(tunnelID string) (*TunnelInfo, error) {
	return a.StopTunnelForProject(a.activeProjectID(), tunnelID)
}

func (a *App) StopTunnelForProject(projectID, tunnelID string) (*TunnelInfo, error) {
	projectID = a.resolveProjectID(projectID)
	t := a.findTunnelInProject(projectID, tunnelID)
	if t == nil {
		return nil, fmt.Errorf("tunnel %q not found", tunnelID)
	}
	proc, err := a.manager.Stop(tunnelID)
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		_ = a.store.UpsertState(d.TunnelState{
			TunnelID: tunnelID, Status: "stopped",
			LastStoppedAt: &now, UpdatedAt: now,
		})
		return a.tunnelInfo(t, "stopped", nil, nil, nil), nil
	}
	_ = a.store.RecordRunStop(proc.RunID, nil, nil)
	_ = a.store.UpsertState(d.TunnelState{
		TunnelID: tunnelID, Status: "stopped",
		LastStoppedAt: &now, UpdatedAt: now,
	})
	return a.tunnelInfo(t, "stopped", nil, nil, nil), nil
}

func (a *App) RestartTunnel(tunnelID string) (*TunnelInfo, error) {
	return a.RestartTunnelForProject(a.activeProjectID(), tunnelID)
}

func (a *App) RestartTunnelForProject(projectID, tunnelID string) (*TunnelInfo, error) {
	projectID = a.resolveProjectID(projectID)
	a.manager.Stop(tunnelID)
	time.Sleep(300 * time.Millisecond)
	return a.StartTunnelForProject(projectID, tunnelID)
}

func (a *App) StartAutoTunnels() (*StartResult, error) {
	return a.StartAutoTunnelsForProject(a.activeProjectID())
}

func (a *App) StartAutoTunnelsForProject(projectID string) (*StartResult, error) {
	return a.startTunnelsForProject(projectID, func(t *d.Tunnel) bool { return t.AutoStart })
}

func (a *App) StartAllTunnels() (*StartResult, error) {
	return a.StartAllTunnelsForProject(a.activeProjectID())
}

func (a *App) StartAllTunnelsForProject(projectID string) (*StartResult, error) {
	return a.startTunnelsForProject(projectID, func(_ *d.Tunnel) bool { return true })
}

func (a *App) startTunnelsForProject(projectID string, allow func(*d.Tunnel) bool) (*StartResult, error) {
	projectID = a.resolveProjectID(projectID)
	res := &StartResult{Started: []string{}, AlreadyRunning: []string{}, Skipped: []string{}, Failed: []ActionFailure{}}
	tunnels, err := a.store.ListTunnelsByProject(projectID)
	if err != nil {
		return res, err
	}

	for _, t := range tunnels {
		if !allow(&t) {
			res.Skipped = append(res.Skipped, t.ID)
			continue
		}
		if a.manager.IsRunning(t.ID) {
			res.AlreadyRunning = append(res.AlreadyRunning, t.ID)
			continue
		}
		if _, err := a.StartTunnelForProject(projectID, t.ID); err != nil {
			wailsRuntime.LogWarningf(a.ctx, "auto-start %q: %v", t.ID, err)
			res.Failed = append(res.Failed, ActionFailure{ID: t.ID, Name: t.Name, Error: err.Error()})
			continue
		}
		res.Started = append(res.Started, t.ID)
	}
	return res, nil
}

func (a *App) StopAllTunnels() error {
	return a.StopAllTunnelsForProject(a.activeProjectID())
}

func (a *App) StopAllTunnelsForProject(projectID string) error {
	projectID = a.resolveProjectID(projectID)
	allowed, err := a.store.ListTunnelsByProject(projectID)
	if err != nil {
		return err
	}
	allowedSet := map[string]struct{}{}
	for _, t := range allowed {
		allowedSet[t.ID] = struct{}{}
	}
	ids := a.manager.RunningIDs()
	for _, id := range ids {
		if _, ok := allowedSet[id]; !ok {
			continue
		}
		a.manager.Stop(id)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = a.store.UpsertState(d.TunnelState{
			TunnelID: id, Status: "stopped", LastStoppedAt: &now, UpdatedAt: now,
		})
	}
	return nil
}

func (a *App) CheckTunnelHealth(tunnelID string) (*health.Result, error) {
	return a.CheckTunnelHealthForProject(a.activeProjectID(), tunnelID)
}

func (a *App) CheckTunnelHealthForProject(projectID, tunnelID string) (*health.Result, error) {
	projectID = a.resolveProjectID(projectID)
	t := a.findTunnelInProject(projectID, tunnelID)
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
	_ = a.store.UpsertState(d.TunnelState{
		TunnelID:         tunnelID,
		Status:           status,
		LastHealthStatus: &healthStatus,
		LastHealthAt:     &now,
		UpdatedAt:        now,
	})
	return &result, nil
}

// DatabaseConfig describes the current and pending database backend for the UI.
type DatabaseConfig struct {
	Current      string `json:"current"`       // backend in use right now: "sqlite"|"postgres"
	Effective    string `json:"effective"`     // backend that will be used on next start
	Source       string `json:"source"`        // "config" | "env" | "sqlite"
	URLMasked    string `json:"url_masked"`    // configured URL with password masked
	SQLitePath   string `json:"sqlite_path"`   // local fallback path
	NeedsRestart bool   `json:"needs_restart"` // saved config differs from running backend
}

// maskDBURL hides the password in a postgres URL for safe display.
func maskDBURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return u
	}
	creds := rest[:at]
	if c := strings.Index(creds, ":"); c >= 0 {
		creds = creds[:c] + ":****"
	}
	return u[:i+3] + creds + rest[at:]
}

// GetDatabaseConfig returns the live + saved database backend for the config page.
func (a *App) GetDatabaseConfig() (DatabaseConfig, error) {
	url, source := d.ResolveDBURL()
	cfg := DatabaseConfig{Source: source}
	if a.store != nil {
		cfg.Current = a.store.Driver()
	}
	if url != "" {
		cfg.Effective = "postgres"
		cfg.URLMasked = maskDBURL(url)
	} else {
		cfg.Effective = "sqlite"
	}
	if p, err := d.DataPath(); err == nil {
		cfg.SQLitePath = p
	}
	cfg.NeedsRestart = cfg.Current != "" && cfg.Current != cfg.Effective
	return cfg, nil
}

// TestDatabaseConnection checks a Postgres URL can connect, without saving it.
func (a *App) TestDatabaseConnection(url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("URL is empty")
	}
	return d.TestPostgres(url)
}

// SetDatabaseConfig saves a Postgres URL (after verifying it connects). Takes
// effect on restart. An empty URL reverts to local SQLite.
func (a *App) SetDatabaseConfig(url string) error {
	url = strings.TrimSpace(url)
	if url != "" {
		if err := d.TestPostgres(url); err != nil {
			return fmt.Errorf("connection test failed: %w", err)
		}
	}
	return d.SaveDBURL(url)
}

// ClearDatabaseConfig reverts to local SQLite on next start.
func (a *App) ClearDatabaseConfig() error {
	return d.SaveDBURL("")
}

// MigrateLocalDatabase copies the local SQLite config into the currently
// connected database (must be Postgres), replacing its contents. Returns the
// per-table row counts copied.
func (a *App) MigrateLocalDatabase() (map[string]int, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	return d.MigrateLocalSQLiteInto(a.store)
}

func (a *App) OpenDBFile() error {
	path, err := d.DataPath()
	if err != nil {
		return err
	}
	return hiddenCmd("cmd", "/c", "start", "", path).Start()
}

func (a *App) OpenURL(url string) {
	wailsRuntime.BrowserOpenURL(a.ctx, url)
}

func (a *App) OrchListProcesses() ([]orchestrator.ProcessInfo, error) {
	return a.orch.ListProcesses()
}

func (a *App) OrchStart() error {
	return a.orch.Start()
}

func (a *App) OrchStartProcess(name string) error {
	return a.orch.StartProcess(name)
}

func (a *App) OrchStopProcess(name string) error {
	return a.orch.StopProcess(name)
}

func (a *App) OrchRestartProcess(name string) error {
	return a.orch.RestartProcess(name)
}

func (a *App) OrchStopAll() error {
	return a.orch.StopAll()
}

func (a *App) OrchStartAll() error {
	return a.orch.StartAll()
}

func (a *App) OrchGetProcessLogs(name string, limit int) (string, error) {
	return a.orch.GetProcessLogs(name, limit)
}

func (a *App) OrchShutdown() error {
	return a.orch.Shutdown()
}

func (a *App) OrchIsRunning() bool {
	return a.orch.IsRunning()
}

func (a *App) OrchConfigPath() string {
	cfg, err := a.store.GetSettings()
	if err != nil {
		return "process-compose.yaml"
	}
	if strings.TrimSpace(cfg.ProcessComposeConfig) != "" {
		return cfg.ProcessComposeConfig
	}
	return "process-compose.yaml"
}

func (a *App) onTunnelExit(tunnelID, runID string, exitCode int, stderr string) {
	wailsRuntime.LogWarningf(a.ctx, "tunnel %q exited (code %d): %s", tunnelID, exitCode, stderr)
	now := time.Now().UTC().Format(time.RFC3339)
	_ = a.store.RecordRunStop(runID, &exitCode, &stderr)
	_ = a.store.UpsertState(d.TunnelState{
		TunnelID:      tunnelID,
		Status:        "stopped",
		LastStoppedAt: &now,
		UpdatedAt:     now,
	})
}

func (a *App) findTunnel(id string) *d.Tunnel {
	return a.findTunnelInProject(a.activeProjectID(), id)
}

func (a *App) findTunnelInProject(projectID, id string) *d.Tunnel {
	projectID = a.resolveProjectID(projectID)
	list, err := a.store.ListTunnelsByProject(projectID)
	if err != nil {
		return nil
	}
	for _, t := range list {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

func (a *App) tunnelInfo(t *d.Tunnel, status string, pid *int64, startedAt *string, healthStatus *string) *TunnelInfo {
	return &TunnelInfo{
		ID: t.ID, Name: t.Name, SSHHost: t.SSHHost,
		LocalPort: t.LocalPort, RemoteHost: t.RemoteHost, RemotePort: t.RemotePort,
		Sort:      t.SortOrder,
		AutoStart: t.AutoStart, OpenURL: t.OpenURL, HealthURL: t.HealthURL,
		Status: status, PID: pid,
		LastStartedAt: startedAt, LastHealthStatus: healthStatus,
	}
}

func (a *App) activeProjectID() string {
	return a.resolveProjectID("")
}

func (a *App) resolveProjectID(projectID string) string {
	if a.store == nil {
		return defaultProjectID
	}
	candidate := strings.TrimSpace(projectID)
	if candidate == "" {
		settings, err := a.store.GetSettings()
		if err == nil {
			candidate = strings.TrimSpace(settings.ActiveProjectID)
		}
	}
	if candidate == "" {
		candidate = defaultProjectID
	}
	if err := a.store.EnsureProjectExists(candidate); err != nil {
		candidate = defaultProjectID
	}
	return candidate
}

func (a *App) ensureProjectSeed(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = defaultProjectID
	}
	if err := a.ensureCoreProject(); err != nil {
		return err
	}
	if err := a.ensureProjectTabs(defaultProjectID); err != nil {
		return err
	}
	if err := a.store.EnsureProjectExists(projectID); err != nil {
		if projectID != defaultProjectID {
			wailsRuntime.LogWarningf(a.ctx, "active project %q missing; defaulting to %s", projectID, defaultProjectID)
		}
		projectID = defaultProjectID
	}
	if err := a.ensureProjectTabs(projectID); err != nil {
		return err
	}
	return a.store.SetActiveProjectID(projectID)
}

func (a *App) ensureCoreProject() error {
	if err := a.store.EnsureProjectExists(defaultProjectID); err == nil {
		return nil
	}
	_, err := a.store.UpsertProject(defaultProjectID, "Main", "main", "main", "Global workspace and non-project services", "", 0, true)
	return err
}

func (a *App) ensureProjectTabs(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = defaultProjectID
	}
	defaults := []d.Tab{
		{
			ID:        projectID + "_main",
			ProjectID: projectID,
			Label:     "Discussion",
			Key:       "main",
			Kind:      "main",
			Enabled:   true,
			Sort:      10,
		},
		{
			ID:        projectID + "_ports",
			ProjectID: projectID,
			Label:     "Open Ports",
			Key:       "ports",
			Kind:      "ports",
			Enabled:   true,
			Sort:      20,
		},
		{
			ID:        projectID + "_tunnels",
			ProjectID: projectID,
			Label:     "Tunnels",
			Key:       "tunnels",
			Kind:      "tunnels",
			Enabled:   true,
			Sort:      30,
		},
		{
			ID:        projectID + "_servers",
			ProjectID: projectID,
			Label:     "Servers",
			Key:       "servers",
			Kind:      "servers",
			Enabled:   true,
			Sort:      35,
		},
		{
			ID:        projectID + "_orchestrator",
			ProjectID: projectID,
			Label:     "Orchestrator",
			Key:       "orchestrator",
			Kind:      "orchestrator",
			Enabled:   true,
			Sort:      40,
		},
	}
	for _, tab := range defaults {
		if err := a.store.UpsertTab(tab); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) ListOpenPorts() ([]OpenPort, error) {
	if goRuntime.GOOS != "windows" {
		return nil, fmt.Errorf("open port scan not supported on %s", goRuntime.GOOS)
	}
	return a.listOpenPortsWindows()
}

func (a *App) KillOpenPort(pid int) error {
	if goRuntime.GOOS != "windows" {
		return fmt.Errorf("killing ports is supported only on windows")
	}
	if pid <= 0 {
		return fmt.Errorf("invalid pid")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if proc == nil {
		return fmt.Errorf("process not found")
	}
	if err := proc.Kill(); err != nil {
		return err
	}
	return nil
}

// hiddenCmd builds an exec.Cmd that does not flash a console window on
// Windows. Status polling (ports, process names, docker) runs commands every
// refresh cycle; without HideWindow each spawn pops a visible console window.
func hiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func (a *App) listOpenPortsWindows() ([]OpenPort, error) {
	out, err := hiddenCmd("cmd", "/c", "netstat -ano -p tcp").Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	ports := make([]OpenPort, 0)
	seen := map[string]struct{}{}
	nameCache := map[int]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Proto") || strings.HasPrefix(line, "Active") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "TCP") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		state := strings.ToUpper(parts[3])
		if state != "LISTENING" {
			continue
		}
		pid, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			continue
		}
		addr := parts[1]
		portNum, ok := parsePortFromAddr(addr)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%d", addr, pid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := nameCache[pid]; !ok {
			nameCache[pid] = processNameByPID(pid)
		}
		ports = append(ports, OpenPort{
			Protocol: parts[0],
			Address:  addr,
			Port:     portNum,
			State:    state,
			PID:      pid,
			Process:  nameCache[pid],
		})
	}

	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port == ports[j].Port {
			if ports[i].Protocol == ports[j].Protocol {
				return ports[i].Address < ports[j].Address
			}
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Port < ports[j].Port
	})
	return ports, nil
}

func processNameByPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := hiddenCmd("cmd", "/c", fmt.Sprintf("tasklist /FI \"PID eq %d\" /FO CSV /NH", pid)).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "info:") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) == 0 {
			continue
		}
		name := strings.Trim(parts[0], `"`)
		if name == "" || strings.EqualFold(name, "Image Name") {
			continue
		}
		return name
	}
	return ""
}

func parsePortFromAddr(addr string) (uint16, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 || i >= len(addr)-1 {
		return 0, false
	}
	num := strings.TrimSpace(addr[i+1:])
	parsed, err := strconv.ParseUint(num, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(parsed), true
}

func projectToInfo(p d.Project) ProjectInfo {
	return ProjectInfo{
		ID:          p.ID,
		Name:        p.Name,
		Slug:        p.Slug,
		Kind:        p.Kind,
		Enabled:     p.Enabled,
		SortOrder:   p.SortOrder,
		Description: p.Description,
		Config:      p.Config,
	}
}

func (a *App) systemInfo() SystemInfo {
	info := SystemInfo{}
	path, err := exec.LookPath("docker")
	if err != nil {
		info.Error = "docker not found in PATH"
		return info
	}
	info.DockerAvailable = true
	info.DockerPath = path

	versionOut, err := hiddenCmd(path, "version", "--format", "{{.Client.Version}}").Output()
	if err == nil {
		info.DockerVersion = strings.TrimSpace(string(versionOut))
	}
	if err := hiddenCmd(path, "info").Run(); err != nil {
		info.Error = strings.TrimSpace(strings.TrimPrefix(err.Error(), ""))
		return info
	}
	info.DockerRunning = true
	if info.DockerVersion == "" {
		info.Error = "docker available but version check failed"
	}
	return info
}

func strPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

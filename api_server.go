package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) bootstrapAPIKey() error {
	envKey := strings.TrimSpace(os.Getenv("MIDNIGHT_CONDUIT_API_KEY"))
	if envKey != "" {
		created, _, err := a.store.CreateAPIKey("bootstrap", envKey)
		if err != nil {
			return err
		}
		a.bootstrapKey = envKey
		runtime.LogInfof(a.ctx, "Using API key from env for key '%s' (prefix=%s)", created.Name, created.Prefix)
		return nil
	}

	count, err := a.store.APIKeyCount()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	generated, err := generateControlAPIKey()
	if err != nil {
		return err
	}
	created, raw, err := a.store.CreateAPIKey("bootstrap", generated)
	if err != nil {
		return err
	}
	a.bootstrapKey = raw
	runtime.LogWarningf(a.ctx, "No API key found. Created bootstrap key '%s...' for local access: %s", created.Prefix, raw)
	return nil
}

func generateControlAPIKey() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "mc_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (a *App) startControlServer() error {
	addr := strings.TrimSpace(os.Getenv("MIDNIGHT_CONDUIT_API_ADDR"))
	if addr == "" {
		addr = defaultAPIAddress
	}

	if a.apiServer != nil {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/api/", a.handleAPI)
	mux.HandleFunc("/api", a.handleAPI)
	mux.HandleFunc(defaultMCPPath, a.handleMCP)
	mux.HandleFunc("/", a.handleRoot)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	a.apiAddress = ln.Addr().String()
	a.apiServer = &http.Server{Handler: mux}
	a.apiListener = ln

	go func() {
		if err := a.apiServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			runtime.LogErrorf(a.ctx, "control server error: %v", err)
		}
	}()

	runtime.LogInfof(a.ctx, "Control API available at http://%s (MCP path %s)", a.apiAddress, defaultMCPPath)
	return nil
}

func (a *App) stopControlServer(ctx context.Context) {
	if a.apiServer == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = a.apiServer.Shutdown(shutdownCtx)
	a.apiServer = nil
	if a.apiListener != nil {
		_ = a.apiListener.Close()
		a.apiListener = nil
	}
	a.apiAddress = ""
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"api_address": a.apiAddress,
		"mcp":         fmt.Sprintf("/api + MCP at %s", defaultMCPPath),
		"api_path":    "/api",
	})
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
	})
}

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeRequest(w, r) {
		return
	}

	switch {
	case strings.HasPrefix(r.URL.Path, "/api/state"):
		a.handleAPIState(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/settings"):
		a.handleAPISettings(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/projects"):
		a.handleAPIProjects(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/tunnels"):
		a.handleAPITunnels(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/servers"):
		a.handleAPIServers(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/orchestrator"):
		a.handleAPIOrchestrator(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/tabs"):
		a.handleAPITabs(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/keys"):
		a.handleAPIKeys(w, r)
	case r.URL.Path == "/api":
		a.handleAPIState(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleAPIState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projectID := a.requestProjectID(r)
	state, err := a.GetAppStateForProject(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = writeJSON(w, http.StatusOK, state)
}

func (a *App) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	parts := trimPathSegments(r.URL.Path, "/api/settings")
	if len(parts) != 0 {
		writeError(w, http.StatusBadRequest, "invalid settings path")
		return
	}

	switch r.Method {
	case http.MethodGet:
		state, err := a.GetAppState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, state.Settings)
	case http.MethodPut, http.MethodPatch:
		var st Settings
		if err := decodeJSON(r, &st); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.UpdateSettings(st); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		state, err := a.GetAppState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":   "ok",
			"settings": state.Settings,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAPITunnels(w http.ResponseWriter, r *http.Request) {
	projectID := a.requestProjectID(r)
	parts := trimPathSegments(r.URL.Path, "/api/tunnels")

	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			state, err := a.GetAppStateForProject(projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{
				"tunnels":    state.Tunnels,
				"project_id": projectID,
			})
		case http.MethodPost:
			var payload TunnelDraft
			if err := decodeJSON(r, &payload); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.TrimSpace(payload.ProjectID) == "" {
				payload.ProjectID = projectID
			}
			created, err := a.UpsertProjectTunnel(payload)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, created)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	action := parts[0]

	switch action {
	case "start-all":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		res, err := a.StartAllTunnelsForProject(projectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, res)
		return
	case "start-auto":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		res, err := a.StartAutoTunnelsForProject(projectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, res)
		return
	case "stop-all":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.StopAllTunnelsForProject(projectID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "project_id": projectID})
		return
	}

	if len(parts) == 1 {
		id := urlUnescape(parts[0])
		if r.Method == http.MethodGet {
			tunnel, ok := a.tunnelForAPIForProject(projectID, id)
			if !ok {
				writeError(w, http.StatusNotFound, "tunnel not found")
				return
			}
			_ = writeJSON(w, http.StatusOK, tunnel)
			return
		}
		if r.Method == http.MethodDelete {
			if err := a.DeleteProjectTunnel(projectID, id); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{"status": "deleted", "id": id, "project_id": projectID})
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "invalid tunnel path")
		return
	}

	tunnelID := urlUnescape(parts[0])
	action = parts[1]
	switch action {
	case "start":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		updated, err := a.StartTunnelForProject(projectID, tunnelID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, updated)
	case "stop":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		updated, err := a.StopTunnelForProject(projectID, tunnelID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, updated)
	case "restart":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		updated, err := a.RestartTunnelForProject(projectID, tunnelID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, updated)
	case "health":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, err := a.CheckTunnelHealthForProject(projectID, tunnelID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusNotFound, "unknown tunnel action")
	}
}

func (a *App) handleAPIServers(w http.ResponseWriter, r *http.Request) {
	projectID := a.requestProjectID(r)
	parts := trimPathSegments(r.URL.Path, "/api/servers")

	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			list, err := a.GetServersForProject(projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{"servers": list, "project_id": projectID})
			return
		case http.MethodPost:
			var payload ServerEntry
			if err := decodeJSON(r, &payload); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.TrimSpace(payload.ProjectID) == "" {
				payload.ProjectID = projectID
			}
			saved, err := a.UpsertServer(payload)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, saved)
			return
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
	}

	if len(parts) != 1 {
		writeError(w, http.StatusBadRequest, "invalid server path")
		return
	}

	id := urlUnescape(parts[0])
	if id == "" {
		writeError(w, http.StatusBadRequest, "server id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		list, err := a.GetServersForProject(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, item := range list {
			if item.ID == id {
				_ = writeJSON(w, http.StatusOK, item)
				return
			}
		}
		writeError(w, http.StatusNotFound, "server not found")
	case http.MethodDelete:
		if err := a.DeleteServerForProject(projectID, id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"status": "deleted", "id": id, "project_id": projectID})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAPIOrchestrator(w http.ResponseWriter, r *http.Request) {
	parts := trimPathSegments(r.URL.Path, "/api/orchestrator")

	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		running := a.OrchIsRunning()
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"running":            running,
			"config":             a.OrchConfigPath(),
			"processes_endpoint": "/api/orchestrator/processes",
		})
		return
	}

	action := parts[0]
	if action == "processes" {
		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				list, err := a.OrchListProcesses()
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]interface{}{"processes": list})
				return
			}
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if len(parts) == 2 && parts[1] == "start-all" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if err := a.OrchStartAll(); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
			return
		}
		if len(parts) == 2 && parts[1] == "stop-all" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if err := a.OrchStopAll(); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
			return
		}

		if len(parts) >= 3 {
			name := urlUnescape(parts[1])
			action := parts[2]
			switch action {
			case "start":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				if err := a.OrchStartProcess(name); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]string{"status": "starting"})
			case "stop":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				if err := a.OrchStopProcess(name); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
			case "restart":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				if err := a.OrchRestartProcess(name); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
			case "logs":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				limit := 200
				if raw := r.URL.Query().Get("limit"); raw != "" {
					if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
						limit = parsed
					}
				}
				logs, err := a.OrchGetProcessLogs(name, limit)
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
			default:
				writeError(w, http.StatusNotFound, "unknown process action")
			}
			return
		}

		writeError(w, http.StatusBadRequest, "invalid process path")
		return
	}

	switch action {
	case "start":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.OrchStart(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]string{"status": "starting"})
	case "shutdown":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.OrchShutdown(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
	case "reload":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.ReloadOrchestratorConfig(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
	default:
		writeError(w, http.StatusNotFound, "unknown orchestrator action")
	}
}

func (a *App) handleAPITabs(w http.ResponseWriter, r *http.Request) {
	projectID := a.requestProjectID(r)
	parts := trimPathSegments(r.URL.Path, "/api/tabs")

	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			tabs, err := a.GetTabsForProject(projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{"tabs": tabs, "project_id": projectID})
			return
		case http.MethodPost:
			var payload UITab
			if err := decodeJSON(r, &payload); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.TrimSpace(payload.ProjectID) == "" {
				payload.ProjectID = projectID
			}
			if strings.TrimSpace(payload.Key) == "" {
				writeError(w, http.StatusBadRequest, "key is required")
				return
			}
			if payload.Kind == "" {
				payload.Kind = "custom"
			}
			if strings.TrimSpace(payload.Label) == "" {
				payload.Label = payload.Key
			}
			saved, err := a.UpsertTab(payload)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, saved)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) != 1 {
		writeError(w, http.StatusBadRequest, "invalid tab path")
		return
	}

	id := urlUnescape(parts[0])
	if id == "" {
		writeError(w, http.StatusBadRequest, "tab id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tabs, err := a.GetTabsForProject(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, tab := range tabs {
			if tab.ID == id {
				_ = writeJSON(w, http.StatusOK, tab)
				return
			}
		}
		writeError(w, http.StatusNotFound, "tab not found")
	case http.MethodDelete:
		if err := a.DeleteTab(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAPIProjects(w http.ResponseWriter, r *http.Request) {
	parts := trimPathSegments(r.URL.Path, "/api/projects")

	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			projects, err := a.GetProjects()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{
				"projects":          projects,
				"active_project_id": a.activeProjectID(),
			})
			return
		case http.MethodPost:
			var payload ProjectInfo
			if err := decodeJSON(r, &payload); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			saved, err := a.UpsertProject(payload)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, saved)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) == 1 {
		if strings.TrimSpace(parts[0]) == "active" {
			switch r.Method {
			case http.MethodGet:
				_ = writeJSON(w, http.StatusOK, map[string]interface{}{"active_project_id": a.activeProjectID()})
			case http.MethodPut, http.MethodPost, http.MethodPatch:
				var req struct {
					ID string `json:"project_id"`
				}
				if err := decodeJSON(r, &req); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				if strings.TrimSpace(req.ID) == "" {
					writeError(w, http.StatusBadRequest, "project_id is required")
					return
				}
				if err := a.SetActiveProject(strings.TrimSpace(req.ID)); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				_ = writeJSON(w, http.StatusOK, map[string]interface{}{
					"status":            "active",
					"active_project_id": strings.TrimSpace(req.ID),
				})
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}

		id := urlUnescape(parts[0])
		if id == "" {
			writeError(w, http.StatusBadRequest, "project id required")
			return
		}
		switch r.Method {
		case http.MethodGet:
			project, err := a.GetProject(id)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, project)
		case http.MethodDelete:
			if err := a.DeleteProject(id); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{"status": "deleted", "id": id})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	writeError(w, http.StatusBadRequest, "invalid project path")
}

func (a *App) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeRequest(w, r) {
		return
	}

	parts := trimPathSegments(r.URL.Path, "/api/keys")
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			keys, err := a.store.ListAPIKeys()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{
				"keys":  keys,
				"count": len(keys),
			})
			return
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			name := strings.TrimSpace(req.Name)
			if name == "" {
				name = "api-client"
			}
			created, raw, err := a.store.CreateAPIKey(name, strings.TrimSpace(req.Key))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			_ = writeJSON(w, http.StatusOK, map[string]interface{}{
				"id":         created.ID,
				"name":       created.Name,
				"prefix":     created.Prefix,
				"created_at": created.CreatedAt,
				"key":        raw,
			})
			return
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) != 1 {
		writeError(w, http.StatusBadRequest, "invalid key path")
		return
	}

	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := urlUnescape(parts[0])
	if id == "" {
		writeError(w, http.StatusBadRequest, "key id required")
		return
	}
	if err := a.store.RevokeAPIKey(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": id})
}

func (a *App) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != defaultMCPPath {
		writeError(w, http.StatusNotFound, "unknown mcp path")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !a.authorizeRequest(w, r) {
		return
	}

	type mcpEnvelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}

	var req mcpEnvelope
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resultEnvelope := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
	}

	switch req.Method {
	case "initialize":
		resultEnvelope["result"] = map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"serverInfo": map[string]interface{}{
				"name":    "midnight-conduit",
				"version": "0.1.0",
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]bool{"listChanged": true},
			},
		}
	case "ping":
		resultEnvelope["result"] = map[string]interface{}{"ok": true}
	case "tools/list":
		resultEnvelope["result"] = map[string]interface{}{
			"tools": a.mcpTools(),
		}
	case "tools/call":
		var call struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &call); err != nil {
			writeJSONError(w, http.StatusBadRequest, req.ID, -32602, "invalid mcp call params")
			return
		}
		value, err := a.callMCPTool(call.Name, call.Arguments)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, req.ID, -32000, err.Error())
			return
		}
		resultEnvelope["result"] = map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": asJSONString(value),
				},
			},
			"structuredContent": value,
		}
	default:
		writeJSONError(w, http.StatusNotFound, req.ID, -32601, "method not found")
		return
	}

	_ = writeJSON(w, http.StatusOK, resultEnvelope)
}

func (a *App) callMCPTool(name string, args map[string]interface{}) (interface{}, error) {
	projectID := a.resolveProjectID(strFromArg(args, "project_id"))

	switch name {
	case "get_app_state":
		return a.GetAppStateForProject(projectID)
	case "get_settings":
		appState, err := a.GetAppStateForProject(projectID)
		if err != nil {
			return nil, err
		}
		return appState.Settings, nil
	case "update_settings":
		var st Settings
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &st); err != nil {
			return nil, err
		}
		if err := a.UpdateSettings(st); err != nil {
			return nil, err
		}
		appState, err := a.GetAppStateForProject(projectID)
		if err != nil {
			return nil, err
		}
		return appState.Settings, nil
	case "list_tunnels":
		state, err := a.GetAppStateForProject(projectID)
		if err != nil {
			return nil, err
		}
		return state.Tunnels, nil
	case "get_tunnel":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		item, ok := a.tunnelForAPIForProject(projectID, id)
		if !ok {
			return nil, fmt.Errorf("tunnel not found")
		}
		return item, nil
	case "start_tunnel":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		return a.StartTunnelForProject(projectID, id)
	case "stop_tunnel":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		return a.StopTunnelForProject(projectID, id)
	case "restart_tunnel":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		return a.RestartTunnelForProject(projectID, id)
	case "start_auto_tunnels":
		return a.StartAutoTunnelsForProject(projectID)
	case "start_all_tunnels":
		return a.StartAllTunnelsForProject(projectID)
	case "stop_all_tunnels":
		return map[string]string{"status": "stopped"}, a.StopAllTunnelsForProject(projectID)
	case "check_tunnel_health":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		return a.CheckTunnelHealthForProject(projectID, id)
	case "upsert_tunnel":
		var payload TunnelDraft
		if raw, ok := args["tunnel"]; ok {
			encoded, _ := json.Marshal(raw)
			if err := json.Unmarshal(encoded, &payload); err != nil {
				return nil, fmt.Errorf("invalid tunnel payload")
			}
		} else {
			encoded, _ := json.Marshal(args)
			if err := json.Unmarshal(encoded, &payload); err != nil {
				return nil, fmt.Errorf("invalid tunnel payload")
			}
		}
		if strings.TrimSpace(payload.ProjectID) == "" {
			payload.ProjectID = projectID
		}
		return a.UpsertProjectTunnel(payload)
	case "delete_tunnel":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.DeleteProjectTunnel(projectID, id); err != nil {
			return nil, err
		}
		return map[string]interface{}{"status": "deleted", "id": id, "project_id": projectID}, nil
	case "list_servers":
		list, err := a.GetServersForProject(projectID)
		if err != nil {
			return nil, err
		}
		return list, nil
	case "get_server":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		list, err := a.GetServersForProject(projectID)
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("server not found")
	case "upsert_server":
		var payload ServerEntry
		if raw, ok := args["server"]; ok {
			encoded, _ := json.Marshal(raw)
			if err := json.Unmarshal(encoded, &payload); err != nil {
				return nil, fmt.Errorf("invalid server payload")
			}
		} else {
			encoded, _ := json.Marshal(args)
			if err := json.Unmarshal(encoded, &payload); err != nil {
				return nil, fmt.Errorf("invalid server payload")
			}
		}
		if strings.TrimSpace(payload.ProjectID) == "" {
			payload.ProjectID = projectID
		}
		return a.UpsertServer(payload)
	case "delete_server":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.DeleteServerForProject(projectID, id); err != nil {
			return nil, err
		}
		return map[string]interface{}{"status": "deleted", "id": id, "project_id": projectID}, nil
	case "list_projects":
		projects, err := a.GetProjects()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"projects": projects, "active_project_id": a.activeProjectID()}, nil
	case "get_project":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		return a.GetProject(id)
	case "create_project":
		var p ProjectInfo
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &p); err != nil {
			return nil, err
		}
		return a.UpsertProject(p)
	case "delete_project":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.DeleteProject(id); err != nil {
			return nil, err
		}
		return map[string]string{"status": "deleted", "id": id}, nil
	case "set_active_project":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.SetActiveProject(id); err != nil {
			return nil, err
		}
		return map[string]string{"status": "active", "active_project_id": id}, nil
	case "orch_status":
		return map[string]interface{}{
			"running": a.OrchIsRunning(),
			"config":  a.OrchConfigPath(),
		}, nil
	case "orch_start":
		return map[string]string{"status": "starting"}, a.OrchStart()
	case "orch_shutdown":
		return map[string]string{"status": "stopped"}, a.OrchShutdown()
	case "orch_reload":
		return map[string]string{"status": "reloaded"}, a.ReloadOrchestratorConfig()
	case "orch_start_all":
		return map[string]string{"status": "starting"}, a.OrchStartAll()
	case "orch_stop_all":
		return map[string]string{"status": "stopped"}, a.OrchStopAll()
	case "orch_list_processes":
		return a.OrchListProcesses()
	case "orch_start_process":
		name, err := requireStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "starting"}, a.OrchStartProcess(name)
	case "orch_stop_process":
		name, err := requireStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "stopped"}, a.OrchStopProcess(name)
	case "orch_restart_process":
		name, err := requireStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "restarting"}, a.OrchRestartProcess(name)
	case "orch_process_logs":
		name, err := requireStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		limit := 200
		if v, ok := args["limit"]; ok {
			switch typed := v.(type) {
			case float64:
				if typed > 0 {
					limit = int(typed)
				}
			case int:
				if typed > 0 {
					limit = typed
				}
			}
		}
		logs, err := a.OrchGetProcessLogs(name, limit)
		if err != nil {
			return nil, err
		}
		return map[string]string{"logs": logs}, nil
	case "list_tabs":
		return a.GetTabsForProject(projectID)
	case "get_tab":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		tabs, err := a.GetTabsForProject(projectID)
		if err != nil {
			return nil, err
		}
		for _, tab := range tabs {
			if tab.ID == id {
				return tab, nil
			}
		}
		return nil, fmt.Errorf("tab not found")
	case "upsert_tab":
		var tab UITab
		if raw, ok := args["tab"]; ok {
			encoded, _ := json.Marshal(raw)
			if err := json.Unmarshal(encoded, &tab); err != nil {
				return nil, fmt.Errorf("invalid tab payload")
			}
		}
		if tab.Key == "" {
			tab.Key = strFromArg(args, "key")
		}
		if tab.Key == "" {
			tab.Key = strFromArg(args, "id")
		}
		if tab.Label == "" {
			tab.Label = strFromArg(args, "label")
		}
		if tab.Kind == "" {
			tab.Kind = strFromArg(args, "kind")
		}
		if tab.Kind == "" {
			tab.Kind = "custom"
		}
		if tab.Label == "" {
			tab.Label = tab.Key
		}
		if tab.ProjectID == "" {
			tab.ProjectID = projectID
		}
		if tab.ID == "" {
			tab.ID = tab.Key
		}
		if args["sort"] != nil {
			if i, ok := args["sort"].(float64); ok {
				tab.Sort = int(i)
			}
		}
		if args["enabled"] != nil {
			if b, ok := args["enabled"].(bool); ok {
				tab.Enabled = b
			}
		}
		if _, ok := args["config"]; ok {
			tab.Config = strFromArg(args, "config")
		}
		return a.UpsertTab(tab)
	case "delete_tab":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.DeleteTab(id); err != nil {
			return nil, err
		}
		return map[string]string{"status": "deleted", "id": id}, nil
	case "list_api_keys":
		keys, err := a.store.ListAPIKeys()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"keys": keys}, nil
	case "create_api_key":
		name := strFromArg(args, "name")
		if name == "" {
			name = "client"
		}
		key := strFromArg(args, "key")
		created, raw, err := a.store.CreateAPIKey(name, key)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"id":     created.ID,
			"name":   created.Name,
			"prefix": created.Prefix,
			"key":    raw,
		}, nil
	case "revoke_api_key":
		id, err := requireStringArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		if err := a.store.RevokeAPIKey(id); err != nil {
			return nil, err
		}
		return map[string]string{"status": "revoked", "id": id}, nil
	default:
		return nil, fmt.Errorf("tool not found")
	}
}

func (a *App) mcpTools() []map[string]any {
	return []map[string]any{
		toolSpec("get_app_state", "Return full application state including tunnels, tabs, and paths."),
		toolSpec("get_settings", "Return current settings."),
		toolSpec("update_settings", "Update start-on-launch and process-compose settings."),
		toolSpec("list_tunnels", "List configured tunnels and runtime status."),
		toolSpec("get_tunnel", "Get one tunnel by id."),
		toolSpec("start_tunnel", "Start a tunnel by id."),
		toolSpec("stop_tunnel", "Stop a tunnel by id."),
		toolSpec("restart_tunnel", "Restart a tunnel by id."),
		toolSpec("start_auto_tunnels", "Start all auto-start tunnels."),
		toolSpec("start_all_tunnels", "Start all tunnels."),
		toolSpec("stop_all_tunnels", "Stop all currently running tunnels."),
		toolSpec("check_tunnel_health", "Run HTTP health check for a tunnel."),
		toolSpec("upsert_tunnel", "Create or update a tunnel in the selected project."),
		toolSpec("delete_tunnel", "Delete a tunnel by id in the selected project."),
		toolSpec("list_servers", "List server entries in the selected project."),
		toolSpec("get_server", "Get a server entry by id."),
		toolSpec("upsert_server", "Create or update a server entry."),
		toolSpec("delete_server", "Delete a server entry by id."),
		toolSpec("list_projects", "List all projects and current active project."),
		toolSpec("get_project", "Get a project by id."),
		toolSpec("create_project", "Create or update a project."),
		toolSpec("delete_project", "Delete a project by id."),
		toolSpec("set_active_project", "Set active project by id."),
		toolSpec("orch_status", "Get orchestrator running status and config path."),
		toolSpec("orch_start", "Start process-compose orchestrator."),
		toolSpec("orch_shutdown", "Stop process-compose orchestrator."),
		toolSpec("orch_reload", "Reload orchestrator config and restart services."),
		toolSpec("orch_list_processes", "List orchestrator-managed processes."),
		toolSpec("orch_start_process", "Start one orchestrator process by name."),
		toolSpec("orch_stop_process", "Stop one orchestrator process by name."),
		toolSpec("orch_restart_process", "Restart one orchestrator process by name."),
		toolSpec("orch_start_all", "Start all orchestrator processes."),
		toolSpec("orch_stop_all", "Stop all orchestrator processes."),
		toolSpec("orch_process_logs", "Get logs for an orchestrator process."),
		toolSpec("list_tabs", "List all tabs in DB."),
		toolSpec("get_tab", "Get one tab by id."),
		toolSpec("upsert_tab", "Create or update a tab."),
		toolSpec("delete_tab", "Delete a tab by id."),
		toolSpec("list_api_keys", "List API keys in DB."),
		toolSpec("create_api_key", "Create a new API key."),
		toolSpec("revoke_api_key", "Disable an API key by id."),
	}
}

func toolSpec(name, desc string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": desc,
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (a *App) requestProjectID(r *http.Request) string {
	if r == nil {
		return a.activeProjectID()
	}
	if queryProject := strings.TrimSpace(r.URL.Query().Get("project_id")); queryProject != "" {
		return a.resolveProjectID(queryProject)
	}
	if headerProject := strings.TrimSpace(r.Header.Get("X-Project-ID")); headerProject != "" {
		return a.resolveProjectID(headerProject)
	}
	return a.activeProjectID()
}

func (a *App) tunnelForAPI(id string) (*TunnelInfo, bool) {
	return a.tunnelForAPIForProject(a.activeProjectID(), id)
}

func (a *App) tunnelForAPIForProject(projectID, id string) (*TunnelInfo, bool) {
	projectID = a.resolveProjectID(projectID)
	state, err := a.GetAppStateForProject(projectID)
	if err != nil {
		return nil, false
	}
	for _, t := range state.Tunnels {
		if t.ID == id {
			copy := t
			return &copy, true
		}
	}
	return nil, false
}

func trimPathSegments(path, prefix string) []string {
	s := strings.TrimPrefix(path, prefix)
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "/")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func urlUnescape(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func (a *App) authorizeRequest(w http.ResponseWriter, r *http.Request) bool {
	key := headerAPIKey(r)
	ok, err := a.store.VerifyAPIKey(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "auth lookup failed")
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return false
	}
	return true
}

func headerAPIKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}

func asJSONString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func writeError(w http.ResponseWriter, status int, message string) {
	_ = writeJSON(w, status, map[string]string{"error": message})
}

func writeJSONError(w http.ResponseWriter, status int, id any, code int, message string) {
	_ = writeJSON(w, status, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

func decodeJSON(r *http.Request, out interface{}) error {
	defer r.Body.Close()
	if r == nil {
		return fmt.Errorf("no request body")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return nil
}

func requireStringArg(args map[string]interface{}, key string, required bool) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return "", fmt.Errorf("missing required arg: %s", key)
		}
		return "", nil
	}
	str, ok := value.(string)
	if !ok {
		if required {
			return "", fmt.Errorf("invalid arg %s", key)
		}
		return "", nil
	}
	return strings.TrimSpace(str), nil
}

func strFromArg(args map[string]interface{}, key string) string {
	if value, ok := args[key]; ok {
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func requireIntArg(args map[string]interface{}, key string, required bool) (int, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return 0, fmt.Errorf("missing required arg: %s", key)
		}
		return 0, nil
	}
	switch v := value.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		if required {
			return 0, fmt.Errorf("invalid arg %s", key)
		}
		return 0, nil
	}
}

var _ = requireIntArg

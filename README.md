# MidnightConduit

MidnightConduit is a Wails desktop app for managing SSH tunnels, local process orchestration, project-scoped configuration, and a local API/MCP control surface.

## Run / Build

```bash
wails build
# run output:
# build/bin/midnight-conduit.exe
```

## Database Model

- `app_settings` key/value config
- `projects` for per-project namespaces (`main` is the built-in global project)
- `tunnels` for tunnel definitions
- `ui_tabs` for tab registry
- `tunnel_state` and `tunnel_runs` for runtime state/history
- `docker_processes` and `cli_jobs` for project-scoped orchestration state

The local SQLite database defaults to `%APPDATA%/Tunnel Deck/state.sqlite`.

## Defaults On First Run

- `projects`, `tunnels`, and `ui_tabs` tables are created.
- Default `main` project and default tabs are inserted (`main`, `ports`, `tunnels`, `orchestrator`) per project context.
- App settings defaults are inserted (`process-compose.yaml`, auto-starts off, active project `main`).
- `GET /api/state` and `GetAppState` include `projects`, `active_project_id`, and project-filtered tunnel/tab lists.

## Local API + MCP Control Endpoints

The app includes a local HTTP control plane and an MCP-compatible endpoint.

Example, replacing `<KEY>` with your bootstrap key:

```bash
curl -H "X-API-Key: <KEY>" http://127.0.0.1:8765/api/state
```

MCP server, JSON-RPC over HTTP:

```bash
curl -H "X-API-Key: <KEY>" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://127.0.0.1:8765/mcp
```

- Base URL: `http://<addr>/` (defaults to `127.0.0.1:8765`)
- API base path: `/api`
- MCP endpoint: `/mcp` (JSON-RPC 2.0)
- Authentication: API key required for all `/api/*` and `/mcp` routes.

### Environment Variables

- `MIDNIGHT_CONDUIT_API_ADDR` (optional): override listen address, e.g. `127.0.0.1:9000`.
- `MIDNIGHT_CONDUIT_API_KEY` (optional): seed key used on first launch; if absent, a bootstrap key is generated and stored.

### Runtime Key Management APIs

- `GET /api/keys`
- `POST /api/keys` body `{ "name": "string", "key": "optional existing key" }`
- `DELETE /api/keys/{id}`

### Settings APIs

- `GET /api/settings`
- `PUT /api/settings`

### Project APIs

- `GET /api/projects`
- `POST /api/projects`
- `GET /api/projects/{id}`
- `DELETE /api/projects/{id}`
- `GET /api/projects/active`
- `PUT /api/projects/active` body `{ "project_id": "<id>" }`

For project-scoped calls, send either:

- query param `project_id=<id>`
- header `X-Project-ID: <id>`

If omitted, active project is used.

### Notable Tunnel APIs

- `GET /api/tunnels`
- `GET /api/tunnels/{id}`
- `POST /api/tunnels/{id}/start`
- `POST /api/tunnels/{id}/stop`
- `POST /api/tunnels/{id}/restart`
- `POST /api/tunnels/{id}/health`
- `POST /api/tunnels/start-all`
- `POST /api/tunnels/start-auto`
- `POST /api/tunnels/stop-all`

### Notable Orchestrator APIs

- `GET /api/orchestrator`
- `POST /api/orchestrator/start|shutdown|reload`
- `GET /api/orchestrator/processes`
- `POST /api/orchestrator/processes/start-all|stop-all`
- `POST /api/orchestrator/processes/{name}/start|stop|restart`
- `GET /api/orchestrator/processes/{name}/logs?limit=200`

### MCP Tool Names

The MCP surface mirrors `/api/*` coverage for settings, tabs, tunnels, orchestrator, and keys:
`get_app_state`, `get_settings`, `update_settings`, `list_tunnels`, `get_tunnel`, `start_tunnel`, `stop_tunnel`, `restart_tunnel`, `start_auto_tunnels`, `start_all_tunnels`, `stop_all_tunnels`, `check_tunnel_health`, `list_projects`, `get_project`, `create_project`, `delete_project`, `set_active_project`, `orch_status`, `orch_start`, `orch_shutdown`, `orch_reload`, `orch_list_processes`, `orch_start_process`, `orch_stop_process`, `orch_restart_process`, `orch_start_all`, `orch_stop_all`, `orch_process_logs`, `list_tabs`, `get_tab`, `upsert_tab`, `delete_tab`, `list_api_keys`, `create_api_key`, `revoke_api_key`.

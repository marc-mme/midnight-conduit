package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Settings struct {
	StartOnLogin             bool
	AutoStartTunnelsOnLaunch bool
	ProcessComposeConfig     string
	ActiveProjectID          string
}

type Project struct {
	ID          string
	Name        string
	Slug        string
	Kind        string
	Enabled     bool
	SortOrder   int
	Description string
	Config      string
}

type DockerProcess struct {
	ID          string
	ProjectID   string
	Name        string
	DisplayName string
	Command     string
	DockerImage string
	AutoStart   bool
	Enabled     bool
	SortOrder   int
}

type Tunnel struct {
	ID           string
	ProjectID    string
	Name         string
	SSHHost      string
	Password     *string
	IdentityFile *string
	LocalPort    uint16
	RemoteHost   string
	RemotePort   uint16
	AutoStart    bool
	OpenURL      *string
	HealthURL    *string
	Enabled      bool
	SortOrder    int
}

type Tab struct {
	ID        string
	ProjectID string
	Label     string
	Key       string
	Kind      string
	Enabled   bool
	Sort      int
	Config    string
}

type APIKey struct {
	ID         string
	Name       string
	Prefix     string
	Enabled    bool
	CreatedAt  string
	LastUsedAt *string
}

type TunnelState struct {
	TunnelID         string
	Status           string
	PID              *int64
	LastStartedAt    *string
	LastStoppedAt    *string
	LastHealthStatus *string
	LastHealthAt     *string
	UpdatedAt        string
}

type TunnelRun struct {
	ID         string
	TunnelID   string
	StartedAt  string
	StoppedAt  *string
	ExitCode   *int
	StderrTail *string
}

type Store struct {
	db     *sql.DB
	driver string // "sqlite" or "postgres"
}

// prep adapts a SQLite-dialect query to the active driver. For Postgres it
// rewrites datetime('now') → now()::text (kept as text so the Go layer's
// string timestamps work unchanged) and renumbers ? placeholders to $1,$2,…
// For SQLite it is a no-op. All Exec/Query/QueryRow go through this.
func (s *Store) prep(q string) string {
	if s.driver != "postgres" {
		return q
	}
	q = strings.ReplaceAll(q, "datetime('now')", "now()::text")
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.prep(query), args...)
}

func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.prep(query), args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(s.prep(query), args...)
}

// execDDL splits a multi-statement schema string and runs each statement
// separately — the Postgres driver (pgx) rejects multiple statements in one
// Exec, while SQLite accepts either.
func (s *Store) execDDL(ddl string) error {
	for _, stmt := range strings.Split(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.exec(stmt); err != nil {
			return fmt.Errorf("ddl: %w", err)
		}
	}
	return nil
}

func DataPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	dir := filepath.Join(appData, "Tunnel Deck")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "state.sqlite")
	legacyPath := filepath.Join(dir, "state_v2.sqlite")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			if err := copyFile(legacyPath, path); err != nil {
				return "", err
			}
		}
	}
	return path, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}

	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// dbConfigFile is the small LOCAL bootstrap config — it records which database
// to connect to. It must live locally (not in the DB it points at), so it sits
// next to the local SQLite file regardless of which backend is active.
type dbConfigFile struct {
	URL string `json:"url"`
}

func dbConfigPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	dir := filepath.Join(appData, "Tunnel Deck")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "db_config.json"), nil
}

// LoadDBURL returns the app-level database URL saved locally, or "" if none.
func LoadDBURL() string {
	path, err := dbConfigPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c dbConfigFile
	if json.Unmarshal(data, &c) != nil {
		return ""
	}
	return strings.TrimSpace(c.URL)
}

// SaveDBURL persists the app-level database URL locally. Empty string clears it
// (revert to local SQLite). Takes effect on the next Open() / app restart.
func SaveDBURL(url string) error {
	path, err := dbConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(dbConfigFile{URL: strings.TrimSpace(url)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ResolveDBURL returns the effective Postgres URL and where it came from:
// the saved local config file first, then the env var, else "" (use SQLite).
func ResolveDBURL() (url string, source string) {
	if u := LoadDBURL(); u != "" {
		return u, "config"
	}
	if u := strings.TrimSpace(os.Getenv("MIDNIGHT_CONDUIT_DB_URL")); u != "" {
		return u, "env"
	}
	return "", "sqlite"
}

// TestPostgres verifies a Postgres URL can connect, without changing app state.
func TestPostgres(url string) error {
	database, err := sql.Open("pgx", strings.TrimSpace(url))
	if err != nil {
		return err
	}
	defer database.Close()
	return database.Ping()
}

// Driver reports the active backend ("sqlite" or "postgres").
func (s *Store) Driver() string { return s.driver }

// migrationTables lists every table in parent-first (insert-safe) order:
// projects must precede the tables that FK it.
var migrationTables = []string{
	"app_settings", "api_keys", "projects",
	"tunnels", "ui_tabs", "docker_processes", "cli_jobs",
	"tunnel_state", "tunnel_runs",
}

// MigrateLocalSQLiteInto copies all rows from the local SQLite file into dst
// (which must be Postgres), replacing dst's contents so it mirrors the local
// config. Runs in one transaction; returns per-table copied row counts.
func MigrateLocalSQLiteInto(dst *Store) (map[string]int, error) {
	if dst == nil || dst.driver != "postgres" {
		return nil, fmt.Errorf("current backend is not postgres — nothing to migrate into")
	}
	path, err := DataPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("local sqlite not found at %s", path)
	}
	src, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open local sqlite: %w", err)
	}
	defer src.Close()

	tx, err := dst.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Clear destination child-first so FK constraints hold.
	for i := len(migrationTables) - 1; i >= 0; i-- {
		if _, err := tx.Exec("DELETE FROM " + migrationTables[i]); err != nil {
			return nil, fmt.Errorf("clear %s: %w", migrationTables[i], err)
		}
	}

	counts := map[string]int{}
	for _, table := range migrationTables {
		n, err := copyTable(src, tx, table)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", table, err)
		}
		counts[table] = n
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

func copyTable(src *sql.DB, tx *sql.Tx, table string) (int, error) {
	rows, err := src.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), strings.Join(ph, ", "))

	n := 0
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		// modernc returns TEXT as []byte; Postgres text columns want string.
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		if _, err := tx.Exec(insert, vals...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// Open connects to Postgres when an app-level URL is configured (saved locally
// or via MIDNIGHT_CONDUIT_DB_URL) — shared remote state across machines —
// otherwise falls back to the local SQLite file so the app still runs offline.
func Open() (*Store, error) {
	if url, _ := ResolveDBURL(); url != "" {
		return openPostgres(url)
	}
	return openSQLite()
}

func openSQLite() (*Store, error) {
	path, err := DataPath()
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, err
	}
	if _, err := database.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, err
	}

	s := &Store{db: database, driver: "sqlite"}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seedDefaults(); err != nil {
		return nil, err
	}
	return s, nil
}

func openPostgres(dsn string) (*Store, error) {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := database.Ping(); err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	s := &Store{db: database, driver: "postgres"}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seedDefaults(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	err := s.execDDL(`
		CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL DEFAULT 'project',
			description TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			config TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS tunnel_state (
			tunnel_id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'stopped',
			pid INTEGER,
			last_started_at TEXT,
			last_stopped_at TEXT,
			last_health_status TEXT,
			last_health_at TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS tunnel_runs (
			id TEXT PRIMARY KEY,
			tunnel_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			stopped_at TEXT,
			exit_code INTEGER,
			stderr_tail TEXT
		);
		CREATE TABLE IF NOT EXISTS tunnels (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT 'main',
			name TEXT NOT NULL,
			ssh_host TEXT NOT NULL,
			password TEXT,
			identity_file TEXT,
			local_port INTEGER NOT NULL,
			remote_host TEXT NOT NULL,
			remote_port INTEGER NOT NULL,
			auto_start INTEGER NOT NULL DEFAULT 0,
			open_url TEXT,
			health_url TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS ui_tabs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT 'main',
			label TEXT NOT NULL,
			key TEXT NOT NULL,
			kind TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			config TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
			UNIQUE(project_id, key)
		);
		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_used_at TEXT
		);
		CREATE TABLE IF NOT EXISTS docker_processes (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT,
			command TEXT,
			docker_image TEXT,
			auto_start INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
			UNIQUE(project_id, name)
		);
		CREATE TABLE IF NOT EXISTS cli_jobs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			name TEXT NOT NULL,
			command TEXT NOT NULL,
			runtime TEXT,
			auto_start INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			config TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
			UNIQUE(project_id, name)
		);
	`)
	if err != nil {
		return err
	}
	// PRAGMA-based column migration is SQLite-only and only needed to upgrade
	// pre-existing local DBs. Fresh Postgres tables already include project_id.
	if s.driver != "postgres" {
		if err := s.ensureColumn("ui_tabs", "project_id", "project_id TEXT NOT NULL DEFAULT 'main'"); err != nil {
			return err
		}
		if err := s.ensureColumn("tunnels", "project_id", "project_id TEXT NOT NULL DEFAULT 'main'"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) seedDefaults() error {
	defaults := map[string]string{
		"start_on_login":               "false",
		"auto_start_tunnels_on_launch": "false",
		"process_compose_config":       "process-compose.yaml",
		"active_project_id":            "main",
	}
	for k, v := range defaults {
		if _, err := s.exec(`INSERT INTO app_settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return err
		}
	}

	if _, err := s.exec(`INSERT INTO projects(id, name, slug, kind, enabled, sort_order, created_at, updated_at)
		VALUES('main', 'Main', 'main', 'main', 1, 0, datetime('now'), datetime('now'))
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, updated_at=datetime('now')`); err != nil {
		return err
	}

	defaultTabs := []struct {
		id, label, key, kind string
		sort                 int
	}{
		{"tunnels", "Tunnels", "tunnels", "tunnels", 10},
		{"orchestrator", "Orchestrator", "orchestrator", "orchestrator", 20},
	}
	for _, t := range defaultTabs {
		if _, err := s.exec(`INSERT INTO ui_tabs(id, project_id, label, key, kind, enabled, sort_order)
			VALUES(?, 'main', ?, ?, ?, 1, ?) ON CONFLICT(project_id, key) DO NOTHING`, t.id, t.label, t.key, t.kind, t.sort); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) ensureColumn(table, column, definition string) error {
	var hasColumn bool
	rows, err := s.query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var colName sql.NullString
		var colType sql.NullString
		var notNull sql.NullString
		var defaultVal sql.NullString
		var pk sql.NullString
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultVal, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if colName.String == column {
			hasColumn = true
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if hasColumn {
		return nil
	}
	_, err = s.exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, definition))
	return err
}

func parseBoolFromDB(v string) bool {
	return v == "1" || v == "true" || v == "TRUE"
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) GetSettings() (Settings, error) {
	rows, err := s.query(`SELECT key, value FROM app_settings`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()

	vals := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, err
		}
		vals[k] = v
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}

	return Settings{
		StartOnLogin:             parseBoolFromDB(vals["start_on_login"]),
		AutoStartTunnelsOnLaunch: parseBoolFromDB(vals["auto_start_tunnels_on_launch"]),
		ProcessComposeConfig:     vals["process_compose_config"],
		ActiveProjectID:          vals["active_project_id"],
	}, nil
}

func (s *Store) SetSettings(st Settings) error {
	entries := map[string]string{
		"start_on_login":               boolToString(st.StartOnLogin),
		"auto_start_tunnels_on_launch": boolToString(st.AutoStartTunnelsOnLaunch),
		"process_compose_config":       st.ProcessComposeConfig,
	}
	if st.ActiveProjectID = strings.TrimSpace(st.ActiveProjectID); st.ActiveProjectID != "" {
		entries["active_project_id"] = st.ActiveProjectID
	}

	for k, v := range entries {
		if _, err := s.exec(`INSERT INTO app_settings(key, value, updated_at)
			VALUES(?, ?, datetime('now')) ON CONFLICT(key)
			DO UPDATE SET value = excluded.value, updated_at = datetime('now')`, k, v); err != nil {
			return err
		}
	}
	return nil
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.query(`
		SELECT id, name, slug, kind, enabled, sort_order,
			COALESCE(description, ''), COALESCE(config, '')
		FROM projects
		WHERE enabled = 1
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Kind, &enabled, &p.SortOrder, &p.Description, &p.Config); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpsertProject(id, name, slug, kind, description, config string, sort int, enabled bool) (Project, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = fmt.Sprintf("project_%d", time.Now().UnixNano())
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Project"
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	}
	if kind == "" {
		kind = "project"
	}
	if sort == 0 {
		sort = 100
	}

	_, err := s.exec(`
		INSERT INTO projects(id, name, slug, kind, description, enabled, sort_order, config, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			slug = excluded.slug,
			kind = excluded.kind,
			description = excluded.description,
			enabled = excluded.enabled,
			sort_order = excluded.sort_order,
			config = excluded.config,
			updated_at = datetime('now')
	`, id, name, slug, kind, emptyToNil(description), boolToInt(enabled), sort, emptyToNil(config))
	if err != nil {
		return Project{}, err
	}
	return Project{
		ID:          id,
		Name:        name,
		Slug:        slug,
		Kind:        kind,
		Description: description,
		Enabled:     enabled,
		SortOrder:   sort,
		Config:      config,
	}, nil
}

func (s *Store) DeleteProject(id string) error {
	id = strings.TrimSpace(id)
	if id == "main" {
		return fmt.Errorf("cannot delete main project")
	}
	_, err := s.exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (s *Store) EnsureProjectExists(projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	var count int
	if err := s.queryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return fmt.Errorf("project %q does not exist", projectID)
}

func (s *Store) SetActiveProjectID(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	// Update only this key. Routing through SetSettings here would write a
	// zero-valued Settings struct, blanking process_compose_config and the
	// boolean settings on every startup (called from App.startup).
	_, err := s.exec(`INSERT INTO app_settings(key, value, updated_at)
		VALUES('active_project_id', ?, datetime('now')) ON CONFLICT(key)
		DO UPDATE SET value = excluded.value, updated_at = datetime('now')`, projectID)
	return err
}

func (s *Store) GetProject(id string) (Project, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Project{}, fmt.Errorf("project id required")
	}
	var p Project
	var enabled int
	err := s.queryRow(`SELECT id, name, slug, kind, enabled, sort_order, COALESCE(description, ''), COALESCE(config, '') FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Slug, &p.Kind, &enabled, &p.SortOrder, &p.Description, &p.Config)
	if err != nil {
		return Project{}, err
	}
	p.Enabled = enabled == 1
	return p, nil
}

func (s *Store) randomID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func (s *Store) ListDockerProcesses(projectID string) ([]DockerProcess, error) {
	rows, err := s.query(`
		SELECT id, project_id, name, display_name, command, docker_image, auto_start, enabled, sort_order
		FROM docker_processes
		WHERE project_id = ? AND enabled = 1
		ORDER BY sort_order, name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DockerProcess
	for rows.Next() {
		var p DockerProcess
		var autoStart int
		var enabled int
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.DisplayName, &p.Command, &p.DockerImage, &autoStart, &enabled, &p.SortOrder); err != nil {
			return nil, err
		}
		p.AutoStart = autoStart == 1
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteDockerProcess(id string) error {
	_, err := s.exec(`DELETE FROM docker_processes WHERE id = ?`, id)
	return err
}

func (s *Store) UpsertDockerProcess(proc DockerProcess) error {
	if strings.TrimSpace(proc.ID) == "" {
		proc.ID = s.randomID("docker")
	}
	_, err := s.exec(`
		INSERT INTO docker_processes(id, project_id, name, display_name, command, docker_image, auto_start, enabled, sort_order, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			display_name = excluded.display_name,
			command = excluded.command,
			docker_image = excluded.docker_image,
			auto_start = excluded.auto_start,
			enabled = excluded.enabled,
			sort_order = excluded.sort_order,
			updated_at = datetime('now')
	`, proc.ID, proc.ProjectID, proc.Name, emptyToNil(proc.DisplayName), emptyToNil(proc.Command), emptyToNil(proc.DockerImage), boolToInt(proc.AutoStart), boolToInt(proc.Enabled), proc.SortOrder)
	return err
}

func randomAPIKey() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "mc_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func apiKeyHash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

func (s *Store) keyByHash(hash string) (APIKey, error) {
	var key APIKey
	var enabled int
	err := s.queryRow(`SELECT id, name, key_prefix, enabled, created_at, last_used_at FROM api_keys WHERE key_hash = ?`, hash).
		Scan(&key.ID, &key.Name, &key.Prefix, &enabled, &key.CreatedAt, &key.LastUsedAt)
	if err != nil {
		return APIKey{}, err
	}
	key.Enabled = enabled == 1
	return key, nil
}

func (s *Store) CreateAPIKey(name, raw string) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		generated, err := randomAPIKey()
		if err != nil {
			return APIKey{}, "", err
		}
		raw = generated
	}

	hash := apiKeyHash(raw)
	existing, err := s.keyByHash(hash)
	if err == nil {
		if existing.Enabled {
			return existing, raw, nil
		}
		existing.Enabled = true
		if _, err := s.exec(`UPDATE api_keys SET enabled = 1 WHERE id = ?`, existing.ID); err != nil {
			return APIKey{}, "", err
		}
		return existing, raw, nil
	}

	id := fmt.Sprintf("key_%d", time.Now().UnixNano())
	prefix := raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}

	_, err = s.exec(`INSERT INTO api_keys(id, name, key_prefix, key_hash, enabled, created_at)
		VALUES (?, ?, ?, ?, 1, datetime('now'))`,
		id, name, prefix, hash)
	if err != nil {
		return APIKey{}, "", err
	}
	created, err := s.keyByHash(hash)
	if err != nil {
		return APIKey{}, "", err
	}
	return created, raw, nil
}

func (s *Store) VerifyAPIKey(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	hash := apiKeyHash(raw)
	var id string
	err := s.queryRow(`SELECT id FROM api_keys WHERE key_hash = ? AND enabled = 1`, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _ = s.exec(`UPDATE api_keys SET last_used_at = datetime('now') WHERE id = ?`, id)
	return true, nil
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.query(`SELECT id, name, key_prefix, enabled, created_at, last_used_at FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var item APIKey
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &enabled, &item.CreatedAt, &item.LastUsedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) APIKeyCount() (int, error) {
	var count int
	err := s.queryRow(`SELECT COUNT(*) FROM api_keys WHERE enabled = 1`).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) RevokeAPIKey(id string) error {
	_, err := s.exec(`UPDATE api_keys SET enabled = 0 WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func (s *Store) ListTunnels() ([]Tunnel, error) {
	rows, err := s.query(`
		SELECT id, project_id, name, ssh_host, password, identity_file, local_port,
			remote_host, remote_port, auto_start, open_url, health_url, enabled, sort_order
		FROM tunnels
		WHERE enabled = 1
		ORDER BY sort_order, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tunnel
	for rows.Next() {
		var t Tunnel
		var password, identityFile, openURL, healthURL sql.NullString
		var autoStart int
		var enabled int
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &t.Name, &t.SSHHost, &password, &identityFile,
			&t.LocalPort, &t.RemoteHost, &t.RemotePort,
			&autoStart, &openURL, &healthURL, &enabled, &t.SortOrder,
		); err != nil {
			return nil, err
		}
		if password.Valid {
			v := password.String
			t.Password = &v
		}
		if identityFile.Valid {
			v := identityFile.String
			t.IdentityFile = &v
		}
		if openURL.Valid {
			v := openURL.String
			t.OpenURL = &v
		}
		if healthURL.Valid {
			v := healthURL.String
			t.HealthURL = &v
		}
		t.AutoStart = autoStart == 1
		t.Enabled = enabled == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpsertTunnel(t Tunnel) error {
	_, err := s.exec(`
		INSERT INTO tunnels(
			id, project_id, name, ssh_host, password, identity_file, local_port, remote_host, remote_port, auto_start, open_url, health_url, enabled, sort_order, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			name=excluded.name,
			ssh_host=excluded.ssh_host,
			password=excluded.password,
			identity_file=excluded.identity_file,
			local_port=excluded.local_port,
			remote_host=excluded.remote_host,
			remote_port=excluded.remote_port,
			auto_start=excluded.auto_start,
			open_url=excluded.open_url,
			health_url=excluded.health_url,
			enabled=excluded.enabled,
			sort_order=excluded.sort_order,
			updated_at=datetime('now')
	`, t.ID, t.ProjectID, t.Name, t.SSHHost, t.Password, t.IdentityFile, t.LocalPort, t.RemoteHost, t.RemotePort, boolToInt(t.AutoStart), t.OpenURL, t.HealthURL, boolToInt(t.Enabled), t.SortOrder)
	return err
}

func (s *Store) DeleteTunnel(id string) error {
	_, err := s.exec(`DELETE FROM tunnels WHERE id = ?`, id)
	return err
}

func (s *Store) ListTabs() ([]Tab, error) {
	rows, err := s.query(`
		SELECT id, project_id, label, key, kind, enabled, sort_order, config
		FROM ui_tabs
		ORDER BY sort_order, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tab
	for rows.Next() {
		var t Tab
		var enabled int
		var cfg sql.NullString
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Label, &t.Key, &t.Kind, &enabled, &t.Sort, &cfg); err != nil {
			return nil, err
		}
		if cfg.Valid {
			t.Config = cfg.String
		}
		t.Enabled = enabled == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpsertTab(tab Tab) error {
	_, err := s.exec(`
		INSERT INTO ui_tabs(id, project_id, label, key, kind, enabled, sort_order, config, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(project_id, key) DO UPDATE SET
			project_id = excluded.project_id,
			label=excluded.label,
			kind=excluded.kind,
			enabled=excluded.enabled,
			sort_order=excluded.sort_order,
			config=excluded.config,
			updated_at=datetime('now')
	`, tab.ID, tab.ProjectID, tab.Label, tab.Key, tab.Kind, boolToInt(tab.Enabled), tab.Sort, emptyToNil(tab.Config))
	return err
}

func (s *Store) GetTab(id string) (Tab, error) {
	id = strings.TrimSpace(id)
	var t Tab
	var enabled int
	var cfg sql.NullString
	err := s.queryRow(`
		SELECT id, project_id, label, key, kind, enabled, sort_order, config
		FROM ui_tabs WHERE id = ?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Label, &t.Key, &t.Kind, &enabled, &t.Sort, &cfg)
	if err != nil {
		return Tab{}, err
	}
	t.Enabled = enabled == 1
	if cfg.Valid {
		t.Config = cfg.String
	}
	return t, nil
}

func (s *Store) DeleteTab(id string) error {
	_, err := s.exec(`DELETE FROM ui_tabs WHERE id = ?`, id)
	return err
}

func (s *Store) UpsertState(st TunnelState) error {
	_, err := s.exec(`
		INSERT INTO tunnel_state (tunnel_id, status, pid, last_started_at, last_stopped_at, last_health_status, last_health_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tunnel_id) DO UPDATE SET
			status = excluded.status,
			pid = excluded.pid,
			last_started_at = excluded.last_started_at,
			last_stopped_at = excluded.last_stopped_at,
			last_health_status = excluded.last_health_status,
			last_health_at = excluded.last_health_at,
			updated_at = excluded.updated_at`,
		st.TunnelID, st.Status, st.PID, st.LastStartedAt, st.LastStoppedAt,
		st.LastHealthStatus, st.LastHealthAt, st.UpdatedAt,
	)
	return err
}

func (s *Store) AllStates() ([]TunnelState, error) {
	rows, err := s.query(`SELECT tunnel_id, status, pid, last_started_at, last_stopped_at, last_health_status, last_health_at, updated_at FROM tunnel_state ORDER BY tunnel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []TunnelState
	for rows.Next() {
		var st TunnelState
		if err := rows.Scan(&st.TunnelID, &st.Status, &st.PID, &st.LastStartedAt, &st.LastStoppedAt, &st.LastHealthStatus, &st.LastHealthAt, &st.UpdatedAt); err != nil {
			return nil, err
		}
		states = append(states, st)
	}
	return states, rows.Err()
}

func (s *Store) ListTunnelsByProject(projectID string) ([]Tunnel, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return s.ListTunnels()
	}
	rows, err := s.query(`
		SELECT id, project_id, name, ssh_host, password, identity_file, local_port,
			remote_host, remote_port, auto_start, open_url, health_url, enabled, sort_order
		FROM tunnels
		WHERE enabled = 1 AND project_id = ?
		ORDER BY sort_order, id
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tunnel
	for rows.Next() {
		var t Tunnel
		var password, identityFile, openURL, healthURL sql.NullString
		var autoStart int
		var enabled int
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &t.Name, &t.SSHHost, &password, &identityFile,
			&t.LocalPort, &t.RemoteHost, &t.RemotePort,
			&autoStart, &openURL, &healthURL, &enabled, &t.SortOrder,
		); err != nil {
			return nil, err
		}
		if password.Valid {
			v := password.String
			t.Password = &v
		}
		if identityFile.Valid {
			v := identityFile.String
			t.IdentityFile = &v
		}
		if openURL.Valid {
			v := openURL.String
			t.OpenURL = &v
		}
		if healthURL.Valid {
			v := healthURL.String
			t.HealthURL = &v
		}
		t.AutoStart = autoStart == 1
		t.Enabled = enabled == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListTabsByProject(projectID string) ([]Tab, error) {
	projectID = strings.TrimSpace(projectID)
	var rows *sql.Rows
	var err error
	if projectID == "" {
		rows, err = s.query(`
			SELECT id, project_id, label, key, kind, enabled, sort_order, config
			FROM ui_tabs
			WHERE enabled = 1
			ORDER BY sort_order, label`)
	} else {
		rows, err = s.query(`
			SELECT id, project_id, label, key, kind, enabled, sort_order, config
			FROM ui_tabs
			WHERE enabled = 1 AND project_id = ?
			ORDER BY sort_order, label`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tab
	for rows.Next() {
		var t Tab
		var enabled int
		var cfg sql.NullString
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Label, &t.Key, &t.Kind, &enabled, &t.Sort, &cfg); err != nil {
			return nil, err
		}
		if cfg.Valid {
			t.Config = cfg.String
		}
		t.Enabled = enabled == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteProjectTabs(projectID string) error {
	_, err := s.exec(`DELETE FROM ui_tabs WHERE project_id = ?`, projectID)
	return err
}

func (s *Store) TunnelCount() (int, error) {
	var count int
	err := s.queryRow(`SELECT COUNT(*) FROM tunnels`).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) RecordRunStart(runID, tunnelID string) error {
	_, err := s.exec(`INSERT INTO tunnel_runs (id, tunnel_id, started_at) VALUES (?, ?, ?)`,
		runID, tunnelID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) RecordRunStop(runID string, exitCode *int, stderr *string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.exec(`UPDATE tunnel_runs SET stopped_at = ?, exit_code = ?, stderr_tail = ? WHERE id = ?`,
		now, exitCode, stderr, runID)
	return err
}

func (s *Store) MarkAllStopped() error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.exec(`UPDATE tunnel_state SET status = 'stopped', pid = NULL, updated_at = ?`, now)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func emptyToNil(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

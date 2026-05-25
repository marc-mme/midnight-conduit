package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

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
	db *sql.DB
}

func DataPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	dir := filepath.Join(appData, "Tunnel Deck")
	os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "state.sqlite"), nil
}

func Open() (*Store, error) {
	path, err := DataPath()
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.Exec("PRAGMA journal_mode=WAL")
	database.Exec("PRAGMA foreign_keys=ON")

	s := &Store{db: database}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
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
	`)
	return err
}

func (s *Store) UpsertState(st TunnelState) error {
	_, err := s.db.Exec(`
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
	rows, err := s.db.Query(`SELECT tunnel_id, status, pid, last_started_at, last_stopped_at, last_health_status, last_health_at, updated_at FROM tunnel_state ORDER BY tunnel_id`)
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
	return states, nil
}

func (s *Store) RecordRunStart(runID, tunnelID string) error {
	_, err := s.db.Exec(`INSERT INTO tunnel_runs (id, tunnel_id, started_at) VALUES (?, ?, ?)`,
		runID, tunnelID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) RecordRunStop(runID string, exitCode *int, stderr *string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE tunnel_runs SET stopped_at = ?, exit_code = ?, stderr_tail = ? WHERE id = ?`,
		now, exitCode, stderr, runID)
	return err
}

func (s *Store) MarkAllStopped() error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE tunnel_state SET status = 'stopped', pid = NULL, updated_at = ?`, now)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

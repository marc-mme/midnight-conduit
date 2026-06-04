package orchestrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

type ProcessInfo struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Status      string `json:"status"`
	IsRunning   bool   `json:"is_running"`
	IsReady     bool   `json:"is_ready"`
	IsReadyText string `json:"is_ready_text"`
	Port        uint16 `json:"port"`
	PID         int    `json:"pid"`
	ExitCode    int    `json:"exit_code"`
}

type Orchestrator struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	running bool
	configs []string
	port    int
	client  *http.Client
	output  *bytes.Buffer
}

func New(configPaths []string) *Orchestrator {
	return &Orchestrator{
		configs: configPaths,
		port:    8080,
		client:  &http.Client{Timeout: 2 * time.Second},
	}
}

func (o *Orchestrator) IsRunning() bool {
	if o.apiAlive() {
		o.mu.Lock()
		o.running = true
		o.mu.Unlock()
		return true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running = false
	return false
}

func (o *Orchestrator) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	// If a process-compose server is already on this port, attach to it.
	if o.apiAliveLocked() {
		o.running = true
		o.cmd = nil
		return nil
	}

	bin, err := exec.LookPath("process-compose")
	if err != nil {
		return fmt.Errorf("process-compose executable not found in PATH: %w", err)
	}

	args := []string{}
	for _, c := range o.configs {
		if strings.TrimSpace(c) != "" {
			args = append(args, "-f", c)
		}
	}
	args = append(args, fmt.Sprintf("-p=%d", o.port), "--keep-project", "up", "-t=false")

	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if len(o.configs) > 0 && strings.TrimSpace(o.configs[0]) != "" {
		cmd.Dir = filepath.Dir(o.configs[0])
	}

	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start process-compose: %w", err)
	}

	o.cmd = cmd
	o.output = output
	o.running = true

	go func() {
		cmd.Wait()
		o.mu.Lock()
		if o.cmd == cmd {
			o.running = false
			o.cmd = nil
		}
		o.mu.Unlock()
	}()

	// Wait for API to become ready (up to 8 seconds). If it never appears,
	// return the captured process-compose output instead of silently claiming success.
	for i := 0; i < 40; i++ {
		time.Sleep(200 * time.Millisecond)
		if o.apiAliveLocked() {
			return nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			o.running = false
			o.cmd = nil
			return fmt.Errorf("process-compose exited before API became ready: %s", tail(output.String(), 1600))
		}
	}

	return fmt.Errorf("process-compose API did not become ready on localhost:%d. Output: %s", o.port, tail(output.String(), 1600))
}

func (o *Orchestrator) Shutdown() error {
	o.mu.Lock()
	cmd := o.cmd
	o.mu.Unlock()

	// Preferred path: ask process-compose to stop the project/API gracefully.
	err := o.doPost("/project/stop")
	time.Sleep(500 * time.Millisecond)
	if !o.apiAlive() {
		o.mu.Lock()
		o.running = false
		o.cmd = nil
		o.mu.Unlock()
		return nil
	}

	// Fallback only for the process we spawned ourselves.
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		time.Sleep(300 * time.Millisecond)
		o.mu.Lock()
		o.running = false
		o.cmd = nil
		o.mu.Unlock()
		return nil
	}

	if err != nil {
		return err
	}
	return fmt.Errorf("process-compose server did not shut down")
}

func (o *Orchestrator) ReloadConfig(configPaths []string) error {
	o.mu.Lock()
	o.configs = configPaths
	o.mu.Unlock()
	_ = o.Shutdown()
	time.Sleep(500 * time.Millisecond)
	return o.Start()
}

func (o *Orchestrator) apiURL(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", o.port, path)
}

func (o *Orchestrator) apiAlive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiAliveLocked()
}

func (o *Orchestrator) apiAliveLocked() bool {
	resp, err := o.client.Get(o.apiURL("/processes"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (o *Orchestrator) doGet(path string, result interface{}) error {
	resp, err := o.client.Get(o.apiURL(path))
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1200))
		return fmt.Errorf("api returned %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func (o *Orchestrator) doPost(path string) error {
	req, _ := http.NewRequest("POST", o.apiURL(path), nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1200))
		return fmt.Errorf("returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (o *Orchestrator) doPatch(path string, body interface{}) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest("PATCH", o.apiURL(path), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1200))
		return fmt.Errorf("returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

type pcProcessesResponse struct {
	Data   []pcProcessRaw `json:"data"`
	States []pcProcessRaw `json:"states"`
}

type pcProcessRaw struct {
	Name          string      `json:"name"`
	Namespace     string      `json:"namespace"`
	Status        string      `json:"status"`
	IsRunning     bool        `json:"is_running"`
	IsReady       interface{} `json:"is_ready"`
	HasReadyProbe bool        `json:"has_ready_probe"`
	Port          uint16      `json:"port"`
	PID           int         `json:"pid"`
	ExitCode      int         `json:"exit_code"`
}

func (o *Orchestrator) ListProcesses() ([]ProcessInfo, error) {
	var s pcProcessesResponse
	if err := o.doGet("/processes", &s); err != nil {
		return nil, err
	}

	rows := s.Data
	if len(rows) == 0 && len(s.States) > 0 {
		rows = s.States
	}

	result := make([]ProcessInfo, 0, len(rows))
	for _, p := range rows {
		ready := boolish(p.IsReady)
		if p.IsRunning && !p.HasReadyProbe {
			ready = true
		}
		result = append(result, ProcessInfo{
			Name:        p.Name,
			Namespace:   p.Namespace,
			Status:      p.Status,
			IsRunning:   p.IsRunning,
			IsReady:     ready,
			IsReadyText: fmt.Sprint(p.IsReady),
			Port:        p.Port,
			PID:         p.PID,
			ExitCode:    p.ExitCode,
		})
	}
	return result, nil
}

type pcLogsResponse struct {
	Logs []string `json:"logs"`
}

func (o *Orchestrator) GetProcessLogs(name string, limit int) (string, error) {
	if limit <= 0 {
		limit = 200
	}
	var res pcLogsResponse
	if err := o.doGet(fmt.Sprintf("/process/logs/%s/0/%d", url.PathEscape(name), limit), &res); err != nil {
		return "", err
	}
	for i, line := range res.Logs {
		res.Logs[i] = ansiRE.ReplaceAllString(line, "")
	}
	return strings.Join(res.Logs, "\n"), nil
}

func (o *Orchestrator) StartProcess(name string) error {
	return o.doPost("/process/start/" + url.PathEscape(name))
}

func (o *Orchestrator) StopProcess(name string) error {
	return o.doPatch("/process/stop/"+url.PathEscape(name), nil)
}

func (o *Orchestrator) RestartProcess(name string) error {
	return o.doPost("/process/restart/" + url.PathEscape(name))
}

func (o *Orchestrator) StartAll() error {
	states, err := o.ListProcesses()
	if err != nil {
		return err
	}
	var errs []string
	for _, p := range states {
		if !p.IsRunning {
			if err := o.StartProcess(p.Name); err != nil {
				errs = append(errs, p.Name+": "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("partial failure: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (o *Orchestrator) StopAll() error {
	states, err := o.ListProcesses()
	if err != nil {
		return err
	}
	names := []string{}
	for _, p := range states {
		if p.IsRunning {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return o.doPatch("/processes/stop", names)
}

func (o *Orchestrator) GetConfigPath() string {
	if len(o.configs) > 0 {
		return o.configs[0]
	}
	return ""
}

func boolish(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "true" || s == "ready" || s == "healthy" || s == "running" || s == "ok"
	case float64:
		return x != 0
	default:
		return false
	}
}

func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

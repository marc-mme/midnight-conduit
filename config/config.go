package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Settings struct {
	StartOnLogin             bool   `toml:"start_on_login"`
	AutoStartTunnelsOnLaunch bool   `toml:"auto_start_tunnels_on_launch"`
	ProcessComposeConfig     string `toml:"process_compose_config"`
}

func DefaultSettings() Settings {
	return Settings{AutoStartTunnelsOnLaunch: false}
}

type Tunnel struct {
	ID         string  `toml:"id"`
	Name       string  `toml:"name"`
	SSHHost    string  `toml:"ssh_host"`
	Password   *string `toml:"password"`
	IdentityFile *string `toml:"identity_file"`
	LocalPort  uint16  `toml:"local_port"`
	RemoteHost string  `toml:"remote_host"`
	RemotePort uint16  `toml:"remote_port"`
	AutoStart  bool    `toml:"auto_start"`
	OpenURL    *string `toml:"open_url"`
	HealthURL  *string `toml:"health_url"`
}

type AppConfig struct {
	Version  uint32   `toml:"version"`
	Settings Settings `toml:"settings"`
	Tunnels  []Tunnel `toml:"tunnels"`
}

func (c *AppConfig) Validate() error {
	ids := map[string]bool{}
	ports := map[uint16]bool{}

	for i, t := range c.Tunnels {
		if t.ID == "" {
			return fmt.Errorf("tunnel #%d: id is required", i+1)
		}
		if t.SSHHost == "" {
			return fmt.Errorf("tunnel %q: ssh_host is required", t.ID)
		}
		if t.RemoteHost == "" {
			return fmt.Errorf("tunnel %q: remote_host is required", t.ID)
		}
		if t.LocalPort == 0 {
			return fmt.Errorf("tunnel %q: local_port is required", t.ID)
		}
		if t.RemotePort == 0 {
			return fmt.Errorf("tunnel %q: remote_port is required", t.ID)
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate tunnel id: %q", t.ID)
		}
		if ports[t.LocalPort] {
			return fmt.Errorf("duplicate local port %d (tunnel %q)", t.LocalPort, t.ID)
		}
		ids[t.ID] = true
		ports[t.LocalPort] = true
	}
	return nil
}

func ConfigDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		appData = filepath.Join(home, ".config")
	}
	return filepath.Join(appData, "Tunnel Deck"), nil
}

func DataDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(appData, "Tunnel Deck"), nil
}

func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tunnels.toml"), nil
}

func Load() (*AppConfig, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	os.MkdirAll(dir, 0700)

	path := filepath.Join(dir, "tunnels.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := Default()
		if err := Save(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg AppConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Save(cfg *AppConfig) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "tunnels.toml")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

func Default() *AppConfig {
	return &AppConfig{
		Version:  1,
		Settings: DefaultSettings(),
		Tunnels:  []Tunnel{},
	}
}

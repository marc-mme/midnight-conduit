# MidnightConduit — Tunnel Deck

SSH tunnel manager. Start, stop, restart local port forwards with a click.

Built with **Go + Wails**.

## Quick Start

```bash
# Build
wails build

# Run
./build/bin/tunnel-deck.exe
```

## Structure

```
├── main.go          # Wails entry point
├── app.go           # Backend logic (all Wails-bound methods)
├── wails.json       # Wails project config
├── config/          # TOML config load/save/validate
├── db/              # SQLite state & run history
├── tunnel/          # SSH process spawn/kill with goroutines
├── health/          # HTTP health checks
├── frontend/        # HTML/CSS/JS (dark theme, vanilla)
└── build/           # Build output → tunnel-deck.exe
```

## Config

First launch auto-creates `%APPDATA%/Tunnel Deck/tunnels.toml`.

Uses your system SSH keys/agent (`BatchMode=yes`, `PasswordAuthentication=no`).

## Dev

```bash
wails dev      # Hot-reload dev mode
wails build    # Production build
```

## License

MIT

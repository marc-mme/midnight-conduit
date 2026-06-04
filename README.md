# MidnightConduit — Tunnel Deck

This repository contains two versions:

- **`/`**: current v1 app (TOML-driven)
- **`/v2`**: new SQLite-first app (experimental, non-breaking)

## v2 focus

`/v2` moves configuration from TOML to SQLite and makes UI tabs database-defined so new features can be added without hardcoding a fixed dashboard layout.

`/v2` also exposes:
- SQLite-backed API key storage
- Local HTTP control API at `/api`
- MCP-compatible tool endpoint at `/mcp`

Set `MIDNIGHT_CONDUIT_API_ADDR` and `MIDNIGHT_CONDUIT_API_KEY` before launch to pin the control plane endpoint/key.

## Root Quick Start

```bash
# Build v1
wails build

# Build v2
cd v2
wails build
```

## Build

```bash
wails build
```

## Dev

```bash
wails dev      # Hot-reload dev mode
wails build    # Production build
```

## License

MIT

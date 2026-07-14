# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- GitHub issue/PR templates and a CI workflow (Go build, Node syntax check, Docker build)
- `CODE_OF_CONDUCT.md`, `CHANGELOG.md`

### Fixed
- Default port standardized on `5678` across all Dockerfiles, compose files, and the Caddy reverse-proxy config, matching the app's built-in default and the README

## [0.1.0] - Initial release

- Realtime clipboard sync over WebSocket, bubbles UI with GSAP animation
- Dual backend: single static Go binary (`FROM scratch`, ~8 MB) or Node/`ws` (`server.js`)
- Access-code auth with first-run claim flow, reset, and lockout recovery
- Optional Windows/macOS background agents for native clipboard integration
- Deployment recipes: plain Docker, Tailscale, public + Caddy reverse proxy, Cloudflare Tunnel

# 🫧 BubbleClip

**A realtime clipboard that lives on your network.** Copy on one device, and the text floats up as a bubble on every screen. Click the bubble — it bursts, and the text is on that device's clipboard.

I built this because I was tired of emailing myself links, or opening WhatsApp just to move one line of text from my laptop to my phone. Everything that exists for this is either cloud-based (your clipboard goes through someone else's server) or clunky. BubbleClip runs on your own machine, in one small Docker container, and nothing ever leaves your network.

![BubbleClip screenshot](docs/screenshot.png)

## Quick start

```bash
git clone https://github.com/saivanka/bubbleclip.git
cd bubbleclip
docker compose up -d --build
docker logs bubbleclip   # ← grab your access code from here
```

Open `http://<host-ip>:8080` on any device, enter the code once, and start pasting. That's the whole setup.

No Docker? `npm install && npm start` works too (Node 18+).

## How you use it

The page opens with the cursor already waiting in the text bar. From there:

- **Ctrl+V anywhere** on the page — the text beams instantly, wrapping itself into a bubble that rises to join the others. Or type in the bar and hit Enter.
- **Click any bubble** — it bursts, the text falls away, and it's copied to that device's clipboard.
- Hover a bubble to see which device sent it and when.

Everything is pushed over WebSocket, so bubbles appear on all connected screens the moment someone sends. There's a dark theme and a soft light theme (the ☀️ button, and it follows your system preference).

## Security

Every BubbleClip instance is locked with an access code:

- If you don't set one, a random code is **generated on first run**, saved in the data volume, and printed in the container logs. Devices enter it once; it's remembered per browser.
- Prefer your own? Set `ACCESS_CODE=whatever-you-like`.
- Fully trusted network and don't want auth? `ACCESS_CODE=disabled`.

Under the hood: constant-time code comparison, a 15-minute IP lockout after 10 failed attempts, per-connection WebSocket rate limiting, size limits on every input, security headers + CSP on every response, and the container runs as a non-root user. The clipboard content itself never leaves your network — there is no cloud component, no telemetry, nothing phoning home.

If you expose BubbleClip beyond your LAN, put it behind HTTPS. The easiest way is the included Tailscale setup (below); a reverse proxy like Caddy or nginx works fine too. See [SECURITY.md](SECURITY.md) for the full threat model and how to report issues.

## Background agents (optional)

The web page is enough for most people, but if you want *real* Ctrl+C / Ctrl+V integration — copy in any app on your PC, paste on your phone — there are small agents in [`agents/`](agents/):

- **Windows**: `agents/windows/bubbleclip-agent.ps1` — polls your clipboard, syncs both ways, shows toast notifications. `install-startup.bat` makes it start at login (edit the server URL and code at the top first).
- **macOS**: `agents/macos/bubbleclip-agent.sh` — same idea with native notifications, plus an optional confirm dialog before anything is sent. A launchd plist is included for auto-start.

Both need the access code (`BUBBLECLIP_CODE` env var).

## API

Everything the UI does goes through a small HTTP/WebSocket API, so scripting is easy. Pass the code as an `X-Access-Code` header (or `?code=`):

```bash
# push text
curl -H "X-Access-Code: XXXX-XXXX" -d "hello world" http://host:8080/api/clipboard

# read the current clipboard as raw text
curl -H "X-Access-Code: XXXX-XXXX" "http://host:8080/api/clipboard?plain=1"

# full state (current + history + connected device count)
curl -H "X-Access-Code: XXXX-XXXX" http://host:8080/api/clipboard

# clear history
curl -H "X-Access-Code: XXXX-XXXX" -X DELETE http://host:8080/api/history
```

`/api/health` is unauthenticated so Docker healthchecks and uptime monitors work.

## Configuration

| Variable | Default | What it does |
|---|---|---|
| `PORT` | `8080` | HTTP + WebSocket port |
| `ACCESS_CODE` | *(auto-generated)* | Access code; `disabled` turns auth off |
| `MAX_HISTORY` | `50` | History entries kept |
| `MAX_TEXT_BYTES` | `1048576` | Max clipboard size (1 MB) |
| `DATA_FILE` | `/app/data/clipboard.json` | Where history is persisted |

## Tailscale (nicest setup)

`docker-compose.tailscale.yml` runs BubbleClip as its own machine on your tailnet with zero ports exposed anywhere, plus a real HTTPS cert — which unlocks the browser clipboard API on every device, iPhone included:

```bash
TS_AUTHKEY=tskey-auth-xxxxx docker compose -f docker-compose.tailscale.yml up -d --build
```

Then open `https://bubbleclip.<your-tailnet>.ts.net`. Needs MagicDNS + HTTPS certs enabled in your Tailscale admin console.

## A few honest notes

- The one-click copy on burst needs HTTPS (or localhost) in some browsers — that's a browser clipboard-permission rule, not something I can work around. On plain LAN http the text still lands in the text bar, ready to grab.
- History and the access code live in the Docker volume, so they survive restarts. `docker volume rm bubbleclip_bubbleclip-data` gives you a factory reset (and a new code).
- Text only, by design. Images and files are a different problem and I'd rather do one thing well.

## Contributing

Bug reports, ideas, and PRs are all welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). If you find a security problem, please go through [SECURITY.md](SECURITY.md) instead of a public issue.

## License

[MIT](LICENSE) — do what you like with it.

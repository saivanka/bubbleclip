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

The default image is a single static Go binary with the UI embedded — about **8 MB**, built `FROM scratch`, nothing inside but BubbleClip itself. Prefer Node? The same server exists as `server.js`; build it with `docker build -f Dockerfile.node -t bubbleclip .` (~95 MB) or just run `npm install && npm start`. Both backends speak the identical protocol and share the same data format.

Open `http://<host-ip>:5678` on any device, enter the code once, and start pasting. That's the whole setup.

No Docker? `npm install && npm start` works too (Node 18+).

## How you use it

The page opens with the cursor already waiting in the text bar. From there:

- **Ctrl+V anywhere** on the page — the text beams instantly, wrapping itself into a bubble that rises to join the others. Or type in the bar and hit Enter.
- **Click any bubble** — it bursts, the text falls away, and it's copied to that device's clipboard.
- Hover a bubble to see which device sent it and when.

Everything is pushed over WebSocket, so bubbles appear on all connected screens the moment someone sends. There's a dark theme and a soft light theme (the ☀️ button, and it follows your system preference).

## Security

Every BubbleClip instance is locked with an access code:

- On a fresh install, the **first device to open the web UI gets a one-click "Generate access code" screen** — no digging through logs. The code appears once, with Copy and Share-via-email buttons, and that device is connected. The moment anyone authenticates, this setup window closes for good and every later visitor sees only the locked screen.
- (The code is also printed in the container logs, and saved in the data volume, if you prefer the terminal route.)
- The **🔑 Code** button in the header shows the current code on any connected device — copy it, or hit **Share via email** to send a ready-made invite (code + link + join steps) to whoever you want in. Paste the code into the unlock screen on the new device, and it's connected. No need to dig through logs after the first setup.
- The same dialog has a **Reset code** button: one click generates a fresh code, keeps the device that reset it connected, and kicks everything else off until the new code is entered. Handy if you shared the code with someone you no longer want in.
- **Everyone locked out?** (code reset and then lost, browsers cleared, the phone with the code died…) The lock screen has a "Generate a fresh code" recovery link. It wipes the clipboard contents and signs out every device, then hands the new code to whoever clicked it — so you can always get back in, but nobody can ever use recovery to *read* an existing clipboard. Rate-limited to one per IP per 5 minutes.
- Prefer your own? Set `ACCESS_CODE=whatever-you-like` (this pins it — reset and recovery from the UI are disabled).
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
curl -H "X-Access-Code: XXXX-XXXX" -d "hello world" http://host:5678/api/clipboard

# read the current clipboard as raw text
curl -H "X-Access-Code: XXXX-XXXX" "http://host:5678/api/clipboard?plain=1"

# full state (current + history + connected device count)
curl -H "X-Access-Code: XXXX-XXXX" http://host:5678/api/clipboard

# clear history
curl -H "X-Access-Code: XXXX-XXXX" -X DELETE http://host:5678/api/history

# view the current access code / rotate it
curl -H "X-Access-Code: XXXX-XXXX" http://host:5678/api/code
curl -H "X-Access-Code: XXXX-XXXX" -X POST http://host:5678/api/code/reset
```

`/api/health` is unauthenticated so Docker healthchecks and uptime monitors work.

## Configuration

| Variable | Default | What it does |
|---|---|---|
| `PORT` | `5678` | HTTP + WebSocket port |
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

## Contributing & support

Bug reports, ideas, and PRs are all welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). If you find a security problem, please go through [SECURITY.md](SECURITY.md) instead of a public issue.

Stuck with setup? Email **bubbleclipservice@gmail.com** (also linked from the app's unlock screen) and include your clipboard URL and what you tried.

## License

[MIT](LICENSE) — do what you like with it.

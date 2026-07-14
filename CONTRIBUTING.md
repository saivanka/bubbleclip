# Contributing to BubbleClip

Glad you're here. This is a small project on purpose, so contributing is simple.

## Running it locally

```bash
npm install
npm start          # server on :8080, access code printed in the terminal
```

No build step, no framework. The whole app is three things:

- `server.js` — Node HTTP + WebSocket server, no dependencies beyond `ws`
- `public/index.html` — the entire UI in one file (GSAP is vendored in `public/vendor/`)
- `agents/` — optional clipboard-sync scripts for Windows and macOS

## Pull requests

- Keep the zero-build philosophy: plain Node, plain HTML/CSS/JS. If a change needs webpack, it probably belongs in a fork.
- One feature or fix per PR, with a short description of *why*.
- Test the realtime path with two browser tabs (they count as two devices) before opening the PR.
- If you touch the server, run at least: connect with a wrong access code (should get rejected), a right one (should sync), and a paste round-trip.

## Bugs & ideas

Open an issue with your OS, browser, and how you're running the server (Docker / bare Node / Tailscale). For anything security-related, see [SECURITY.md](SECURITY.md) first — please don't post exploits in public issues.

## Code style

Match what's there: two-space indent, no semicolon golf, comments only where the *why* isn't obvious. Nothing is enforced by tooling yet; use your judgment.

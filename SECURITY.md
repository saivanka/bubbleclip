# Security Policy

## Reporting a vulnerability

If you find a security issue, please email **satyapavanvanka1999@gmail.com** instead of opening a public issue. Include steps to reproduce and what version/commit you tested. I'll respond as quickly as I can and credit you in the fix unless you'd rather stay anonymous.

## Threat model — what BubbleClip does and doesn't defend against

BubbleClip is a self-hosted LAN tool. It's designed so that a stranger on the same network **cannot read or write your clipboard**:

- Access-code authentication on every API call and WebSocket connection (auto-generated on first run if you don't set one)
- Constant-time code comparison, so timing attacks don't leak the code
- Per-IP lockout: 10 failed attempts → 15-minute ban
- WebSocket flood guard (per-connection message rate limit) and size limits on all inputs
- Security headers and a CSP on every response; path traversal blocked on static files
- The Docker container runs as a non-root user

What it deliberately does **not** do:

- **Transport encryption.** Plain `http://` on your LAN means clipboard text crosses the network unencrypted. If that matters for your setup (it should for anything sensitive), use the Tailscale compose file or put a TLS reverse proxy in front. Never port-forward BubbleClip directly to the internet.
- **Per-user accounts.** One code = one shared clipboard. Everyone with the code sees everything. That's the product, not a bug.
- **Encryption at rest.** History is stored as plain JSON in the Docker volume on your own machine.

## Good practice

- Treat the access code like a Wi-Fi password: fine to share with your own devices, rotate it if it leaks (`ACCESS_CODE=newcode`, or delete `secret.json` in the data volume for a fresh one).
- Don't run `ACCESS_CODE=disabled` outside a network where you trust every device.
- Keep the image updated; `ws` is the only runtime dependency, but it does get patches.

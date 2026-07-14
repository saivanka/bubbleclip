/**
 * BubbleClip — realtime network clipboard
 *
 * Holds shared clipboard text in memory (with optional disk persistence)
 * and pushes every change to all connected devices over WebSocket.
 *
 * Security model: every instance is protected by an access code. If you
 * don't set one via ACCESS_CODE, a random code is generated on first run,
 * persisted next to the clipboard data, and printed to the logs. Devices
 * present the code via the X-Access-Code header, ?code= query param, or
 * the WebSocket URL. Set ACCESS_CODE=disabled to run open (trusted LAN only).
 */
const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const WebSocket = require('ws');

const PORT = process.env.PORT || 8080;
const MAX_HISTORY = parseInt(process.env.MAX_HISTORY || '50', 10);
const MAX_TEXT_BYTES = parseInt(process.env.MAX_TEXT_BYTES || '1048576', 10); // 1 MB
const DATA_FILE = process.env.DATA_FILE || path.join(__dirname, 'data', 'clipboard.json');
const SECRET_FILE = process.env.SECRET_FILE || path.join(path.dirname(DATA_FILE), 'secret.json');

// auth throttling
const MAX_AUTH_FAILS = 10;              // failed attempts per IP…
const LOCKOUT_MS = 15 * 60 * 1000;      // …before a 15 minute lockout
const WS_MSG_LIMIT = 30;                // messages allowed…
const WS_MSG_WINDOW_MS = 5000;          // …per 5 seconds per connection

// ---------- access code ----------
let accessCode = null; // null = auth disabled

function generateCode() {
  // unambiguous alphabet (no 0/O, 1/I/L), format XXXX-XXXX
  const alphabet = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789';
  const pick = n => Array.from(crypto.randomBytes(n)).map(b => alphabet[b % alphabet.length]).join('');
  return `${pick(4)}-${pick(4)}`;
}

function initAccessCode() {
  const env = (process.env.ACCESS_CODE || '').trim();
  if (env.toLowerCase() === 'disabled') {
    accessCode = null;
    console.warn('[bubbleclip] ACCESS_CODE=disabled — running without authentication. Only do this on a network you fully trust.');
    return;
  }
  if (env) { accessCode = env; return; }
  try {
    const saved = JSON.parse(fs.readFileSync(SECRET_FILE, 'utf8'));
    if (saved && typeof saved.accessCode === 'string' && saved.accessCode) {
      accessCode = saved.accessCode;
      return;
    }
  } catch (_) { /* first run */ }
  accessCode = generateCode();
  try {
    fs.mkdirSync(path.dirname(SECRET_FILE), { recursive: true });
    fs.writeFileSync(SECRET_FILE, JSON.stringify({ accessCode }), { mode: 0o600 });
  } catch (e) {
    console.error('[bubbleclip] could not persist access code:', e.message);
  }
}

function codeMatches(candidate) {
  if (accessCode === null) return true;
  if (typeof candidate !== 'string' || !candidate) return false;
  // hash both sides so timingSafeEqual gets equal-length buffers
  const a = crypto.createHash('sha256').update(candidate).digest();
  const b = crypto.createHash('sha256').update(accessCode).digest();
  return crypto.timingSafeEqual(a, b);
}

// ---------- per-IP lockout for failed auth ----------
const authFails = new Map(); // ip -> { count, until }

function ipOf(req) {
  return (req.socket && req.socket.remoteAddress) || 'unknown';
}

function isLockedOut(ip) {
  const rec = authFails.get(ip);
  if (!rec) return false;
  if (rec.until && Date.now() < rec.until) return true;
  if (rec.until && Date.now() >= rec.until) authFails.delete(ip);
  return false;
}

function recordAuthFail(ip) {
  const rec = authFails.get(ip) || { count: 0, until: 0 };
  rec.count += 1;
  if (rec.count >= MAX_AUTH_FAILS) rec.until = Date.now() + LOCKOUT_MS;
  authFails.set(ip, rec);
}

function clearAuthFails(ip) { authFails.delete(ip); }

// prune stale entries hourly
setInterval(() => {
  const now = Date.now();
  for (const [ip, rec] of authFails) {
    if (rec.until && now >= rec.until) authFails.delete(ip);
  }
}, 60 * 60 * 1000).unref();

// ---------- state ----------
let state = {
  current: { id: null, text: '', device: null, ts: null },
  history: [], // newest first
};

function loadState() {
  try {
    const raw = fs.readFileSync(DATA_FILE, 'utf8');
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') state = { ...state, ...parsed };
  } catch (_) { /* first run */ }
}

let saveTimer = null;
function saveState() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    try {
      fs.mkdirSync(path.dirname(DATA_FILE), { recursive: true });
      fs.writeFileSync(DATA_FILE, JSON.stringify(state));
    } catch (e) {
      console.error('[bubbleclip] persist failed:', e.message);
    }
  }, 200);
}

function setClipboard(text, device) {
  if (typeof text !== 'string') return { error: 'text must be a string' };
  if (Buffer.byteLength(text, 'utf8') > MAX_TEXT_BYTES) {
    return { error: `text exceeds ${MAX_TEXT_BYTES} bytes` };
  }
  const entry = {
    id: crypto.randomUUID(),
    text,
    device: (device || 'unknown').toString().slice(0, 64),
    ts: Date.now(),
  };
  state.current = entry;
  if (text.trim() !== '') {
    state.history = [entry, ...state.history.filter(h => h.text !== text)].slice(0, MAX_HISTORY);
  }
  saveState();
  broadcast({ type: 'clipboard', current: state.current, history: state.history });
  return { ok: true, entry };
}

function clearHistory() {
  state.history = [];
  saveState();
  broadcast({ type: 'clipboard', current: state.current, history: state.history });
}

// ---------- http ----------
const PUBLIC_DIR = path.join(__dirname, 'public');
const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon' };

const SECURITY_HEADERS = {
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy':
    "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com; " +
    "style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; " +
    "base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
};

function json(res, code, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(code, { 'Content-Type': 'application/json', ...SECURITY_HEADERS });
  res.end(body);
}

function readBody(req, cb) {
  let size = 0;
  const chunks = [];
  req.on('data', c => {
    size += c.length;
    if (size > MAX_TEXT_BYTES + 4096) { req.destroy(); return; }
    chunks.push(c);
  });
  req.on('end', () => cb(Buffer.concat(chunks).toString('utf8')));
}

function authorize(req, url) {
  if (accessCode === null) return { ok: true };
  const ip = ipOf(req);
  if (isLockedOut(ip)) return { ok: false, code: 429, error: 'too many failed attempts — try again later' };
  const candidate = req.headers['x-access-code'] || url.searchParams.get('code') || '';
  if (codeMatches(candidate)) { clearAuthFails(ip); return { ok: true }; }
  recordAuthFail(ip);
  return { ok: false, code: 401, error: 'invalid or missing access code' };
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);

  // --- API ---
  if (url.pathname.startsWith('/api/')) {
    // health stays open so container orchestration can probe it
    if (url.pathname === '/api/health') {
      return json(res, 200, { ok: true, devices: wss.clients.size, uptime: process.uptime() });
    }

    const auth = authorize(req, url);
    if (!auth.ok) return json(res, auth.code, { error: auth.error });

    if (url.pathname === '/api/clipboard') {
      if (req.method === 'GET') {
        // plain=1 → raw text (handy for curl / shell agents)
        if (url.searchParams.get('plain') === '1') {
          res.writeHead(200, {
            'Content-Type': 'text/plain; charset=utf-8',
            ...SECURITY_HEADERS,
            'X-Id': state.current.id || '',
            'X-Device': (state.current.device || '').replace(/[^\x20-\x7E]/g, '?'),
            'X-Ts': state.current.ts || 0,
          });
          return res.end(state.current.text || '');
        }
        return json(res, 200, { current: state.current, history: state.history, devices: wss.clients.size });
      }
      if (req.method === 'POST') {
        return readBody(req, raw => {
          let text, device;
          try {
            const parsed = JSON.parse(raw);
            text = parsed.text; device = parsed.device;
          } catch (_) {
            text = raw; // allow raw text POSTs: curl -d "hello"
          }
          const result = setClipboard(text, device || url.searchParams.get('device') || 'api');
          return result.error ? json(res, 400, result) : json(res, 200, result);
        });
      }
    }
    if (url.pathname === '/api/history' && req.method === 'DELETE') {
      clearHistory();
      return json(res, 200, { ok: true });
    }
    return json(res, 404, { error: 'not found' });
  }

  // --- static UI (open: the app itself shows the unlock screen) ---
  let filePath = url.pathname === '/' ? '/index.html' : url.pathname;
  filePath = path.normalize(path.join(PUBLIC_DIR, filePath));
  if (!filePath.startsWith(PUBLIC_DIR)) { res.writeHead(403, SECURITY_HEADERS); return res.end(); }
  fs.readFile(filePath, (err, data) => {
    if (err) { res.writeHead(404, SECURITY_HEADERS); return res.end('Not found'); }
    res.writeHead(200, { 'Content-Type': MIME[path.extname(filePath)] || 'application/octet-stream', ...SECURITY_HEADERS });
    res.end(data);
  });
});

// ---------- websocket ----------
const wss = new WebSocket.Server({ server, path: '/ws' });

function broadcast(msg, except) {
  const data = JSON.stringify(msg);
  wss.clients.forEach(c => {
    if (c.readyState === WebSocket.OPEN && c !== except) c.send(data);
  });
}

function broadcastPresence() {
  const devices = [...wss.clients].filter(c => c.authed).map(c => c.deviceName || 'unknown');
  broadcast({ type: 'presence', count: devices.length, devices });
}

wss.on('connection', (ws, req) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const ip = ipOf(req);

  if (accessCode !== null) {
    if (isLockedOut(ip)) { ws.close(4029, 'locked out'); return; }
    if (!codeMatches(url.searchParams.get('code') || '')) {
      recordAuthFail(ip);
      ws.close(4001, 'unauthorized');
      return;
    }
    clearAuthFails(ip);
  }

  ws.authed = true;
  ws.deviceName = 'device';
  ws.isAlive = true;
  ws.msgCount = 0;
  ws.msgWindowStart = Date.now();
  ws.on('pong', () => { ws.isAlive = true; });

  // initial sync
  ws.send(JSON.stringify({ type: 'clipboard', current: state.current, history: state.history }));
  broadcastPresence();

  ws.on('message', raw => {
    // simple flood guard
    const now = Date.now();
    if (now - ws.msgWindowStart > WS_MSG_WINDOW_MS) { ws.msgWindowStart = now; ws.msgCount = 0; }
    if (++ws.msgCount > WS_MSG_LIMIT) { ws.close(4008, 'rate limited'); return; }
    if (raw.length > MAX_TEXT_BYTES + 4096) { ws.close(1009, 'message too big'); return; }

    let msg;
    try { msg = JSON.parse(raw); } catch (_) { return; }
    if (msg.type === 'hello') {
      ws.deviceName = (msg.device || 'device').toString().slice(0, 64);
      broadcastPresence();
    } else if (msg.type === 'copy') {
      setClipboard(msg.text, ws.deviceName);
    }
  });

  ws.on('close', broadcastPresence);
});

// heartbeat — drop dead connections
setInterval(() => {
  wss.clients.forEach(ws => {
    if (!ws.isAlive) return ws.terminate();
    ws.isAlive = false;
    ws.ping();
  });
}, 30000);

initAccessCode();
loadState();
server.listen(PORT, () => {
  console.log(`BubbleClip listening on :${PORT}`);
  if (accessCode !== null) {
    console.log('');
    console.log('  ┌──────────────────────────────────────┐');
    console.log(`  │   Access code:  ${accessCode}            │`);
    console.log('  └──────────────────────────────────────┘');
    console.log('  Enter this code on each device the first time it connects.');
    console.log('  (Set your own with ACCESS_CODE=..., or ACCESS_CODE=disabled to turn auth off.)');
    console.log('');
  }
});

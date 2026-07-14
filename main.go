// BubbleClip — realtime network clipboard (Go edition)
//
// Feature-identical port of server.js, compiled to a single static binary
// with the web UI embedded. Exists so the Docker image can be ~8 MB
// (FROM scratch) instead of ~100 MB with the Node runtime.
//
// Protocol, endpoints, JSON shapes and on-disk formats match the Node
// server exactly — the UI and the OS agents work with either backend.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed public
var publicFS embed.FS

// ---------- config ----------

var (
	port         = envOr("PORT", "5678")
	maxHistory   = envInt("MAX_HISTORY", 50)
	maxTextBytes = envInt("MAX_TEXT_BYTES", 1048576) // 1 MB
	dataFile     = envOr("DATA_FILE", filepath.Join("data", "clipboard.json"))
	secretFile   = envOr("SECRET_FILE", "")
	startTime    = time.Now()
)

const (
	maxAuthFails      = 10
	lockoutDuration   = 15 * time.Minute
	recoverCooldown   = 5 * time.Minute
	wsMsgLimit        = 30
	wsMsgWindow       = 5 * time.Second
	wsPingInterval    = 30 * time.Second
	wsReadWait        = 75 * time.Second
	codeAlphabet      = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return def
}

// ---------- shared state ----------

type Entry struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Device string `json:"device"`
	TS     int64  `json:"ts"`
}

type State struct {
	Current Entry   `json:"current"`
	History []Entry `json:"history"`
}

type secretData struct {
	AccessCode string `json:"accessCode"`
	Claimed    bool   `json:"claimed"`
}

type failRecord struct {
	count int
	until time.Time
}

var (
	mu          sync.Mutex
	state       = State{History: []Entry{}}
	accessCode  = "" // "" = auth disabled
	codePinned  = false
	codeClaimed = false
	authFails   = map[string]*failRecord{}
	lastRecover = map[string]time.Time{}
)

// ---------- persistence ----------

func saveStateLocked() {
	_ = os.MkdirAll(filepath.Dir(dataFile), 0o755)
	if b, err := json.Marshal(state); err == nil {
		if err := os.WriteFile(dataFile, b, 0o644); err != nil {
			log.Printf("[bubbleclip] persist failed: %v", err)
		}
	}
}

func loadState() {
	b, err := os.ReadFile(dataFile)
	if err != nil {
		return // first run
	}
	var s State
	if json.Unmarshal(b, &s) == nil {
		if s.History == nil {
			s.History = []Entry{}
		}
		state = s
	}
}

func persistCodeLocked() {
	_ = os.MkdirAll(filepath.Dir(secretFile), 0o755)
	b, _ := json.Marshal(secretData{AccessCode: accessCode, Claimed: codeClaimed})
	if err := os.WriteFile(secretFile, b, 0o600); err != nil {
		log.Printf("[bubbleclip] could not persist access code: %v", err)
	}
}

// ---------- access code ----------

func generateCode() string {
	pick := func(n int) string {
		buf := make([]byte, n)
		_, _ = rand.Read(buf)
		out := make([]byte, n)
		for i, b := range buf {
			out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
		}
		return string(out)
	}
	return pick(4) + "-" + pick(4)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func initAccessCode() {
	env := strings.TrimSpace(os.Getenv("ACCESS_CODE"))
	if strings.EqualFold(env, "disabled") {
		accessCode = ""
		log.Println("[bubbleclip] ACCESS_CODE=disabled — running without authentication. Only do this on a network you fully trust.")
		return
	}
	if env != "" {
		accessCode = env
		codePinned = true
		codeClaimed = true
		return
	}
	if b, err := os.ReadFile(secretFile); err == nil {
		var s secretData
		if json.Unmarshal(b, &s) == nil && s.AccessCode != "" {
			accessCode = s.AccessCode
			codeClaimed = s.Claimed
			return
		}
	}
	accessCode = generateCode()
	persistCodeLocked()
}

func markClaimedLocked() {
	if codeClaimed {
		return
	}
	codeClaimed = true
	persistCodeLocked()
}

func codeMatches(candidate string) bool {
	if accessCode == "" {
		return true
	}
	if candidate == "" {
		return false
	}
	a := sha256.Sum256([]byte(candidate))
	b := sha256.Sum256([]byte(accessCode))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// ---------- per-IP lockout ----------

func ipOf(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func isLockedOutLocked(ip string) bool {
	rec, ok := authFails[ip]
	if !ok {
		return false
	}
	if !rec.until.IsZero() && time.Now().Before(rec.until) {
		return true
	}
	if !rec.until.IsZero() && !time.Now().Before(rec.until) {
		delete(authFails, ip)
	}
	return false
}

func recordAuthFailLocked(ip string) {
	rec, ok := authFails[ip]
	if !ok {
		rec = &failRecord{}
		authFails[ip] = rec
	}
	rec.count++
	if rec.count >= maxAuthFails {
		rec.until = time.Now().Add(lockoutDuration)
	}
}

// ---------- clipboard ops ----------

func setClipboard(text, device string) (Entry, string) {
	if len(text) > maxTextBytes {
		return Entry{}, fmt.Sprintf("text exceeds %d bytes", maxTextBytes)
	}
	if len(device) > 64 {
		device = device[:64]
	}
	if device == "" {
		device = "unknown"
	}
	entry := Entry{ID: newUUID(), Text: text, Device: device, TS: time.Now().UnixMilli()}

	mu.Lock()
	state.Current = entry
	if strings.TrimSpace(text) != "" {
		hist := []Entry{entry}
		for _, h := range state.History {
			if h.Text != text {
				hist = append(hist, h)
			}
		}
		if len(hist) > maxHistory {
			hist = hist[:maxHistory]
		}
		state.History = hist
	}
	saveStateLocked()
	cur, hist := state.Current, append([]Entry{}, state.History...)
	mu.Unlock()

	hub.broadcast(clipboardMsg(cur, hist))
	return entry, ""
}

func clipboardMsg(cur Entry, hist []Entry) map[string]any {
	return map[string]any{"type": "clipboard", "current": cur, "history": hist}
}

// ---------- websocket hub ----------

type client struct {
	conn   *websocket.Conn
	wmu    sync.Mutex
	device string
}

func (c *client) sendJSON(v any) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = c.conn.WriteJSON(v)
}

func (c *client) closeWith(code int, reason string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	_ = c.conn.Close()
}

type hubT struct {
	mu      sync.Mutex
	clients map[*client]bool
}

var hub = &hubT{clients: map[*client]bool{}}

func (h *hubT) add(c *client) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *hubT) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hubT) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hubT) broadcast(v any) {
	h.mu.Lock()
	list := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		c.sendJSON(v)
	}
}

func (h *hubT) closeAll(code int, reason string) {
	h.mu.Lock()
	list := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		list = append(list, c)
	}
	h.clients = map[*client]bool{}
	h.mu.Unlock()
	for _, c := range list {
		c.closeWith(code, reason)
	}
}

func (h *hubT) broadcastPresence() {
	h.mu.Lock()
	devices := make([]string, 0, len(h.clients))
	for c := range h.clients {
		devices = append(devices, c.device)
	}
	h.mu.Unlock()
	h.broadcast(map[string]any{"type": "presence", "count": len(devices), "devices": devices})
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // same as the Node server
}

type wsInMsg struct {
	Type   string `json:"type"`
	Device string `json:"device"`
	Text   string `json:"text"`
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	ip := ipOf(r)

	mu.Lock()
	authEnabled := accessCode != ""
	locked := isLockedOutLocked(ip)
	ok := codeMatches(r.URL.Query().Get("code"))
	if authEnabled && !locked && ok {
		delete(authFails, ip)
		markClaimedLocked()
	} else if authEnabled && !locked && !ok {
		recordAuthFailLocked(ip)
	}
	mu.Unlock()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, device: "device"}

	if authEnabled && locked {
		c.closeWith(4029, "locked out")
		return
	}
	if authEnabled && !ok {
		c.closeWith(4001, "unauthorized")
		return
	}

	conn.SetReadLimit(int64(maxTextBytes + 4096))
	_ = conn.SetReadDeadline(time.Now().Add(wsReadWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadWait))
	})

	hub.add(c)

	mu.Lock()
	cur, hist := state.Current, append([]Entry{}, state.History...)
	mu.Unlock()
	c.sendJSON(clipboardMsg(cur, hist))
	hub.broadcastPresence()

	// per-connection flood guard
	msgCount := 0
	windowStart := time.Now()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsReadWait))

		if time.Since(windowStart) > wsMsgWindow {
			windowStart = time.Now()
			msgCount = 0
		}
		msgCount++
		if msgCount > wsMsgLimit {
			c.closeWith(4008, "rate limited")
			break
		}

		var msg wsInMsg
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "hello":
			name := msg.Device
			if name == "" {
				name = "device"
			}
			if len(name) > 64 {
				name = name[:64]
			}
			c.device = name
			hub.broadcastPresence()
		case "copy":
			setClipboard(msg.Text, c.device)
		}
	}

	hub.remove(c)
	_ = conn.Close()
	hub.broadcastPresence()
}

// ---------- http helpers ----------

var securityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
	"Content-Security-Policy": "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com; " +
		"style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
}

func setSecurityHeaders(w http.ResponseWriter) {
	for k, v := range securityHeaders {
		w.Header().Set(k, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// authorize returns true on success, otherwise writes the error response.
func authorize(w http.ResponseWriter, r *http.Request) bool {
	ip := ipOf(r)
	candidate := r.Header.Get("X-Access-Code")
	if candidate == "" {
		candidate = r.URL.Query().Get("code")
	}

	mu.Lock()
	if accessCode == "" {
		mu.Unlock()
		return true
	}
	if isLockedOutLocked(ip) {
		mu.Unlock()
		jsonError(w, 429, "too many failed attempts — try again later")
		return false
	}
	if codeMatches(candidate) {
		delete(authFails, ip)
		markClaimedLocked()
		mu.Unlock()
		return true
	}
	recordAuthFailLocked(ip)
	mu.Unlock()
	jsonError(w, 401, "invalid or missing access code")
	return false
}

var mimeTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript",
	".css":  "text/css",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".ico":  "image/x-icon",
}

// ---------- handlers ----------

func handleAPI(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// open endpoints
	if p == "/api/health" {
		writeJSON(w, 200, map[string]any{"ok": true, "devices": hub.count(), "uptime": time.Since(startTime).Seconds()})
		return
	}
	if p == "/api/setup" && r.Method == http.MethodGet {
		mu.Lock()
		auth := accessCode != ""
		unclaimed := auth && !codeClaimed
		mu.Unlock()
		writeJSON(w, 200, map[string]bool{"auth": auth, "unclaimed": unclaimed})
		return
	}
	if p == "/api/code/claim" && r.Method == http.MethodPost {
		mu.Lock()
		if accessCode == "" {
			mu.Unlock()
			jsonError(w, 400, "auth is disabled")
			return
		}
		if codeClaimed {
			mu.Unlock()
			jsonError(w, 403, "already claimed")
			return
		}
		markClaimedLocked()
		code := accessCode
		mu.Unlock()
		log.Printf("[bubbleclip] access code claimed by %s via first-run setup", ipOf(r))
		writeJSON(w, 200, map[string]string{"code": code})
		return
	}
	if p == "/api/code/recover" && r.Method == http.MethodPost {
		ip := ipOf(r)
		mu.Lock()
		if accessCode == "" {
			mu.Unlock()
			jsonError(w, 400, "auth is disabled")
			return
		}
		if codePinned {
			mu.Unlock()
			jsonError(w, 400, "code is pinned via the ACCESS_CODE env var — change it there and restart")
			return
		}
		if t, ok := lastRecover[ip]; ok && time.Since(t) < recoverCooldown {
			mu.Unlock()
			jsonError(w, 429, "recovery was just used — wait a few minutes")
			return
		}
		lastRecover[ip] = time.Now()
		state = State{History: []Entry{}}
		saveStateLocked()
		accessCode = generateCode()
		codeClaimed = true
		persistCodeLocked()
		code := accessCode
		mu.Unlock()
		hub.closeAll(4001, "code recovered")
		log.Printf("[bubbleclip] RECOVERY by %s: clipboard wiped, new code → %s", ip, code)
		writeJSON(w, 200, map[string]string{"code": code})
		return
	}

	// everything else requires the code
	if !authorize(w, r) {
		return
	}

	switch {
	case p == "/api/clipboard" && r.Method == http.MethodGet:
		mu.Lock()
		cur, hist := state.Current, append([]Entry{}, state.History...)
		mu.Unlock()
		if r.URL.Query().Get("plain") == "1" {
			setSecurityHeaders(w)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Id", cur.ID)
			w.Header().Set("X-Device", sanitizeHeader(cur.Device))
			w.Header().Set("X-Ts", strconv.FormatInt(cur.TS, 10))
			_, _ = w.Write([]byte(cur.Text))
			return
		}
		writeJSON(w, 200, map[string]any{"current": cur, "history": hist, "devices": hub.count()})

	case p == "/api/clipboard" && r.Method == http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxTextBytes+4096)))
		if err != nil {
			jsonError(w, 400, "body too large")
			return
		}
		text := string(body)
		device := r.URL.Query().Get("device")
		var parsed struct {
			Text   *string `json:"text"`
			Device string  `json:"device"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Text != nil {
			text = *parsed.Text
			if parsed.Device != "" {
				device = parsed.Device
			}
		}
		if device == "" {
			device = "api"
		}
		entry, errMsg := setClipboard(text, device)
		if errMsg != "" {
			jsonError(w, 400, errMsg)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "entry": entry})

	case p == "/api/history" && r.Method == http.MethodDelete:
		mu.Lock()
		state.History = []Entry{}
		saveStateLocked()
		cur, hist := state.Current, append([]Entry{}, state.History...)
		mu.Unlock()
		hub.broadcast(clipboardMsg(cur, hist))
		writeJSON(w, 200, map[string]bool{"ok": true})

	case p == "/api/code" && r.Method == http.MethodGet:
		mu.Lock()
		disabled := accessCode == ""
		code, pinned := accessCode, codePinned
		mu.Unlock()
		if disabled {
			writeJSON(w, 200, map[string]bool{"disabled": true})
			return
		}
		writeJSON(w, 200, map[string]any{"code": code, "pinned": pinned})

	case p == "/api/code/reset" && r.Method == http.MethodPost:
		mu.Lock()
		if accessCode == "" {
			mu.Unlock()
			jsonError(w, 400, "auth is disabled (ACCESS_CODE=disabled)")
			return
		}
		if codePinned {
			mu.Unlock()
			jsonError(w, 400, "code is pinned via the ACCESS_CODE env var — change it there and restart")
			return
		}
		accessCode = generateCode()
		persistCodeLocked()
		code := accessCode
		mu.Unlock()
		hub.closeAll(4001, "code rotated")
		log.Printf("[bubbleclip] access code rotated → %s", code)
		writeJSON(w, 200, map[string]string{"code": code})

	default:
		jsonError(w, 404, "not found")
	}
}

func sanitizeHeader(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x20 && s[i] <= 0x7e {
			out = append(out, s[i])
		} else {
			out = append(out, '?')
		}
	}
	return string(out)
}

func handleStatic(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if strings.Contains(p, "..") {
		setSecurityHeaders(w)
		w.WriteHeader(403)
		return
	}
	data, err := publicFS.ReadFile("public/" + p)
	if err != nil {
		setSecurityHeaders(w)
		w.WriteHeader(404)
		_, _ = w.Write([]byte("Not found"))
		return
	}
	setSecurityHeaders(w)
	ct := mimeTypes[strings.ToLower(filepath.Ext(p))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}

// ---------- main ----------

func main() {
	// container healthcheck mode: bubbleclip -health
	if len(os.Args) > 1 && os.Args[1] == "-health" {
		resp, err := http.Get("http://localhost:" + port + "/api/health")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}

	if secretFile == "" {
		secretFile = filepath.Join(filepath.Dir(dataFile), "secret.json")
	}

	initAccessCode()
	loadState()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWS)
	mux.HandleFunc("/api/", handleAPI)
	mux.HandleFunc("/", handleStatic)

	// keepalive pings, same cadence as the Node server
	go func() {
		t := time.NewTicker(wsPingInterval)
		for range t.C {
			hub.mu.Lock()
			list := make([]*client, 0, len(hub.clients))
			for c := range hub.clients {
				list = append(list, c)
			}
			hub.mu.Unlock()
			for _, c := range list {
				c.wmu.Lock()
				_ = c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				c.wmu.Unlock()
			}
		}
	}()

	log.Printf("BubbleClip listening on :%s", port)
	mu.Lock()
	if accessCode != "" {
		log.Println("")
		log.Println("  ┌──────────────────────────────────────┐")
		log.Printf("  │   Access code:  %s            │", accessCode)
		log.Println("  └──────────────────────────────────────┘")
		log.Println("  Enter this code on each device the first time it connects.")
		if !codeClaimed {
			log.Println("  Tip: the first device to open the web UI can claim this code with one click.")
		}
		log.Println("  (Set your own with ACCESS_CODE=..., or ACCESS_CODE=disabled to turn auth off.)")
		log.Println("")
	}
	mu.Unlock()

	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// Anonymous chat server for Tor Onion Services. No JS, no bloat. One binary.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// --- Configuration ---

const (
	maxMessages = 200
	maxMsgLen   = 500
	maxBodySize = 2048
	rateLimitS  = 1.0
	dupWindowS  = 30.0
	maxStreams   = 100
	pingInterval = 30 * time.Second
)

// --- Nickname generation ---

var words = []string{
	"Ash", "Bark", "Bear", "Blade", "Blaze", "Bolt", "Bone", "Briar",
	"Brook", "Cairn", "Cave", "Cinder", "Claw", "Clay", "Cliff", "Cloud",
	"Coal", "Cobra", "Coral", "Crane", "Creek", "Crow", "Dagger", "Dawn",
	"Deer", "Drift", "Dune", "Dusk", "Eagle", "Echo", "Ember", "Falcon",
	"Fang", "Fern", "Flare", "Flame", "Flint", "Fog", "Forge", "Fox",
	"Frost", "Gale", "Ghost", "Glacier", "Glyph", "Granite", "Grove", "Hail",
	"Hawk", "Haze", "Hollow", "Hornet", "Iron", "Ivy", "Jackal", "Jade",
	"Lark", "Lava", "Lichen", "Lynx", "Marsh", "Mist", "Moon", "Moss",
	"Moth", "Night", "Oak", "Obsidian", "Onyx", "Orca", "Osprey", "Owl",
	"Peak", "Pebble", "Pine", "Plume", "Quartz", "Raven", "Reed", "Ridge",
	"Root", "Ruin", "Rust", "Sage", "Shard", "Slate", "Smoke", "Snake",
	"Spark", "Star", "Stone", "Storm", "Thorn", "Thunder", "Tide", "Viper",
	"Wave", "Willow", "Wolf", "Wren",
}

var nickRE = regexp.MustCompile(`^[A-Za-z]{2,10}-[0-9a-f]{4}$`)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func makeNick() string {
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return words[int(b[0])%len(words)] + "-" + randomHex(2)
}

// --- Message store ---

type message struct {
	ID   int
	Nick string
	Time string
	Text string
}

type store struct {
	mu        sync.RWMutex
	msgs      []message
	counter   int
	lastSent  map[string]time.Time
	listeners []chan struct{}
	streams   int
}

func newStore() *store {
	return &store{
		lastSent: make(map[string]time.Time),
	}
}

func (s *store) add(nick, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Rate limit
	if last, ok := s.lastSent[nick]; ok && now.Sub(last).Seconds() < rateLimitS {
		return false
	}

	// Duplicate filter (per nick, 30s window)
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if s.msgs[i].Nick == nick {
			if s.msgs[i].Text == text && now.Sub(s.lastSent[nick]).Seconds() < dupWindowS {
				return false
			}
			break
		}
	}

	s.msgs = append(s.msgs, message{
		ID:   s.counter,
		Nick: nick,
		Time: now.UTC().Format("2006-01-02T15:04Z"),
		Text: text,
	})
	s.counter++
	s.lastSent[nick] = now

	// Ring buffer
	if len(s.msgs) > maxMessages {
		s.msgs = s.msgs[len(s.msgs)-maxMessages:]
	}

	// Clean old rate limit entries
	if len(s.lastSent) > 256 {
		cutoff := now.Add(-time.Duration(dupWindowS * float64(time.Second)))
		for k, v := range s.lastSent {
			if v.Before(cutoff) {
				delete(s.lastSent, k)
			}
		}
	}

	// Notify listeners (channels are persistent, not cleared)
	for _, ch := range s.listeners {
		select {
		case ch <- struct{}{}:
		default: // already has pending notification
		}
	}

	return true
}

func (s *store) subscribe() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	s.listeners = append(s.listeners, ch)
	return ch
}

func (s *store) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.listeners {
		if c == ch {
			s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
			return
		}
	}
}

func (s *store) snapshot() []message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]message, len(s.msgs))
	copy(cp, s.msgs)
	return cp
}

func (s *store) since(lastID int) []message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []message
	for _, m := range s.msgs {
		if m.ID > lastID {
			out = append(out, m)
		}
	}
	return out
}

func (s *store) addStream() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams >= maxStreams {
		return false
	}
	s.streams++
	return true
}

func (s *store) removeStream() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams--
}

func (s *store) streamCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streams
}

func (s *store) msgCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.msgs)
}

// --- Cookie handling ---

func getNick(r *http.Request, w http.ResponseWriter) string {
	if c, err := r.Cookie("nick"); err == nil && nickRE.MatchString(c.Value) {
		return c.Value
	}
	nick := makeNick()
	http.SetCookie(w, &http.Cookie{
		Name:     "nick",
		Value:    nick,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
	return nick
}

// --- HTML rendering ---

func renderMsg(m message) string {
	hhmm := ""
	if len(m.Time) >= 16 {
		hhmm = m.Time[11:16]
	}
	return fmt.Sprintf(`<div class="msg"><span class="ts">%s</span> <span class="nick">%s</span> <span class="text">%s</span></div>`+"\n",
		html.EscapeString(hhmm),
		html.EscapeString(m.Nick),
		html.EscapeString(m.Text))
}

const msgHead = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8">
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; background: #0d0d0d; }
  #s {
    height: 100%;
    display: flex;
    flex-direction: column-reverse;
    overflow-y: auto;
    scrollbar-color: #222 #0d0d0d;
    scrollbar-width: thin;
  }
  #m {
    color: #c0c0c0;
    font-family: monospace;
    font-size: 14px;
    padding: 12px;
    flex-shrink: 0;
  }
  .msg { margin-bottom: 4px; word-wrap: break-word; }
  .msg .ts { color: #666; }
  .msg .nick { color: #888; font-weight: bold; }
  .msg .text { color: #00cc66; }
</style>
</head>
<body><div id="s"><div id="m">
`

const chatHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>onionchat</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { background: #0d0d0d; display: flex; flex-direction: column; height: 100vh; }
iframe { border: none; }
iframe#chat { flex: 1; background: #0d0d0d; animation: fadein 1.2s ease-in-out; }
@keyframes fadein { from { opacity: 0; } to { opacity: 1; } }
#input { border-top: 1px solid #222; padding: 8px 12px; background: #111; display: flex; gap: 8px; }
#input input[type=text] { flex: 1; background: #0d0d0d; color: #00cc66; border: 1px solid #333; padding: 8px; font-family: monospace; font-size: 14px; outline: none; }
#input input[type=text]:focus { border-color: #00cc66; }
#input button { background: #00cc66; color: #0d0d0d; border: none; padding: 8px 16px; font-family: monospace; font-size: 14px; font-weight: bold; cursor: pointer; }
#input button:hover { background: #00ff7f; }
#bar { background: #0d0d0d; padding: 4px 12px; }
#bar iframe { border: none; height: 14px; width: 100%; background: transparent; display: block; }
</style>
</head>
<body>
<iframe src="/messages" id="chat"></iframe>
<form id="input" action="/send" method="post" autocomplete="off">
<input type="text" name="msg" placeholder="Message..." maxlength="500" autofocus>
<button type="submit">&gt;</button>
</form>
<div id="bar"><iframe src="/clock"></iframe></div>
</body>
</html>`

const fullHTML = `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
	`<meta http-equiv="refresh" content="10;url=/messages"><style>` +
	`*{margin:0;padding:0;box-sizing:border-box}` +
	`body{background:#0d0d0d;color:#666;font-family:monospace;font-size:14px;` +
	`display:flex;justify-content:center;align-items:center;height:100vh}` +
	`</style></head><body>Chat full — retrying...</body></html>`

// --- Security headers ---

var securityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"Referrer-Policy":        "no-referrer",
	"X-Frame-Options":        "SAMEORIGIN",
	"Cache-Control":          "no-store",
	"Permissions-Policy":     "camera=(), microphone=(), geolocation=(), interest-cohort=()",
	"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; frame-src 'self'; form-action 'self'; img-src 'self'",
	"Server":                 "onionchat",
}

func setSecurityHeaders(w http.ResponseWriter) {
	for k, v := range securityHeaders {
		w.Header().Set(k, v)
	}
}

// --- Handlers ---

func handleIndex(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			setSecurityHeaders(w)
			w.WriteHeader(404)
			return
		}
		setSecurityHeaders(w)
		getNick(r, w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, chatHTML)
	}
}

func handleMessages(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)

		if !s.addStream() {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, fullHTML)
			return
		}
		defer s.removeStream()

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "", 500)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, msgHead)

		// Subscribe BEFORE snapshot to close the race window —
		// any message arriving between snapshot and subscribe
		// will still trigger the channel.
		ch := s.subscribe()
		defer s.unsubscribe(ch)

		msgs := s.snapshot()
		lastID := -1
		for _, m := range msgs {
			fmt.Fprint(w, renderMsg(m))
			lastID = m.ID
		}
		flusher.Flush()

		timer := time.NewTimer(pingInterval)
		defer timer.Stop()

		for {
			select {
			case <-ch:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(pingInterval)
				for _, m := range s.since(lastID) {
					fmt.Fprint(w, renderMsg(m))
					lastID = m.ID
				}
				flusher.Flush()
			case <-timer.C:
				timer.Reset(pingInterval)
				fmt.Fprint(w, "<!-- ping -->\n")
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

func handleSend(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)

		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(413)
			return
		}

		nick := getNick(r, w)
		text := strings.TrimSpace(r.FormValue("msg"))
		if runes := []rune(text); len(runes) > maxMsgLen {
			text = string(runes[:maxMsgLen])
		}

		if text != "" {
			s.add(nick, text)
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func handleClock(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">`+
		`<meta http-equiv="refresh" content="30">`+
		`<style>*{margin:0;padding:0}body{background:transparent;`+
		`font-family:monospace;font-size:10px;color:#333}</style>`+
		`</head><body>%s</body></html>`, now)
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.WriteHeader(204)
}

type apiMessage struct {
	Nick string `json:"nick"`
	Time string `json:"time"`
	Text string `json:"text"`
}

func handleAPIMessages(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		msgs := s.snapshot()
		out := make([]apiMessage, len(msgs))
		for i, m := range msgs {
			out[i] = apiMessage{Nick: m.Nick, Time: m.Time, Text: m.Text}
		}
		json.NewEncoder(w).Encode(out)
	}
}

func handleAPIStatus(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"streams":%d,"messages":%d,"limits":{"max_streams":%d,"max_message_length":%d,"max_body_bytes":%d,"rate_limit_seconds":%.1f,"message_buffer":%d}}`,
			s.streamCount(), s.msgCount(), maxStreams, maxMsgLen, maxBodySize, rateLimitS, maxMessages)
	}
}

// --- Main ---

func main() {
	s := newStore()

	http.HandleFunc("/", handleIndex(s))
	http.HandleFunc("/messages", handleMessages(s))
	http.HandleFunc("/send", handleSend(s))
	http.HandleFunc("/clock", handleClock)
	http.HandleFunc("/favicon.ico", handleFavicon)
	http.HandleFunc("/api/messages", handleAPIMessages(s))
	http.HandleFunc("/api/status", handleAPIStatus(s))

	fmt.Println("[*] Listening on 127.0.0.1:8181")
	srv := &http.Server{
		Addr:              "127.0.0.1:8181",
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout — would kill streaming connections
	}
	log.Fatal(srv.ListenAndServe())
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func freshStore() *store {
	return newStore()
}

// --- Nickname ---

func TestMakeNickFormat(t *testing.T) {
	nick := makeNick()
	if !nickRE.MatchString(nick) {
		t.Errorf("nick %q does not match expected format", nick)
	}
}

func TestMakeNickUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		seen[makeNick()] = true
	}
	if len(seen) < 10 {
		t.Errorf("expected >10 unique nicks, got %d", len(seen))
	}
}

// --- Pages ---

func TestIndex(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleIndex(s)(w, r)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "onionchat") {
		t.Error("expected 'onionchat' in body")
	}
	if !strings.Contains(w.Body.String(), "autofocus") {
		t.Error("expected 'autofocus' in body")
	}
}

func TestIndexSetsCookie(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleIndex(s)(w, r)

	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "nick" {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("expected nick cookie")
	}
}

func TestClock(t *testing.T) {
	r := httptest.NewRequest("GET", "/clock", nil)
	w := httptest.NewRecorder()
	handleClock(w, r)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "UTC") {
		t.Error("expected 'UTC' in body")
	}
}

func Test404NoFrameworkLeak(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/wp-login.php", nil)
	w := httptest.NewRecorder()
	handleIndex(s)(w, r)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "Not Found") {
		t.Error("should not leak framework error text")
	}
}

func TestFavicon(t *testing.T) {
	r := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	handleFavicon(w, r)

	if w.Code != 204 {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

// --- Send ---

func postMsg(handler http.HandlerFunc, msg, nick string) *httptest.ResponseRecorder {
	form := url.Values{"msg": {msg}}
	r := httptest.NewRequest("POST", "/send", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if nick != "" {
		r.AddCookie(&http.Cookie{Name: "nick", Value: nick})
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestSendMessage(t *testing.T) {
	s := freshStore()
	w := postMsg(handleSend(s), "hello", "Fox-ab12")

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	if s.msgCount() != 1 {
		t.Errorf("expected 1 message, got %d", s.msgCount())
	}
	msgs := s.snapshot()
	if msgs[0].Text != "hello" {
		t.Errorf("expected 'hello', got %q", msgs[0].Text)
	}
}

func TestSendEmptyIgnored(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "   ", "Fox-ab12")

	if s.msgCount() != 0 {
		t.Errorf("expected 0 messages, got %d", s.msgCount())
	}
}

func TestSendPreservesNick(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "hi", "Fox-ab12")

	msgs := s.snapshot()
	if msgs[0].Nick != "Fox-ab12" {
		t.Errorf("expected 'Fox-ab12', got %q", msgs[0].Nick)
	}
}

func TestSendMethodNotAllowed(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/send", nil)
	w := httptest.NewRecorder()
	handleSend(s)(w, r)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// --- XSS ---

func TestXSSEscaped(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "<script>alert(1)</script>", "Fox-ab12")

	msgs := s.snapshot()
	html := renderMsg(msgs[0])
	if strings.Contains(html, "<script>") {
		t.Error("XSS: unescaped script tag in output")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("expected escaped script tag")
	}
}

// --- Message limit ---

func TestMessageTruncated(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), strings.Repeat("A", 1000), "Fox-ab12")

	msgs := s.snapshot()
	if len(msgs[0].Text) != maxMsgLen {
		t.Errorf("expected %d chars, got %d", maxMsgLen, len(msgs[0].Text))
	}
}

// --- Ring buffer ---

func TestRingBuffer(t *testing.T) {
	s := freshStore()
	for i := 0; i < 250; i++ {
		s.mu.Lock()
		s.msgs = append(s.msgs, message{ID: i, Nick: "X-0000", Time: "2026-01-01T00:00Z", Text: "x"})
		if len(s.msgs) > maxMessages {
			s.msgs = s.msgs[len(s.msgs)-maxMessages:]
		}
		s.mu.Unlock()
	}

	msgs := s.snapshot()
	if len(msgs) != maxMessages {
		t.Errorf("expected %d messages, got %d", maxMessages, len(msgs))
	}
	if msgs[0].ID != 50 {
		t.Errorf("expected oldest ID 50, got %d", msgs[0].ID)
	}
}

// --- Rate limiting ---

func TestRateLimit(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "first", "Wolf-aa11")
	postMsg(handleSend(s), "second", "Wolf-aa11")

	if s.msgCount() != 1 {
		t.Errorf("expected 1 message, got %d", s.msgCount())
	}
}

func TestRateLimitExpires(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "first", "Wolf-bb22")

	s.mu.Lock()
	s.lastSent["Wolf-bb22"] = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()

	postMsg(handleSend(s), "second", "Wolf-bb22")

	if s.msgCount() != 2 {
		t.Errorf("expected 2 messages, got %d", s.msgCount())
	}
}

// --- Duplicate filter ---

func TestDuplicateDropped(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "spam", "Owl-cc33")

	s.mu.Lock()
	s.lastSent["Owl-cc33"] = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()

	postMsg(handleSend(s), "spam", "Owl-cc33")

	if s.msgCount() != 1 {
		t.Errorf("expected 1 message, got %d", s.msgCount())
	}
}

func TestDuplicateAfterDelayAllowed(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "ping", "Owl-cc33")

	s.mu.Lock()
	s.lastSent["Owl-cc33"] = time.Now().Add(-31 * time.Second)
	s.mu.Unlock()

	postMsg(handleSend(s), "ping", "Owl-cc33")

	if s.msgCount() != 2 {
		t.Errorf("expected 2 messages, got %d", s.msgCount())
	}
}

func TestDuplicateBlockedWithOtherNickBetween(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "spam", "Owl-cc33")

	s.mu.Lock()
	s.lastSent["Fox-dd44"] = time.Time{}
	s.mu.Unlock()

	postMsg(handleSend(s), "other", "Fox-dd44")
	postMsg(handleSend(s), "spam", "Owl-cc33")

	count := 0
	for _, m := range s.snapshot() {
		if m.Text == "spam" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 'spam' message, got %d", count)
	}
}

func TestDuplicateFromDifferentNickAllowed(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "hello", "Fox-dd44")

	s.mu.Lock()
	s.lastSent["Owl-ee55"] = time.Time{}
	s.mu.Unlock()

	postMsg(handleSend(s), "hello", "Owl-ee55")

	if s.msgCount() != 2 {
		t.Errorf("expected 2 messages, got %d", s.msgCount())
	}
}

// --- Cookie validation ---

func TestInvalidCookieGetsNewNick(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "hi", "<script>evil</script>")

	msgs := s.snapshot()
	if !nickRE.MatchString(msgs[0].Nick) {
		t.Errorf("expected valid nick, got %q", msgs[0].Nick)
	}
}

func TestTooLongCookieGetsNewNick(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "hi", strings.Repeat("A", 100)+"-abcd")

	msgs := s.snapshot()
	if !nickRE.MatchString(msgs[0].Nick) {
		t.Errorf("expected valid nick, got %q", msgs[0].Nick)
	}
}

// --- Body limit ---

func TestBodyLimitRejectsLargePost(t *testing.T) {
	s := freshStore()
	form := url.Values{"msg": {strings.Repeat("A", 3000)}}
	r := httptest.NewRequest("POST", "/send", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleSend(s)(w, r)

	if w.Code != 413 {
		t.Errorf("expected 413, got %d", w.Code)
	}
}

// --- Security headers ---

func TestSecurityHeaders(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleIndex(s)(w, r)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "SAMEORIGIN",
		"Server":                 "onionchat",
	}
	for k, v := range checks {
		if got := w.Header().Get(k); got != v {
			t.Errorf("header %s: expected %q, got %q", k, v, got)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP missing default-src: %s", csp)
	}
}

// --- Stream limit ---

func TestStreamLimit(t *testing.T) {
	s := freshStore()
	for i := 0; i < maxStreams; i++ {
		s.addStream()
	}

	if s.addStream() {
		t.Error("should reject stream over limit")
	}
}

// --- API ---

func TestAPIMessagesEmpty(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/api/messages", nil)
	w := httptest.NewRecorder()
	handleAPIMessages(s)(w, r)

	if w.Body.String() != "[]" {
		t.Errorf("expected '[]', got %q", w.Body.String())
	}
}

func TestAPIMessages(t *testing.T) {
	s := freshStore()
	postMsg(handleSend(s), "hello", "Fox-ab12")

	r := httptest.NewRequest("GET", "/api/messages", nil)
	w := httptest.NewRecorder()
	handleAPIMessages(s)(w, r)

	var msgs []map[string]string
	json.Unmarshal(w.Body.Bytes(), &msgs)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0]["text"] != "hello" {
		t.Errorf("expected 'hello', got %q", msgs[0]["text"])
	}
	if _, ok := msgs[0]["id"]; ok {
		t.Error("internal id should not be exposed")
	}
}

func TestAPIStatus(t *testing.T) {
	s := freshStore()
	r := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	handleAPIStatus(s)(w, r)

	var status map[string]any
	json.Unmarshal(w.Body.Bytes(), &status)

	if status["streams"].(float64) != 0 {
		t.Error("expected 0 streams")
	}
	if status["messages"].(float64) != 0 {
		t.Error("expected 0 messages")
	}
}

// --- Timestamp ---

func TestRenderMsgShowsOnlyHHMM(t *testing.T) {
	m := message{ID: 0, Nick: "Fox-ab12", Time: "2026-03-30T15:23Z", Text: "hi"}
	out := renderMsg(m)
	if !strings.Contains(out, "15:23") {
		t.Error("expected HH:MM in output")
	}
	if strings.Contains(out, "2026") {
		t.Error("date should not appear in rendered message")
	}
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func solve(challenge string, difficulty int) string {
	prefix := strings.Repeat("0", difficulty)
	for i := 0; ; i++ {
		n := strconv.Itoa(i)
		sum := sha256.Sum256([]byte(challenge + n))
		if strings.HasPrefix(hex.EncodeToString(sum[:]), prefix) {
			return n
		}
	}
}

func newContactApp(t *testing.T) (*fiber.App, *ContactHandler) {
	t.Helper()
	h := &ContactHandler{secret: []byte("integration-secret"), difficulty: 3, used: map[string]time.Time{}}
	app := fiber.New()
	app.Get("/api/contact-form/challenge", h.getChallenge)
	app.Post("/api/contact-form", h.postContact)
	return app, h
}

func fetchChallenge(t *testing.T, app *fiber.App) challengeResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/contact-form/challenge", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	var ch challengeResponse
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatalf("bad challenge json: %v (%s)", err, body)
	}
	return ch
}

func postForm(t *testing.T, app *fiber.App, values url.Values) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/contact-form", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// validSolvedForm builds a form with a freshly solved, aged challenge.
func validSolvedForm(t *testing.T, app *fiber.App, name, email, message string) url.Values {
	ch := fetchChallenge(t, app)
	// backdate the timestamp past the min-fill window without expiring
	ch.Timestamp -= contactMinFillSeconds + 1
	h := &ContactHandler{secret: []byte("integration-secret")}
	sig := h.sign(ch.Challenge, ch.Timestamp, ch.Difficulty)
	nonce := solve(ch.Challenge, ch.Difficulty)
	return url.Values{
		"name": {name}, "email": {email}, "message": {message},
		"challenge": {ch.Challenge}, "ts": {strconv.FormatInt(ch.Timestamp, 10)},
		"sig": {sig}, "nonce": {nonce},
	}
}

func TestContactHappyPath(t *testing.T) {
	var hits int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		var p map[string]interface{}
		json.Unmarshal(body, &p)
		if got := p["content"]; got != "Jane Doe jane@example.com: Hi, I would love to work with you on the filter project." {
			t.Errorf("unexpected webhook content: %v", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	t.Setenv("CONTACT_WEBHOOK_URL", webhook.URL)

	app, _ := newContactApp(t)
	form := validSolvedForm(t, app, "Jane Doe", "jane@example.com", "Hi, I would love to work with you on the filter project.")
	if code := postForm(t, app, form); code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected webhook to be hit once, got %d", hits)
	}
}

func TestContactSpamNeverReachesWebhook(t *testing.T) {
	var hits int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	t.Setenv("CONTACT_WEBHOOK_URL", webhook.URL)

	app, _ := newContactApp(t)
	form := validSolvedForm(t, app, "IsaacHoono", "myhrtsdrm60@gmail.com",
		"IMPORTANT MESSAGE! WITHDRAW 1.3426 BTC https://qrlinkgenerator.com/kLrUG")
	// even with a perfectly solved challenge, content blacklist drops it
	if code := postForm(t, app, form); code != 200 {
		t.Fatalf("expected silent 200, got %d", code)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("spam must not reach webhook, hits=%d", hits)
	}
}

func TestContactHoneypot(t *testing.T) {
	app, _ := newContactApp(t)
	form := validSolvedForm(t, app, "Jane", "jane@example.com", "hello there friend")
	form.Set("website", "http://spam.example")
	if code := postForm(t, app, form); code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
}

func TestContactMissingChallengeRejected(t *testing.T) {
	app, _ := newContactApp(t)
	form := url.Values{"name": {"Jane"}, "email": {"jane@example.com"}, "message": {"hello there friend"}}
	if code := postForm(t, app, form); code != 400 {
		t.Fatalf("expected 400 protocol error, got %d", code)
	}
}

func TestContactReplayRejected(t *testing.T) {
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	t.Setenv("CONTACT_WEBHOOK_URL", webhook.URL)

	app, _ := newContactApp(t)
	form := validSolvedForm(t, app, "Jane Doe", "jane@example.com", "Legit message about a project idea.")
	if code := postForm(t, app, form); code != 200 {
		t.Fatalf("first submit expected 200, got %d", code)
	}
	// resubmitting the exact same solved challenge must be treated as replay
	if code := postForm(t, app, form); code != 400 {
		t.Fatalf("replay expected 400, got %d", code)
	}
}

func TestContactTooFastRejected(t *testing.T) {
	app, _ := newContactApp(t)
	ch := fetchChallenge(t, app) // fresh timestamp = "now", fails min-fill window
	h := &ContactHandler{secret: []byte("integration-secret")}
	form := url.Values{
		"name": {"Jane"}, "email": {"jane@example.com"}, "message": {"hello there friend"},
		"challenge": {ch.Challenge}, "ts": {strconv.FormatInt(ch.Timestamp, 10)},
		"sig": {h.sign(ch.Challenge, ch.Timestamp, ch.Difficulty)}, "nonce": {solve(ch.Challenge, ch.Difficulty)},
	}
	if code := postForm(t, app, form); code != 400 {
		t.Fatalf("expected 400 protocol error, got %d", code)
	}
}

func TestContactBadPoWRejected(t *testing.T) {
	app, _ := newContactApp(t)
	form := validSolvedForm(t, app, "Jane", "jane@example.com", "hello there friend")
	form.Set("nonce", "1") // not a valid solution for difficulty 3
	if code := postForm(t, app, form); code != 400 {
		t.Fatalf("expected 400 protocol error, got %d", code)
	}
}

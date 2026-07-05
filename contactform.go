package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	contactCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "contact_form_total",
		Help: "the times a contact form message was accepted and forwarded",
	})

	contactSpamCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "contact_form_spam_total",
		Help: "the times a contact form message was rejected as spam",
	}, []string{"layer"})
)

// contactChallengeTTL is how long a proof-of-work challenge stays valid after
// it was issued. Long enough for a human to fill the form, short enough that
// harvested challenges become useless quickly.
const contactChallengeTTL = 20 * time.Minute

// contactMinFillSeconds is the minimum time between requesting a challenge and
// submitting the form. Humans need at least a few seconds to type; bots that
// pipeline challenge->submit trip this.
const contactMinFillSeconds = 3

// ContactHandler bundles the state needed to run the anti-spam contact form
// endpoint: the HMAC secret used to sign challenges, the required proof-of-work
// difficulty and a small in-memory cache to prevent challenge replay.
type ContactHandler struct {
	secret     []byte
	difficulty int

	mu   sync.Mutex
	used map[string]time.Time // solved challenge nonce-token -> expiry, replay guard
}

func NewContactHandler() *ContactHandler {
	secret := []byte(os.Getenv("CONTACT_CHALLENGE_SECRET"))
	if len(secret) == 0 {
		// No secret configured: generate an ephemeral one. Challenges won't
		// survive a restart, but they expire within minutes anyway.
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			panic(fmt.Sprintf("could not generate contact challenge secret: %v", err))
		}
		slog.Warn("CONTACT_CHALLENGE_SECRET not set; using an ephemeral random secret")
	}

	difficulty := 4
	if v := os.Getenv("CONTACT_POW_DIFFICULTY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 8 {
			difficulty = n
		}
	}

	h := &ContactHandler{
		secret:     secret,
		difficulty: difficulty,
		used:       make(map[string]time.Time),
	}
	go h.cleanupLoop()
	return h
}

// cleanupLoop periodically drops expired entries from the replay cache so it
// doesn't grow without bound.
func (h *ContactHandler) cleanupLoop() {
	ticker := time.NewTicker(contactChallengeTTL)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		h.mu.Lock()
		for k, exp := range h.used {
			if now.After(exp) {
				delete(h.used, k)
			}
		}
		h.mu.Unlock()
	}
}

// challengeResponse is what the browser fetches before it can submit the form.
type challengeResponse struct {
	Challenge  string `json:"challenge"`
	Timestamp  int64  `json:"ts"`
	Signature  string `json:"sig"`
	Difficulty int    `json:"difficulty"`
}

// sign returns the HMAC that binds a challenge string to the timestamp and
// difficulty, so the client can't tamper with any of them.
func (h *ContactHandler) sign(challenge string, ts int64, difficulty int) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%s|%d|%d", challenge, ts, difficulty)
	return hex.EncodeToString(mac.Sum(nil))
}

// getChallenge issues a fresh, signed proof-of-work challenge.
func (h *ContactHandler) getChallenge(c *fiber.Ctx) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fiber.NewError(http.StatusInternalServerError, "could not generate challenge")
	}
	challenge := hex.EncodeToString(raw)
	ts := time.Now().Unix()

	return c.JSON(challengeResponse{
		Challenge:  challenge,
		Timestamp:  ts,
		Signature:  h.sign(challenge, ts, h.difficulty),
		Difficulty: h.difficulty,
	})
}

// verifyPoW checks that sha256(challenge + nonce) has `difficulty` leading
// hex zeros. This is the same computation the browser performs.
func verifyPoW(challenge, nonce string, difficulty int) bool {
	sum := sha256.Sum256([]byte(challenge + nonce))
	h := hex.EncodeToString(sum[:])
	prefix := strings.Repeat("0", difficulty)
	return strings.HasPrefix(h, prefix)
}

func (h *ContactHandler) reject(c *fiber.Ctx, layer, reason string) error {
	contactSpamCounter.WithLabelValues(layer).Inc()
	slog.Warn("contact form rejected", "layer", layer, "reason", reason, "ip", c.IP())
	// Return a generic 200 so bots can't tell which layer caught them and
	// don't retry with tweaks. The message is silently dropped.
	return c.SendStatus(http.StatusOK)
}

func (h *ContactHandler) postContact(c *fiber.Ctx) error {
	// Layer 1: honeypot. The form ships a hidden field named "website" that a
	// human never sees or fills. Any value means an automated submitter.
	if strings.TrimSpace(c.FormValue("website")) != "" {
		return h.reject(c, "honeypot", "honeypot field filled")
	}

	// Layer 2: proof-of-work challenge. Validate the signed challenge, its age
	// and the submitted solution.
	challenge := c.FormValue("challenge")
	sig := c.FormValue("sig")
	nonce := c.FormValue("nonce")
	tsStr := c.FormValue("ts")
	difficulty := h.difficulty

	if challenge == "" || sig == "" || nonce == "" || tsStr == "" {
		return h.reject(c, "challenge", "missing challenge fields")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return h.reject(c, "challenge", "unparseable timestamp")
	}
	expected := h.sign(challenge, ts, difficulty)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sig)) != 1 {
		return h.reject(c, "challenge", "bad signature")
	}
	age := time.Now().Unix() - ts
	if age < contactMinFillSeconds {
		return h.reject(c, "timing", "submitted too fast")
	}
	if age > int64(contactChallengeTTL.Seconds()) {
		return h.reject(c, "challenge", "challenge expired")
	}
	if !verifyPoW(challenge, nonce, difficulty) {
		return h.reject(c, "pow", "invalid proof of work")
	}

	// Replay guard: a solved challenge may be used exactly once.
	h.mu.Lock()
	if _, seen := h.used[challenge]; seen {
		h.mu.Unlock()
		return h.reject(c, "replay", "challenge reused")
	}
	h.used[challenge] = time.Now().Add(contactChallengeTTL)
	h.mu.Unlock()

	// Read the actual message fields.
	name := strings.TrimSpace(c.FormValue("name"))
	email := strings.TrimSpace(c.FormValue("email"))
	message := strings.TrimSpace(c.FormValue("message"))

	if name == "" || email == "" || message == "" {
		return h.reject(c, "validation", "empty required field")
	}
	if !looksLikeEmail(email) {
		return h.reject(c, "validation", "invalid email")
	}

	// Layer 3: content blacklists / spam scoring.
	if score, why := spamScore(name, email, message); score >= spamRejectThreshold {
		return h.reject(c, "blacklist", fmt.Sprintf("spam score %d: %s", score, why))
	}

	if err := sendContactToDiscord(name, email, message); err != nil {
		slog.Error("sending contact message to discord failed", "err", err)
		errorsCounter.Inc()
		return fiber.NewError(http.StatusInternalServerError, "could not deliver message")
	}

	contactCounter.Inc()
	return c.SendStatus(http.StatusOK)
}

var emailRegex = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func looksLikeEmail(s string) bool {
	if utf8.RuneCountInString(s) > 254 {
		return false
	}
	return emailRegex.MatchString(s)
}

func sendContactToDiscord(name, email, message string) error {
	webhookURL := os.Getenv("CONTACT_WEBHOOK_URL")
	if webhookURL == "" {
		webhookURL = os.Getenv("WEBHOOK_URL")
	}
	if webhookURL == "" || webhookURL == "YOUR_WEBHOOK_URL_HERE" {
		return fmt.Errorf("no contact webhook configured (set CONTACT_WEBHOOK_URL)")
	}

	// Keep the historical "<name> <email>: <message>" format.
	content := fmt.Sprintf("%s %s: %s", name, email, message)

	payload := map[string]interface{}{
		"content": content,
		// Never let a submitted @everyone/@here or role mention fire.
		"allowed_mentions": map[string]interface{}{"parse": []string{}},
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error creating JSON payload: %w", err)
	}

	req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("error creating HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received unexpected status code: %s", resp.Status)
	}
	return nil
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
)

func TestDiscordKeepsFullDiagnosticsInAttachment(t *testing.T) {
	stack := "Error: Invalid time value\n" + strings.Repeat("    at chart (https://sky.coflnet.com/_next/static/chunks/chart.js:123:45)\n", 100)
	data := map[string]interface{}{
		"reportId": "diagnostic-test-reference", "timestamp": "2026-09-05T13:06:10.220Z",
		"href": "https://sky.coflnet.com/item/BOOSTER_COOKIE", "isTest": true,
		"additionalInformation": "@everyone " + strings.Repeat("🍪", 3000),
		"error": map[string]interface{}{
			"name": "RangeError", "message": "Invalid time value", "stack": stack,
			"traceId": "test-trace", "digest": "test-digest",
			"cause": map[string]interface{}{"message": "original cause", "stack": stack},
		},
		"errorLog": []interface{}{map[string]interface{}{"error": map[string]interface{}{"stack": stack}}},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, feedbackName := range []string{"web-error", "reload"} {
		t.Run(feedbackName, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Query().Get("wait") != "true" || r.URL.Query().Get("thread_id") != "123" {
					t.Error("webhook must confirm delivery and preserve existing query parameters")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				var payload struct {
					Content         string `json:"content"`
					AllowedMentions struct {
						Parse []string `json:"parse"`
					} `json:"allowed_mentions"`
				}
				if err := json.Unmarshal([]byte(r.FormValue("payload_json")), &payload); err != nil {
					t.Error(err)
				}
				if !utf8.ValidString(payload.Content) || len(utf16.Encode([]rune(payload.Content))) > 2000 {
					t.Error("Discord summary exceeds the content limit or has broken Unicode")
				}
				for _, expected := range []string{"diagnostic-test-reference", "test-trace", "test-digest", "Invalid time value", "feedback.json"} {
					if !strings.Contains(payload.Content, expected) {
						t.Errorf("missing %q in Discord summary", expected)
					}
				}
				if payload.AllowedMentions.Parse == nil || len(payload.AllowedMentions.Parse) != 0 {
					t.Error("user-supplied content must not trigger mentions")
				}
				file, header, err := r.FormFile("files[0]")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				attachment, _ := io.ReadAll(file)
				if header.Filename != "feedback.json" || !bytes.Equal(attachment, raw) {
					t.Error("attachment did not preserve the complete original diagnostics")
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			t.Setenv("WEBHOOK_URL", server.URL+"?thread_id=123")
			feedback := &Feedback{Feedback: string(raw), FeedbackName: feedbackName, AdditionalInformations: data["additionalInformation"].(string)}
			if err := sendMessageToDiscordBot(feedback); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("report was silently discarded")
			}
		})
	}
}

type feedbackTestStore struct {
	*DatabaseHandler
	stored *Feedback
	err    error
	saves  int
}

func (s *feedbackTestStore) SaveFeedback(f *Feedback) error {
	if s.err != nil {
		return s.err
	}
	if s.stored != nil {
		*f = *s.stored
		return ErrDuplicateFeedback
	}
	f.ID = 42
	s.stored = f
	s.saves++
	return nil
}

func feedbackTestRequest(t *testing.T, app *fiber.App, nameKey, name, raw string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]string{"Feedback": raw, nameKey: name, "Context": "Skyblock"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestFeedbackParsingAcceptsErrorsWithoutCommentAndRejectsMalformedData(t *testing.T) {
	for _, nameKey := range []string{"FeedbackName", "feedbackName", "fedbackName"} {
		t.Run(nameKey, func(t *testing.T) {
			app := fiber.New()
			app.Post("/api", func(c *fiber.Ctx) error {
				feedback, err := parseFeedbackFromRequest(c)
				if err != nil {
					return err
				}
				if feedback.FeedbackName != "web-error" {
					t.Error("report type was lost")
				}
				return c.SendStatus(http.StatusNoContent)
			})
			response := feedbackTestRequest(t, app, nameKey, "web-error", `{"reportId":"test-id","error":{"message":"Invalid time value","stack":"full stack"}}`)
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("unexpected status %d", response.StatusCode)
			}
			for _, raw := range []string{`[]`, `null`, `"text"`, `{`, `{"reportId":"test-id"}`, `{"reportId":{},"error":{"message":[]}}`} {
				response := feedbackTestRequest(t, app, nameKey, "web-error", raw)
				if response.StatusCode != http.StatusBadRequest {
					t.Errorf("%s: unexpected status %d", raw, response.StatusCode)
				}
			}
		})
	}
	app := fiber.New()
	app.Post("/api", func(c *fiber.Ctx) error { _, err := parseFeedbackFromRequest(c); return err })
	response := feedbackTestRequest(t, app, "FeedbackName", "ordinary", `{"additionalInformation":{}}`)
	if response.StatusCode < 400 {
		t.Error("ordinary feedback still requires a comment")
	}
}

func TestFeedbackRetriesDiscordAfterStoredReportFailedDelivery(t *testing.T) {
	store := &feedbackTestStore{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if store.stored == nil {
			t.Error("webhook called before durable storage")
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"code":50035,"message":"Invalid Form Body"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("WEBHOOK_URL", server.URL)
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)
	raw := `{"reportId":"retry-reference","error":{"message":"test error","stack":"full stack"}}`
	for _, expected := range []int{http.StatusBadGateway, http.StatusNoContent} {
		response := feedbackTestRequest(t, app, "FeedbackName", "web-error", raw)
		if response.StatusCode != expected {
			t.Errorf("got %d, want %d", response.StatusCode, expected)
		}
	}
	if calls != 2 || store.saves != 1 {
		t.Fatalf("got %d webhook attempts and %d database inserts", calls, store.saves)
	}
	store.err = errors.New("database unavailable")
	response := feedbackTestRequest(t, app, "FeedbackName", "web-error", raw)
	if response.StatusCode != 500 || calls != 2 {
		t.Error("must not acknowledge or forward an unpersisted report")
	}
}

func TestDiscordFailureIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":50035,"message":"Invalid Form Body"}`)
	}))
	defer server.Close()
	t.Setenv("WEBHOOK_URL", server.URL)
	err := sendMessageToDiscordBot(&Feedback{Feedback: `{"additionalInformation":"normal feedback comment"}`, AdditionalInformations: "normal feedback comment"})
	if err == nil || !strings.Contains(err.Error(), "50035") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("missing Discord diagnostic response: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

var (
	feedbackCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "feedback_total",
		Help: "the times feedback was given",
	})

	errorsCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "feedback_errors",
		Help: "the times errors occured",
	})
)

// openapi.yaml will be located and read at startup from either the
// executable directory or the current working directory. This avoids
// requiring go:embed and works when the file is deployed alongside
// the binary.

type ApiHandler struct {
	databaseHandler interface {
		LegalActionStore
		LegalReceiptOutboxStore
		ContactEmailOutboxStore
		SaveFeedback(*Feedback) error
	}
}

func NewApiHandler(databaseHandler *DatabaseHandler) *ApiHandler {
	return &ApiHandler{
		databaseHandler: databaseHandler,
	}
}

func (h *ApiHandler) startApi() error {
	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "https://pro.skyblock.bz,https://songvoter.coflnet.com,https://sky.coflnet.com,https://coflnet.com,https://www.coflnet.com",
		AllowMethods:     "GET,POST,HEAD,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "*",
		AllowCredentials: false,
		MaxAge:           600,
	}))

	// Try to locate openapi.yaml next to the executable, otherwise fall back
	// to looking in the current working directory. If not found, we'll log
	// a warning and the /openapi.yaml handler will return 404.
	var openapiSpec []byte
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if b, err := os.ReadFile(filepath.Join(exeDir, "openapi.yaml")); err == nil {
			openapiSpec = b
		}
	}
	if openapiSpec == nil {
		if b, err := os.ReadFile("openapi.yaml"); err == nil {
			openapiSpec = b
		}
	}
	if openapiSpec == nil {
		slog.Warn("openapi.yaml not found next to executable or in working dir; /openapi.yaml will return 404")
	}

	app.Get("/health", h.healthRequest)
	app.Post("/api", h.feedbackPostRequest)
	app.Post("/api/songvoter-feedback", h.feedbackSongvoterPostRequest)
	app.Post("/api/pro-skyblock-feedback", h.feedbackProSkyblocPostRequest)

	// Contact form (landing page) with multi-layered anti-spam.
	contact := NewContactHandler(h.databaseHandler)
	smtpConfig, smtpConfigured := SMTPConfigFromEnv()
	legalActionInbox, inboxConfigured := LegalActionInboxFromEnv()
	contactInbox, contactInboxConfigured := ContactInboxFromEnv()
	legalRetentionYears, retentionConfigured := LegalActionRetentionFromEnv()
	contact.legalActionsConfigured = retentionConfigured
	contact.legalRetentionYears = legalRetentionYears
	contact.contactMailConfigured = smtpConfigured && contactInboxConfigured
	app.Get("/api/contact-form/challenge", contact.getChallenge)
	app.Post("/api/contact-form", contact.postContact)
	app.Post("/api/legal-action", contact.postLegalAction)
	workerContext, stopWorker := context.WithCancel(context.Background())
	app.Hooks().OnShutdown(func() error {
		stopWorker()
		return nil
	})
	go NewLegalReceiptWorker(h.databaseHandler, nil).RunPurger(workerContext)
	if smtpConfigured {
		if contactInboxConfigured {
			go NewContactEmailWorker(
				h.databaseHandler,
				NewSMTPContactMailer(smtpConfig, contactInbox),
			).Run(workerContext)
		} else {
			slog.Warn("contact form inbox is not configured; contact-form submissions are rejected")
		}
		if inboxConfigured {
			go NewLegalReceiptWorker(
				h.databaseHandler,
				NewSMTPReceiptMailer(smtpConfig, legalActionInbox),
			).Run(workerContext)
		}
	} else {
		slog.Warn("SMTP is not configured; accepted legal-action email jobs remain queued and contact-form submissions are rejected")
	}
	if !inboxConfigured {
		slog.Warn("legal action inbox is not configured; accepted legal-action email jobs remain queued")
	}
	if !retentionConfigured {
		slog.Warn("legal action retention override is invalid; legal action endpoint is disabled")
	}

	// Serve OpenAPI spec (embedded) and a minimal Swagger UI
	app.Get("/openapi.yaml", func(c *fiber.Ctx) error {
		if openapiSpec == nil {
			return c.Status(404).SendString("openapi.yaml not found on server")
		}
		c.Set("Content-Type", "application/yaml")
		return c.Send(openapiSpec)
	})

	app.Get("/api", func(c *fiber.Ctx) error {
		html := `<!doctype html>
<html lang="en">
	<head>
		<meta charset="utf-8" />
		<meta name="viewport" content="width=device-width, initial-scale=1" />
		<title>Feedback API Docs</title>
		<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@4.18.3/swagger-ui.css" />
	</head>
	<body>
		<div id="swagger-ui"></div>
		<script src="https://unpkg.com/swagger-ui-dist@4.18.3/swagger-ui-bundle.js"></script>
		<script>
			window.onload = function() {
				const ui = SwaggerUIBundle({
					url: '/openapi.yaml',
					dom_id: '#swagger-ui',
				})
			}
		</script>
	</body>
</html>`

		c.Type("html")
		return c.SendString(html)
	})

	return app.Listen(":3000")
}

func (h *ApiHandler) healthRequest(c *fiber.Ctx) error {
	return c.SendString("ok")
}

func (h *ApiHandler) feedbackPostRequest(c *fiber.Ctx) error {
	feedback, err := parseFeedbackFromRequest(c)
	if err != nil {
		slog.Error("there was an error when parsing feedback", "err", err)
		errorsCounter.Inc()
		return err
	}

	err = h.saveFeedback(feedback)
	if err != nil && !errors.Is(err, ErrDuplicateFeedback) {
		slog.Error("there was an error when saving feedback in db", "err", err)
		errorsCounter.Inc()
		return err
	}

	// Saving succeeds before delivery. Retry delivery even when the payload was
	// already stored: the previous webhook attempt may have failed.
	err = sendMessageToDiscordBot(feedback)
	if err != nil {
		slog.Error("feedback.discord.delivery.failed", "feedbackId", feedback.ID, "err", err)
		errorsCounter.Inc()
		return fiber.NewError(fiber.StatusBadGateway, "Report saved, but Discord delivery failed. Please retry.")
	}

	feedbackCounter.Inc()
	c.Status(204)
	return nil
}

func (h *ApiHandler) feedbackSongvoterPostRequest(c *fiber.Ctx) error {
	feedback, err := parseFeedbackFromRequest(c)
	if err != nil {
		slog.Error("there was an error when parsing feedback", "err", err)
		errorsCounter.Inc()
		return err
	}

	err = h.saveFeedback(feedback)
	if err != nil {
		if errors.Is(err, ErrDuplicateFeedback) {
			slog.Warn("duplicate feedback received; skipping storage")
			c.Status(204)
			return nil
		}

		slog.Error("there was an error when saving feedback in db", "err", err)
		errorsCounter.Inc()
		return err
	}

	feedbackCounter.Inc()

	c.Status(204)
	return nil
}

func (h *ApiHandler) feedbackProSkyblocPostRequest(c *fiber.Ctx) error {
	feedback, err := parseFeedbackFromRequest(c)
	if err != nil {
		slog.Error("there was an error when parsing feedback", "err", err)
		errorsCounter.Inc()
		return err
	}

	err = h.saveFeedback(feedback)
	if err != nil {
		if errors.Is(err, ErrDuplicateFeedback) {
			slog.Warn("duplicate feedback received; skipping storage")
			c.Status(204)
			return nil
		}

		slog.Error("there was an error when saving feedback in db", "err", err)
		errorsCounter.Inc()
		return err
	}

	feedbackCounter.Inc()

	c.Status(204)
	return nil
}

func parseFeedbackFromRequest(c *fiber.Ctx) (*Feedback, error) {
	c.Accepts("application/json")

	var feedback FeedbackRequest
	if err := c.BodyParser(&feedback); err != nil {
		slog.Error("could not parse request")
		errorsCounter.Inc()

		return nil, err
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(feedback.Feedback), &data); err != nil || data == nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "feedback must contain a JSON object")
	}
	feedback.Timestamp = time.Now()
	if feedback.FeedbackName == "" {
		feedback.FeedbackName = feedback.LegacyFeedbackName
	}
	content, _ := data["additionalInformation"].(string)
	if feedback.FeedbackName == "web-error" {
		reportID, _ := data["reportId"].(string)
		errorDetails, _ := data["error"].(map[string]interface{})
		message, _ := errorDetails["message"].(string)
		if strings.TrimSpace(reportID) == "" || strings.TrimSpace(message) == "" {
			return nil, fiber.NewError(fiber.StatusBadRequest, "web-error requires reportId and error.message")
		}
	} else if content == "" {
		return nil, &AdditionalInformationIsEmptyError{}
	}

	return &Feedback{
		Feedback:               feedback.Feedback,
		AdditionalInformations: content,
		User:                   feedback.User,
		Context:                feedback.Context,
		FeedbackName:           feedback.FeedbackName,
		Timestamp:              feedback.Timestamp,
	}, nil
}

func (h *ApiHandler) saveFeedback(f *Feedback) error {
	err := h.databaseHandler.SaveFeedback(f)
	if err != nil {
		return err
	}

	return nil
}

func sendMessageToDiscordBot(feedback *Feedback) error {
	isErrorReport := feedback.FeedbackName == "web-error"

	// If additional information is provided but it's too short, don't send the message.
	// This prevents sending trivial additional info (shorter than 5 characters).
	trimmed := strings.TrimSpace(feedback.AdditionalInformations)
	if !isErrorReport && trimmed != "" && utf8.RuneCountInString(trimmed) < 5 {
		slog.Warn("additionalInformation is too short; not sending message to Discord")
		return nil
	}

	// try to parse the raw feedback JSON into a map
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(feedback.Feedback), &parsed); err != nil {
		slog.Error("could not parse feedback JSON", "err", err)
		return err
	}

	if isErrorReport {
		return deliverDiscordFeedback(feedback, parsed, "")
	}

	// helper to read boolean safely
	getBool := func(k string) bool {
		if v, ok := parsed[k]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
		return false
	}

	// extract fields
	loadNew := getBool("loadNewInformation")

	var additional string
	if v, ok := parsed["additionalInformation"]; ok && v != nil {
		if s, ok := v.(string); ok {
			additional = s
		} else {
			// fallback: marshal non-string additionalInformation to string
			if b, err := json.Marshal(v); err == nil {
				additional = string(b)
			}
		}
	}

	// ignore feedback when loadNewInformation is false and additionalInformation is empty or too short
	if !loadNew && len(additional) < 10 {
		slog.Warn("ignoring feedback: loadNewInformation is false and additionalInformation is too short")
		return nil
	}

	// format properties nicely into a message
	var buf bytes.Buffer
	buf.WriteString("New feedback received\n\n")

	// helper to check if a value is empty/false
	isEmpty := func(v interface{}) bool {
		switch val := v.(type) {
		case bool:
			return !val
		case string:
			return val == ""
		case nil:
			return true
		default:
			return false
		}
	}

	// iterate through all fields in parsed feedback and include non-empty ones
	for key, value := range parsed {
		if isEmpty(value) {
			continue
		}

		switch key {
		case "additionalInformation":
			if s, ok := value.(string); ok && s != "" {
				buf.WriteString(fmt.Sprintf("**additionalInformation:**\n%s\n\n", s))
			}
		case "errorLog":
			// pretty-print errorLog
			if b, err := json.MarshalIndent(value, "", "  "); err == nil {
				buf.WriteString(fmt.Sprintf("**errorLog:**\n```\n%s\n```\n\n", string(b)))
			}
		case "href":
			if s, ok := value.(string); ok && s != "" {
				buf.WriteString(fmt.Sprintf("**href:** %s\n\n", s))
			}
		case "reason":
			if s, ok := value.(string); ok && s != "" {
				buf.WriteString(fmt.Sprintf("**reason:** %s\n", s))
			}
		case "rating":
			if num, ok := value.(float64); ok {
				buf.WriteString(fmt.Sprintf("**rating:** %.0f\n", num))
			}
		case "subscriptionStatus":
			if s, ok := value.(string); ok && s != "" {
				buf.WriteString(fmt.Sprintf("**subscriptionStatus:** %s\n", s))
			}
		case "timestamp":
			if s, ok := value.(string); ok && s != "" {
				buf.WriteString(fmt.Sprintf("**timestamp:** %s\n", s))
			}
		default:
			// include any other non-empty fields
			switch val := value.(type) {
			case string:
				if val != "" {
					buf.WriteString(fmt.Sprintf("**%s:** %s\n", key, val))
				}
			case bool:
				if val {
					buf.WriteString(fmt.Sprintf("**%s:** true\n", key))
				}
			case float64:
				buf.WriteString(fmt.Sprintf("**%s:** %.0f\n", key, val))
			default:
				if b, err := json.Marshal(value); err == nil && string(b) != "null" {
					buf.WriteString(fmt.Sprintf("**%s:** %s\n", key, string(b)))
				}
			}
		}
	}
	buf.WriteString("\n")

	return deliverDiscordFeedback(feedback, parsed, buf.String())
}

type AdditionalInformationIsEmptyError struct{}

func (e *AdditionalInformationIsEmptyError) Error() string {
	return "additionalInformation is empty, that is classified as an error by now"
}

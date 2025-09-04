package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

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

type ApiHandler struct {
	databaseHandler *DatabaseHandler
}

func NewApiHandler(databaseHandler *DatabaseHandler) *ApiHandler {
	return &ApiHandler{
		databaseHandler: databaseHandler,
	}
}

func (h *ApiHandler) startApi() error {
	app := fiber.New()
	app.Use(cors.New())

	app.Get("/health", h.healthRequest)
	app.Post("/api", h.feedbackPostRequest)
	app.Post("/api/songvoter-feedback", h.feedbackSongvoterPostRequest)

	return app.Listen(":3000")
}

func (h *ApiHandler) healthRequest(c *fiber.Ctx) error {
	return c.SendString("ok")
}

func (h *ApiHandler) feedbackPostRequest(c *fiber.Ctx) error {
	feedback, err := parseFeedbackFromRequest(c)
	if err != nil {
		slog.Error("there was an error when parsing feedback", err)
		errorsCounter.Inc()
		return err
	}

	err = h.saveFeedback(feedback)
	if err != nil {
		slog.Error("there was an error when saving feedback in db", err)
		errorsCounter.Inc()
		return err
	}

	err = sendMessageToDiscordBot(feedback)
	if err != nil {
		slog.Error("sending message to discord failed", "err", err)
		return err
	}

	feedbackCounter.Inc()
	c.Status(204)
	return nil
}

func (h *ApiHandler) feedbackSongvoterPostRequest(c *fiber.Ctx) error {
	feedback, err := parseFeedbackFromRequest(c)
	if err != nil {
		slog.Error("there was an error when parsing feedback", err)
		errorsCounter.Inc()
		return err
	}

	err = h.saveFeedback(feedback)
	if err != nil {
		slog.Error("there was an error when saving feedback in db", err)
		errorsCounter.Inc()
		return err
	}

	feedbackCounter.Inc()

	c.Status(204)
	return nil
}

func parseFeedbackFromRequest(c *fiber.Ctx) (*Feedback, error) {
	c.Accepts("application/json")

	var feedback MongoFeedback
	if err := c.BodyParser(&feedback); err != nil {
		slog.Error("could not parse request")
		errorsCounter.Inc()

		return nil, err
	}

	// parse data
	var d interface{}
	err := json.Unmarshal([]byte(feedback.Feedback), &d)
	if err != nil {
		slog.Error("could not parse feedback", err)
		errorsCounter.Inc()

		return nil, err
	}
	feedback.Data = d
	feedback.Timestamp = time.Now()

	content := ""

	if feedback.Data != nil {
		// try to extract additionalInformation
		additionalInformation, ok := feedback.Data.(map[string]interface{})["additionalInformation"]
		if ok {
			// check if additionalInformation is a string
			if _, ok = additionalInformation.(string); !ok {
				slog.Warn("additionalInformation is not a string, can't use it")
			}
			content = additionalInformation.(string)
			slog.Warn("found additionalInformation in feedback data")
		} else {
			slog.Warn("could not find additionalInformation in feedback data")
		}
	}

	if content == "" {
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
	webhookUrl := os.Getenv("WEBHOOK_URL")

	if webhookUrl == "YOUR_WEBHOOK_URL_HERE" {
		return fmt.Errorf("please replace 'YOUR_WEBHOOK_URL_HERE' with your actual Discord webhook URL")
	}

	// try to parse the raw feedback JSON into a map
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(feedback.Feedback), &parsed); err != nil {
		slog.Error("could not parse feedback JSON", "err", err)
		return err
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
	buf.WriteString(fmt.Sprintf("• loadNewInformation: %v\n", loadNew))
	buf.WriteString(fmt.Sprintf("• otherIssue: %v\n", getBool("otherIssue")))
	buf.WriteString(fmt.Sprintf("• somethingBroke: %v\n\n", getBool("somethingBroke")))

	buf.WriteString("additionalInformation:\n")
	if additional == "" {
		buf.WriteString("_(empty)_\n\n")
	} else {
		buf.WriteString(additional + "\n\n")
	}

	// pretty-print errorLog if present
	if el, ok := parsed["errorLog"]; ok {
		if b, err := json.MarshalIndent(el, "", "  "); err == nil {
			buf.WriteString("errorLog:\n")
			buf.Write(b)
			buf.WriteString("\n\n")
		}
	}

	// include href if present
	if href, ok := parsed["href"].(string); ok && href != "" {
		buf.WriteString(fmt.Sprintf("href: %s\n", href))
	}

	// use the formatted text as the Discord message content
	payload := map[string]string{
		"content": buf.String(),
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error creating JSON payload: %w", err)
	}

	req, err := http.NewRequest("POST", webhookUrl, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("error creating HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("received non-204 status code: %s", resp.Status)
	}

	return nil
}

type AdditionalInformationIsEmptyError struct{}

func (e *AdditionalInformationIsEmptyError) Error() string {
	return "additionalInformation is empty, that is classified as an error by now"
}

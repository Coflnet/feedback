package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Limit only the Discord preview; the attachment and database keep the original
// JSON, including all stack frames and nested causes. A byte limit also fits
// Discord's 2,000-character limit for non-ASCII text.
func discordPreview(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit - len("…")
	for !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}

func discordReportSummary(feedback *Feedback, data map[string]interface{}) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "**Feedback report** · database ID: %d\n", feedback.ID)
	writeField := func(key string, value interface{}) {
		if value != nil && value != "" {
			fmt.Fprintf(&summary, "**%s:** %s\n", key, discordPreview(fmt.Sprint(value), 180))
		}
	}
	writeField("reportId", data["reportId"])
	writeField("type", feedback.FeedbackName)
	for _, key := range []string{"timestamp", "source", "isTest", "href"} {
		writeField(key, data[key])
	}
	if details, ok := data["error"].(map[string]interface{}); ok {
		for _, key := range []string{"traceId", "digest", "name", "message"} {
			writeField(key, details[key])
		}
	}
	writeField("additionalInformation", data["additionalInformation"])
	return discordPreview(summary.String(), 1850) + "\nFull diagnostics, including stack traces, are in feedback.json."
}

func deliverDiscordFeedback(feedback *Feedback, data map[string]interface{}, content string) error {
	webhook, err := url.Parse(os.Getenv("WEBHOOK_URL"))
	if err != nil || webhook.Host == "" || (webhook.Scheme != "http" && webhook.Scheme != "https") {
		return errors.New("WEBHOOK_URL must be a valid HTTP(S) Discord webhook URL")
	}
	query := webhook.Query()
	query.Set("wait", "true") // Require Discord to confirm that it saved the message.
	webhook.RawQuery = query.Encode()

	attach := feedback.FeedbackName == "web-error" || len(content) > 2000
	if attach {
		content = discordReportSummary(feedback, data)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"content":          content,
		"allowed_mentions": map[string]interface{}{"parse": []string{}},
	})
	if err != nil {
		return err
	}
	var body bytes.Buffer
	contentType := "application/json"
	if attach {
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("payload_json", string(payload)); err != nil {
			return err
		}
		file, err := writer.CreateFormFile("files[0]", "feedback.json")
		if err != nil {
			return err
		}
		if _, err := io.WriteString(file, feedback.Feedback); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		contentType = writer.FormDataContentType()
	} else {
		body.Write(payload)
	}
	req, err := http.NewRequest(http.MethodPost, webhook.String(), &body)
	if err != nil {
		return errors.New("could not create Discord webhook request")
	}
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 15 * time.Second}
	slog.Info("feedback.discord.delivery.attempt", "feedbackId", feedback.ID, "reportId", data["reportId"])
	response, err := client.Do(req)
	if err != nil {
		// url.Error includes the webhook token; keep it out of logs and responses.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return fmt.Errorf("Discord webhook request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Discord webhook returned %s: %s", response.Status, detail)
	}
	slog.Info("feedback.discord.delivery.completed", "feedbackId", feedback.ID, "reportId", data["reportId"])
	return nil
}

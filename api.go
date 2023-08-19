package main

import (
	"context"
	"encoding/json"
	"github.com/Coflnet/coflnet-bot/pkg/discord"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"log/slog"
	"time"

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

func startApi() error {
	app := fiber.New()

	app.Use(cors.New())

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	app.Post("/api", func(c *fiber.Ctx) error {

		c.Accepts("application/json")

		var feedback Feedback
		if err := c.BodyParser(&feedback); err != nil {
			slog.Error("could not parse request")
			errorsCounter.Inc()

			return err
		}

		// parse data
		var d interface{}
		err := json.Unmarshal([]byte(feedback.Feedback), &d)
		if err != nil {
			slog.Error("could not parse feedback", err)
			errorsCounter.Inc()

			return err
		}
		feedback.Data = d
		feedback.Timestamp = time.Now()

		err = saveFeedback(c.Context(), feedback)
		if err != nil {
			slog.Error("there was an error when saving feedback in db", err)
			errorsCounter.Inc()
			return err
		}

		go func() {
			err = sendMessageToDiscordBot(feedback)
			if err != nil {
				if _, ok := err.(*AdditionalInformationIsEmptyError); ok {
					slog.Warn("additionalInformation is empty, not sending message to discord bot")
					return
				}

				slog.Error("could not send message to discord bot", "err", err)
				errorsCounter.Inc()
				return
			}
			slog.Info("successfully send feedback to the discord bot")
		}()

		feedbackCounter.Inc()

		c.Status(204)
		return nil
	})

	return app.Listen(":3000")
}

func saveFeedback(ctx context.Context, f Feedback) error {
	err := save(ctx, f)
	if err != nil {
		return err
	}

	return nil
}

func sendMessageToDiscordBot(feedback Feedback) error {
	// try to extract additionalInformation
	content, err := extractMessageContent(feedback)
	if err != nil {
		return err
	}

	return discord.SendMessageToChannel(content, discord.FeedbackChannel)
}

func extractMessageContent(feedback Feedback) (string, error) {
	content := ""

	if feedback.Data != nil {
		// try to extract additionalInformation
		additionalInformation, ok := feedback.Data.(map[string]interface{})["additionalInformation"]
		if ok {
			// check if additionalInformation is a string
			if _, ok := additionalInformation.(string); !ok {
				slog.Warn("additionalInformation is not a string, can't use it")
			}
			content = additionalInformation.(string)
			slog.Warn("found additionalInformation in feedback data")
		} else {
			slog.Warn("could not find additionalInformation in feedback data")
		}
	}

	// unable to extract additionalInformation, return an error
	if content == "" {
		return "", &AdditionalInformationIsEmptyError{}
	}

	return content, nil
}

type AdditionalInformationIsEmptyError struct{}

func (e *AdditionalInformationIsEmptyError) Error() string {
	return "additionalInformation is empty, that is classified as an error by now"
}

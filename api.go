package main

import (
	"context"
	"encoding/json"
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

		// send message to kafka
		// err := discord.SendMessageToFeedback(feedback.Feedback)
		// if err != nil {
		// 	slog.Error("could not send message to kafka", err)
		// 	return err
		// }
		slog.Warn("not sending message to kafka, currently disabled")

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

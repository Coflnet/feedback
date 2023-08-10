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
		Name: "times_feedback_given",
		Help: "the times feedback was given",
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
			return err
		}

		// parse data
		var d interface{}
		feedback.Data = json.Unmarshal([]byte(feedback.Feedback), &d)
		feedback.Data = d

		// set timestamp
		feedback.Timestamp = time.Now()

		if err := saveFeedback(c.Context(), feedback); err != nil {
			slog.Error("there was an error when saving feedback in db", err)
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

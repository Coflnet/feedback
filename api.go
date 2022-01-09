package main

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/rs/zerolog/log"
)

func startApi(errorCh chan<- error) {
	app := fiber.New()

	app.Use(cors.New())

	app.Post("/api", func(c *fiber.Ctx) error {

		c.Accepts("application/json")

		var feedback Feedback
		if err := c.BodyParser(&feedback); err != nil {
			log.Error().Err(err).Msg("could not parse request")
			return err
		}

		feedback.Timestamp = time.Now()

		if err := saveFeedback(feedback); err != nil {
			log.Error().Msg("there was an error when saving feedback in db")
		}

		incrementCounter()

		c.Status(204)
		return nil
	})

	errorCh <- app.Listen(":3000")
}

func saveFeedback(f Feedback) error {
	err := save(f)
	if err != nil {
		log.Error().Err(err).Msgf("something went wrong when inserting feedback %s in db", f.Feedback)
		return err
	}

	return nil
}

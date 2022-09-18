package main

import (
	"encoding/json"
	"time"

	"github.com/Coflnet/coflnet-bot/pkg/discord"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/rs/zerolog/log"
)

func startApi() error {
	app := fiber.New()

	app.Use(cors.New())

	app.Post("/api", func(c *fiber.Ctx) error {

		c.Accepts("application/json")

		var feedback Feedback
		if err := c.BodyParser(&feedback); err != nil {
			log.Error().Err(err).Msg("could not parse request")
			return err
		}

		// parse data
		var d interface{}
		feedback.Data = json.Unmarshal([]byte(feedback.Feedback), &d)
		feedback.Data = d

		// set timestamp
		feedback.Timestamp = time.Now()

		if err := saveFeedback(feedback); err != nil {
			log.Error().Err(err).Msg("there was an error when saving feedback in db")
			return err
		}

		// send message to kafka
		err := discord.SendMessageToFeedback(feedback.Feedback)
		if err != nil {
			log.Error().Err(err).Msg("could not send message to kafka")
			return err
		}

		incrementCounter()

		c.Status(204)
		return nil
	})

	return app.Listen(":3000")
}

func saveFeedback(f Feedback) error {
	err := save(f)
	if err != nil {
		log.Error().Err(err).Msgf("something went wrong when inserting feedback %s in db", f.Feedback)
		return err
	}

	return nil
}

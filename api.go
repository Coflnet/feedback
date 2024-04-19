package main

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Coflnet/coflnet-bot/pkg/discord"
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

	go func() {
		err := sendFeedbackToDiscordChannel(feedback, discord.FeedbackChannel)
		if err != nil {
			slog.Error("there was an error when sending feedback to discord channel", err)
			errorsCounter.Inc()
		}
		slog.Info("successfully send feedback to discord channel")
	}()

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

	go func() {
		err := sendFeedbackToDiscordChannel(feedback, discord.SongvoterFeedbackChannel)
		if err != nil {
			slog.Error("there was an error when sending feedback to discord channel", err)
			errorsCounter.Inc()
		}
		slog.Info("successfully send feedback to discord channel")
	}()

	feedbackCounter.Inc()

	c.Status(204)
	return nil
}

func sendFeedbackToDiscordChannel(feedback *Feedback, channel discord.DiscordChannel) error {
	err := sendMessageToDiscordBot(feedback, channel)
	if err != nil {
		if _, ok := err.(*AdditionalInformationIsEmptyError); ok {
			slog.Warn("additionalInformation is empty, not sending message to discord bot")
		}

		slog.Error("could not send message to discord bot", "err", err)
		errorsCounter.Inc()
		return err
	}
	slog.Info("successfully send feedback to the discord bot")

	return nil
}

func parseFeedbackFromRequest(c *fiber.Ctx) (*Feedback, error) {
	c.Accepts("application/json")

	var feedback Feedback
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

	return &feedback, nil
}

func (h *ApiHandler) saveFeedback(f *Feedback) error {
	err := h.databaseHandler.SaveFeedback(f)
	if err != nil {
		return err
	}

	return nil
}

func sendMessageToDiscordBot(feedback *Feedback, channel discord.DiscordChannel) error {
	// try to extract additionalInformation
	content, err := extractMessageContent(feedback)
	if err != nil {
		return err
	}

	return discord.SendMessageToChannel(content, channel)
}

func extractMessageContent(feedback *Feedback) (string, error) {
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

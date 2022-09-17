package main

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
	"math/big"
	"os"
	"time"
)

type DiscordMessageToSend struct {
	Message string
	Channel string
}

var (
	w *kafka.Writer
)

func Init() {
	w = &kafka.Writer{
		Addr:     kafka.TCP(getEnv("KAFKA_HOST")),
		Topic:    getEnv("DISCORD_MESSAGES_TOPIC"),
		Balancer: &kafka.LeastBytes{},
	}
}

func getEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Panic().Msgf("%s not set", key)
	}
	return v
}

func Disconnect() error {
	return w.Close()
}

func SendFeedbackToDiscord(msg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	message := DiscordMessageToSend{
		Message: msg,
		Channel: "feedback",
	}

	v, err := json.Marshal(message)
	if err != nil {
		return err
	}

	return w.WriteMessages(ctx, kafka.Message{
		Key:   big.NewInt(time.Now().Unix()).Bytes(),
		Value: v,
	})
}

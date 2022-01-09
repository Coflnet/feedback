package main

import (
	_ "github.com/joho/godotenv/autoload"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {

	errorCh := make(chan error)

	connect()
	defer disconnect()

	log.Info().Msgf("starting metrics..")
	go startMetrics(errorCh)

	log.Info().Msgf("starting api..")
	go startApi(errorCh)

	err := <-errorCh
	log.Fatal().Err(err).Msgf("received critical error stopping application")
}

type Feedback struct {
	ID           primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Feedback     interface{}        `bson:"feedback" json:"feedback"`
	User         string             `bson:"user" json:"user"`
	Context      string             `bson:"context" json:"context"`
	FeedbackName string             `bson:"feedback_name" json:"fedbackName"`
}

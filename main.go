package main

import (
	"time"

	_ "github.com/joho/godotenv/autoload"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {

	errorCh := make(chan error)

	err := connect()
	if err != nil {
		log.Panic().Err(err).Msg("could not connect to db")
	}
	defer func() {
		err := disconnect()
		if err != nil {
			log.Error().Err(err).Msg("could not disconnect from db")
		}
	}()

	log.Info().Msgf("feedback to data migration..")
	err = FeedbackToDataMigration()
	if err != nil {
		log.Panic().Err(err).Msg("could not migrate feedback to data")
	}
	log.Info().Msgf("finished migrating feedback to data")

	log.Info().Msgf("starting metrics..")
	go startMetrics(errorCh)
	log.Info().Msgf("starting api..")

	err = startApi()
	log.Fatal().Err(err).Msgf("received critical error stopping application")
}

type Feedback struct {
	ID           primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Feedback     string             `bson:"feedback" json:"feedback"`
	Data         interface{}        `bson:"data" json:"data"`
	User         string             `bson:"user" json:"user"`
	Context      string             `bson:"context" json:"context"`
	FeedbackName string             `bson:"feedback_name" json:"fedbackName"`
	Timestamp    time.Time          `bson:"timestamp" json:"timestamp"`
}

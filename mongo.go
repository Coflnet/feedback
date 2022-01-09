package main

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var client *mongo.Client
var collection *mongo.Collection

func connect() error {
	var err error
	client, err = mongo.NewClient(options.Client().ApplyURI(os.Getenv("MONGO_HOST")))

	if err != nil {
		log.Error().Err(err).Msgf("could not connect to mongo, using uri %s", os.Getenv("MONGO_HOST"))
		return err
	}

	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	err = client.Connect(ctx)
	if err != nil {
		log.Error().Err(err).Msgf("error connecting to database")
		return err
	}

	collection = client.Database("feedback").Collection("feedbacks")

	return nil
}

func disconnect() error {
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	return client.Disconnect(ctx)
}

func save(f Feedback) error {
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	res, err := collection.InsertOne(ctx, f)

	if err != nil {
		log.Error().Err(err).Msgf("there went something wrong when inserting feedback %s", f.FeedbackName)
		return err
	}

	log.Info().Msgf("feedback with id %s was inserted", res.InsertedID)
	return nil
}

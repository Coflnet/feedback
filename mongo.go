package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = client.Connect(ctx)
	if err != nil {
		log.Error().Err(err).Msgf("error connecting to database")
		return err
	}

	collection = client.Database("feedback").Collection("feedbacks")

	return nil
}

func disconnect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Disconnect(ctx)
}

func save(f Feedback) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := collection.InsertOne(ctx, f)

	if err != nil {
		log.Error().Err(err).Msgf("there went something wrong when inserting feedback %s", f.FeedbackName)
		return err
	}

	log.Info().Msgf("feedback with id %s was inserted", res.InsertedID)
	return nil
}

func FeedbackToDataMigration() error {
	ctx := context.Background()

	cursor, err := collection.Find(ctx, bson.D{})
	if err != nil {
		log.Error().Err(err).Msgf("could not find feedbacks")
		return err
	}

	for cursor.Next(ctx) {
		var f Feedback
		err := cursor.Decode(&f)
		if err != nil {
			log.Error().Err(err).Msgf("could not decode feedback")
			return err
		}

		if f.Data != nil {
			continue
		}

		var d interface{}
		err = json.Unmarshal([]byte(f.Feedback), &d)
		if err != nil {
			log.Error().Err(err).Msgf("could not unmarshal feedback")
			continue
		}

		f.Data = d

		_, err = collection.UpdateOne(ctx, bson.M{"_id": f.ID}, bson.M{"$set": bson.M{"data": f.Data}})
		if err != nil {
			log.Error().Err(err).Msgf("could not update feedback")
			continue
		}

		_, err = collection.UpdateOne(ctx, bson.M{"_id": f.ID}, bson.M{"$unset": bson.M{"feedback": ""}})
		if err != nil {
			log.Error().Err(err).Msgf("could not update feedback")
			continue
		}
	}

	return nil
}

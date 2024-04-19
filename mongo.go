package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var client *mongo.Client
var collection *mongo.Collection

func connect() error {
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err = mongo.Connect(ctx, options.Client().ApplyURI(os.Getenv("MONGO_HOST")))
	if err != nil {
		slog.Error("could not connect to mongo, using uri %s", os.Getenv("MONGO_HOST"), err)
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

func loadAll(ctx context.Context) ([]*MongoFeedback, error) {
	cursor, err := collection.Find(ctx, nil)
	if err != nil {
		return nil, err
	}

	var feedbacks []*MongoFeedback
	if err = cursor.All(ctx, &feedbacks); err != nil {
		return nil, err
	}

	return feedbacks, nil
}

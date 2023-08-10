package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log/slog"
	"net/http"
	"time"

	_ "github.com/joho/godotenv/autoload"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	errorCh := make(chan error)

	// connect to db
	err := connect()
	if err != nil {
		panic("could not connect to db")
	}
	defer func() {
		err = disconnect()
		if err != nil {
			slog.Error("could not disconnect from db")
		}
	}()

	slog.Info("starting metrics..")
	go startMetrics(errorCh)

	slog.Info("starting api..")
	err = startApi()
	panic(fmt.Sprintf("received critical error stopping application, %v", err))
}

func startMetrics(errorCh chan<- error) {
	http.Handle("/metrics", promhttp.Handler())
	errorCh <- http.ListenAndServe(":2112", nil)
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

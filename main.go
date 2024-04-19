package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "github.com/joho/godotenv/autoload"
)

func main() {
	errorCh := make(chan error)

	// connect to the legacy mongo db
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

	// connect to the new cockroach database
	db := NewDatabaseHandler()
	err = db.Connect()
	if err != nil {
		slog.Error("could not connect to the database", err)
		panic(err)
	}

	slog.Info("starting metrics..")
	go startMetrics(errorCh)

	err = migrateFeedback(db)
	if err != nil {
		slog.Error("could not migrate feedback", err)
		panic(err)
	}

	// start the api
	apiHandler := NewApiHandler(db)

	slog.Info("starting api..")
	err = apiHandler.startApi()
	panic(fmt.Sprintf("received critical error stopping application, %v", err))
}

func startMetrics(errorCh chan<- error) {
	http.Handle("/metrics", promhttp.Handler())
	errorCh <- http.ListenAndServe(":2112", nil)
}

func migrateFeedback(d *DatabaseHandler) error {
	slog.Info("starting feedback migration..")
	ctx := context.Background()

	slog.Info("loading feedbacks..")
	feedbacks, err := loadAll(ctx)
	if err != nil {
		slog.Error("could not load feedbacks", "err", err)
		return err
	}

	slog.Info(fmt.Sprintf("loaded %d feedbacks", len(feedbacks)))

	for _, f := range feedbacks {
		if f == nil {
			slog.Warn("nil feedback")
			continue
		}

		slog.Info(fmt.Sprintf("found feedback from %s at %s", f.User, f.Timestamp))
		feedback := &Feedback{
			Feedback:     f.Feedback,
			Data:         f.Data,
			User:         f.User,
			Context:      f.Context,
			FeedbackName: f.FeedbackName,
			Timestamp:    f.Timestamp,
		}

		err = d.SaveFeedback(feedback)
		if err != nil {
			return err
		}
		slog.Info("saved feedback")
	}

	return nil
}

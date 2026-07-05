package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "github.com/joho/godotenv/autoload"
)

func main() {
	errorCh := make(chan error)

	// connect to the cockroach database
	db := NewDatabaseHandler()
	err := db.Connect()
	if err != nil {
		slog.Error("could not connect to the database", "err", err)
		panic(err)
	}

	slog.Info("starting metrics..")
	go startMetrics(errorCh)

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

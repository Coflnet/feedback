package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	feedbackCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "times_feedback_given",
		Help: "the times feedback was given",
	})
)

func startMetrics(errorCh chan<- error) {
	http.Handle("/metrics", promhttp.Handler())
	errorCh <- http.ListenAndServe(":2112", nil)
}

func incrementCounter() {
	feedbackCounter.Inc()
}

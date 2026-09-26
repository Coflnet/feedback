package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a counter without prometheus/testutil, which is not in go.mod.
func counterValue(c prometheus.Collector) float64 {
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var m dto.Metric
	if err := (<-ch).Write(&m); err != nil {
		panic(err)
	}
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	return 0
}

var rejectReasons = []string{"body_parse", "feedback_not_json_object", "additional_information_empty", "web_error_missing_fields"}

type feedbackValidationCase struct {
	name, feedbackName, feedbackRaw   string
	store                             *feedbackTestStore
	useRawBody                        bool
	wantStatus, wantSaves, wantErrors int
	wantRejected                      map[string]int
}

func TestFeedbackValidation(t *testing.T) {
	cases := []feedbackValidationCase{
		{name: "badSearchResults", feedbackName: "badSearchResults", store: &feedbackTestStore{}, feedbackRaw: `{"query":"diamond sword","results":[]}`, wantStatus: http.StatusNoContent, wantSaves: 1},
		{name: "someOtherType", feedbackName: "someOtherType", store: &feedbackTestStore{}, feedbackRaw: `{"additionalInformation":""}`, wantStatus: http.StatusBadRequest, wantRejected: map[string]int{"additional_information_empty": 1}},
		// Regression: an invalid body used to be counted once inside parseFeedbackFromRequest and once more by the caller.
		{name: "invalidJSONBody", useRawBody: true, store: &feedbackTestStore{}, feedbackRaw: `{not valid json`, wantStatus: http.StatusBadRequest, wantRejected: map[string]int{"body_parse": 1}},
		{name: "storeError", feedbackName: "someOtherType", store: &feedbackTestStore{err: errors.New("boom")}, feedbackRaw: `{"additionalInformation":"a real comment"}`, wantStatus: http.StatusInternalServerError, wantErrors: 1},
		{name: "webError", feedbackName: "web-error", store: &feedbackTestStore{}, feedbackRaw: `{"error":{"message":"Invalid time value"}}`, wantStatus: http.StatusBadRequest, wantRejected: map[string]int{"web_error_missing_fields": 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &ApiHandler{databaseHandler: tc.store}
			app := fiber.New()
			app.Post("/api", handler.feedbackPostRequest)

			errsBefore := counterValue(errorsCounter)
			rejectedBefore := map[string]float64{}
			for _, reason := range rejectReasons {
				rejectedBefore[reason] = counterValue(feedbackRejectedCounter.WithLabelValues(reason))
			}

			var response *http.Response
			if tc.useRawBody {
				req := httptest.NewRequest(http.MethodPost, "/api", bytes.NewReader([]byte(tc.feedbackRaw)))
				req.Header.Set("Content-Type", "application/json")
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { resp.Body.Close() })
				response = resp
			} else {
				response = feedbackTestRequest(t, app, "FeedbackName", tc.feedbackName, tc.feedbackRaw)
			}

			if response.StatusCode != tc.wantStatus {
				t.Fatalf("got status %d, want %d", response.StatusCode, tc.wantStatus)
			}
			if tc.store.saves != tc.wantSaves {
				t.Errorf("got %d saves, want %d", tc.store.saves, tc.wantSaves)
			}
			if got := counterValue(errorsCounter) - errsBefore; got != float64(tc.wantErrors) {
				t.Errorf("feedback_errors changed by %v, want %d", got, tc.wantErrors)
			}
			for _, reason := range rejectReasons {
				want := float64(tc.wantRejected[reason])
				if got := counterValue(feedbackRejectedCounter.WithLabelValues(reason)) - rejectedBefore[reason]; got != want {
					t.Errorf("feedback_rejected_total{reason=%q} changed by %v, want %v", reason, got, want)
				}
			}
		})
	}
}

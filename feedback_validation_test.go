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

// counterValue reads a prometheus.Counter's current value directly via its
// Write method. This avoids depending on
// github.com/prometheus/client_golang/prometheus/testutil, which pulls in
// github.com/kylelemons/godebug -- a module not currently listed in go.mod
// (go.mod/go.sum are someone else's in-progress work and must not be touched
// here).
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

func rejectedValue(reason string) float64 {
	return counterValue(feedbackRejectedCounter.WithLabelValues(reason))
}

// rejectedDelta returns how much feedback_rejected_total{reason=...} grew
// between before and now.
func rejectedDelta(reason string, before float64) float64 {
	return rejectedValue(reason) - before
}

func TestStructuredFeedbackAcceptsEmptyAdditionalInformation(t *testing.T) {
	store := &feedbackTestStore{}
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)

	errsBefore := counterValue(errorsCounter)
	rejectedBefore := map[string]float64{}
	for _, reason := range []string{"body_parse", "feedback_not_json_object", "additional_information_empty", "web_error_missing_fields"} {
		rejectedBefore[reason] = counterValue(feedbackRejectedCounter.WithLabelValues(reason))
	}

	// No additionalInformation and no loadNewInformation: sendMessageToDiscordBot
	// treats this as a trivial update and returns nil without needing a
	// WEBHOOK_URL, so the request completes with 204.
	response := feedbackTestRequest(t, app, "FeedbackName", "badSearchResults", `{"query":"diamond sword","results":[]}`)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("got %d, want 204", response.StatusCode)
	}
	if store.saves != 1 {
		t.Fatalf("expected SaveFeedback to be called once, got %d", store.saves)
	}
	if got := counterValue(errorsCounter); got != errsBefore {
		t.Errorf("feedback_errors changed: before=%v after=%v", errsBefore, got)
	}
	for reason, before := range rejectedBefore {
		if delta := rejectedDelta(reason, before); delta != 0 {
			t.Errorf("feedback_rejected_total{reason=%q} changed by %v, want 0", reason, delta)
		}
	}
}

func TestNonStructuredFeedbackRejectsEmptyAdditionalInformation(t *testing.T) {
	store := &feedbackTestStore{}
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)

	errsBefore := counterValue(errorsCounter)
	rejectedBefore := counterValue(feedbackRejectedCounter.WithLabelValues("additional_information_empty"))

	response := feedbackTestRequest(t, app, "FeedbackName", "someOtherType", `{"additionalInformation":""}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
	if store.saves != 0 {
		t.Fatalf("SaveFeedback must not be called, got %d calls", store.saves)
	}
	if delta := rejectedDelta("additional_information_empty", rejectedBefore); delta != 1 {
		t.Errorf("feedback_rejected_total{reason=\"additional_information_empty\"} changed by %v, want 1", delta)
	}
	if got := counterValue(errorsCounter); got != errsBefore {
		t.Errorf("feedback_errors changed: before=%v after=%v", errsBefore, got)
	}
}

func TestInvalidJSONBodyIsCountedOnceAsBodyParseRejection(t *testing.T) {
	store := &feedbackTestStore{}
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)

	errsBefore := counterValue(errorsCounter)
	rejectedBefore := counterValue(feedbackRejectedCounter.WithLabelValues("body_parse"))

	req := httptest.NewRequest(http.MethodPost, "/api", bytes.NewReader([]byte("{not valid json")))
	req.Header.Set("Content-Type", "application/json")
	response, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
	if store.saves != 0 {
		t.Fatalf("SaveFeedback must not be called, got %d calls", store.saves)
	}
	// Regression: this used to be incremented once inside
	// parseFeedbackFromRequest and once more by the caller.
	if delta := rejectedDelta("body_parse", rejectedBefore); delta != 1 {
		t.Errorf("feedback_rejected_total{reason=\"body_parse\"} changed by %v, want exactly 1 (double-count regression)", delta)
	}
	if got := counterValue(errorsCounter); got != errsBefore {
		t.Errorf("feedback_errors changed: before=%v after=%v", errsBefore, got)
	}
}

func TestDatabaseSaveFailureIsServerErrorNotRejection(t *testing.T) {
	store := &feedbackTestStore{err: errors.New("boom")}
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)

	errsBefore := counterValue(errorsCounter)
	rejectedBefore := map[string]float64{}
	for _, reason := range []string{"body_parse", "feedback_not_json_object", "additional_information_empty", "web_error_missing_fields"} {
		rejectedBefore[reason] = counterValue(feedbackRejectedCounter.WithLabelValues(reason))
	}

	response := feedbackTestRequest(t, app, "FeedbackName", "someOtherType", `{"additionalInformation":"a real comment"}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", response.StatusCode)
	}
	if got := counterValue(errorsCounter); got != errsBefore+1 {
		t.Errorf("feedback_errors changed by %v, want 1", got-errsBefore)
	}
	for reason, before := range rejectedBefore {
		if delta := rejectedDelta(reason, before); delta != 0 {
			t.Errorf("feedback_rejected_total{reason=%q} changed by %v, want 0", reason, delta)
		}
	}
}

func TestWebErrorMissingReportIdIsRejected(t *testing.T) {
	store := &feedbackTestStore{}
	handler := &ApiHandler{databaseHandler: store}
	app := fiber.New()
	app.Post("/api", handler.feedbackPostRequest)

	errsBefore := counterValue(errorsCounter)
	rejectedBefore := counterValue(feedbackRejectedCounter.WithLabelValues("web_error_missing_fields"))

	response := feedbackTestRequest(t, app, "FeedbackName", "web-error", `{"error":{"message":"Invalid time value"}}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
	if store.saves != 0 {
		t.Fatalf("SaveFeedback must not be called, got %d calls", store.saves)
	}
	if delta := rejectedDelta("web_error_missing_fields", rejectedBefore); delta != 1 {
		t.Errorf("feedback_rejected_total{reason=\"web_error_missing_fields\"} changed by %v, want 1", delta)
	}
	if got := counterValue(errorsCounter); got != errsBefore {
		t.Errorf("feedback_errors changed: before=%v after=%v", errsBefore, got)
	}
}

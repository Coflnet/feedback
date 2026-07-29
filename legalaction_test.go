package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

type memoryLegalActionStore struct {
	mu            sync.Mutex
	action        *LegalAction
	receiptQueued bool
	acceptCount   int
	err           error
}

func (s *memoryLegalActionStore) AcceptLegalAction(action *LegalAction) (*LegalAction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, false, s.err
	}
	if s.action != nil && s.action.SubmissionID == action.SubmissionID {
		copy := *s.action
		return &copy, false, nil
	}
	copy := *action
	s.action = &copy
	s.receiptQueued = true
	s.acceptCount++
	return &copy, true, nil
}

func newLegalActionApp(store LegalActionStore) *fiber.App {
	h := &ContactHandler{
		secret:                 []byte("integration-secret"),
		difficulty:             3,
		legalStore:             store,
		legalActionsConfigured: true,
		legalRetentionYears:    6,
		used:                   map[string]time.Time{},
	}
	app := fiber.New()
	app.Get("/api/contact-form/challenge", h.getChallenge)
	app.Post("/api/legal-action", h.postLegalAction)
	return app
}

func validLegalActionForm(t *testing.T, app *fiber.App) url.Values {
	t.Helper()
	form := validSolvedForm(t, app, "Jane Doe", "jane@example.com", "unused")
	form.Set("action", "withdrawal")
	form.Set("submissionId", "8ce4f2f7-cfca-4b2d-8649-b4143b6c8f7a")
	form.Set("language", "en")
	form.Set("contract", "Prufi order 123")
	form.Set("scope", "monthly subscription")
	return form
}

func validCancellationForm(t *testing.T, app *fiber.App) url.Values {
	t.Helper()
	form := validLegalActionForm(t, app)
	form.Set("action", "cancellation")
	form.Set("terminationType", "ordinary")
	form.Set("requestedEnd", "earliest")
	return form
}

func postLegalAction(t *testing.T, app *fiber.App, form url.Values) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/legal-action", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestLegalActionPersistsBeforeAcknowledging(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	status, body := postLegalAction(t, app, validLegalActionForm(t, app))
	if status != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", status, body)
	}
	if store.action == nil {
		t.Fatal("action was not persisted")
	}
	if !store.receiptQueued {
		t.Fatal("201 was returned before receipt delivery was queued")
	}
	if store.action.Declaration != "I hereby withdraw from the identified contract, limited to: monthly subscription." {
		t.Fatalf("wrong declaration persisted: %q", store.action.Declaration)
	}
	if store.action.ExpiresAt == nil || !store.action.ExpiresAt.Equal(legalActionExpiry(store.action.ReceivedAt, 6)) {
		t.Fatalf("retention deadline was not persisted: %#v", store.action.ExpiresAt)
	}
	if store.action.Reference == "" || store.action.ReceivedAt.IsZero() {
		t.Fatal("server reference and receipt timestamp must be persisted")
	}

	var response legalActionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Reference != store.action.Reference ||
		!response.ReceivedAt.Equal(store.action.ReceivedAt) {
		t.Fatal("response does not identify the persisted record")
	}
	for _, expected := range []string{
		response.Reference,
		store.action.ReceivedAt.Format(time.RFC3339Nano),
		"Submitted using the confirmation button “Confirm withdrawal”",
		store.action.Name,
		store.action.Email,
		store.action.ContractIdentifier,
		store.action.Declaration,
	} {
		if !strings.Contains(response.Receipt, expected) {
			t.Fatalf("receipt missing %q: %s", expected, response.Receipt)
		}
	}
}

func TestCancellationRequiresActionableIdentificationAndReceiptChannel(t *testing.T) {
	for _, field := range []string{"name", "email", "contract"} {
		t.Run(field, func(t *testing.T) {
			store := &memoryLegalActionStore{}
			app := newLegalActionApp(store)
			form := validCancellationForm(t, app)
			form.Del(field)

			status, body := postLegalAction(t, app, form)

			if status != fiber.StatusBadRequest {
				t.Fatalf("missing %s returned %d: %s", field, status, body)
			}
			if store.action != nil {
				t.Fatalf("cancellation without %s was persisted", field)
			}
		})
	}
}

func TestWithdrawalRequiresSection356aIdentificationAndReceiptChannel(t *testing.T) {
	for _, field := range []string{"name", "email", "contract"} {
		t.Run(field, func(t *testing.T) {
			store := &memoryLegalActionStore{}
			app := newLegalActionApp(store)
			form := validLegalActionForm(t, app)
			form.Del(field)
			status, body := postLegalAction(t, app, form)
			if status != fiber.StatusBadRequest {
				t.Fatalf("missing %s: expected 400, got %d: %s", field, status, body)
			}
			if store.action != nil || store.receiptQueued {
				t.Fatalf("withdrawal without required %s was accepted", field)
			}
		})
	}
}

func TestLegalActionNeverSilentlyAcceptsSpamScoredContent(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validLegalActionForm(t, app)
	form.Set("scope", "1.3426 BTC before the daily cycle ends")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("legal declaration must not be silently filtered: %d %s", status, body)
	}
	if store.action == nil {
		t.Fatal("legal declaration was acknowledged without persistence")
	}
}

func TestLegalActionDoesNotAcknowledgeStorageFailure(t *testing.T) {
	app := newLegalActionApp(&memoryLegalActionStore{err: errors.New("database unavailable")})
	status, body := postLegalAction(t, app, validLegalActionForm(t, app))
	if status != fiber.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", status, body)
	}
}

func TestCancellationWithoutTerminationTypeDefaultsToOrdinary(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Del("terminationType")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("optional termination type blocked submission: %d %s", status, body)
	}
	if store.action.TerminationType != "ordinary" {
		t.Fatalf("omitted termination type did not default to ordinary: %q", store.action.TerminationType)
	}
}

func TestExtraordinaryCancellationReasonIsOptional(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Set("terminationType", "extraordinary")
	form.Del("reason")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("optional extraordinary reason blocked submission: %d %s", status, body)
	}
	if store.action.Reason != "" ||
		strings.Contains(store.action.Declaration, "Reason:") ||
		strings.Contains(string(body), "Reason for extraordinary termination:") {
		t.Fatalf("empty optional reason was rendered: action=%#v receipt=%s", store.action, body)
	}
}

func TestCancellationWithoutRequestedEndDefaultsToEarliest(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Del("requestedEnd")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", status, body)
	}
	if store.action.RequestedEnd != "earliest" {
		t.Fatalf("omitted requested end did not default to earliest: %q", store.action.RequestedEnd)
	}
}

func TestCancellationStructuredFieldsArePersistedAndReceipted(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Set("terminationType", "extraordinary")
	form.Set("requestedEnd", "2026-07-31")
	form.Set("reason", "Material breach")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", status, body)
	}
	if store.action.TerminationType != "extraordinary" ||
		store.action.RequestedEnd != "2026-07-31" ||
		store.action.Reason != "Material breach" {
		t.Fatalf("structured cancellation fields were not persisted: %#v", store.action)
	}
	var response legalActionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Submitted using the confirmation button “Cancel now”",
		"Termination type: Extraordinary",
		"Requested end: on 2026-07-31",
		"Reason for extraordinary termination: Material breach",
		"I hereby give extraordinary notice to terminate the identified contract on 2026-07-31. Reason: Material breach",
	} {
		if !strings.Contains(response.Receipt, expected) {
			t.Fatalf("receipt missing %q: %s", expected, response.Receipt)
		}
	}
}

func TestLegalActionCanonicalizesAndMinimizesSubmittedFields(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Set("scope", "must not be retained")
	form.Set("reason", "must not be retained")
	form.Set("declaration", "This contradicts the structured cancellation.")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", status, body)
	}
	if store.action.Scope != "" || store.action.Reason != "" {
		t.Fatalf("irrelevant data was retained: %#v", store.action)
	}
	expected := "I hereby give ordinary notice to terminate the identified contract at the earliest possible date."
	if store.action.Declaration != expected {
		t.Fatalf("declaration was not generated from structured fields: %q", store.action.Declaration)
	}
	if strings.Contains(string(body), "must not be retained") ||
		strings.Contains(string(body), "contradicts") {
		t.Fatalf("receipt exposed ignored fields: %s", body)
	}
}

func TestCancellationRejectsAmbiguousRequestedEnd(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Set("requestedEnd", "whenever convenient")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", status, body)
	}
	if store.action != nil {
		t.Fatal("ambiguous requested end must not be persisted")
	}
}

func TestGermanCancellationReceiptIsLocalizedAndDocumentsButton(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validCancellationForm(t, app)
	form.Set("language", "de")
	status, body := postLegalAction(t, app, form)
	if status != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", status, body)
	}
	var response legalActionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Abgabe über die Bestätigungsschaltfläche „jetzt kündigen“",
		"Art: Kündigung",
		"Kündigungsart: ordentlich",
		"Gewünschter Beendigungszeitpunkt: zum nächstmöglichen Zeitpunkt",
		"Hiermit kündige ich den bezeichneten Vertrag ordentlich zum nächstmöglichen Zeitpunkt.",
	} {
		if !strings.Contains(response.Receipt, expected) {
			t.Fatalf("receipt missing %q: %s", expected, response.Receipt)
		}
	}
}

func TestLegalActionReplayReturnsOriginalReceiptWithoutDuplicateJobs(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	firstStatus, firstBody := postLegalAction(t, app, validLegalActionForm(t, app))
	secondStatus, secondBody := postLegalAction(t, app, validLegalActionForm(t, app))
	if firstStatus != fiber.StatusCreated || secondStatus != fiber.StatusCreated {
		t.Fatalf("expected two 201 responses, got %d and %d", firstStatus, secondStatus)
	}
	var first, second legalActionResponse
	if err := json.Unmarshal(firstBody, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(secondBody, &second); err != nil {
		t.Fatal(err)
	}
	if first.Reference != second.Reference ||
		!first.ReceivedAt.Equal(second.ReceivedAt) ||
		first.Receipt != second.Receipt {
		t.Fatalf("replay did not return the original receipt:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if store.acceptCount != 1 {
		t.Fatalf("replay created %d actions/outbox pairs, want 1", store.acceptCount)
	}
}

func TestLegalActionSubmissionIDCannotBeReusedForDifferentContent(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	firstStatus, firstBody := postLegalAction(t, app, validLegalActionForm(t, app))
	if firstStatus != fiber.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", firstStatus, firstBody)
	}
	changed := validLegalActionForm(t, app)
	changed.Set("scope", "different scope")
	status, body := postLegalAction(t, app, changed)
	if status != fiber.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", status, body)
	}
	if store.acceptCount != 1 {
		t.Fatalf("mismatched replay created %d actions/outbox pairs, want 1", store.acceptCount)
	}
}

func TestConcurrentLegalActionReplayCreatesOneAction(t *testing.T) {
	const requestCount = 8
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	forms := make([]url.Values, requestCount)
	for index := range forms {
		forms[index] = validLegalActionForm(t, app)
	}

	type result struct {
		status int
		body   []byte
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, requestCount)
	var wait sync.WaitGroup
	for _, form := range forms {
		wait.Add(1)
		go func(form url.Values) {
			defer wait.Done()
			<-start
			request := httptest.NewRequest("POST", "/api/legal-action", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err := app.Test(request, -1)
			if err != nil {
				results <- result{err: err}
				return
			}
			body, err := io.ReadAll(response.Body)
			results <- result{status: response.StatusCode, body: body, err: err}
		}(form)
	}
	close(start)
	wait.Wait()
	close(results)

	reference := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.status != fiber.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", result.status, result.body)
		}
		var response legalActionResponse
		if err := json.Unmarshal(result.body, &response); err != nil {
			t.Fatal(err)
		}
		if reference == "" {
			reference = response.Reference
		} else if response.Reference != reference {
			t.Fatalf("concurrent replay returned reference %q, want %q", response.Reference, reference)
		}
	}
	if store.acceptCount != 1 {
		t.Fatalf("concurrent replay created %d actions/outbox pairs, want 1", store.acceptCount)
	}
}

func TestLegalActionFailsClosedWhenDurableStorageIsUnconfigured(t *testing.T) {
	store := &memoryLegalActionStore{}
	h := &ContactHandler{
		secret:     []byte("integration-secret"),
		difficulty: 3,
		legalStore: store,
		used:       map[string]time.Time{},
	}
	app := fiber.New()
	app.Get("/api/contact-form/challenge", h.getChallenge)
	app.Post("/api/legal-action", h.postLegalAction)
	status, body := postLegalAction(t, app, validLegalActionForm(t, app))
	if status != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", status, body)
	}
	if store.action != nil || store.receiptQueued {
		t.Fatal("unconfigured durable storage must not accept or queue an action")
	}
}

func TestLegalActionAcceptanceDoesNotDependOnSMTPDelivery(t *testing.T) {
	store := &memoryLegalActionStore{}
	h := &ContactHandler{
		secret:                 []byte("integration-secret"),
		difficulty:             3,
		legalStore:             store,
		legalActionsConfigured: true,
		legalRetentionYears:    6,
		used:                   map[string]time.Time{},
	}
	app := fiber.New()
	app.Get("/api/contact-form/challenge", h.getChallenge)
	app.Post("/api/legal-action", h.postLegalAction)
	status, body := postLegalAction(t, app, validLegalActionForm(t, app))
	if status != fiber.StatusCreated {
		t.Fatalf("email delivery configuration blocked durable acceptance: %d %s", status, body)
	}
	if store.action == nil || !store.receiptQueued {
		t.Fatal("accepted action and delivery state were not persisted")
	}
}

func TestLegalActionRejectsMalformedRequest(t *testing.T) {
	store := &memoryLegalActionStore{}
	app := newLegalActionApp(store)
	form := validLegalActionForm(t, app)
	form.Set("email", "not-an-email")
	status, _ := postLegalAction(t, app, form)
	if status != fiber.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if store.action != nil {
		t.Fatal("malformed action must not be persisted")
	}
}

func TestLegalActionAndChallengeResponsesAreNotCacheable(t *testing.T) {
	app := newLegalActionApp(&memoryLegalActionStore{})
	challengeResponse, err := app.Test(httptest.NewRequest("GET", "/api/contact-form/challenge", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := challengeResponse.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("challenge Cache-Control = %q, want no-store", got)
	}

	form := validLegalActionForm(t, app)
	request := httptest.NewRequest("POST", "/api/legal-action", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := app.Test(request, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected 201, got %d: %s", response.StatusCode, body)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("legal-action Cache-Control = %q, want no-store", got)
	}
}

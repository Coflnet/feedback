package main

import (
	"errors"
	"net/mail"
	"strings"
	"testing"
	"time"
)

type memoryOutboxStore struct {
	action      *LegalAction
	job         LegalReceiptOutbox
	nextAttempt time.Time
	sent        bool
	retries     int
	purged      int64
}

type memoryReviewOutboxStore struct {
	action      *LegalAction
	job         LegalReviewOutbox
	nextAttempt time.Time
	sent        bool
	retries     int
}

func (s *memoryOutboxStore) ClaimLegalReceiptOutbox(now time.Time, _ time.Duration, _ int) ([]LegalReceiptOutbox, error) {
	if s.sent || now.Before(s.nextAttempt) {
		return nil, nil
	}
	s.job.AttemptCount++
	return []LegalReceiptOutbox{s.job}, nil
}

func (s *memoryOutboxStore) LoadLegalAction(string) (*LegalAction, error) {
	return s.action, nil
}

func (s *memoryOutboxStore) MarkLegalReceiptSent(_ uint64, _ time.Time) error {
	s.sent = true
	return nil
}

func (s *memoryOutboxStore) RetryLegalReceipt(_ uint64, next time.Time) error {
	s.retries++
	s.nextAttempt = next
	return nil
}

func (s *memoryOutboxStore) PurgeExpiredLegalActions(time.Time) (int64, error) {
	return s.purged, nil
}

func (s *memoryReviewOutboxStore) ClaimLegalReviewOutbox(now time.Time, _ time.Duration, _ int) ([]LegalReviewOutbox, error) {
	if s.sent || now.Before(s.nextAttempt) {
		return nil, nil
	}
	s.job.AttemptCount++
	return []LegalReviewOutbox{s.job}, nil
}

func (s *memoryReviewOutboxStore) LoadLegalAction(string) (*LegalAction, error) {
	return s.action, nil
}

func (s *memoryReviewOutboxStore) MarkLegalReviewSent(_ uint64, _ time.Time) error {
	s.sent = true
	return nil
}

func (s *memoryReviewOutboxStore) RetryLegalReview(_ uint64, next time.Time) error {
	s.retries++
	s.nextAttempt = next
	return nil
}

type retryMailer struct {
	failures int
	sends    int
}

func (m *retryMailer) SendReceipt(*LegalAction) error {
	return m.send()
}

func (m *retryMailer) SendReview(*LegalAction) error {
	return m.send()
}

func (m *retryMailer) send() error {
	m.sends++
	if m.failures > 0 {
		m.failures--
		return errors.New("temporary SMTP failure")
	}
	return nil
}

func TestLegalReceiptWorkerRetriesThenMarksSent(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	action := &LegalAction{
		Reference:          "LA-test",
		ReceivedAt:         now,
		Action:             "withdrawal",
		Language:           "en",
		Name:               "Jane Doe",
		Email:              "jane@example.com",
		ContractIdentifier: "Order 123",
		Declaration:        "I hereby withdraw.",
	}
	store := &memoryOutboxStore{
		action: action,
		job: LegalReceiptOutbox{
			ID:                   1,
			LegalActionReference: action.Reference,
		},
	}
	mailer := &retryMailer{failures: 1}
	worker := NewLegalReceiptWorker(store, mailer)

	worker.ProcessOnce(now)
	if store.sent || store.retries != 1 || mailer.sends != 1 {
		t.Fatalf("first failure was not queued for retry: sent=%v retries=%d sends=%d", store.sent, store.retries, mailer.sends)
	}
	worker.ProcessOnce(now.Add(30 * time.Second))
	if mailer.sends != 1 {
		t.Fatal("worker retried before the scheduled time")
	}
	worker.ProcessOnce(store.nextAttempt)
	if !store.sent || mailer.sends != 2 {
		t.Fatalf("retry did not send and mark the receipt: sent=%v sends=%d", store.sent, mailer.sends)
	}
}

func TestLegalReviewWorkerRetriesIndependently(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	action := &LegalAction{
		Reference:          "LA-review",
		ReceivedAt:         now,
		Action:             "cancellation",
		Language:           "en",
		Name:               "Jane Doe",
		Email:              "jane@example.com",
		ContractIdentifier: "Order 123",
		TerminationType:    "ordinary",
		RequestedEnd:       "At the earliest possible date",
		Declaration:        "I hereby terminate the contract.",
	}
	store := &memoryReviewOutboxStore{
		action: action,
		job: LegalReviewOutbox{
			ID:                   2,
			LegalActionReference: action.Reference,
		},
	}
	mailer := &retryMailer{failures: 1}
	worker := NewLegalReviewWorker(store, mailer)

	worker.ProcessOnce(now)
	if store.sent || store.retries != 1 || mailer.sends != 1 {
		t.Fatalf("first review failure was not queued independently: sent=%v retries=%d sends=%d", store.sent, store.retries, mailer.sends)
	}
	worker.ProcessOnce(now.Add(30 * time.Second))
	if mailer.sends != 1 {
		t.Fatal("review worker retried before the scheduled time")
	}
	worker.ProcessOnce(store.nextAttempt)
	if !store.sent || mailer.sends != 2 {
		t.Fatalf("review retry did not send and mark the job: sent=%v sends=%d", store.sent, mailer.sends)
	}
}

func TestReceiptEmailContainsCompleteServerReceipt(t *testing.T) {
	action := &LegalAction{
		Reference:          "LA-test",
		ReceivedAt:         time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Action:             "cancellation",
		Language:           "en",
		Name:               "Jane Doe",
		Email:              "jane@example.com",
		ContractIdentifier: "Order 123",
		Scope:              "Subscription",
		TerminationType:    "extraordinary",
		Reason:             "Material breach",
		RequestedEnd:       "Immediately",
		Declaration:        "I hereby terminate the contract.",
	}
	replyTo := mail.Address{Name: "Coflnet Legal", Address: "legal@coflnet.com"}
	message := receiptEmail(
		mail.Address{Name: "Coflnet", Address: "noreply@coflnet.com"},
		replyTo,
		action,
	)
	parts := strings.SplitN(message, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("receipt email has no body: %s", message)
	}
	expectedBody := strings.ReplaceAll(action.receipt(), "\n", "\r\n") + "\r\n"
	if parts[1] != expectedBody {
		t.Fatalf("email body does not contain the exact server receipt:\n%s", parts[1])
	}
	if !strings.Contains(message, "To: <jane@example.com>") {
		t.Fatalf("receipt email has wrong recipient: %s", message)
	}
	if !strings.Contains(message, "Reply-To: "+replyTo.String()) {
		t.Fatalf("receipt email has wrong reply address: %s", message)
	}
}

func TestReviewEmailContainsExactCanonicalRecord(t *testing.T) {
	action := &LegalAction{
		Reference:          "LA-review",
		ReceivedAt:         time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Action:             "withdrawal",
		Language:           "de",
		Name:               "Jane Doe",
		Email:              "jane@example.com",
		ContractIdentifier: "Order 123",
		Scope:              "Subscription",
		Declaration:        "Hiermit widerrufe ich den Vertrag.",
	}
	inbox := mail.Address{Name: "Coflnet Legal", Address: "legal@example.com"}
	message := reviewEmail(mail.Address{Name: "Coflnet", Address: "sender@example.com"}, inbox, action)
	parts := strings.SplitN(message, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("review email has no body: %s", message)
	}
	expectedBody := strings.ReplaceAll(action.receipt(), "\n", "\r\n") + "\r\n"
	if parts[1] != expectedBody {
		t.Fatalf("review email does not contain the exact canonical record:\n%s", parts[1])
	}
	if !strings.Contains(message, "To: "+inbox.String()) ||
		!strings.Contains(message, "Message-ID: <LA-review-review@coflnet.com>") {
		t.Fatalf("review email has wrong routing metadata: %s", message)
	}
	if strings.Contains(message, "\r\nReply-To:") {
		t.Fatalf("internal review email unexpectedly has a reply address: %s", message)
	}
}

func TestSMTPConfigRequiresEverySetting(t *testing.T) {
	for _, key := range []string{
		"SMTP_DPA_VERIFIED", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM",
	} {
		t.Setenv(key, "")
	}
	if _, ok := SMTPConfigFromEnv(); ok {
		t.Fatal("empty SMTP configuration must fail closed")
	}

	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USER", "legal")
	t.Setenv("SMTP_PASSWORD", "secret")
	t.Setenv("SMTP_FROM", "Coflnet Legal <legal@example.com>")
	if _, ok := SMTPConfigFromEnv(); ok {
		t.Fatal("SMTP must remain disabled until its privacy review is attested")
	}
	t.Setenv("SMTP_DPA_VERIFIED", "true")
	config, ok := SMTPConfigFromEnv()
	if !ok || config.Host != "smtp.example.com" || config.From.Address != "legal@example.com" {
		t.Fatalf("valid SMTP configuration was rejected: %#v", config)
	}
	t.Setenv("SMTP_PASSWORD", "")
	if _, ok := SMTPConfigFromEnv(); !ok {
		t.Fatal("SMTP configuration without authentication was rejected")
	}
}

func TestLegalActionInboxRequiresValidAddress(t *testing.T) {
	for _, value := range []string{"", "not-an-address"} {
		t.Setenv("LEGAL_ACTION_INBOX", value)
		if _, ok := LegalActionInboxFromEnv(); ok {
			t.Fatalf("invalid legal action inbox %q was accepted", value)
		}
	}
	t.Setenv("LEGAL_ACTION_INBOX", "Coflnet Legal <legal@example.com>")
	inbox, ok := LegalActionInboxFromEnv()
	if !ok || inbox.Address != "legal@example.com" {
		t.Fatalf("valid legal action inbox was rejected: %#v", inbox)
	}
}

func TestLegalActionRetentionDefaultsToSixYearsAndRejectsInvalidOverrides(t *testing.T) {
	t.Setenv("LEGAL_ACTION_RETENTION_YEARS", "")
	if retention, ok := LegalActionRetentionFromEnv(); !ok || retention != 6 {
		t.Fatalf("empty retention configuration should use six-year default: %d %v", retention, ok)
	}
	t.Setenv("LEGAL_ACTION_RETENTION_YEARS", "0")
	if _, ok := LegalActionRetentionFromEnv(); ok {
		t.Fatal("zero-year retention must fail closed")
	}
	t.Setenv("LEGAL_ACTION_RETENTION_YEARS", "7")
	if _, ok := LegalActionRetentionFromEnv(); ok {
		t.Fatal("overlong retention must fail closed")
	}
	t.Setenv("LEGAL_ACTION_RETENTION_YEARS", "6")
	retention, ok := LegalActionRetentionFromEnv()
	if !ok || retention != 6 {
		t.Fatalf("valid retention configuration was rejected: %v", retention)
	}
}

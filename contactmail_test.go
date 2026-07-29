package main

import (
	"errors"
	"net/mail"
	"strings"
	"testing"
	"time"
)

type memoryContactEmailOutboxStore struct {
	job         ContactEmailOutbox
	nextAttempt time.Time
	completed   bool
	retries     int
}

func (*memoryContactEmailOutboxStore) QueueContactEmail(string, string, string) error {
	return nil
}

func (s *memoryContactEmailOutboxStore) ClaimContactEmailOutbox(now time.Time, _ time.Duration, _ int) ([]ContactEmailOutbox, error) {
	if s.completed || now.Before(s.nextAttempt) {
		return nil, nil
	}
	s.job.AttemptCount++
	return []ContactEmailOutbox{s.job}, nil
}

func (s *memoryContactEmailOutboxStore) CompleteContactEmail(uint64) error {
	s.completed = true
	return nil
}

func (s *memoryContactEmailOutboxStore) RetryContactEmail(_ uint64, next time.Time) error {
	s.retries++
	s.nextAttempt = next
	return nil
}

type retryContactMailer struct {
	failures int
	sends    int
}

func (m *retryContactMailer) SendContact(*ContactEmailOutbox) error {
	m.sends++
	if m.failures > 0 {
		m.failures--
		return errors.New("temporary SMTP failure")
	}
	return nil
}

func TestContactEmailWorkerRetriesThenCompletes(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := &memoryContactEmailOutboxStore{
		job: ContactEmailOutbox{ID: 1, Name: "Jane Doe", Email: "jane@example.com", Message: "Hi there"},
	}
	mailer := &retryContactMailer{failures: 1}
	worker := NewContactEmailWorker(store, mailer)

	worker.ProcessOnce(now)
	if store.completed || store.retries != 1 || mailer.sends != 1 {
		t.Fatalf("first SMTP failure was not queued for retry: completed=%v retries=%d sends=%d", store.completed, store.retries, mailer.sends)
	}
	worker.ProcessOnce(now.Add(30 * time.Second))
	if mailer.sends != 1 {
		t.Fatal("worker retried before the scheduled time")
	}
	worker.ProcessOnce(store.nextAttempt)
	if !store.completed || mailer.sends != 2 {
		t.Fatalf("retry did not send and complete the contact email: completed=%v sends=%d", store.completed, mailer.sends)
	}
}

func TestContactSubjectTruncatesLongMessages(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{"short message kept verbatim", "Hi, quick question", "Contact form: Hi, quick question"},
		{"empty message falls back", "   ", "Contact form submission"},
		{
			"long message truncated to first words",
			"one two three four five six seven eight nine ten",
			"Contact form: one two three four five six seven eight…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contactSubject(tc.message); got != tc.want {
				t.Fatalf("contactSubject(%q) = %q, want %q", tc.message, got, tc.want)
			}
		})
	}
}

func TestSMTPContactMailerComposesExpectedEmail(t *testing.T) {
	from := mail.Address{Name: "Coflnet", Address: "noreply@coflnet.com"}
	inbox := mail.Address{Name: "Coflnet Staff", Address: "staff@example.com"}

	job := &ContactEmailOutbox{
		ID:      42,
		Name:    "Jane Doe",
		Email:   "jane@example.com",
		Message: "Hi, I would love to work with you on the filter project.",
	}
	message := contactEmail(from, inbox, job)

	if !strings.Contains(message, "To: "+inbox.String()) {
		t.Fatalf("email is not addressed to the configured inbox: %s", message)
	}
	if !strings.Contains(message, "Reply-To: \"Jane Doe\" <jane@example.com>") {
		t.Fatalf("email reply-to does not target the submitter: %s", message)
	}
	if !strings.Contains(message, "Message-ID: <contact-42@coflnet.com>") {
		t.Fatalf("email has wrong message id: %s", message)
	}
	parts := strings.SplitN(message, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("contact email has no body: %s", message)
	}
	if !strings.Contains(parts[1], "Email: jane@example.com") ||
		!strings.Contains(parts[1], "Name: Jane Doe") ||
		!strings.Contains(parts[1], job.Message) {
		t.Fatalf("contact email body is missing submitted fields: %s", parts[1])
	}
}

func TestContactEmailReplyToOmitsNameWhenNotProvided(t *testing.T) {
	from := mail.Address{Address: "noreply@coflnet.com"}
	inbox := mail.Address{Address: "staff@example.com"}
	job := &ContactEmailOutbox{ID: 7, Email: "anon@example.com", Message: "no name given"}

	message := contactEmail(from, inbox, job)
	if !strings.Contains(message, "Reply-To: <anon@example.com>") {
		t.Fatalf("unexpected reply-to for a nameless submission: %s", message)
	}
}

func TestContactInboxFallsBackToLegalActionInboxWhenUnset(t *testing.T) {
	t.Setenv("CONTACT_INBOX", "")
	t.Setenv("LEGAL_ACTION_INBOX", "legal@example.com")

	inbox, ok := ContactInboxFromEnv()
	if !ok || inbox.Address != "legal@example.com" {
		t.Fatalf("expected fallback to legal action inbox, got %#v ok=%v", inbox, ok)
	}
}

func TestContactInboxPrefersItsOwnSettingAndFailsClosedWhenMalformed(t *testing.T) {
	t.Setenv("LEGAL_ACTION_INBOX", "legal@example.com")

	t.Setenv("CONTACT_INBOX", "Coflnet Contact <contact@example.com>")
	inbox, ok := ContactInboxFromEnv()
	if !ok || inbox.Address != "contact@example.com" {
		t.Fatalf("expected dedicated contact inbox to take precedence, got %#v ok=%v", inbox, ok)
	}

	t.Setenv("CONTACT_INBOX", "not-an-address")
	if _, ok := ContactInboxFromEnv(); ok {
		t.Fatal("a malformed CONTACT_INBOX must fail closed instead of silently falling back")
	}
}

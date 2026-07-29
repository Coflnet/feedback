package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"strings"
	"time"
)

const (
	contactEmailBatchSize = 1 // Keep the lease longer than the maximum SMTP attempt.
	contactEmailLease     = 2 * time.Minute
	contactEmailPoll      = 5 * time.Second
)

// ContactEmailOutbox durably stores a landing-page contact submission until it
// has been emailed to staff. Unlike the legal-action outboxes, there is no
// other persistent record of the submission to reference by foreign key, so
// this row carries the name/email/message itself. It is deleted as soon as
// delivery succeeds, keeping the retained window as short as possible.
type ContactEmailOutbox struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement"`
	Name          string     `gorm:"size:200"`
	Email         string     `gorm:"not null;size:254"`
	Message       string     `gorm:"type:text;not null"`
	CreatedAt     time.Time  `gorm:"not null"`
	NextAttemptAt time.Time  `gorm:"not null;index"`
	LockedUntil   *time.Time `gorm:"index"`
	AttemptCount  int        `gorm:"not null;default:0"`
}

type ContactEmailOutboxStore interface {
	QueueContactEmail(name, email, message string) error
	ClaimContactEmailOutbox(time.Time, time.Duration, int) ([]ContactEmailOutbox, error)
	CompleteContactEmail(uint64) error
	RetryContactEmail(uint64, time.Time) error
}

type ContactMailer interface {
	SendContact(*ContactEmailOutbox) error
}

// ContactInboxFromEnv resolves the mailbox contact-form submissions are
// delivered to. CONTACT_INBOX takes precedence; when it is not set at all the
// shared LEGAL_ACTION_INBOX is used instead. A CONTACT_INBOX that is set but
// malformed fails closed rather than silently falling back, matching the
// fail-closed handling of every other delivery setting in this service.
func ContactInboxFromEnv() (mail.Address, bool) {
	value := strings.TrimSpace(os.Getenv("CONTACT_INBOX"))
	if value == "" {
		return LegalActionInboxFromEnv()
	}
	inbox, err := mail.ParseAddress(value)
	if err != nil || inbox.Address == "" {
		return mail.Address{}, false
	}
	return *inbox, true
}

// SMTPContactMailer sends a queued contact-form submission over the service's
// existing SMTP configuration, replying-to the submitter's own address so
// staff can answer directly.
type SMTPContactMailer struct {
	sender *SMTPReceiptMailer
	inbox  mail.Address
}

func NewSMTPContactMailer(config SMTPConfig, inbox mail.Address) *SMTPContactMailer {
	return &SMTPContactMailer{sender: &SMTPReceiptMailer{config: config}, inbox: inbox}
}

func (m *SMTPContactMailer) SendContact(job *ContactEmailOutbox) error {
	return m.sender.send(m.inbox.Address, contactEmail(m.sender.config.From, m.inbox, job))
}

// contactEmail renders the staff-facing message for a queued contact-form
// submission: subject "Contact form: <truncated first words>", the
// submitter's email/optional name/message in the body, and the submitter's
// own address as Reply-To so staff can answer directly.
func contactEmail(from, inbox mail.Address, job *ContactEmailOutbox) string {
	replyTo := mail.Address{Address: job.Email}
	if job.Name != "" {
		replyTo.Name = job.Name
	}
	return plainTextEmail(
		from,
		inbox,
		replyTo,
		contactSubject(job.Message),
		fmt.Sprintf("contact-%d", job.ID),
		contactBody(job),
	)
}

// contactSubject builds "Contact form: <first words>", truncated so a spammy
// wall of text never produces an unreadable subject line.
func contactSubject(message string) string {
	const maxWords = 8
	const maxRunes = 60

	words := strings.Fields(message)
	truncated := false
	if len(words) > maxWords {
		words = words[:maxWords]
		truncated = true
	}
	subject := strings.Join(words, " ")
	if runes := []rune(subject); len(runes) > maxRunes {
		subject = strings.TrimSpace(string(runes[:maxRunes]))
		truncated = true
	}
	if subject == "" {
		return "Contact form submission"
	}
	if truncated {
		subject += "…"
	}
	return "Contact form: " + subject
}

func contactBody(job *ContactEmailOutbox) string {
	lines := []string{
		"New contact form submission from the coflnet.com landing page.",
		"",
		"Email: " + job.Email,
	}
	if job.Name != "" {
		lines = append(lines, "Name: "+job.Name)
	}
	lines = append(lines, "", "Message:", job.Message)
	return strings.Join(lines, "\n")
}

// ContactEmailWorker claims queued contact-form submissions and emails them,
// retrying transient SMTP failures with backoff. A submission is never lost
// on a temporary outage: it stays queued until it is delivered.
type ContactEmailWorker struct {
	store  ContactEmailOutboxStore
	mailer ContactMailer
}

func NewContactEmailWorker(store ContactEmailOutboxStore, mailer ContactMailer) *ContactEmailWorker {
	return &ContactEmailWorker{store: store, mailer: mailer}
}

func (w *ContactEmailWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(contactEmailPoll)
	defer ticker.Stop()
	w.ProcessOnce(time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.ProcessOnce(now.UTC())
		}
	}
}

func (w *ContactEmailWorker) ProcessOnce(now time.Time) {
	jobs, err := w.store.ClaimContactEmailOutbox(now, contactEmailLease, contactEmailBatchSize)
	if err != nil {
		slog.Error("claiming contact form email outbox failed")
		return
	}
	for _, job := range jobs {
		if err := w.mailer.SendContact(&job); err == nil {
			if err := w.store.CompleteContactEmail(job.ID); err != nil {
				slog.Error("completing contact form email failed", "outbox_id", job.ID)
			}
			continue
		}
		next := now.Add(receiptRetryDelay(job.AttemptCount))
		if err := w.store.RetryContactEmail(job.ID, next); err != nil {
			slog.Error("scheduling contact form email retry failed", "outbox_id", job.ID)
		}
	}
}

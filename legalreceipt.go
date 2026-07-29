package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	receiptBatchSize = 1 // Keep the lease longer than the maximum SMTP attempt.
	receiptLease     = 2 * time.Minute
	receiptPoll      = 5 * time.Second
	receiptPurgePoll = 24 * time.Hour
)

type LegalReceiptOutbox struct {
	ID                   uint64       `gorm:"primaryKey;autoIncrement"`
	LegalActionReference string       `gorm:"not null;size:35;uniqueIndex"`
	LegalAction          *LegalAction `gorm:"foreignKey:LegalActionReference;references:Reference;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
	CreatedAt            time.Time    `gorm:"not null"`
	NextAttemptAt        time.Time    `gorm:"not null;index"`
	LockedUntil          *time.Time   `gorm:"index"`
	AttemptCount         int          `gorm:"not null;default:0"`
	SentAt               *time.Time   `gorm:"index"`
}

type LegalReceiptOutboxStore interface {
	ClaimLegalReceiptOutbox(time.Time, time.Duration, int) ([]LegalReceiptOutbox, error)
	LoadLegalAction(string) (*LegalAction, error)
	MarkLegalReceiptSent(uint64, time.Time) error
	RetryLegalReceipt(uint64, time.Time) error
	PurgeExpiredLegalActions(time.Time) (int64, error)
}

type ReceiptMailer interface {
	SendReceipt(*LegalAction) error
}

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     mail.Address
}

func SMTPConfigFromEnv() (SMTPConfig, bool) {
	if strings.TrimSpace(os.Getenv("SMTP_DPA_VERIFIED")) != "true" {
		return SMTPConfig{}, false
	}
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	port, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SMTP_PORT")))
	user := strings.TrimSpace(os.Getenv("SMTP_USER"))
	password := os.Getenv("SMTP_PASSWORD")
	from, fromErr := mail.ParseAddress(strings.TrimSpace(os.Getenv("SMTP_FROM")))
	if host == "" || err != nil || port < 1 || port > 65535 ||
		user == "" || fromErr != nil || from.Address == "" {
		return SMTPConfig{}, false
	}
	return SMTPConfig{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		From:     *from,
	}, true
}

func LegalActionInboxFromEnv() (mail.Address, bool) {
	inbox, err := mail.ParseAddress(strings.TrimSpace(os.Getenv("LEGAL_ACTION_INBOX")))
	if err != nil || inbox.Address == "" {
		return mail.Address{}, false
	}
	return *inbox, true
}

func LegalActionRetentionFromEnv() (int, bool) {
	value := strings.TrimSpace(os.Getenv("LEGAL_ACTION_RETENTION_YEARS"))
	if value == "" {
		return 6, true
	}
	years, err := strconv.Atoi(value)
	if err != nil || years != 6 {
		return 0, false
	}
	return years, true
}

type SMTPReceiptMailer struct {
	config SMTPConfig
	inbox  mail.Address
}

func NewSMTPReceiptMailer(config SMTPConfig, inbox mail.Address) *SMTPReceiptMailer {
	return &SMTPReceiptMailer{config: config, inbox: inbox}
}

func (m *SMTPReceiptMailer) SendReceipt(action *LegalAction) error {
	return m.send(
		[]string{action.Email, m.inbox.Address},
		receiptEmail(m.config.From, m.inbox, action),
	)
}

func (m *SMTPReceiptMailer) send(recipients []string, message string) error {
	client, err := m.connect()
	if err != nil {
		return err
	}
	defer client.Close()

	if m.config.Password != "" {
		if err := client.Auth(smtp.PlainAuth("", m.config.User, m.config.Password, m.config.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(m.config.From.Address); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		key := strings.ToLower(strings.TrimSpace(recipient))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (m *SMTPReceiptMailer) connect() (*smtp.Client, error) {
	address := net.JoinHostPort(m.config.Host, strconv.Itoa(m.config.Port))
	tlsConfig := &tls.Config{
		ServerName: m.config.Host,
		MinVersion: tls.VersionTLS12,
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if m.config.Port == 465 {
		connection, err := tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
		if err != nil {
			return nil, err
		}
		_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
		return smtp.NewClient(connection, m.config.Host)
	}

	connection, err := dialer.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	client, err := smtp.NewClient(connection, m.config.Host)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if supported, _ := client.Extension("STARTTLS"); !supported {
		_ = client.Close()
		return nil, fmt.Errorf("SMTP server does not support STARTTLS")
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func receiptEmail(from, replyTo mail.Address, action *LegalAction) string {
	subject := "Coflnet electronic receipt"
	if action.Language == "de" {
		subject = "Coflnet Eingangsbestätigung"
	}
	return legalActionEmail(
		from,
		mail.Address{Address: action.Email},
		replyTo,
		replyTo,
		subject,
		action.Reference+"-receipt",
		action,
	)
}

func legalActionEmail(from, to, cc, replyTo mail.Address, subject, messageID string, action *LegalAction) string {
	return plainTextEmail(from, to, cc, replyTo, subject, messageID, action.receipt())
}

// plainTextEmail renders a minimal RFC 5322 message with a UTF-8 plain-text
// body. Shared by every outbound mailer so header formatting stays identical
// across delivery paths.
func plainTextEmail(from, to, cc, replyTo mail.Address, subject, messageID, body string) string {
	headers := []string{
		"From: " + from.String(),
		"To: " + to.String(),
	}
	if cc.Address != "" {
		headers = append(headers, "Cc: "+cc.String())
	}
	if replyTo.Address != "" {
		headers = append(headers, "Reply-To: "+replyTo.String())
	}
	headers = append(headers,
		"Date: "+time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <"+messageID+"@coflnet.com>",
		"Subject: "+mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
	)
	normalizedBody := strings.ReplaceAll(body, "\n", "\r\n")
	return strings.Join(headers, "\r\n") + "\r\n\r\n" + normalizedBody + "\r\n"
}

type LegalReceiptWorker struct {
	store  LegalReceiptOutboxStore
	mailer ReceiptMailer
}

func NewLegalReceiptWorker(store LegalReceiptOutboxStore, mailer ReceiptMailer) *LegalReceiptWorker {
	return &LegalReceiptWorker{store: store, mailer: mailer}
}

func (w *LegalReceiptWorker) Run(ctx context.Context) {
	receiptTicker := time.NewTicker(receiptPoll)
	defer receiptTicker.Stop()
	w.ProcessOnce(time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-receiptTicker.C:
			w.ProcessOnce(now.UTC())
		}
	}
}

func (w *LegalReceiptWorker) ProcessOnce(now time.Time) {
	jobs, err := w.store.ClaimLegalReceiptOutbox(now, receiptLease, receiptBatchSize)
	if err != nil {
		slog.Error("claiming legal receipt outbox failed")
		return
	}
	for _, job := range jobs {
		action, err := w.store.LoadLegalAction(job.LegalActionReference)
		if err == nil {
			err = w.mailer.SendReceipt(action)
		}
		if err == nil {
			if err := w.store.MarkLegalReceiptSent(job.ID, time.Now().UTC()); err != nil {
				slog.Error("marking legal receipt sent failed", "outbox_id", job.ID)
			}
			continue
		}
		next := now.Add(receiptRetryDelay(job.AttemptCount))
		if err := w.store.RetryLegalReceipt(job.ID, next); err != nil {
			slog.Error("scheduling legal receipt retry failed", "outbox_id", job.ID)
		}
	}
}

func (w *LegalReceiptWorker) RunPurger(ctx context.Context) {
	ticker := time.NewTicker(receiptPurgePoll)
	defer ticker.Stop()
	w.PurgeOnce(time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			w.PurgeOnce(now.UTC())
		}
	}
}

func (w *LegalReceiptWorker) PurgeOnce(now time.Time) {
	deleted, err := w.store.PurgeExpiredLegalActions(now)
	if err != nil {
		slog.Error("purging expired legal actions failed")
		return
	}
	if deleted > 0 {
		slog.Info("purged expired legal actions", "count", deleted)
	}
}

func receiptRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}

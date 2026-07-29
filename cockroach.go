package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type FeedbackRequest struct {
	Feedback     string      `json:"feedback"`
	Data         interface{} `json:"data"`
	User         string      `json:"user"`
	Context      string      `json:"context"`
	FeedbackName string      `json:"fedbackName"`
	Timestamp    time.Time   `json:"timestamp"`
}

type Feedback struct {
	gorm.Model
	Feedback               string    `json:"feedback"`
	AdditionalInformations string    `json:"additionalInformations"`
	User                   string    `json:"user"`
	Context                string    `json:"context"`
	FeedbackName           string    `json:"fedbackName"`
	Timestamp              time.Time `json:"timestamp"`
}

type DatabaseHandler struct {
	db *gorm.DB
}

// ErrDuplicateFeedback is returned when the last stored feedback matches the
// incoming one and should not be saved or forwarded again.
var ErrDuplicateFeedback = errors.New("duplicate feedback")

func NewDatabaseHandler() *DatabaseHandler {
	d := &DatabaseHandler{}
	return d
}

func (d *DatabaseHandler) Connect() error {
	slog.Debug("connecting to the database..")

	db, err := gorm.Open(postgres.Open(d.dsnString()))
	if err != nil {
		return err
	}

	d.db = db

	return d.migrations()
}

func (d *DatabaseHandler) migrations() error {
	err := d.db.AutoMigrate(
		&Feedback{},
		&LegalAction{},
		&LegalReceiptOutbox{},
		&LegalReviewOutbox{},
		&ContactEmailOutbox{},
	)
	if err != nil {
		return err
	}

	return nil
}

func (d *DatabaseHandler) QueueContactEmail(name, email, message string) error {
	now := time.Now().UTC()
	// The default GORM logger interpolates query values. Suppress it so the
	// submitted name/email/message never enter SQL logs.
	return d.db.Session(&gorm.Session{Logger: logger.Discard}).Create(&ContactEmailOutbox{
		Name:          name,
		Email:         email,
		Message:       message,
		CreatedAt:     now,
		NextAttemptAt: now,
	}).Error
}

func (d *DatabaseHandler) ClaimContactEmailOutbox(now time.Time, lease time.Duration, limit int) ([]ContactEmailOutbox, error) {
	var jobs []ContactEmailOutbox
	err := d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("next_attempt_at <= ? AND (locked_until IS NULL OR locked_until < ?)", now, now).
			Order("next_attempt_at, id").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uint64, len(jobs))
		lockedUntil := now.Add(lease)
		for index := range jobs {
			ids[index] = jobs[index].ID
			jobs[index].AttemptCount++
			jobs[index].LockedUntil = &lockedUntil
		}
		return tx.Model(&ContactEmailOutbox{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"attempt_count": gorm.Expr("attempt_count + 1"),
				"locked_until":  lockedUntil,
			}).Error
	})
	return jobs, err
}

func (d *DatabaseHandler) CompleteContactEmail(id uint64) error {
	return d.db.Delete(&ContactEmailOutbox{}, id).Error
}

func (d *DatabaseHandler) RetryContactEmail(id uint64, nextAttemptAt time.Time) error {
	return d.db.Model(&ContactEmailOutbox{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"next_attempt_at": nextAttemptAt,
			"locked_until":    nil,
		}).Error
}

func (d *DatabaseHandler) AcceptLegalAction(action *LegalAction) (*LegalAction, bool, error) {
	// The default GORM logger interpolates query values. Suppress it for this
	// transaction so declarations and email addresses cannot enter SQL logs.
	var accepted *LegalAction
	created := false
	err := d.db.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "submission_id"}},
			DoNothing: true,
		}).Create(action)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing LegalAction
			if err := tx.Where("submission_id = ?", action.SubmissionID).First(&existing).Error; err != nil {
				return err
			}
			accepted = &existing
			return nil
		}
		receipt := &LegalReceiptOutbox{
			LegalActionReference: action.Reference,
			CreatedAt:            action.ReceivedAt,
			NextAttemptAt:        action.ReceivedAt,
		}
		if action.Email == "" {
			// The HTTP response is the immediate durable requester
			// confirmation when no optional email address was supplied.
			receipt.SentAt = &action.ReceivedAt
		}
		if err := tx.Create(receipt).Error; err != nil {
			return err
		}
		if err := tx.Create(&LegalReviewOutbox{
			LegalActionReference: action.Reference,
			CreatedAt:            action.ReceivedAt,
			NextAttemptAt:        action.ReceivedAt,
		}).Error; err != nil {
			return err
		}
		accepted = action
		created = true
		return nil
	})
	return accepted, created, err
}

func (d *DatabaseHandler) ClaimLegalReceiptOutbox(now time.Time, lease time.Duration, limit int) ([]LegalReceiptOutbox, error) {
	var jobs []LegalReceiptOutbox
	err := d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("sent_at IS NULL AND next_attempt_at <= ? AND (locked_until IS NULL OR locked_until < ?)", now, now).
			Order("next_attempt_at, id").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uint64, len(jobs))
		lockedUntil := now.Add(lease)
		for index := range jobs {
			ids[index] = jobs[index].ID
			jobs[index].AttemptCount++
			jobs[index].LockedUntil = &lockedUntil
		}
		return tx.Model(&LegalReceiptOutbox{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"attempt_count": gorm.Expr("attempt_count + 1"),
				"locked_until":  lockedUntil,
			}).Error
	})
	return jobs, err
}

func (d *DatabaseHandler) LoadLegalAction(reference string) (*LegalAction, error) {
	var action LegalAction
	if err := d.db.First(&action, "reference = ?", reference).Error; err != nil {
		return nil, err
	}
	return &action, nil
}

func (d *DatabaseHandler) MarkLegalReceiptSent(id uint64, sentAt time.Time) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		var outbox LegalReceiptOutbox
		if err := tx.Select("legal_action_reference").
			Where("id = ? AND sent_at IS NULL", id).
			First(&outbox).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if err := tx.Model(&LegalReceiptOutbox{}).
			Where("id = ? AND sent_at IS NULL", id).
			Updates(map[string]interface{}{
				"sent_at":      sentAt,
				"locked_until": nil,
			}).Error; err != nil {
			return err
		}
		return extendLegalActionExpiry(tx, outbox.LegalActionReference, sentAt)
	})
}

func (d *DatabaseHandler) RetryLegalReceipt(id uint64, nextAttemptAt time.Time) error {
	return d.db.Model(&LegalReceiptOutbox{}).
		Where("id = ? AND sent_at IS NULL", id).
		Updates(map[string]interface{}{
			"next_attempt_at": nextAttemptAt,
			"locked_until":    nil,
		}).Error
}

func (d *DatabaseHandler) ClaimLegalReviewOutbox(now time.Time, lease time.Duration, limit int) ([]LegalReviewOutbox, error) {
	var jobs []LegalReviewOutbox
	err := d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("sent_at IS NULL AND next_attempt_at <= ? AND (locked_until IS NULL OR locked_until < ?)", now, now).
			Order("next_attempt_at, id").
			Limit(limit).
			Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uint64, len(jobs))
		lockedUntil := now.Add(lease)
		for index := range jobs {
			ids[index] = jobs[index].ID
			jobs[index].AttemptCount++
			jobs[index].LockedUntil = &lockedUntil
		}
		return tx.Model(&LegalReviewOutbox{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"attempt_count": gorm.Expr("attempt_count + 1"),
				"locked_until":  lockedUntil,
			}).Error
	})
	return jobs, err
}

func (d *DatabaseHandler) MarkLegalReviewSent(id uint64, sentAt time.Time) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		var outbox LegalReviewOutbox
		if err := tx.Select("legal_action_reference").
			Where("id = ? AND sent_at IS NULL", id).
			First(&outbox).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if err := tx.Model(&LegalReviewOutbox{}).
			Where("id = ? AND sent_at IS NULL", id).
			Updates(map[string]interface{}{
				"sent_at":      sentAt,
				"locked_until": nil,
			}).Error; err != nil {
			return err
		}
		return extendLegalActionExpiry(tx, outbox.LegalActionReference, sentAt)
	})
}

func extendLegalActionExpiry(tx *gorm.DB, reference string, sentAt time.Time) error {
	expiresAt := legalActionExpiry(sentAt, 6)
	return tx.Model(&LegalAction{}).
		Where("reference = ? AND (expires_at IS NULL OR expires_at < ?)", reference, expiresAt).
		Update("expires_at", expiresAt).Error
}

func (d *DatabaseHandler) RetryLegalReview(id uint64, nextAttemptAt time.Time) error {
	return d.db.Model(&LegalReviewOutbox{}).
		Where("id = ? AND sent_at IS NULL", id).
		Updates(map[string]interface{}{
			"next_attempt_at": nextAttemptAt,
			"locked_until":    nil,
		}).Error
}

func (d *DatabaseHandler) PurgeExpiredLegalActions(now time.Time) (int64, error) {
	var deleted int64
	err := d.db.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		sentReceiptReferences := tx.Model(&LegalReceiptOutbox{}).
			Select("legal_action_reference").
			Where("sent_at IS NOT NULL")
		sentReviewReferences := tx.Model(&LegalReviewOutbox{}).
			Select("legal_action_reference").
			Where("sent_at IS NOT NULL")
		var references []string
		if err := tx.Model(&LegalAction{}).
			Where("expires_at IS NOT NULL AND expires_at <= ? AND legal_hold = ?", now, false).
			Where("reference IN (?)", sentReceiptReferences).
			Where("reference IN (?)", sentReviewReferences).
			Pluck("reference", &references).Error; err != nil || len(references) == 0 {
			return err
		}
		if err := tx.Where("legal_action_reference IN ? AND sent_at IS NOT NULL", references).
			Delete(&LegalReceiptOutbox{}).Error; err != nil {
			return err
		}
		if err := tx.Where("legal_action_reference IN ? AND sent_at IS NOT NULL", references).
			Delete(&LegalReviewOutbox{}).Error; err != nil {
			return err
		}
		result := tx.Where("reference IN ? AND expires_at <= ? AND legal_hold = ?", references, now, false).
			Delete(&LegalAction{})
		deleted = result.RowsAffected
		return result.Error
	})
	return deleted, err
}

func (d *DatabaseHandler) SaveFeedback(f *Feedback) error {
	// Try to load the most recent feedback and compare. If identical, skip.
	var last Feedback
	res := d.db.Order("created_at desc").First(&last)
	if res.Error == nil {
		if last.Feedback == f.Feedback && last.AdditionalInformations == f.AdditionalInformations {
			slog.Debug("detected duplicate feedback; skipping save")
			return ErrDuplicateFeedback
		}
	} else if !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return res.Error
	}

	res = d.db.Create(f)
	if res.Error != nil {
		return res.Error
	}

	slog.Debug(fmt.Sprintf("Inserted feedback with id %d", f.ID))
	return nil
}

func (d *DatabaseHandler) dsnString() string {
	v := os.Getenv("COCKROACH_CONNECTION")
	if v == "" {
		panic("COCKROACH_CONNECTION is not set")
	}
	return v
}

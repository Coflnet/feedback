package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type legacyLegalAction struct {
	Reference    string `gorm:"primaryKey;size:35"`
	SubmissionID string `gorm:"size:36;uniqueIndex"`
}

func (legacyLegalAction) TableName() string { return "legal_actions" }

type legacyLegalReceiptOutbox struct {
	ID                   uint64 `gorm:"primaryKey;autoIncrement"`
	LegalActionReference string `gorm:"not null;size:35;uniqueIndex"`
}

func (legacyLegalReceiptOutbox) TableName() string { return "legal_receipt_outboxes" }

func TestMigrationModelsUseUniqueConstraints(t *testing.T) {
	tests := []struct {
		name  string
		model any
		field string
	}{
		{name: "legal action submission", model: &LegalAction{}, field: "SubmissionID"},
		{name: "legal receipt reference", model: &LegalReceiptOutbox{}, field: "LegalActionReference"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := schema.Parse(test.model, &sync.Map{}, schema.NamingStrategy{})
			if err != nil {
				t.Fatal(err)
			}
			parsed.ParseIndexes()
			field := parsed.LookUpField(test.field)
			if field == nil {
				t.Fatalf("field %s is missing", test.field)
			}
			if !field.Unique || field.UniqueIndex != "" {
				t.Fatalf("field %s must use a unique constraint, got Unique=%v UniqueIndex=%q", test.field, field.Unique, field.UniqueIndex)
			}
		})
	}
}

func TestMigrationsAreRepeatableOnCockroach(t *testing.T) {
	dsn := os.Getenv("COCKROACH_TEST_CONNECTION")
	if dsn == "" {
		t.Skip("COCKROACH_TEST_CONNECTION is not set to a disposable test database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var databaseName string
	if err := db.Raw("SELECT current_database()").Scan(&databaseName).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "feedback_migration_test") {
		t.Fatalf("refusing migration test against non-test database %q", databaseName)
	}
	var existingTables int64
	if err := db.Raw(`
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('legal_actions', 'legal_receipt_outboxes')
	`).Scan(&existingTables).Error; err != nil {
		t.Fatal(err)
	}
	if existingTables != 0 {
		t.Fatalf("refusing migration test because target tables already exist in %q", databaseName)
	}

	handler := &DatabaseHandler{db: db}
	if err := db.AutoMigrate(&legacyLegalAction{}, &legacyLegalReceiptOutbox{}); err != nil {
		t.Fatalf("creating legacy unique-index schema failed: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := handler.migrations(); err != nil {
			t.Fatalf("migration attempt %d failed: %v", attempt, err)
		}
	}
}

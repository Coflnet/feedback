package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type MongoFeedback struct {
	ID           primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Feedback     string             `bson:"feedback" json:"feedback"`
	Data         interface{}        `bson:"data" json:"data"`
	User         string             `bson:"user" json:"user"`
	Context      string             `bson:"context" json:"context"`
	FeedbackName string             `bson:"feedback_name" json:"fedbackName"`
	Timestamp    time.Time          `bson:"timestamp" json:"timestamp"`
}

type Feedback struct {
	gorm.Model
	Feedback     string    `json:"feedback"`
	User         string    `json:"user"`
	Context      string    `json:"context"`
	FeedbackName string    `json:"fedbackName"`
	Timestamp    time.Time `json:"timestamp"`
}

type DatabaseHandler struct {
	db *gorm.DB
}

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
	err := d.db.AutoMigrate(&Feedback{})
	if err != nil {
		return err
	}

	return nil
}

func (d *DatabaseHandler) SaveFeedback(f *Feedback) error {
	res := d.db.Create(f)
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

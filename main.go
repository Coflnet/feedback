package main

import (
	"os"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/mysql"
  	"gorm.io/gorm"
)

func main() {
	app := fiber.New()

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	initDB()

	app.Post("/api/", func(c *fiber.Ctx) error {
		c.Accepts("application/json")

		var feedback Feedback
		if err := c.BodyParser(&feedback); err != nil {
			log.Error().Err(err).Msg("fiber exited")
			return err
		}

		if err := saveFeedback(&feedback); err != nil {
			return err
		}
		return nil
	})

	err := app.Listen(":3000")
	log.Error().Err(err).Msg("fiber exited")
}

func saveFeedback(f *Feedback) error {

	db := getDbConnection()
	mysqlDB, err := db.DB()
	if err != nil {
		log.Error().Err(err).Msg("Error connecting to mysql db")
		return err
	}
	defer mysqlDB.Close()
	db.Create(f)
	log.Info().Msg("Added Feedback " + f.Feedback)
	return nil
}

func getDbConnection() *gorm.DB {
	dsn := os.Getenv("DB_USER")+":"+os.Getenv("DB_PASSWORD")+"@tcp("+os.Getenv("DB_HOST")+":"+os.Getenv("DB_PORT")+")/"+os.Getenv("DB_NAME")+"?charset=utf8&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.New(mysql.Config{
  		DSN: dsn,
  		DefaultStringSize: 256,
  		DisableDatetimePrecision: true,
  		DontSupportRenameIndex: true,
  		DontSupportRenameColumn: true,
  		SkipInitializeWithVersion: false,
	}), &gorm.Config{})
	if err != nil {
		log.Error().Err(err).Msg("Error connecting to database")
		os.Exit(1)
	}
	return db
}

func initDB() {
	db := getDbConnection()
	mysqlDB, err := db.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("Error connecting to database")
	}
	defer mysqlDB.Close()
	db.AutoMigrate(&Feedback{})
}

type Feedback struct {
	Feedback 		string
	User     		string
	Context  		string
	FeedbackName 	string
}

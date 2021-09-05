package main

import (
	"net/http"
	"os"
	"time"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/driver/mysql"
  	"gorm.io/gorm"
	opentracing "github.com/opentracing/opentracing-go"
    "github.com/uber/jaeger-lib/metrics"

    "github.com/uber/jaeger-client-go"
    jaegercfg "github.com/uber/jaeger-client-go/config"
    jaegerlog "github.com/uber/jaeger-client-go/log"
)

var (
    feedbacksGivenHistogram = promauto.NewCounter(prometheus.CounterOpts{
        Name: "times_feedback_given",
        Help: "the times feedback was given",
    })
)

func main() {
	time.Sleep(time.Second * 30)
	go startMetrics()
	initTracing()
	app := fiber.New()

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	initDB()

	app.Post("/api/", func(c *fiber.Ctx) error {
		tracer := opentracing.GlobalTracer()

		span := tracer.StartSpan("say-hello")
		log.Info().Msg("start span")
		c.Accepts("application/json")

		var feedback Feedback
		if err := c.BodyParser(&feedback); err != nil {
			log.Error().Err(err).Msg("fiber exited")
			return err
		}

		if err := saveFeedback(&feedback); err != nil {
			return err
		}
		c.Status(204)
		span.Finish()
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
	feedbacksGivenHistogram.Inc()

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

func startMetrics() {
	http.Handle("/metrics", promhttp.Handler())
    http.ListenAndServe(":2112", nil)
}

func initTracing() {
	cfg := jaegercfg.Configuration{
        ServiceName: "your_service_name",
        Sampler:     &jaegercfg.SamplerConfig{
            Type:  jaeger.SamplerTypeConst,
            Param: 1,
        },
        Reporter:    &jaegercfg.ReporterConfig{
            LogSpans: true,
        },
    }

    // Example logger and metrics factory. Use github.com/uber/jaeger-client-go/log
    // and github.com/uber/jaeger-lib/metrics respectively to bind to real logging and metrics
    // frameworks.
    jLogger := jaegerlog.StdLogger
    jMetricsFactory := metrics.NullFactory

    // Initialize tracer with a logger and a metrics factory
    tracer, closer, err := cfg.NewTracer(
        jaegercfg.Logger(jLogger),
        jaegercfg.Metrics(jMetricsFactory),
    )

	if err != nil {
		log.Fatal().Err(err).Msg("failed initialize jaeger")
	}

    // Set the singleton opentracing.Tracer with the Jaeger tracer.
    opentracing.SetGlobalTracer(tracer)
    defer closer.Close()
}

type Feedback struct {
	Feedback 		string `gorm:"type:text"`
	User     		string `gorm:"type:text"`
	Context  		string `gorm:"type:text"`
	FeedbackName 		string `gorm:"type:text"`
}

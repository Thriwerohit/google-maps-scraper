package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gosom/google-maps-scraper/config"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/databaserunner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
	"github.com/gosom/google-maps-scraper/runner/installplaywright"
	"github.com/gosom/google-maps-scraper/runner/lambdaaws"
	"github.com/gosom/google-maps-scraper/runner/webrunner"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var kafkaConfig runner.KafkaConfig

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	runner.Banner()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan

		log.Println("Received signal, shutting down...")

		cancel()
	}()

	loggerConfig := zap.NewProductionConfig()
	loggerLevel, _ := zapcore.ParseLevel("info")
	loggerConfig.Level = zap.NewAtomicLevelAt(loggerLevel)

	logger, _ := loggerConfig.Build(
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	defer logger.Sync()

	sugar := logger.Sugar()

	kafkaClient, err := runner.NewKafkaClient(kafkaConfig, sugar)
	if err != nil {
		cancel()
		sugar.Errorw("Failed to create Kafka client", "error", err)
		os.Exit(1)
	}
	cfg := runner.ParseConfig()
	cfg.KafkaConfig = kafkaConfig
	cfg.KafkaClient = kafkaClient

	runnerInstance, err := runnerFactory(cfg)
	if err != nil {
		cancel()
		os.Stderr.WriteString(err.Error() + "\n")

		runner.Telemetry().Close()

		os.Exit(1)
	}

	if err := runnerInstance.Run(ctx); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")

		_ = runnerInstance.Close(ctx)
		runner.Telemetry().Close()

		cancel()

		os.Exit(1)
	}

	_ = runnerInstance.Close(ctx)
	runner.Telemetry().Close()

	cancel()

	os.Exit(0)
}

func runnerFactory(cfg *runner.Config) (runner.Runner, error) {
	switch cfg.RunMode {
	case runner.RunModeFile:
		return filerunner.New(cfg)
	case runner.RunModeDatabase, runner.RunModeDatabaseProduce:
		return databaserunner.New(cfg)
	case runner.RunModeInstallPlaywright:
		return installplaywright.New(cfg)
	case runner.RunModeWeb:
		return webrunner.New(cfg)
	case runner.RunModeAwsLambda:
		return lambdaaws.New(cfg)
	case runner.RunModeAwsLambdaInvoker:
		return lambdaaws.NewInvoker(cfg)
	default:
		return nil, fmt.Errorf("%w: %d", runner.ErrInvalidRunMode, cfg.RunMode)
	}
}

func init() {
	Cfg := config.Init()

	kafkaConfig = runner.KafkaConfig{
		Topics:                Cfg.KafkaConfig.Topics,
		Brokers:               Cfg.KafkaConfig.Brokers,
		Subjects:              Cfg.KafkaConfig.Subjects,
		SchemaRegistryUrl:     Cfg.KafkaConfig.SchemaRegistryUrl,
		SchemaRegistrySubject: Cfg.KafkaConfig.SchemaRegistrySubject,
		SASLUser:              Cfg.KafkaConfig.SASLUser,
		SASLPassword:          Cfg.KafkaConfig.SASLPassword,
	}

}

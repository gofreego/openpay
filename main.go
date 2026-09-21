package main

import (
	"context"
	"flag"
	"time"

	"github.com/gofreego/openpay/cmd/grpc_server"
	"github.com/gofreego/openpay/cmd/http_server"
	"github.com/gofreego/openpay/cmd/worker"
	"github.com/gofreego/openpay/internal/configs"
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/middleware"
	"github.com/gofreego/openpay/internal/telemetry"

	"github.com/gofreego/goutils/apputils"
	"github.com/gofreego/goutils/logger"
)

var (
	env  string
	path string
)

func main() {
	flag.StringVar(&env, "env", "dev", "-env=dev")
	flag.StringVar(&path, "path", ".", "-path=./")
	flag.Parse()
	ctx := context.Background()

	conf := configs.LoadConfig(ctx, path, env)

	conf.Logger.InitiateLogger()
	logger.AddMiddleLayers(logger.RequestMiddleLayer, middleware.TraceMiddleLayer)

	shutdownTelemetry, err := telemetry.Setup(ctx, &conf.Telemetry)
	if err != nil {
		// Telemetry must never stop a payments service from starting. Carry on
		// blind rather than refusing to serve.
		logger.Error(ctx, "failed to start telemetry, continuing without it: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer flushTelemetry(ctx, shutdownTelemetry)

	// starting application
	var apps []apputils.Application
	for _, appName := range conf.AppNames {
		switch appName {
		case constants.HTTP_SERVER:
			apps = append(apps, http_server.NewHTTPServer(conf))
		case constants.GRPC_SERVER:
			apps = append(apps, grpc_server.NewGRPCServer(conf))
		case constants.WORKER:
			apps = append(apps, worker.NewWorker(conf))
		default:
			logger.Panic(ctx, "invalid application name provided `%s`", appName)
		}
	}

	for _, app := range apps {
		logger.Info(ctx, "Starting %s", app.Name())
		go app.Run(ctx)
	}

	apputils.GracefulShutdown(ctx, apps...)
}

// flushTelemetry pushes buffered spans and metrics before the process exits.
// Without it the final spans of a shutdown — often the ones explaining why —
// never leave the process. It gets its own timeout because the parent context
// may already be cancelled by the time we get here.
func flushTelemetry(ctx context.Context, shutdown telemetry.Shutdown) {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := shutdown(flushCtx); err != nil {
		logger.Error(flushCtx, "failed to flush telemetry: %v", err)
	}
}

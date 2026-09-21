package http_server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/configs"
	"github.com/gofreego/openpay/internal/health"
	"github.com/gofreego/openpay/internal/middleware"
	"github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/gofreego/goutils/api"
	"github.com/gofreego/goutils/api/debug"

	"github.com/gofreego/goutils/logger"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

type HTTPServer struct {
	cfg    *configs.Configuration
	server *http.Server
}

func (a *HTTPServer) Name() string {
	return "HTTP_Server"
}

func (a *HTTPServer) Shutdown(ctx context.Context) {
	if err := a.server.Shutdown(ctx); err != nil {
		logger.Panic(ctx, "failed to shutdown %s : %v", a.Name(), err)
	}
}

func NewHTTPServer(cfg *configs.Configuration) *HTTPServer {
	return &HTTPServer{
		cfg: cfg,
	}
}

func (a *HTTPServer) Run(ctx context.Context) error {

	if a.cfg.Server.HTTPPort == 0 {
		logger.Panic(ctx, "http port is not provided")
	}

	repo := repository.GetInstance(ctx, &a.cfg.Repository)
	service := service.NewService(ctx, &a.cfg.Service, repo)

	// The service is registered in-process below, which bypasses gRPC
	// interceptors — so the gateway needs its own copies of the same concerns.
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(middleware.IncomingHeaderMatcher),
		runtime.WithMiddlewares(middleware.CallerMiddleware()),
		runtime.WithErrorHandler(middleware.ErrorHandler),
	)

	api.RegisterSwaggerHandler(ctx, mux, "/openpay/v1/swagger", "./api/docs/proto", "/openpay/v1/openpay.swagger.json")
	err := openpay_v1.RegisterOpenPayHandlerServer(ctx, mux, service)
	if err != nil {
		logger.Panic(ctx, "failed to register ping service : %v", err)
	}

	// Register debug endpoints if enabled
	if a.cfg.Debug.Enabled {
		debug.RegisterDebugHandlersWithGateway(ctx, &a.cfg.Debug, mux, a.cfg.Logger.AppName, string(a.cfg.Logger.Build), "/openpay/v1")
	}

	// Probes sit outside the gateway: they must answer even when the API is
	// unhealthy, and they are not part of the versioned API surface.
	root := http.NewServeMux()
	root.Handle("/healthz", health.Live())
	root.Handle("/readyz", health.Ready(repo))
	root.Handle("/", mux)

	// otelhttp is where HTTP spans come from. The gRPC server gets the
	// equivalent from otelgrpc; this path needs its own because the gateway
	// calls the service in-process rather than over gRPC.
	handler := otelhttp.NewHandler(
		logger.WithRequestMiddleware(logger.WithRequestTimeMiddleware(api.CORSMiddleware(root))),
		"openpay.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		// Probes run every few seconds forever and would swamp the traces.
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
	)

	a.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", a.cfg.Server.HTTPPort),
		Handler: handler,
	}

	logger.Info(ctx, "Starting HTTP server on port %d", a.cfg.Server.HTTPPort)
	logger.Info(ctx, "Swagger UI is available at `http://localhost:%d/openpay/v1/swagger`", a.cfg.Server.HTTPPort)
	if a.cfg.Debug.Enabled {
		logger.Info(ctx, "Debug dashboard available at `http://localhost:%d/openpay/v1/debug`", a.cfg.Server.HTTPPort)
	}
	// Start HTTP server (and proxy calls to gRPC server endpoint)
	err = a.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		logger.Panic(ctx, "failed to start http server : %v", err)
	}
	return nil
}

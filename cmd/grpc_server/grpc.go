package grpc_server

import (
	"context"
	"fmt"
	"net"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/configs"
	"github.com/gofreego/openpay/internal/middleware"
	"github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"

	"github.com/gofreego/goutils/logger"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

type GRPCServer struct {
	cfg    *configs.Configuration
	server *grpc.Server
}

func (a *GRPCServer) Name() string {
	return "GRPC_Server"
}

func (a *GRPCServer) Shutdown(ctx context.Context) {
	a.server.GracefulStop()
}

func NewGRPCServer(cfg *configs.Configuration) *GRPCServer {
	return &GRPCServer{
		cfg: cfg,
	}
}

func (a *GRPCServer) Run(ctx context.Context) error {

	if a.cfg.Server.GRPCPort == 0 {
		logger.Panic(ctx, "grpc port is not provided")
	}

	repository := repository.GetInstance(ctx, &a.cfg.Repository)

	service := service.NewService(ctx, &a.cfg.Service, repository)

	// Create a new gRPC server. Interceptors run in order, so the caller is on
	// context before anything can fail and want to log it.
	a.server = grpc.NewServer(
		// otelgrpc starts the span, so the interceptors after it can annotate
		// it and their logs carry the trace id.
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			middleware.CallerUnaryInterceptor(),
			middleware.ErrorUnaryInterceptor(),
		),
	)

	openpay_v1.RegisterOpenPayServer(a.server, service)

	logger.Info(ctx, "Starting gRPC server on port %d", a.cfg.Server.GRPCPort)

	// Listen on a TCP port
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", a.cfg.Server.GRPCPort))
	if err != nil {
		logger.Panic(ctx, "failed to listen for grpc server: %v", err)
	}

	// Start the gRPC server
	if err := a.server.Serve(lis); err != nil {
		logger.Panic(ctx, "failed to start grpc server: %v", err)
	}
	return nil
}

// Package main is the entry point for the Consonant API server.
//
// This server exposes the gRPC Balance API that SDKs connect to for real-time
// AI cost enforcement. The server is designed for production operation with:
//
// - Graceful shutdown on SIGTERM/SIGINT
// - Health check endpoint for load balancers
// - Prometheus metrics endpoint for monitoring
// - Structured logging with log levels
// - Comprehensive error recovery
//
// The server initializes:
// 1. Database connections (Redis + PostgreSQL)
// 2. The ledger with Lua scripts
// 3. Authentication system
// 4. gRPC server with interceptors
// 5. HTTP server for health checks and metrics
//
// Configuration is via environment variables (12-factor app pattern).
//
// Lifecycle:
// 1. Load configuration from env
// 2. Initialize dependencies
// 3. Start gRPC server
// 4. Wait for shutdown signal
// 5. Gracefully drain connections
// 6. Clean up resources
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/consonant/backend/internal/api"
	"github.com/consonant/backend/internal/auth"
	"github.com/consonant/backend/internal/ledger"
	"github.com/consonant/backend/internal/sync"
	pb "github.com/consonant/backend/pkg/proto/balance/v1"
	"github.com/go-redis/redis/v8"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// Config holds all configuration for the server.
// All fields are loaded from environment variables.
type Config struct {
	GRPCPort     string
	HTTPPort     string
	RedisAddr    string
	RedisPassword string
	PostgresURL  string
	LogLevel     string
	Environment  string
}

// LoadConfig loads configuration from environment variables with defaults.
func LoadConfig() *Config {
	return &Config{
		GRPCPort:     getEnv("GRPC_PORT", "9090"),
		HTTPPort:     getEnv("HTTP_PORT", "8080"),
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		PostgresURL:   getEnv("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/consonant?sslmode=disable"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		Environment:   getEnv("ENVIRONMENT", "development"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func main() {
	// Load configuration
	cfg := LoadConfig()

	// Initialize structured logger
	logger := setupLogger(cfg.LogLevel, cfg.Environment)
	logger.Info().
		Str("environment", cfg.Environment).
		Str("grpc_port", cfg.GRPCPort).
		Str("http_port", cfg.HTTPPort).
		Msg("starting consonant api server")

	// Initialize Redis connection
	redisClient := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		PoolSize:     100,
		MinIdleConns: 25,
	})

	// Verify Redis connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to redis")
	}
	cancel()

	logger.Info().Str("addr", cfg.RedisAddr).Msg("connected to redis")

	// Initialize ledger (handles PostgreSQL connection internally)
	ldgr, err := ledger.NewLedger(cfg.RedisAddr, cfg.PostgresURL, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize ledger")
	}
	defer ldgr.Close()

	logger.Info().Msg("ledger initialized")

	// Initialize sync service for Redis initialization
	// This is CRITICAL - without this, Redis is empty and all requests fail
	syncer := sync.NewSyncer(redisClient, ldgr.GetDB(), logger)

	// Perform initial sync from PostgreSQL to Redis
	// This populates Redis with all customer balances and API keys
	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := syncer.InitializeRedis(initCtx); err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize redis from postgresql")
	}
	initCancel()

	logger.Info().Msg("redis initialized from postgresql")

	// Sync API keys to Redis for fast authentication
	if err := syncer.SyncAPIKeys(context.Background()); err != nil {
		logger.Fatal().Err(err).Msg("failed to sync api keys to redis")
	}

	logger.Info().Msg("api keys synced to redis")

	// Start periodic sync to keep Redis in sync with PostgreSQL
	// Runs every 5 minutes to catch manual balance adjustments
	syncer.StartPeriodicSync(5 * time.Minute)
	defer syncer.Stop()

	// Initialize authenticator
	authenticator := auth.NewAuthenticator(redisClient, logger)

	// For development, store a test API key
	if cfg.Environment == "development" {
		testKey := "consonant_test_key_1234567890"
		if err := authenticator.StoreAPIKey(context.Background(), testKey, "test_user_1"); err != nil {
			logger.Warn().Err(err).Msg("failed to store test API key")
		} else {
			logger.Info().Msg("test API key stored: consonant_test_key_1234567890")
		}
	}

	// Initialize gRPC server with middleware
	grpcServer := createGRPCServer(logger)

	// Register balance service
	balanceService := api.NewBalanceService(ldgr, authenticator, logger)
	pb.RegisterBalanceServiceServer(grpcServer, balanceService)

	// Register reflection service for development (allows grpcurl to work)
	if cfg.Environment == "development" {
		reflection.Register(grpcServer)
		logger.Info().Msg("grpc reflection enabled")
	}

	// Start gRPC server in goroutine
	go func() {
		listener, err := net.Listen("tcp", ":"+cfg.GRPCPort)
		if err != nil {
			logger.Fatal().Err(err).Msg("failed to create listener")
		}

		logger.Info().
			Str("port", cfg.GRPCPort).
			Msg("grpc server listening")

		if err := grpcServer.Serve(listener); err != nil {
			logger.Fatal().Err(err).Msg("grpc server failed")
		}
	}()

	// Start HTTP server for health checks and metrics
	httpServer := createHTTPServer(cfg.HTTPPort, ldgr, logger)
	go func() {
		logger.Info().
			Str("port", cfg.HTTPPort).
			Msg("http server listening")

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("http server failed")
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	logger.Info().
		Str("signal", sig.String()).
		Msg("shutdown signal received, starting graceful shutdown")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop accepting new connections
	grpcServer.GracefulStop()
	logger.Info().Msg("grpc server stopped")

	// Shutdown HTTP server
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("http server shutdown failed")
	}
	logger.Info().Msg("http server stopped")

	// Close database connections (ledger.Close() is deferred above)
	logger.Info().Msg("shutdown complete")
}

// setupLogger creates a structured logger with appropriate configuration.
func setupLogger(levelStr, environment string) zerolog.Logger {
	// Parse log level
	level, err := zerolog.ParseLevel(levelStr)
	if err != nil {
		level = zerolog.InfoLevel
	}

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	// In development, use pretty console output
	// In production, use JSON for structured logging
	var logger zerolog.Logger
	if environment == "development" {
		logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			Level(level).
			With().
			Timestamp().
			Caller().
			Logger()
	} else {
		logger = zerolog.New(os.Stdout).
			Level(level).
			With().
			Timestamp().
			Str("service", "consonant-api").
			Str("environment", environment).
			Logger()
	}

	return logger
}

// createGRPCServer creates a gRPC server with middleware and interceptors.
func createGRPCServer(logger zerolog.Logger) *grpc.Server {
	// Recovery interceptor to prevent panics from crashing the server
	recoveryOpts := []grpc_recovery.Option{
		grpc_recovery.WithRecoveryHandler(func(p interface{}) error {
			logger.Error().
				Interface("panic", p).
				Msg("recovered from panic in gRPC handler")
			return status.Errorf(codes.Internal, "internal server error")
		}),
	}

	// Logging interceptor
	loggingInterceptor := func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()

		// Call the handler
		resp, err := handler(ctx, req)

		// Log request details
		duration := time.Since(start)
		logger.Info().
			Str("method", info.FullMethod).
			Dur("duration_ms", duration).
			Err(err).
			Msg("grpc request completed")

		return resp, err
	}

	// Create server with interceptors
	server := grpc.NewServer(
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			grpc_recovery.UnaryServerInterceptor(recoveryOpts...),
			loggingInterceptor,
		)),

		// Keepalive settings to maintain connections and detect dead connections
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     15 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Minute,
			Time:                  5 * time.Minute,
			Timeout:               1 * time.Minute,
		}),

		// Set max message sizes (important for large requests)
		grpc.MaxRecvMsgSize(4 * 1024 * 1024), // 4MB
		grpc.MaxSendMsgSize(4 * 1024 * 1024), // 4MB
	)

	return server
}

// createHTTPServer creates an HTTP server for health checks and metrics.
func createHTTPServer(port string, ldgr *ledger.Ledger, logger zerolog.Logger) *http.Server {
	mux := http.NewServeMux()

	// Health check endpoint
	// Load balancers use this to determine if the server is healthy
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Simple health check - could be more sophisticated
		// (e.g., check Redis and PostgreSQL connectivity)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Readiness check endpoint
	// Kubernetes uses this to determine if the server is ready to receive traffic
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		// Check if ledger is operational
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		// Try to get balance for test customer
		_, _, _, err := ldgr.GetBalance(ctx, "test_customer_1")
		if err != nil {
			logger.Warn().Err(err).Msg("readiness check failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	// Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return server
}
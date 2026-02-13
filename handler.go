// Package rest provides HTTP/JSON REST API endpoints for Beam.
//
// This package wraps the gRPC service to provide a REST API for clients
// that don't want to use gRPC. All gRPC methods are exposed as REST endpoints.
//
// Endpoints:
//   GET  /v1/balance/:customer_id        - Get balance
//   POST /v1/balance/check               - Check and reserve balance
//   POST /v1/balance/deduct              - Deduct tokens
//   POST /v1/balance/finalize            - Finalize request
//   GET  /health                         - Health check
//   GET  /ready                          - Readiness check
//   GET  /metrics                        - Prometheus metrics
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yourusername/beam/internal/api"
	"github.com/yourusername/beam/internal/auth"
	"github.com/yourusername/beam/internal/ledger"
	pb "github.com/yourusername/beam/pkg/proto/balance/v1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/metadata"
)

// Handler provides REST API endpoints.
type Handler struct {
	balanceService *api.BalanceService
	log            zerolog.Logger
}

// NewHandler creates a new REST API handler.
func NewHandler(l *ledger.Ledger, a *auth.Authenticator, logger zerolog.Logger) *Handler {
	return &Handler{
		balanceService: api.NewBalanceService(l, a, logger),
		log:            logger.With().Str("component", "rest_handler").Logger(),
	}
}

// RegisterRoutes registers all REST API routes on the provided mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// API v1 endpoints
	mux.HandleFunc("/v1/balance/", h.handleBalance)
	mux.HandleFunc("/v1/balance/check", h.handleCheckBalance)
	mux.HandleFunc("/v1/balance/deduct", h.handleDeductTokens)
	mux.HandleFunc("/v1/balance/finalize", h.handleFinalizeRequest)

	// Health and monitoring endpoints
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/ready", h.handleReady)
	mux.Handle("/metrics", promhttp.Handler())
}

// handleBalance handles GET /v1/balance/:customer_id
func (h *Handler) handleBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract customer_id from path
	customerID := strings.TrimPrefix(r.URL.Path, "/v1/balance/")
	if customerID == "" || strings.Contains(customerID, "/") {
		h.writeError(w, http.StatusBadRequest, "Invalid customer_id")
		return
	}

	// Create context with auth header
	ctx := h.contextWithAuth(r)

	// Call gRPC service
	resp, err := h.balanceService.GetBalance(ctx, &pb.GetBalanceRequest{
		CustomerId: customerID,
	})

	if err != nil {
		h.handleGRPCError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// handleCheckBalance handles POST /v1/balance/check
func (h *Handler) handleCheckBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req pb.CheckBalanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	ctx := h.contextWithAuth(r)

	resp, err := h.balanceService.CheckBalance(ctx, &req)
	if err != nil {
		h.handleGRPCError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// handleDeductTokens handles POST /v1/balance/deduct
func (h *Handler) handleDeductTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req pb.DeductTokensRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	ctx := h.contextWithAuth(r)

	resp, err := h.balanceService.DeductTokens(ctx, &req)
	if err != nil {
		h.handleGRPCError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// handleFinalizeRequest handles POST /v1/balance/finalize
func (h *Handler) handleFinalizeRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req pb.FinalizeRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	ctx := h.contextWithAuth(r)

	resp, err := h.balanceService.FinalizeRequest(ctx, &req)
	if err != nil {
		h.handleGRPCError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// handleHealth handles GET /health
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleReady handles GET /ready
func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	// TODO: Add actual readiness checks (database connectivity, etc.)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}

// contextWithAuth creates a context with auth metadata from HTTP headers.
func (h *Handler) contextWithAuth(r *http.Request) context.Context {
	ctx := r.Context()

	// Extract Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		md := metadata.New(map[string]string{
			"authorization": authHeader,
		})
		ctx = metadata.NewIncomingContext(ctx, md)
	}

	return ctx
}

// handleGRPCError converts gRPC errors to HTTP errors.
func (h *Handler) handleGRPCError(w http.ResponseWriter, err error) {
	// Map gRPC errors to HTTP status codes
	statusCode := http.StatusInternalServerError
	message := err.Error()

	if strings.Contains(message, "invalid API key") || strings.Contains(message, "unauthenticated") {
		statusCode = http.StatusUnauthorized
	} else if strings.Contains(message, "invalid argument") || strings.Contains(message, "required") {
		statusCode = http.StatusBadRequest
	} else if strings.Contains(message, "permission denied") {
		statusCode = http.StatusForbidden
	} else if strings.Contains(message, "not found") {
		statusCode = http.StatusNotFound
	}

	h.log.Error().Err(err).Int("status", statusCode).Msg("REST API error")
	h.writeError(w, statusCode, message)
}

// writeJSON writes a JSON response.
func (h *Handler) writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.log.Error().Err(err).Msg("failed to encode JSON response")
	}
}

// writeError writes a JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, statusCode int, message string) {
	h.writeJSON(w, statusCode, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    statusCode,
			"message": message,
		},
		"timestamp": time.Now().Unix(),
	})
}

// CORS middleware for development
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs all HTTP requests
func LoggingMiddleware(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Create a response writer wrapper to capture status code
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", wrapped.statusCode).
				Dur("duration_ms", time.Since(start)).
				Str("remote_addr", r.RemoteAddr).
				Msg("HTTP request")
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
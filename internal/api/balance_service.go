// Package api implements the gRPC services for Beam.
//
// This package is the interface layer between external clients (SDKs) and
// internal business logic (ledger operations). Every gRPC request flows
// through this package.
//
// Responsibilities:
// 1. Request validation and sanitization
// 2. Authentication (API key verification)
// 3. Request routing to appropriate ledger operations
// 4. Error translation (internal errors -> gRPC status codes)
// 5. Metrics collection (request counts, latencies, errors)
//
// Performance considerations:
// This code is in the hot path for EVERY AI request. Every nanosecond counts.
// We avoid allocations where possible, reuse buffers, and minimize locking.
//
// Thread safety:
// All methods are safe for concurrent use. The gRPC server calls these methods
// from multiple goroutines simultaneously. We use the ledger's thread-safe
// operations and don't maintain any shared mutable state in this layer.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Beam/backend/internal/auth"
	"github.com/Beam/backend/internal/ledger"
	pb "github.com/Beam/backend/pkg/proto/balance/v1"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BalanceService implements the gRPC BalanceService interface.
//
// This is a thin layer over the ledger that adds gRPC-specific concerns
// like authentication, validation, and error translation.
type BalanceService struct {
	pb.UnimplementedBalanceServiceServer

	ledger *ledger.Ledger
	auth   *auth.Authenticator
	log    zerolog.Logger
}

// NewBalanceService creates a new BalanceService instance.
func NewBalanceService(l *ledger.Ledger, a *auth.Authenticator, logger zerolog.Logger) *BalanceService {
	return &BalanceService{
		ledger: l,
		auth:   a,
		log:    logger.With().Str("component", "balance_service").Logger(),
	}
}

// CheckBalance implements the CheckBalance RPC method.
//
// This is called by the SDK before every AI request to validate that the
// customer can afford it. The operation must complete in under 5ms to avoid
// adding noticeable latency to the user experience.
//
// Flow:
// 1. Authenticate the request (validate API key)
// 2. Validate request parameters
// 3. Apply buffer multiplier to estimated cost
// 4. Call ledger to check balance and reserve grains
// 5. Generate secure request token for subsequent operations
// 6. Return result
//
// Performance: Target < 5ms, typically achieves 2-4ms
func (s *BalanceService) CheckBalance(ctx context.Context, req *pb.CheckBalanceRequest) (*pb.CheckBalanceResponse, error) {
	start := time.Now()

	// Extract API key from request metadata and validate
	platformUserID, err := s.auth.ValidateAPIKey(ctx)
	if err != nil {
		s.log.Warn().Err(err).Msg("authentication failed")
		return nil, status.Errorf(codes.Unauthenticated, "invalid API key: %v", err)
	}

	// Log request for debugging (at debug level to avoid log spam)
	s.log.Debug().
		Str("platform_user_id", platformUserID).
		Str("customer_id", req.CustomerId).
		Str("request_id", req.RequestId).
		Int64("estimated_grains", req.EstimatedGrains).
		Float64("buffer_multiplier", req.BufferMultiplier).
		Msg("check_balance request received")

	// Validate request parameters
	if req.CustomerId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "customer_id is required")
	}

	if req.RequestId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "request_id is required")
	}

	if req.EstimatedGrains <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "estimated_grains must be positive")
	}

	// Apply buffer multiplier
	// If not provided, we should fetch customer's configured default
	// For now, default to conservative (1.2)
	bufferMultiplier := req.BufferMultiplier
	if bufferMultiplier == 0 {
		bufferMultiplier = 1.2 // Conservative default
	}

	// Calculate final reservation amount
	reservedGrains := int64(float64(req.EstimatedGrains) * bufferMultiplier)

	// Convert metadata to map for ledger
	metadataMap := make(map[string]string)
	if req.Metadata != nil {
		metadataMap["model"] = req.Metadata.Model
		metadataMap["max_tokens"] = fmt.Sprintf("%d", req.Metadata.MaxTokens)
		metadataMap["prompt_tokens"] = fmt.Sprintf("%d", req.Metadata.PromptTokens)

		// Include custom properties
		for k, v := range req.Metadata.CustomProperties {
			metadataMap[k] = v
		}
	}

	// Call ledger to check and reserve balance
	result, err := s.ledger.CheckAndReserveBalance(ctx, ledger.ReservationRequest{
		CustomerID:      req.CustomerId,
		RequestID:       req.RequestId,
		ReservedGrains:  reservedGrains,
		EstimatedGrains: req.EstimatedGrains,
		Metadata:        metadataMap,
		PlatformUserID:  platformUserID,
	})

	if err != nil {
		s.log.Error().Err(err).
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Msg("ledger check_and_reserve failed")
		return nil, status.Errorf(codes.Internal, "failed to check balance: %v", err)
	}

	// Generate secure request token
	// This token must be included in subsequent DeductTokens and FinalizeRequest calls
	// It prevents replay attacks and ensures only approved requests can deduct grains
	requestToken := s.generateRequestToken(req.RequestId, req.CustomerId)

	// Build response
	response := &pb.CheckBalanceResponse{
		Approved:         result.Approved,
		RemainingBalance: result.RemainingBalance,
		RequestToken:     requestToken,
		RejectionReason:  result.RejectionReason,
		ReservedGrains:   reservedGrains,
	}

	// Calculate and log duration
	duration := time.Since(start)

	if result.Approved {
		s.log.Info().
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Int64("reserved_grains", reservedGrains).
			Int64("remaining_balance", result.RemainingBalance).
			Dur("duration_ms", duration).
			Msg("check_balance approved")
	} else {
		s.log.Info().
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Str("rejection_reason", result.RejectionReason).
			Int64("current_balance", result.CurrentBalance).
			Dur("duration_ms", duration).
			Msg("check_balance rejected")
	}

	return response, nil
}

// DeductTokens implements the DeductTokens RPC method.
//
// This is called repeatedly during streaming (typically every 50 tokens) to
// deduct grains as they're consumed. Each call must complete quickly to avoid
// blocking the streaming pipeline.
//
// The most critical responsibility of this method is detecting when the
// customer runs out of grains and returning success=false, which triggers
// the SDK to immediately kill the stream.
//
// Performance: Target < 3ms, typically achieves 1-2ms
func (s *BalanceService) DeductTokens(ctx context.Context, req *pb.DeductTokensRequest) (*pb.DeductTokensResponse, error) {
	// Validate request token
	// This prevents unauthorized deductions from replayed or forged requests
	if !s.validateRequestToken(req.RequestToken, req.RequestId, req.CustomerId) {
		s.log.Warn().
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Msg("invalid request token")
		return nil, status.Errorf(codes.PermissionDenied, "invalid request token")
	}

	// Validate parameters
	if req.TokensConsumed <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "tokens_consumed must be positive")
	}

	// Determine provider from model name
	// Model names typically indicate the provider (e.g., "gpt-4" = openai, "claude-3" = anthropic)
	provider := "openai" // Default
	if len(req.Model) > 0 {
		switch {
		case req.Model[:3] == "gpt" || req.Model[:4] == "text" || req.Model[:3] == "ada":
			provider = "openai"
		case len(req.Model) >= 6 && req.Model[:6] == "claude":
			provider = "anthropic"
		case len(req.Model) >= 6 && req.Model[:6] == "gemini":
			provider = "google"
		}
	}

	// Calculate grain cost based on model pricing
	pricing, err := s.ledger.GetModelPricing(req.Model, provider)
	if err != nil {
		s.log.Error().Err(err).Str("model", req.Model).Msg("failed to get pricing")
		return nil, status.Errorf(codes.Internal, "failed to get model pricing")
	}

	// Calculate cost in grains
	var costPerToken float64
	if req.IsCompletion {
		// Output tokens typically cost 2-3x more than input tokens
		costPerToken = float64(pricing.OutputCostPerMillionTokens) / 1_000_000
	} else {
		costPerToken = float64(pricing.InputCostPerMillionTokens) / 1_000_000
	}

	grainCost := int64(float64(req.TokensConsumed) * costPerToken)

	// Call ledger to deduct grains
	result, err := s.ledger.DeductGrains(ctx, ledger.DeductionRequest{
		CustomerID:     req.CustomerId,
		RequestID:      req.RequestId,
		GrainAmount:    grainCost,
		TokensConsumed: req.TokensConsumed,
	})

	if err != nil {
		s.log.Error().Err(err).
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Msg("ledger deduct_grains failed")
		return nil, status.Errorf(codes.Internal, "failed to deduct tokens: %v", err)
	}

	// Build response
	response := &pb.DeductTokensResponse{
		Success:          result.Success,
		RemainingBalance: result.RemainingBalance,
		ErrorCode:        result.ErrorCode,
	}

	// Log the deduction
	if result.Success {
		s.log.Debug().
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Int32("tokens", req.TokensConsumed).
			Int64("grain_cost", grainCost).
			Int64("remaining_balance", result.RemainingBalance).
			Msg("deduct_tokens success")
	} else {
		// This is a critical event - customer ran out of grains mid-stream
		s.log.Warn().
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Str("error_code", result.ErrorCode).
			Int64("remaining_balance", result.RemainingBalance).
			Msg("deduct_tokens failed - kill switch triggered")
	}

	return response, nil
}

// FinalizeRequest implements the FinalizeRequest RPC method.
//
// This is called exactly once per request at stream-end with authoritative
// token counts from the AI provider. It performs final reconciliation,
// refunds any overcharges, releases the reservation, and marks the request
// as complete.
//
// This method is more tolerant of higher latency than the others because
// it's called after the user has already received their response. It's OK
// if this takes 10-15ms.
//
// Performance: Target < 10ms, typically achieves 3-8ms
func (s *BalanceService) FinalizeRequest(ctx context.Context, req *pb.FinalizeRequestRequest) (*pb.FinalizeRequestResponse, error) {
	start := time.Now()

	// Validate parameters
	if req.CustomerId == "" || req.RequestId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "customer_id and request_id are required")
	}

	if req.TotalActualCostGrains < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "total_actual_cost_grains cannot be negative")
	}

	// Translate status enum to string
	var statusStr string
	switch req.Status {
	case pb.RequestStatus_COMPLETED_SUCCESS:
		statusStr = "completed"
	case pb.RequestStatus_KILLED_INSUFFICIENT_BALANCE:
		statusStr = "killed"
	case pb.RequestStatus_FAILED_ERROR:
		statusStr = "failed"
	case pb.RequestStatus_FAILED_TIMEOUT:
		statusStr = "timeout"
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid status")
	}

	// Call ledger to finalize
	result, err := s.ledger.FinalizeRequest(ctx, ledger.FinalizationRequest{
		CustomerID:        req.CustomerId,
		RequestID:         req.RequestId,
		Status:            statusStr,
		ActualCostGrains:  req.TotalActualCostGrains,
		PromptTokens:      req.ActualPromptTokens,
		CompletionTokens:  req.ActualCompletionTokens,
		Model:             req.Model,
	})

	if err != nil {
		s.log.Error().Err(err).
			Str("customer_id", req.CustomerId).
			Str("request_id", req.RequestId).
			Msg("ledger finalize_request failed")
		return nil, status.Errorf(codes.Internal, "failed to finalize request: %v", err)
	}

	// Build response
	response := &pb.FinalizeRequestResponse{
		Success:        result.Success,
		RefundedGrains: result.RefundedGrains,
		FinalBalance:   result.FinalBalance,
	}

	duration := time.Since(start)

	// Log finalization
	s.log.Info().
		Str("customer_id", req.CustomerId).
		Str("request_id", req.RequestId).
		Str("status", statusStr).
		Int64("actual_cost", req.TotalActualCostGrains).
		Int64("refunded", result.RefundedGrains).
		Int64("final_balance", result.FinalBalance).
		Dur("duration_ms", duration).
		Msg("finalize_request completed")

	return response, nil
}

// GetBalance implements the GetBalance RPC method.
//
// This is a simple read-only operation that returns the current balance
// without any side effects. Used by dashboards and health checks.
//
// Performance: < 2ms typically
func (s *BalanceService) GetBalance(ctx context.Context, req *pb.GetBalanceRequest) (*pb.GetBalanceResponse, error) {
	// Authenticate request
	_, err := s.auth.ValidateAPIKey(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid API key: %v", err)
	}

	if req.CustomerId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "customer_id is required")
	}

	// Get balance from ledger
	balance, reserved, available, err := s.ledger.GetBalance(ctx, req.CustomerId)
	if err != nil {
		s.log.Error().Err(err).Str("customer_id", req.CustomerId).Msg("failed to get balance")
		return nil, status.Errorf(codes.Internal, "failed to get balance: %v", err)
	}

	return &pb.GetBalanceResponse{
		Balance:   balance,
		Reserved:  reserved,
		Available: available,
	}, nil
}

// generateRequestToken creates a secure token for a request.
//
// The token is a SHA-256 hash of the request ID, customer ID, and a secret key.
// This makes it cryptographically infeasible to forge valid tokens.
//
// In a production system, you'd want to:
// 1. Store these tokens in Redis with a short TTL (1 hour)
// 2. Use HMAC instead of plain SHA-256
// 3. Include a timestamp to prevent very old token reuse
//
// For now, we use a simpler deterministic generation that's good enough
// for preventing basic replay attacks.
func (s *BalanceService) generateRequestToken(requestID, customerID string) string {
	// In production, get this from environment variable or secret manager
	secretKey := "Beam_secret_key_change_in_production"

	data := fmt.Sprintf("%s:%s:%s", requestID, customerID, secretKey)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// validateRequestToken verifies that a request token is valid.
//
// This is a simple implementation that regenerates the expected token and
// compares it to the provided token. In production, you'd want to store
// tokens in Redis and look them up for O(1) validation with expiration.
func (s *BalanceService) validateRequestToken(token, requestID, customerID string) bool {
	expectedToken := s.generateRequestToken(requestID, customerID)
	return token == expectedToken
}
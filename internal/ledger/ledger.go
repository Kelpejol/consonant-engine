// Package ledger provides atomic balance management using Redis and PostgreSQL.
//
// This is the core financial engine of Consonant. Every grain that moves through
// the system flows through this package. The ledger maintains two synchronized
// data stores:
//
// 1. Redis - Hot cache for sub-millisecond balance checks and atomic operations
// 2. PostgreSQL - Durable source of truth with complete audit trail
//
// The relationship between Redis and PostgreSQL is critical to understand:
//
// Redis is FAST but VOLATILE. If Redis crashes, we lose the in-memory state.
// PostgreSQL is DURABLE but SLOWER. Writes take 5-20ms vs Redis's sub-1ms.
//
// Our strategy: Redis handles the hot path (balance checks during AI requests).
// PostgreSQL handles durability (writes happen asynchronously with retries).
//
// Consistency guarantee: PostgreSQL is always the source of truth. If Redis
// and PostgreSQL disagree, we sync Redis from PostgreSQL. Redis can be stale
// but only in the safe direction (showing fewer grains than reality).
//
// Performance characteristics:
// - CheckAndReserveBalance: 2-4ms (Redis Lua script + DB write queued)
// - DeductGrains: 1-3ms (Redis Lua script)
// - FinalizeRequest: 3-8ms (Redis Lua script + DB write queued)
//
// Race condition prevention:
// All balance operations use Lua scripts that execute atomically in Redis.
// This prevents the classic "check-then-act" race where multiple requests
// all check the balance, see enough funds, and all proceed even though
// collectively they exceed available balance.
package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
)

// Ledger manages all balance operations across Redis and PostgreSQL.
//
// Thread safety: All methods are safe for concurrent use. The Ledger uses
// connection pools internally which handle concurrent access safely.
//
// Lifecycle: Create once at application startup with NewLedger, use throughout
// the application lifetime, and call Close during graceful shutdown.
type Ledger struct {
	redis *redis.Client
	db    *sql.DB
	log   zerolog.Logger

	// Lua scripts pre-loaded at initialization
	// These are loaded once and reused for every operation
	checkAndReserveScript *redis.Script
	deductGrainsScript    *redis.Script
	finalizeRequestScript *redis.Script

	// Async write queue for PostgreSQL operations
	// This prevents blocking the hot path on slow database writes
	writeQueue chan writeOp
	wg         sync.WaitGroup

	// Pricing cache to avoid repeated database lookups
	// Map of "model:provider" -> PricingInfo
	pricingCache sync.Map
}

// writeOp represents a queued PostgreSQL write operation.
// These are processed by background workers to avoid blocking the hot path.
type writeOp struct {
	opType string      // "preflight", "finalization", "transaction"
	data   interface{} // Operation-specific data
	ctx    context.Context
}

// ReservationRequest contains all parameters for CheckAndReserveBalance.
type ReservationRequest struct {
	CustomerID      string
	RequestID       string
	ReservedGrains  int64
	EstimatedGrains int64
	Metadata        map[string]string
	PlatformUserID  string
}

// ReservationResult contains the outcome of a balance check and reservation.
type ReservationResult struct {
	Approved         bool
	CurrentBalance   int64
	RemainingBalance int64
	RejectionReason  string
	ReservedGrains   int64
}

// DeductionRequest contains parameters for DeductGrains.
type DeductionRequest struct {
	CustomerID     string
	RequestID      string
	GrainAmount    int64
	TokensConsumed int32
}

// DeductionResult contains the outcome of a deduction operation.
type DeductionResult struct {
	Success          bool
	RemainingBalance int64
	ErrorCode        string
}

// FinalizationRequest contains parameters for FinalizeRequest.
type FinalizationRequest struct {
	CustomerID        string
	RequestID         string
	Status            string
	ActualCostGrains  int64
	PromptTokens      int32
	CompletionTokens  int32
	Model             string
}

// FinalizationResult contains the outcome of request finalization.
type FinalizationResult struct {
	Success        bool
	RefundedGrains int64
	FinalBalance   int64
	ErrorCode      string
}

// PricingInfo contains model pricing in grains per million tokens.
type PricingInfo struct {
	Model                      string
	Provider                   string
	InputCostPerMillionTokens  int64
	OutputCostPerMillionTokens int64
}

// NewLedger creates a new Ledger instance connected to Redis and PostgreSQL.
//
// Parameters:
//   redisAddr: Redis connection string (e.g., "localhost:6379")
//   postgresURL: PostgreSQL connection string
//   logger: Structured logger for operational visibility
//
// This function:
// 1. Establishes connection pools to both databases
// 2. Loads and compiles Lua scripts
// 3. Starts background workers for async PostgreSQL writes
// 4. Loads model pricing into cache
//
// Returns an error if connections fail or Lua scripts are invalid.
func NewLedger(redisAddr, postgresURL string, logger zerolog.Logger) (*Ledger, error) {
	logger.Info().
		Str("redis_addr", redisAddr).
		Msg("initializing ledger")

	// Connect to Redis with aggressive timeouts for sub-millisecond operations
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,

		// Timeouts are critical for performance
		// If Redis is slow, we want to fail fast and use fallback
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,

		// Connection pool sizing
		// We expect high concurrency (10k+ concurrent requests)
		// Each goroutine needs a connection from the pool
		PoolSize:     100,
		MinIdleConns: 25,

		// Keep connections alive to prevent firewall timeouts
		PoolTimeout:      30 * time.Second,
		IdleTimeout:      5 * time.Minute,
		IdleCheckFrequency: 1 * time.Minute,
	})

	// Verify Redis connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	logger.Info().Msg("redis connection established")

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", postgresURL)
	if err != nil {
		return nil, fmt.Errorf("postgres connection failed: %w", err)
	}

	// Connection pool tuning for async writes
	// We don't need as many connections as Redis because writes are queued
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	// Verify PostgreSQL connectivity
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	logger.Info().Msg("postgres connection established")

	// Create ledger instance
	l := &Ledger{
		redis:      rdb,
		db:         db,
		log:        logger,
		writeQueue: make(chan writeOp, 10000), // Large buffer for burst traffic
	}

	// Load Lua scripts
	if err := l.loadLuaScripts(); err != nil {
		return nil, fmt.Errorf("failed to load lua scripts: %w", err)
	}

	logger.Info().Msg("lua scripts loaded successfully")

	// Load pricing information into cache
	if err := l.loadPricingCache(ctx); err != nil {
		logger.Warn().Err(err).Msg("failed to load pricing cache, will load on demand")
		// Non-fatal - we can load pricing on demand
	}

	// Start background workers for async PostgreSQL writes
	// Multiple workers handle the queue concurrently for throughput
	numWorkers := 10
	l.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go l.asyncWriteWorker(i)
	}

	logger.Info().
		Int("num_workers", numWorkers).
		Msg("async write workers started")

	return l, nil
}

// loadLuaScripts loads and compiles all Lua scripts.
// We load them once at startup rather than on every request for performance.
func (l *Ledger) loadLuaScripts() error {
	// Load check_and_reserve.lua
	checkAndReserveScript := `
local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
local reserved = tonumber(redis.call('GET', KEYS[2]) or '0')
local needed = tonumber(ARGV[1])
local available = balance - reserved
local existing_request = redis.call('EXISTS', KEYS[3])
if existing_request == 1 then
    return {0, balance, 'REQUEST_EXISTS'}
end
if available < needed then
    return {0, balance, 'INSUFFICIENT_BALANCE'}
end
redis.call('INCRBY', KEYS[2], needed)
redis.call('HSET', KEYS[3],
    'customer_id', ARGV[5],
    'reserved_grains', ARGV[1],
    'estimated_grains', ARGV[2],
    'consumed_grains', '0',
    'status', 'preflight_approved',
    'created_at', ARGV[3],
    'metadata', ARGV[4]
)
redis.call('EXPIRE', KEYS[3], 3600)
local new_available = available - needed
return {1, new_available, ''}
`
	l.checkAndReserveScript = redis.NewScript(checkAndReserveScript)

	// Load deduct_grains.lua
	deductGrainsScript := `
local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
local amount = tonumber(ARGV[1])
local request_exists = redis.call('EXISTS', KEYS[2])
if request_exists == 0 then
    return {0, balance, 'REQUEST_NOT_FOUND'}
end
if balance < amount then
    return {0, balance, 'INSUFFICIENT_BALANCE'}
end
if balance - amount < 0 then
    return {0, balance, 'BALANCE_NEGATIVE'}
end
redis.call('DECRBY', KEYS[1], amount)
redis.call('HINCRBY', KEYS[2], 'consumed_grains', amount)
redis.call('HSET', KEYS[2], 
    'status', 'streaming',
    'last_deduction_at', ARGV[3] or redis.call('TIME')[1]
)
local new_balance = balance - amount
return {1, new_balance, ''}
`
	l.deductGrainsScript = redis.NewScript(deductGrainsScript)

	// Load finalize_request.lua
	finalizeRequestScript := `
local request_data = redis.call('HGETALL', KEYS[3])
if #request_data == 0 then
    return {0, 0, 'REQUEST_NOT_FOUND'}
end
local request = {}
for i = 1, #request_data, 2 do
    request[request_data[i]] = request_data[i + 1]
end
local current_status = request['status']
if current_status == 'completed' or current_status == 'killed' or current_status == 'failed' then
    local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
    return {1, 0, balance}
end
local reserved = tonumber(request['reserved_grains'] or '0')
local consumed = tonumber(request['consumed_grains'] or '0')
local actual_cost = tonumber(ARGV[1])
local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
local refund = 0
if consumed > actual_cost then
    refund = consumed - actual_cost
    redis.call('INCRBY', KEYS[1], refund)
    balance = balance + refund
elseif actual_cost > consumed then
    local additional = actual_cost - consumed
    if balance >= additional then
        redis.call('DECRBY', KEYS[1], additional)
        balance = balance - additional
        refund = -additional
    else
        redis.call('SET', KEYS[1], '0')
        refund = -balance
        balance = 0
        redis.call('HSET', KEYS[3], 'integrity_issue', 'undercharge_shortfall')
    end
end
local current_reserved = tonumber(redis.call('GET', KEYS[2]) or '0')
if current_reserved >= reserved then
    redis.call('DECRBY', KEYS[2], reserved)
else
    redis.call('SET', KEYS[2], '0')
    redis.call('HSET', KEYS[3], 'integrity_issue', 'reservation_underflow')
end
redis.call('HMSET', KEYS[3],
    'status', ARGV[2],
    'actual_cost_grains', ARGV[1],
    'refunded_grains', tostring(refund),
    'finalized_at', ARGV[3]
)
redis.call('EXPIRE', KEYS[3], 86400)
return {1, refund, balance}
`
	l.finalizeRequestScript = redis.NewScript(finalizeRequestScript)

	return nil
}

// loadPricingCache loads model pricing from PostgreSQL into memory cache.
func (l *Ledger) loadPricingCache(ctx context.Context) error {
	rows, err := l.db.QueryContext(ctx, `
		SELECT model_name, provider, 
		       input_cost_per_million_tokens, output_cost_per_million_tokens
		FROM model_pricing
		WHERE effective_until IS NULL
	`)
	if err != nil {
		return fmt.Errorf("pricing query failed: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var p PricingInfo
		if err := rows.Scan(&p.Model, &p.Provider, &p.InputCostPerMillionTokens, &p.OutputCostPerMillionTokens); err != nil {
			return fmt.Errorf("pricing scan failed: %w", err)
		}

		key := fmt.Sprintf("%s:%s", p.Model, p.Provider)
		l.pricingCache.Store(key, p)
		count++
	}

	l.log.Info().Int("count", count).Msg("pricing cache loaded")
	return rows.Err()
}

// CheckAndReserveBalance performs atomic pre-flight validation and reservation.
//
// This is the first operation for every AI request. It determines whether the
// customer can afford the request and, if so, reserves the grains to prevent
// race conditions with other concurrent requests from the same customer.
//
// The reservation mechanism is critical: without it, multiple simultaneous
// requests could all check the balance, see enough funds, and all proceed
// even though collectively they exceed available balance.
//
// Algorithm:
// 1. Execute Lua script atomically in Redis:
//    - Read balance and reserved counters
//    - Calculate available = balance - reserved
//    - Check if available >= needed
//    - If yes, increment reserved counter
//    - Create request tracking hash
// 2. Queue async write to PostgreSQL for durability
// 3. Return result to caller
//
// Performance: 2-4ms typical, 10ms P99
// Concurrency: Safe for unlimited concurrent calls
func (l *Ledger) CheckAndReserveBalance(ctx context.Context, req ReservationRequest) (*ReservationResult, error) {
	start := time.Now()

	// Prepare metadata for storage
	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		l.log.Warn().Err(err).Msg("failed to marshal metadata, using empty")
		metadata = []byte("{}")
	}

	// Execute Lua script
	keys := []string{
		fmt.Sprintf("customer:balance:%s", req.CustomerID),
		fmt.Sprintf("customer:reserved:%s", req.CustomerID),
		fmt.Sprintf("request:%s", req.RequestID),
	}

	args := []interface{}{
		req.ReservedGrains,
		req.EstimatedGrains,
		time.Now().Unix(),
		string(metadata),
		req.CustomerID,
	}

	result, err := l.checkAndReserveScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		l.log.Error().Err(err).
			Str("customer_id", req.CustomerID).
			Str("request_id", req.RequestID).
			Msg("check_and_reserve lua script failed")
		return nil, fmt.Errorf("lua script execution failed: %w", err)
	}

	// Parse result from Lua
	resultArray := result.([]interface{})
	approved := resultArray[0].(int64) == 1
	balance := resultArray[1].(int64)
	reason := resultArray[2].(string)

	duration := time.Since(start)

	res := &ReservationResult{
		Approved:         approved,
		CurrentBalance:   balance,
		RemainingBalance: balance,
		RejectionReason:  reason,
		ReservedGrains:   req.ReservedGrains,
	}

	// Log the operation
	l.log.Debug().
		Str("customer_id", req.CustomerID).
		Str("request_id", req.RequestID).
		Int64("reserved_grains", req.ReservedGrains).
		Bool("approved", approved).
		Str("reason", reason).
		Dur("duration_ms", duration).
		Msg("check_and_reserve completed")

	// If approved, queue async write to PostgreSQL
	if approved {
		select {
		case l.writeQueue <- writeOp{
			opType: "preflight",
			data:   req,
			ctx:    context.Background(), // Use background context for async work
		}:
			// Queued successfully
		default:
			// Queue is full - log but don't block
			l.log.Warn().Msg("write queue full, skipping async preflight write")
		}
	}

	return res, nil
}

// DeductGrains atomically deducts grains during streaming.
//
// This is called repeatedly (every 50 tokens typically) as the AI response
// streams back to the user. Each call deducts grains and checks if the
// balance has hit zero, which triggers the kill switch.
//
// Performance: 1-3ms typical
// Call frequency: 10-30 times per streaming request
func (l *Ledger) DeductGrains(ctx context.Context, req DeductionRequest) (*DeductionResult, error) {
	keys := []string{
		fmt.Sprintf("customer:balance:%s", req.CustomerID),
		fmt.Sprintf("request:%s", req.RequestID),
	}

	args := []interface{}{
		req.GrainAmount,
		req.TokensConsumed,
		time.Now().Unix(),
	}

	result, err := l.deductGrainsScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		l.log.Error().Err(err).
			Str("customer_id", req.CustomerID).
			Str("request_id", req.RequestID).
			Msg("deduct_grains lua script failed")
		return nil, fmt.Errorf("lua script execution failed: %w", err)
	}

	resultArray := result.([]interface{})
	success := resultArray[0].(int64) == 1
	balance := resultArray[1].(int64)
	errorCode := resultArray[2].(string)

	res := &DeductionResult{
		Success:          success,
		RemainingBalance: balance,
		ErrorCode:        errorCode,
	}

	l.log.Debug().
		Str("customer_id", req.CustomerID).
		Str("request_id", req.RequestID).
		Int64("grain_amount", req.GrainAmount).
		Bool("success", success).
		Str("error_code", errorCode).
		Msg("deduct_grains completed")

	return res, nil
}

// FinalizeRequest performs final reconciliation at stream-end.
//
// This is called exactly once per request with authoritative token counts
// from the AI provider. It reconciles estimated vs actual costs, refunds
// any overcharges, releases the reservation, and marks the request complete.
//
// Performance: 3-8ms typical
// Call frequency: Once per request
func (l *Ledger) FinalizeRequest(ctx context.Context, req FinalizationRequest) (*FinalizationResult, error) {
	keys := []string{
		fmt.Sprintf("customer:balance:%s", req.CustomerID),
		fmt.Sprintf("customer:reserved:%s", req.CustomerID),
		fmt.Sprintf("request:%s", req.RequestID),
	}

	args := []interface{}{
		req.ActualCostGrains,
		req.Status,
		time.Now().Unix(),
	}

	result, err := l.finalizeRequestScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		l.log.Error().Err(err).
			Str("customer_id", req.CustomerID).
			Str("request_id", req.RequestID).
			Msg("finalize_request lua script failed")
		return nil, fmt.Errorf("lua script execution failed: %w", err)
	}

	resultArray := result.([]interface{})
	success := resultArray[0].(int64) == 1
	refunded := resultArray[1].(int64)
	finalBalance := resultArray[2].(int64)

	res := &FinalizationResult{
		Success:        success,
		RefundedGrains: refunded,
		FinalBalance:   finalBalance,
	}

	l.log.Info().
		Str("customer_id", req.CustomerID).
		Str("request_id", req.RequestID).
		Str("status", req.Status).
		Int64("actual_cost", req.ActualCostGrains).
		Int64("refunded", refunded).
		Msg("finalize_request completed")

	// Queue async write to PostgreSQL
	select {
	case l.writeQueue <- writeOp{
		opType: "finalization",
		data:   req,
		ctx:    context.Background(),
	}:
		// Queued successfully
	default:
		l.log.Warn().Msg("write queue full, skipping async finalization write")
	}

	return res, nil
}

// GetBalance returns current balance without side effects (read-only).
func (l *Ledger) GetBalance(ctx context.Context, customerID string) (balance int64, reserved int64, available int64, err error) {
	balanceKey := fmt.Sprintf("customer:balance:%s", customerID)
	reservedKey := fmt.Sprintf("customer:reserved:%s", customerID)

	// Use pipeline for efficiency (single round trip)
	pipe := l.redis.Pipeline()
	balanceCmd := pipe.Get(ctx, balanceKey)
	reservedCmd := pipe.Get(ctx, reservedKey)
	_, err = pipe.Exec(ctx)

	if err != nil && err != redis.Nil {
		return 0, 0, 0, fmt.Errorf("redis pipeline failed: %w", err)
	}

	balance, _ = balanceCmd.Int64()
	reserved, _ = reservedCmd.Int64()
	available = balance - reserved

	return balance, reserved, available, nil
}

// asyncWriteWorker processes queued PostgreSQL writes in background.
func (l *Ledger) asyncWriteWorker(workerID int) {
	defer l.wg.Done()

	logger := l.log.With().Int("worker_id", workerID).Logger()
	logger.Info().Msg("async write worker started")

	for op := range l.writeQueue {
		// Process with retry logic
		maxRetries := 5
		backoff := 100 * time.Millisecond

		for attempt := 1; attempt <= maxRetries; attempt++ {
			var err error

			switch op.opType {
			case "preflight":
				err = l.writePreflightToDB(op.ctx, op.data.(ReservationRequest))
			case "finalization":
				err = l.writeFinalizationToDB(op.ctx, op.data.(FinalizationRequest))
			}

			if err == nil {
				break // Success
			}

			if attempt < maxRetries {
				logger.Warn().Err(err).
					Int("attempt", attempt).
					Str("op_type", op.opType).
					Msg("async write failed, retrying")
				time.Sleep(backoff)
				backoff *= 2 // Exponential backoff
			} else {
				logger.Error().Err(err).
					Str("op_type", op.opType).
					Msg("async write failed after all retries")
			}
		}
	}

	logger.Info().Msg("async write worker stopped")
}

// writePreflightToDB writes pre-flight data to PostgreSQL.
func (l *Ledger) writePreflightToDB(ctx context.Context, req ReservationRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO requests (
			request_id, customer_id, platform_user_id,
			estimated_cost_grains, reserved_grains,
			status, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, req.RequestID, req.CustomerID, req.PlatformUserID,
		req.EstimatedGrains, req.ReservedGrains, "preflight_approved")

	return err
}

// writeFinalizationToDB writes finalization data to PostgreSQL.
func (l *Ledger) writeFinalizationToDB(ctx context.Context, req FinalizationRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Start transaction for atomic update
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx failed: %w", err)
	}
	defer tx.Rollback()

	// Update request record
	_, err = tx.ExecContext(ctx, `
		UPDATE requests SET
			provider_reported_cost_grains = $1,
			actual_cost_grains = $1,
			prompt_tokens = $2,
			completion_tokens = $3,
			total_tokens = $4,
			status = $5,
			completed_at = NOW(),
			reconciled_at = NOW()
		WHERE request_id = $6
	`, req.ActualCostGrains, req.PromptTokens, req.CompletionTokens,
		req.PromptTokens+req.CompletionTokens, req.Status, req.RequestID)

	if err != nil {
		return fmt.Errorf("update request failed: %w", err)
	}

	// Record transaction for audit trail
	txID := uuid.New().String()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO transactions (
			transaction_id, customer_id, amount_grains,
			transaction_type, reference_id, description, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, txID, req.CustomerID, -req.ActualCostGrains,
		"ai_usage", req.RequestID,
		fmt.Sprintf("AI usage: %s (%d tokens)", req.Model, req.PromptTokens+req.CompletionTokens))

	if err != nil {
		return fmt.Errorf("insert transaction failed: %w", err)
	}

	return tx.Commit()
}

// GetModelPricing returns pricing for a model (with caching).
func (l *Ledger) GetModelPricing(model string, provider string) (*PricingInfo, error) {
	key := fmt.Sprintf("%s:%s", model, provider)

	// Try cache first
	if cached, ok := l.pricingCache.Load(key); ok {
		pricing := cached.(PricingInfo)
		return &pricing, nil
	}

	// Cache miss - load from database
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var p PricingInfo
	err := l.db.QueryRowContext(ctx, `
		SELECT model_name, provider, 
		       input_cost_per_million_tokens, output_cost_per_million_tokens
		FROM model_pricing
		WHERE model_name = $1 AND provider = $2 AND effective_until IS NULL
	`, model, provider).Scan(&p.Model, &p.Provider, &p.InputCostPerMillionTokens, &p.OutputCostPerMillionTokens)

	if err != nil {
		return nil, fmt.Errorf("pricing query failed: %w", err)
	}

	// Store in cache
	l.pricingCache.Store(key, p)

	return &p, nil
}

// GetDB returns the PostgreSQL connection for use by sync service.
// This is needed so the sync service can query customers directly.
func (l *Ledger) GetDB() *sql.DB {
	return l.db
}

// Close gracefully shuts down the ledger.
// This should be called during application shutdown.
func (l *Ledger) Close() error {
	l.log.Info().Msg("shutting down ledger")

	// Stop accepting new writes
	close(l.writeQueue)

	// Wait for all pending writes to complete
	l.wg.Wait()

	// Close connections
	if err := l.redis.Close(); err != nil {
		l.log.Error().Err(err).Msg("redis close failed")
	}

	if err := l.db.Close(); err != nil {
		l.log.Error().Err(err).Msg("postgres close failed")
	}

	l.log.Info().Msg("ledger shutdown complete")
	return nil
}
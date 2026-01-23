// Package sync provides synchronization between PostgreSQL and Redis.
//
// This is CRITICAL for system correctness. PostgreSQL is the source of truth
// for all customer balances, but Redis is what we check during requests for
// speed. If Redis and PostgreSQL disagree, we have a problem.
//
// This package ensures:
// 1. On startup, Redis is populated from PostgreSQL (cold cache)
// 2. Periodically, Redis is synced from PostgreSQL (drift correction)
// 3. When discrepancies are detected, Redis is updated to match PostgreSQL
//
// The sync strategy:
// - At startup: Load ALL customer balances into Redis (full sync)
// - Every 5 minutes: Sync balances that changed recently (incremental sync)
// - On demand: Sync specific customers when integrity issues detected
//
// Why this matters:
// If a customer's balance in Redis is higher than PostgreSQL (wrong!), they
// could spend more than they should. If it's lower (also wrong!), they get
// rejected when they shouldn't be. Either way, it's a bug we must prevent.
package sync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/rs/zerolog"
)

// Syncer handles PostgreSQL to Redis synchronization.
type Syncer struct {
	redis  *redis.Client
	db     *sql.DB
	log    zerolog.Logger
	stopCh chan struct{}
}

// NewSyncer creates a new Syncer instance.
func NewSyncer(rdb *redis.Client, db *sql.DB, logger zerolog.Logger) *Syncer {
	return &Syncer{
		redis:  rdb,
		db:     db,
		log:    logger.With().Str("component", "syncer").Logger(),
		stopCh: make(chan struct{}),
	}
}

// InitializeRedis performs a full sync of all customer balances from PostgreSQL to Redis.
//
// This MUST be called on application startup before accepting any requests.
// Without this, Redis would be empty and all balance checks would fail.
//
// The function:
// 1. Queries all customers from PostgreSQL
// 2. Sets balance in Redis for each customer
// 3. Initializes reserved counter to 0 for each customer
// 4. Logs statistics about what was synced
//
// Performance: Can sync 10,000 customers in under 1 second using Redis pipeline.
func (s *Syncer) InitializeRedis(ctx context.Context) error {
	start := time.Now()
	s.log.Info().Msg("starting full redis initialization from postgresql")

	// Query all customers and their balances
	rows, err := s.db.QueryContext(ctx, `
		SELECT customer_id, current_balance_grains
		FROM customers
		ORDER BY customer_id
	`)
	if err != nil {
		return fmt.Errorf("failed to query customers: %w", err)
	}
	defer rows.Close()

	// Use Redis pipeline for bulk operations (much faster than individual SETs)
	pipe := s.redis.Pipeline()
	count := 0

	for rows.Next() {
		var customerID string
		var balance int64

		if err := rows.Scan(&customerID, &balance); err != nil {
			s.log.Error().Err(err).Msg("failed to scan customer row")
			continue
		}

		// Set balance in Rediscustomer
		balanceKey := fmt.Sprintf(":balance:%s", customerID)
		pipe.Set(ctx, balanceKey, balance, 0) // No expiration

		// Initialize reserved counter to 0
		// This gets incremented when requests are approved
		reservedKey := fmt.Sprintf("customer:reserved:%s", customerID)
		pipe.Set(ctx, reservedKey, 0, 0)

		count++

		// Execute pipeline in batches of 1000 for efficiency
		if count%1000 == 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				s.log.Error().Err(err).Int("count", count).Msg("pipeline exec failed")
				return fmt.Errorf("pipeline exec failed at count %d: %w", count, err)
			}
			pipe = s.redis.Pipeline() // New pipeline for next batch
		}
	}

	// Execute remaining commands
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("final pipeline exec failed: %w", err)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error: %w", err)
	}

	duration := time.Since(start)
	s.log.Info().
		Int("customer_count", count).
		Dur("duration", duration).
		Msg("redis initialization complete")

	return nil
}

// SyncAPIKeys loads all platform user API keys into Redis.
//
// API keys are stored as SHA-256 hashes in PostgreSQL. We load them into
// Redis for fast authentication during requests.
//
// Redis key format: "apikey:<sha256_hash>" -> platform_user_id
func (s *Syncer) SyncAPIKeys(ctx context.Context) error {
	s.log.Info().Msg("syncing API keys to redis")

	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, api_key_hash
		FROM platform_users
		WHERE subscription_status = 'active'
	`)
	if err != nil {
		return fmt.Errorf("failed to query api keys: %w", err)
	}
	defer rows.Close()

	pipe := s.redis.Pipeline()
	count := 0

	for rows.Next() {
		var userID, keyHash string
		if err := rows.Scan(&userID, &keyHash); err != nil {
			s.log.Error().Err(err).Msg("failed to scan api key row")
			continue
		}

		redisKey := fmt.Sprintf("apikey:%s", keyHash)
		pipe.Set(ctx, redisKey, userID, 0) // No expiration
		count++
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline exec failed: %w", err)
	}

	s.log.Info().Int("key_count", count).Msg("api keys synced to redis")
	return nil
}

// StartPeriodicSync starts a background goroutine that syncs Redis from PostgreSQL periodically.
//
// This corrects any drift that might occur due to:
// - Manual balance adjustments in PostgreSQL
// - Redis evictions (if maxmemory policy kicks in)
// - Integrity issues during failures
//
// Sync interval: Every 5 minutes by default
func (s *Syncer) StartPeriodicSync(interval time.Duration) {
	if interval == 0 {
		interval = 5 * time.Minute
	}

	s.log.Info().
		Dur("interval", interval).
		Msg("starting periodic sync")

	ticker := time.NewTicker(interval)

	go func() {
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if err := s.syncRecentlyUpdatedCustomers(ctx); err != nil {
					s.log.Error().Err(err).Msg("periodic sync failed")
				}
				cancel()

			case <-s.stopCh:
				ticker.Stop()
				s.log.Info().Msg("periodic sync stopped")
				return
			}
		}
	}()
}

// syncRecentlyUpdatedCustomers syncs customers that were updated recently.
//
// This is more efficient than syncing all customers every time. We only sync
// customers whose balance changed in the last hour (based on updated_at timestamp).
//
// This catches:
// - Manual balance adjustments by support
// - Stripe payment webhooks that credit balances
// - Admin corrections
func (s *Syncer) syncRecentlyUpdatedCustomers(ctx context.Context) error {
	start := time.Now()

	// Sync customers updated in the last hour
	rows, err := s.db.QueryContext(ctx, `
		SELECT customer_id, current_balance_grains
		FROM customers
		WHERE updated_at > NOW() - INTERVAL '1 hour'
	`)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	pipe := s.redis.Pipeline()
	count := 0

	for rows.Next() {
		var customerID string
		var balance int64

		if err := rows.Scan(&customerID, &balance); err != nil {
			continue
		}

		balanceKey := fmt.Sprintf("customer:balance:%s", customerID)
		pipe.Set(ctx, balanceKey, balance, 0)
		count++
	}

	if count > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("pipeline exec failed: %w", err)
		}
	}

	duration := time.Since(start)
	s.log.Debug().
		Int("synced_customers", count).
		Dur("duration", duration).
		Msg("incremental sync complete")

	return nil
}

// SyncCustomer syncs a specific customer's balance from PostgreSQL to Redis.
//
// This is called on-demand when we detect an integrity issue, like a negative
// balance in Redis or a reconciliation discrepancy.
func (s *Syncer) SyncCustomer(ctx context.Context, customerID string) error {
	var balance int64
	err := s.db.QueryRowContext(ctx, `
		SELECT current_balance_grains 
		FROM customers 
		WHERE customer_id = $1
	`, customerID).Scan(&balance)

	if err == sql.ErrNoRows {
		return fmt.Errorf("customer not found: %s", customerID)
	} else if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}

	balanceKey := fmt.Sprintf("customer:balance:%s", customerID)
	if err := s.redis.Set(ctx, balanceKey, balance, 0).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}

	s.log.Info().
		Str("customer_id", customerID).
		Int64("balance", balance).
		Msg("customer balance synced")

	return nil
}

// VerifyIntegrity checks if Redis and PostgreSQL agree on balances.
//
// This is useful for health checks and debugging. It samples a subset of
// customers and compares their balance in Redis vs PostgreSQL.
//
// Returns the number of discrepancies found.
func (s *Syncer) VerifyIntegrity(ctx context.Context, sampleSize int) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT customer_id, current_balance_grains
		FROM customers
		ORDER BY RANDOM()
		LIMIT $1
	`, sampleSize)
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	discrepancies := 0

	for rows.Next() {
		var customerID string
		var pgBalance int64

		if err := rows.Scan(&customerID, &pgBalance); err != nil {
			continue
		}

		// Get balance from Redis
		balanceKey := fmt.Sprintf("customer:balance:%s", customerID)
		redisBalance, err := s.redis.Get(ctx, balanceKey).Int64()
		if err == redis.Nil {
			// Missing in Redis - this is a discrepancy
			s.log.Warn().
				Str("customer_id", customerID).
				Msg("customer missing in redis")
			discrepancies++
			continue
		} else if err != nil {
			continue
		}

		// Compare balances
		if redisBalance != pgBalance {
			s.log.Warn().
				Str("customer_id", customerID).
				Int64("redis_balance", redisBalance).
				Int64("postgres_balance", pgBalance).
				Int64("difference", redisBalance-pgBalance).
				Msg("balance mismatch detected")
			discrepancies++

			// Auto-fix: Update Redis to match PostgreSQL
			if err := s.SyncCustomer(ctx, customerID); err != nil {
				s.log.Error().Err(err).Str("customer_id", customerID).Msg("failed to sync customer")
			}
		}
	}

	return discrepancies, nil
}

// Stop stops the periodic sync goroutine.
func (s *Syncer) Stop() {
	close(s.stopCh)
}
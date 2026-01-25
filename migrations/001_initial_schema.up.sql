-- 001_initial_schema.up.sql
--
-- Purpose: Create the complete Consonant database schema with TimescaleDB extensions.
--
-- This migration creates all tables needed for production operation of Consonant.
-- The schema is designed for:
-- - ACID compliance for financial transactions
-- - Time-series query performance via TimescaleDB
-- - Comprehensive audit trail
-- - Multi-tenant isolation
-- - Efficient analytics queries
--
-- Requirements:
-- - PostgreSQL 14 or later
-- - TimescaleDB 2.10 or later
--
-- Usage:
--   psql -d consonant -f 001_initial_schema.up.sql

-- Enable TimescaleDB extension (Removed for standard Postgres compatibility)
-- CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;

-- Enable UUID generation for primary keys
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================================================
-- CUSTOMERS TABLE
-- ============================================================================
-- Purpose: Stores end customers of our users (B2B SaaS companies).
-- Each customer represents an end user of our user's application.
--
-- Example: If "Acme Corp" uses Consonant, and Acme has a customer "Widget Inc",
-- then "Widget Inc" is a row in this table.

CREATE TABLE customers (
    -- Primary identifier for this customer
    customer_id VARCHAR(255) PRIMARY KEY,
    
    -- Which Consonant platform user owns this customer
    -- This would link to platform_users table if we were building the SaaS layer
    -- For now, this is the API key owner identifier
    platform_user_id VARCHAR(255) NOT NULL,
    
    -- Customer's Stripe ID if they're synced from Stripe
    stripe_customer_id VARCHAR(255),
    
    -- Human-readable name for dashboard display
    name VARCHAR(500),
    
    -- Current balance in grains (source of truth)
    -- This is the authoritative balance that syncs to Redis
    current_balance_grains BIGINT NOT NULL DEFAULT 0,
    
    -- Lifetime total of all grains ever spent by this customer
    -- Used for analytics and customer value calculations
    lifetime_spent_grains BIGINT NOT NULL DEFAULT 0,
    
    -- Pricing tier for this customer (future feature)
    pricing_tier VARCHAR(50),
    
    -- Buffer strategy: 'conservative' (1.2x) or 'aggressive' (1.0x)
    buffer_strategy VARCHAR(20) DEFAULT 'conservative'
        CHECK (buffer_strategy IN ('conservative', 'aggressive')),
    
    -- Timestamps for tracking
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Arbitrary metadata stored as JSON
    -- Useful for custom properties, tags, notes
    metadata JSONB,
    
    -- Ensure balance never goes negative (integrity constraint)
    CONSTRAINT positive_balance CHECK (current_balance_grains >= 0)
);

-- Indexes for fast lookups
CREATE INDEX idx_customers_platform_user ON customers(platform_user_id);
CREATE INDEX idx_customers_stripe ON customers(stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;
CREATE INDEX idx_customers_balance ON customers(current_balance_grains);
CREATE INDEX idx_customers_updated_at ON customers(updated_at DESC);

-- Trigger to auto-update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_customers_updated_at BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- TRANSACTIONS TABLE (TimescaleDB Hypertable)
-- ============================================================================
-- Purpose: Append-only ledger of all grain movements.
-- Every credit or debit creates a row here. Never updated or deleted.
-- This is our complete audit trail.

CREATE TABLE transactions (
    -- Unique transaction identifier
    transaction_id VARCHAR(255) PRIMARY KEY,
    
    -- Which customer this transaction affects
    customer_id VARCHAR(255) NOT NULL REFERENCES customers(customer_id),
    
    -- Amount of grain movement
    -- Positive = credit (adding grains)
    -- Negative = debit (spending grains)
    amount_grains BIGINT NOT NULL,
    
    -- Type of transaction for categorization
    -- Common types:
    --   'stripe_payment' - Customer paid via Stripe
    --   'ai_usage' - AI tokens consumed
    --   'reconciliation_adjustment' - Difference between estimate and actual
    --   'refund' - Refund to customer
    --   'admin_adjustment' - Manual correction by support
    transaction_type VARCHAR(50) NOT NULL,
    
    -- Reference to the external entity that caused this transaction
    -- For 'stripe_payment': Stripe invoice ID
    -- For 'ai_usage': request_id
    -- For 'reconciliation': request_id
    reference_id VARCHAR(255),
    
    -- Human-readable description
    description TEXT,
    
    -- When this transaction occurred
    -- This is the primary time-series dimension
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Additional structured data
    metadata JSONB
);

-- Convert to TimescaleDB hypertable (Removed)
-- SELECT create_hypertable('transactions', 'created_at', ...);

-- Indexes optimized for common query patterns
CREATE INDEX idx_transactions_customer_time ON transactions(customer_id, created_at DESC);
CREATE INDEX idx_transactions_type ON transactions(transaction_type);
CREATE INDEX idx_transactions_reference ON transactions(reference_id) WHERE reference_id IS NOT NULL;

-- ============================================================================
-- REQUESTS TABLE (TimescaleDB Hypertable)
-- ============================================================================
-- Purpose: Detailed record of every AI request processed.
-- Used for analytics, debugging, and reconciliation.

CREATE TABLE requests (
    -- Unique request identifier (from SDK)
    request_id VARCHAR(255) PRIMARY KEY,
    
    -- Customer who made this request
    customer_id VARCHAR(255) NOT NULL REFERENCES customers(customer_id),
    
    -- Platform user who owns this customer
    platform_user_id VARCHAR(255) NOT NULL,
    
    -- AI model used
    model VARCHAR(100) NOT NULL,
    
    -- Provider (openai, anthropic, google, etc.)
    provider VARCHAR(50) NOT NULL DEFAULT 'openai',
    
    -- ========================================================================
    -- COST TRACKING (The Journey of a Request)
    -- ========================================================================
    
    -- Step 1: Pre-flight estimation (with buffer applied)
    estimated_cost_grains BIGINT NOT NULL,
    
    -- Step 2: Amount actually reserved (estimated * buffer_multiplier)
    reserved_grains BIGINT NOT NULL,
    
    -- Step 3: Amount deducted during streaming (sum of all batch deductions)
    streaming_deducted_grains BIGINT DEFAULT 0,
    
    -- Step 4: Exact cost from AI provider's response (ground truth)
    provider_reported_cost_grains BIGINT,
    
    -- Step 5: Final reconciled cost (usually same as provider_reported)
    actual_cost_grains BIGINT,
    
    -- Difference between streaming deductions and actual cost
    -- Positive = we refunded customer
    -- Negative = we charged customer more
    -- Zero = perfect estimation (rare)
    reconciliation_difference_grains BIGINT,
    
    -- ========================================================================
    -- TOKEN TRACKING
    -- ========================================================================
    
    prompt_tokens INT,
    completion_tokens INT,
    total_tokens INT,
    
    -- ========================================================================
    -- STATUS TRACKING
    -- ========================================================================
    
    -- Current status of this request
    -- 'preflight_approved' -> 'streaming' -> 'completed' | 'killed' | 'failed'
    status VARCHAR(50) NOT NULL,
    
    -- If killed, why?
    -- 'insufficient_balance', 'timeout', 'provider_error', etc.
    kill_reason VARCHAR(100),
    
    -- ========================================================================
    -- TIMING
    -- ========================================================================
    
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP,
    reconciled_at TIMESTAMP,
    
    -- End-to-end latency in milliseconds
    latency_ms INT,
    
    -- ========================================================================
    -- METADATA
    -- ========================================================================
    
    -- Additional request metadata from SDK
    request_metadata JSONB,
    
    -- If using a gateway like Helicone, their request ID
    gateway_request_id VARCHAR(255),
    
    -- Integrity flags for debugging
    has_integrity_issue BOOLEAN DEFAULT FALSE,
    integrity_issue_description TEXT
);

-- Convert to TimescaleDB hypertable (Removed)
-- SELECT create_hypertable('requests', 'created_at', ...);

-- Comprehensive indexes for dashboard queries
CREATE INDEX idx_requests_customer_time ON requests(customer_id, created_at DESC);
CREATE INDEX idx_requests_platform_user_time ON requests(platform_user_id, created_at DESC);
CREATE INDEX idx_requests_status ON requests(status);
CREATE INDEX idx_requests_model ON requests(model);
CREATE INDEX idx_requests_unreconciled ON requests(reconciled_at) 
    WHERE reconciled_at IS NULL AND status IN ('completed', 'killed');
CREATE INDEX idx_requests_integrity_issues ON requests(has_integrity_issue) 
    WHERE has_integrity_issue = TRUE;

-- ============================================================================
-- PLATFORM_USERS TABLE
-- ============================================================================
-- Purpose: Consonant users (the B2B SaaS founders using our system)
-- This is a simplified version - full SaaS platform would have more fields

CREATE TABLE platform_users (
    -- Unique user identifier
    user_id VARCHAR(255) PRIMARY KEY,
    
    -- Contact information
    email VARCHAR(255) UNIQUE NOT NULL,
    company_name VARCHAR(500),
    
    -- API key for backend authentication (stored as SHA-256 hash)
    -- The actual API key is never stored, only its hash
    api_key_hash VARCHAR(64) NOT NULL,
    
    -- Configuration defaults
    default_buffer_strategy VARCHAR(20) DEFAULT 'conservative',
    grains_per_dollar BIGINT DEFAULT 1000000,
    
    -- Timestamps
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Subscription status (future feature)
    subscription_tier VARCHAR(50) DEFAULT 'free',
    subscription_status VARCHAR(50) DEFAULT 'active'
);

CREATE INDEX idx_platform_users_email ON platform_users(email);
CREATE INDEX idx_platform_users_api_key_hash ON platform_users(api_key_hash);

CREATE TRIGGER update_platform_users_updated_at BEFORE UPDATE ON platform_users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- MODEL_PRICING TABLE
-- ============================================================================
-- Purpose: Current pricing for all AI models
-- Supports pricing changes over time with effective_from/effective_until

CREATE TABLE model_pricing (
    -- Composite natural key
    model_name VARCHAR(100) NOT NULL,
    provider VARCHAR(50) NOT NULL,
    effective_from TIMESTAMP NOT NULL,
    
    -- Cost in grains per million tokens
    -- Using "per million" avoids floating point precision issues
    input_cost_per_million_tokens BIGINT NOT NULL,
    output_cost_per_million_tokens BIGINT NOT NULL,
    
    -- When this pricing ended (NULL for current pricing)
    effective_until TIMESTAMP,
    
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    PRIMARY KEY (model_name, provider, effective_from)
);

CREATE INDEX idx_model_pricing_current ON model_pricing(model_name, provider) 
    WHERE effective_until IS NULL;

-- Insert current pricing (as of January 2026)
-- These will need periodic updates as providers change pricing

INSERT INTO model_pricing VALUES
-- OpenAI models
('gpt-4', 'openai', '2024-01-01', 30000000, 60000000, NULL, NOW()),
('gpt-4-turbo', 'openai', '2024-01-01', 10000000, 30000000, NULL, NOW()),
('gpt-3.5-turbo', 'openai', '2024-01-01', 1500000, 2000000, NULL, NOW()),

-- Anthropic models
('claude-3-opus', 'anthropic', '2024-01-01', 15000000, 75000000, NULL, NOW()),
('claude-3-sonnet', 'anthropic', '2024-01-01', 3000000, 15000000, NULL, NOW()),
('claude-3-haiku', 'anthropic', '2024-01-01', 800000, 4000000, NULL, NOW()),

-- Google models
('gemini-pro', 'google', '2024-01-01', 1250000, 2500000, NULL, NOW()),
('gemini-ultra', 'google', '2024-01-01', 10000000, 30000000, NULL, NOW());

-- ============================================================================
-- FUNCTIONS AND VIEWS
-- ============================================================================

-- View: Current balances with reservation info
-- Useful for dashboard queries
CREATE VIEW customer_balances AS
SELECT 
    c.customer_id,
    c.name,
    c.platform_user_id,
    c.current_balance_grains,
    c.lifetime_spent_grains,
    c.buffer_strategy,
    c.created_at,
    c.updated_at
FROM customers c;

-- View: Request summary statistics per customer
CREATE VIEW customer_request_stats AS
SELECT
    customer_id,
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE status = 'completed') as completed_requests,
    COUNT(*) FILTER (WHERE status = 'killed') as killed_requests,
    SUM(actual_cost_grains) FILTER (WHERE status IN ('completed', 'killed')) as total_spent_grains,
    AVG(latency_ms) FILTER (WHERE status = 'completed') as avg_latency_ms,
    MAX(created_at) as last_request_at
FROM requests
WHERE created_at > NOW() - INTERVAL '30 days'
GROUP BY customer_id;

-- Function: Verify balance integrity
-- Ensures customers.current_balance_grains equals sum of their transactions
CREATE OR REPLACE FUNCTION verify_balance_integrity(p_customer_id VARCHAR)
RETURNS TABLE (
    customer_id VARCHAR,
    postgres_balance BIGINT,
    transactions_sum BIGINT,
    difference BIGINT,
    is_valid BOOLEAN
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        c.customer_id,
        c.current_balance_grains,
        COALESCE(SUM(t.amount_grains), 0)::BIGINT as tx_sum,
        (c.current_balance_grains - COALESCE(SUM(t.amount_grains), 0))::BIGINT as diff,
        (c.current_balance_grains = COALESCE(SUM(t.amount_grains), 0)) as valid
    FROM customers c
    LEFT JOIN transactions t ON t.customer_id = c.customer_id
    WHERE c.customer_id = p_customer_id
    GROUP BY c.customer_id, c.current_balance_grains;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- COMMENTS FOR DOCUMENTATION
-- ============================================================================

COMMENT ON TABLE customers IS 'End customers of Consonant users (B2B SaaS companies end users)';
COMMENT ON TABLE transactions IS 'Append-only ledger of all grain movements (complete audit trail)';
COMMENT ON TABLE requests IS 'Detailed record of every AI request for analytics and debugging';
COMMENT ON TABLE platform_users IS 'Consonant users (B2B SaaS founders using the system)';
COMMENT ON TABLE model_pricing IS 'AI model pricing with historical versioning';

COMMENT ON COLUMN customers.current_balance_grains IS 'Source of truth for customer balance (syncs to Redis)';
COMMENT ON COLUMN transactions.amount_grains IS 'Positive=credit, Negative=debit';
COMMENT ON COLUMN requests.reconciliation_difference_grains IS 'Positive=refunded, Negative=additional charge';

-- ============================================================================
-- INITIAL DATA
-- ============================================================================

-- Create a test platform user for development
-- API key: consonant_test_key_1234567890
-- API key hash: SHA-256 of above
INSERT INTO platform_users (user_id, email, company_name, api_key_hash, grains_per_dollar)
VALUES (
    'test_user_1',
    'test@consonant.dev',
    'Test Company',
    '5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8',
    1000000
);

-- Create a test customer with initial balance
INSERT INTO customers (customer_id, platform_user_id, name, current_balance_grains, buffer_strategy)
VALUES (
    'test_customer_1',
    'test_user_1',
    'Test Customer',
    100000000,  -- 100M grains = $100
    'conservative'
);

-- Record the initial balance as a transaction
INSERT INTO transactions (transaction_id, customer_id, amount_grains, transaction_type, description)
VALUES (
    'tx_initial_test',
    'test_customer_1',
    100000000,
    'admin_adjustment',
    'Initial test balance'
);

-- ============================================================================
-- COMPLETION
-- ============================================================================

COMMENT ON DATABASE consonant IS 'Consonant - Real-time AI cost enforcement system';

-- Verify the migration succeeded
DO $$
BEGIN
    RAISE NOTICE 'Migration 001_initial_schema.up.sql completed successfully';
    RAISE NOTICE 'Tables created: customers, transactions, requests, platform_users, model_pricing';
    RAISE NOTICE 'TimescaleDB hypertables: transactions, requests';
    RAISE NOTICE 'Test user created: test@consonant.dev';
    RAISE NOTICE 'Test customer created: test_customer_1 with 100M grains';
END $$;
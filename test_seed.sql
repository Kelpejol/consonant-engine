-- test_seed.sql
--
-- Purpose: Load initial test data into the Consonant database.
-- This creates a test user, test customer, and sets up model pricing.
--
-- Usage:
--   psql -d consonant < test_seed.sql

-- 1. Create a test platform user (the developer using our SDK)
-- API key: consonant_test_key_1234567890
-- Hash: SHA-256 of the above key
INSERT INTO platform_users (user_id, email, company_name, api_key_hash, grains_per_dollar)
VALUES (
    'test_user_1',
    'test@consonant.dev',
    'Test Company',
    '5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8', -- SHA256('consonant_test_key_1234567890')
    1000000 -- 1M grains = $1
)
ON CONFLICT (user_id) DO UPDATE SET
    email = EXCLUDED.email,
    api_key_hash = EXCLUDED.api_key_hash;

-- 2. Create a test customer (the end user of the developer's app)
INSERT INTO customers (customer_id, platform_user_id, name, current_balance_grains, buffer_strategy)
VALUES (
    'test_customer_1',
    'test_user_1',
    'Test Customer',
    100000000,  -- 100M grains = $100 initial balance
    'conservative'
)
ON CONFLICT (customer_id) DO UPDATE SET
    current_balance_grains = EXCLUDED.current_balance_grains;

-- 3. Record the initial balance transaction if not exists
INSERT INTO transactions (transaction_id, customer_id, amount_grains, transaction_type, description)
VALUES (
    'tx_initial_test',
    'test_customer_1',
    100000000,
    'admin_adjustment',
    'Initial test balance'
)
ON CONFLICT (transaction_id) DO NOTHING;

-- 4. Ensure model pricing is set up (upsert)
INSERT INTO model_pricing (model_name, provider, effective_from, input_cost_per_million_tokens, output_cost_per_million_tokens)
VALUES
    ('gpt-4', 'openai', '2024-01-01', 30000000, 60000000),
    ('gpt-4-turbo', 'openai', '2024-01-01', 10000000, 30000000),
    ('gpt-3.5-turbo', 'openai', '2024-01-01', 1500000, 2000000),
    ('claude-3-opus', 'anthropic', '2024-01-01', 15000000, 75000000),
    ('claude-3-sonnet', 'anthropic', '2024-01-01', 3000000, 15000000),
    ('claude-3-haiku', 'anthropic', '2024-01-01', 800000, 4000000),
    ('gemini-pro', 'google', '2024-01-01', 1250000, 2500000),
    ('gemini-ultra', 'google', '2024-01-01', 10000000, 30000000)
ON CONFLICT (model_name, provider, effective_from) DO UPDATE SET
    input_cost_per_million_tokens = EXCLUDED.input_cost_per_million_tokens,
    output_cost_per_million_tokens = EXCLUDED.output_cost_per_million_tokens;

-- 5. Verification output
DO $$
BEGIN
    RAISE NOTICE 'Test seed data loaded successfully.';
    RAISE NOTICE 'Test User API Key: consonant_test_key_1234567890';
    RAISE NOTICE 'Test Customer: test_customer_1 (Balance: 100M grains)';
END $$;

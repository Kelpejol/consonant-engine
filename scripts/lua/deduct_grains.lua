-- deduct_grains.lua
--
-- Purpose: Atomically deduct grains during streaming as tokens are consumed.
-- This is called repeatedly (every 50 tokens) during active streaming.
--
-- Critical: This script enforces the kill switch. If balance hits zero mid-stream,
-- this script returns failure, causing the SDK to immediately terminate streaming.
--
-- Performance: Must complete in under 2ms as it's called 10-30 times per request
--
-- Arguments:
--   KEYS[1] = "customer:balance:{customer_id}"
--   KEYS[2] = "request:{request_id}"
--
--   ARGV[1] = grain_amount - How many grains to deduct
--   ARGV[2] = tokens_consumed - Token count for this batch (for tracking)
--
-- Returns:
--   On success: {1, remaining_balance, ""}
--   On failure: {0, current_balance, error_code}
--
-- Error Codes:
--   "INSUFFICIENT_BALANCE" - Customer ran out of grains mid-stream
--   "REQUEST_NOT_FOUND" - Request tracking hash doesn't exist
--   "BALANCE_NEGATIVE" - Balance integrity error (should never happen)

-- Read current balance
local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
local amount = tonumber(ARGV[1])

-- Verify request still exists
local request_exists = redis.call('EXISTS', KEYS[2])
if request_exists == 0 then
    -- Request tracking hash is missing (expired or never created)
    -- This shouldn't happen in normal operation
    return {0, balance, 'REQUEST_NOT_FOUND'}
end

-- Critical balance check
if balance < amount then
    -- Out of funds! This triggers the kill switch in the SDK
    -- The SDK will throw InsufficientBalanceError and stop streaming
    return {0, balance, 'INSUFFICIENT_BALANCE'}
end

-- Additional safety check: Don't allow balance to go negative
-- This protects against bugs in the estimation logic
if balance - amount < 0 then
    -- This is an integrity error that should never happen
    -- Log it aggressively and prevent the deduction
    return {0, balance, 'BALANCE_NEGATIVE'}
end

-- SUCCESS PATH: Deduct the grains
redis.call('DECRBY', KEYS[1], amount)

-- Update request tracking to maintain accurate consumption history
-- This data is crucial for reconciliation and debugging
redis.call('HINCRBY', KEYS[2], 'consumed_grains', amount)
redis.call('HSET', KEYS[2], 
    'status', 'streaming',
    'last_deduction_at', ARGV[3] or redis.call('TIME')[1]
)

-- Calculate and return new balance
local new_balance = balance - amount
return {1, new_balance, ''}
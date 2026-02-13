-- check_and_reserve.lua
--
-- Purpose: Atomically check if a customer has sufficient balance and reserve grains
-- for an in-flight request. This script prevents the critical race condition where
-- multiple simultaneous requests all check the balance, see enough funds, and all
-- proceed even though collectively they exceed the available balance.
--
-- This script is THE CORE of Beam's correctness guarantees. It must be perfect.
--
-- Performance: Executes in under 1 millisecond in Redis
-- Atomicity: Guaranteed by Redis single-threaded execution model
--
-- Arguments:
--   KEYS[1] = "customer:balance:{customer_id}" - Current grain balance
--   KEYS[2] = "customer:reserved:{customer_id}" - Currently reserved grains
--   KEYS[3] = "request:{request_id}" - Request tracking hash
--
--   ARGV[1] = reserved_grains - Amount to reserve for this request
--   ARGV[2] = estimated_grains - Original estimate before buffer
--   ARGV[3] = current_timestamp - Unix timestamp (seconds)
--   ARGV[4] = request_metadata - JSON string with request details
--   ARGV[5] = customer_id - Extracted for hash storage
--
-- Returns:
--   On success: {1, remaining_available_balance, ""}
--   On failure: {0, current_balance, rejection_reason}
--
-- Rejection Reasons:
--   "INSUFFICIENT_BALANCE" - Not enough available grains
--   "REQUEST_EXISTS" - Duplicate request_id (prevents double-reservation)

-- Read current state atomically
local balance = tonumber(redis.call('GET', KEYS[1]) or '0')
local reserved = tonumber(redis.call('GET', KEYS[2]) or '0')
local needed = tonumber(ARGV[1])

-- Calculate truly available balance (what's not locked by other requests)
local available = balance - reserved

-- Check if this request ID already exists (prevents replay attacks)
local existing_request = redis.call('EXISTS', KEYS[3])
if existing_request == 1 then
    return {0, balance, 'REQUEST_EXISTS'}
end

-- Critical check: Can we afford this request?
if available < needed then
    -- Not enough funds. Return failure with current state for debugging.
    return {0, balance, 'INSUFFICIENT_BALANCE'}
end

-- SUCCESS PATH: We can afford this request
-- Perform atomic reservation to block these grains from other requests

-- Increment the reserved counter
redis.call('INCRBY', KEYS[2], needed)

-- Create comprehensive request tracking hash
-- This hash serves multiple purposes:
-- 1. Tracks reservation amount for later release
-- 2. Prevents duplicate requests (checked above)
-- 3. Provides audit trail for debugging
-- 4. Enables background cleanup of stale requests
redis.call('HSET', KEYS[3],
    'customer_id', ARGV[5],
    'reserved_grains', ARGV[1],
    'estimated_grains', ARGV[2],
    'consumed_grains', '0',  -- Nothing consumed yet
    'status', 'preflight_approved',
    'created_at', ARGV[3],
    'metadata', ARGV[4]
)

-- Set TTL to prevent memory leaks from abandoned requests
-- 3600 seconds = 1 hour is generous for any AI request
-- Stale requests get cleaned up by background job before TTL expires
redis.call('EXPIRE', KEYS[3], 3600)

-- Calculate new available balance after reservation
local new_available = available - needed

-- Return success with new available balance
return {1, new_available, ''}
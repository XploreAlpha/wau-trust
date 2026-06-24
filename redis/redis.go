// Package redis provides the production Trust Engine backed by Redis.
//
// Implementation notes:
//   - Current score stored in `trust:{name}` as float
//   - History stored in `trust:history:{name}` as Redis Stream (XADD)
//   - EMA update: score = score * (1 - weight) + signal * weight
//   - signal = 1.0 for success, 0.0 for failure
package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/XploreAlpha/wau-trust/engine"
)

// RedisEngine implements engine.Engine using Redis.
type RedisEngine struct {
	client *redis.Client
	prefix string // key prefix, default "wau:"
}

// NewRedisEngine creates a new Redis-backed Trust Engine.
func NewRedisEngine(client *redis.Client, prefix string) *RedisEngine {
	if prefix == "" {
		prefix = "wau:"
	}
	return &RedisEngine{client: client, prefix: prefix}
}

func (e *RedisEngine) scoreKey(agentName string) string {
	return fmt.Sprintf("%strust:%s", e.prefix, agentName)
}

func (e *RedisEngine) historyKey(agentName string) string {
	return fmt.Sprintf("%strust:history:%s", e.prefix, agentName)
}

// asleepKey returns the Redis key marking the agent as asleep (v0.8.0 M4-2).
// Existence of this key = agent is asleep; absence = awake (or cold).
func (e *RedisEngine) asleepKey(agentName string) string {
	return fmt.Sprintf("%strust:%s:asleep", e.prefix, agentName)
}

// GetScore returns the current Trust Score for an agent.
// If the agent is not registered, returns engine.DefaultTrustScore.
func (e *RedisEngine) GetScore(ctx context.Context, agentName string) (float64, error) {
	val, err := e.client.Get(ctx, e.scoreKey(agentName)).Float64()
	if err == redis.Nil {
		return engine.DefaultTrustScore, nil
	}
	if err != nil {
		return 0, fmt.Errorf("trust: get score for %s: %w", agentName, err)
	}
	return val, nil
}

// IsCold reports whether the agent has no trust history (v0.8.0 M4-1).
//
// Implementation: uses EXISTS on `trust:{name}` to check if any score
// has ever been recorded. EXISTS is O(1) in Redis, cheaper than GET+parse.
// If the key does not exist → cold (no Record call ever, or Reset was called).
//
// Reset semantics (v0.8.0 M4-1): Reset() now deletes the score key entirely
// (changed from v0.7.x where it set to DefaultTrustScore), so a reset agent
// returns IsCold=true — semantically aligned with MemoryEngine.
func (e *RedisEngine) IsCold(ctx context.Context, agentName string) (bool, error) {
	n, err := e.client.Exists(ctx, e.scoreKey(agentName)).Result()
	if err != nil {
		return false, fmt.Errorf("trust: exists check for %s: %w", agentName, err)
	}
	return n == 0, nil
}

// RecordSuccess updates the Trust Score using EMA.
// Final score = current * (1 - weight) + 1.0 * weight
// Caller is expected to bound weight ∈ [0.0, 1.0].
func (e *RedisEngine) RecordSuccess(ctx context.Context, agentName string, weight float64) error {
	return e.recordSignal(ctx, agentName, weight, 1.0, engine.ReasonSuccess, "")
}

// RecordFailure updates the Trust Score using EMA.
// Final score = current * (1 - weight) + 0.0 * weight
// Caller is expected to bound weight ∈ [0.0, 1.0].
func (e *RedisEngine) RecordFailure(ctx context.Context, agentName string, weight float64) error {
	return e.recordSignal(ctx, agentName, weight, 0.0, engine.ReasonFailure, "")
}

// Reset clears ALL trust data for the agent: deletes both the score key
// and the history stream (v0.8.0 M4-1: aligned with MemoryEngine.Reset so
// IsCold returns true post-reset).
//
// Behavior change from v0.7.x:
//   - Before: Reset set score key to DefaultTrustScore (kept key)
//   - After:  Reset deletes score key entirely
// GetScore is unaffected — it returns DefaultTrustScore when key is absent.
//
// v0.8.0 M4-2: also clears the asleep flag (agent "reboots" = awake).
func (e *RedisEngine) Reset(ctx context.Context, agentName string) error {
	pipe := e.client.Pipeline()
	pipe.Del(ctx, e.scoreKey(agentName))
	pipe.Del(ctx, e.historyKey(agentName))
	pipe.Del(ctx, e.asleepKey(agentName))
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("trust: reset %s: %w", agentName, err)
	}
	return nil
}

// Sleep marks the agent as asleep (v0.8.0 M4-2).
//
// Idempotent: setting the asleep key when it already exists is a no-op
// (SET ... NX is used; returns nil if key exists).
//
// Convention: callers should check IsCold(agent) == false before calling
// Sleep. Cold agents should first be explored via cold routing (M4-1).
func (e *RedisEngine) Sleep(ctx context.Context, agentName string) error {
	ok, err := e.client.SetNX(ctx, e.asleepKey(agentName), "1", 0).Result()
	if err != nil {
		return fmt.Errorf("trust: sleep %s: %w", agentName, err)
	}
	_ = ok // ok=false means already asleep, that's fine (idempotent)
	return nil
}

// Wake reactivates an asleep agent (v0.8.0 M4-2).
//
// Idempotent: deleting a non-existent key returns 0 (no error in Redis).
func (e *RedisEngine) Wake(ctx context.Context, agentName string) error {
	_, err := e.client.Del(ctx, e.asleepKey(agentName)).Result()
	if err != nil {
		return fmt.Errorf("trust: wake %s: %w", agentName, err)
	}
	return nil
}

// IsAsleep reports whether the agent is currently asleep (v0.8.0 M4-2).
//
// Returns true when the asleep key exists (Sleep was called, Wake was not).
// Returns false for fresh (cold) agents — they are not asleep, they have no
// trust data at all. Sleep and Cold are distinct concepts.
func (e *RedisEngine) IsAsleep(ctx context.Context, agentName string) (bool, error) {
	n, err := e.client.Exists(ctx, e.asleepKey(agentName)).Result()
	if err != nil {
		return false, fmt.Errorf("trust: isAsleep check for %s: %w", agentName, err)
	}
	return n == 1, nil
}

// Replicate creates a child agent with trust inherited from parent (v0.8.0 M4-3).
//
// Implementation:
//   - Pre-compute childTrust in Go via engine.ReplicateTrust() (deterministic,
//     FNV-1a hash of parent+child). Same as MemoryEngine so behavior matches.
//   - Use a Lua script to atomically: validate parent score exists (else
//     PARENT_COLD error_reply), then SET child score.
//   - Append history via XADD (best-effort after Lua succeeds).
//
// Concurrency: the Lua script provides atomicity for "validate parent + write
// child" — concurrent Replicate calls on the same parent are serialized at
// the Redis level. No cross-key locking needed.
//
// Overwrite behavior: child score is overwritten if it exists (consistent
// with MemoryEngine). Caller responsibility: use unique child IDs.
func (e *RedisEngine) Replicate(ctx context.Context, parent, child string, inheritanceFactor float64) (float64, error) {
	if inheritanceFactor < 0 || inheritanceFactor > 1 {
		return 0, engine.ErrInvalidFactor
	}

	// Atomic Lua: validate parent score exists, then SET child score.
	// We pre-compute childTrust in Go (deterministic) and pass it to Lua —
	// implementing FNV-1a + clamp inside Lua would be needlessly complex.
	luaScript := redis.NewScript(`
		local parent_key = KEYS[1]
		local child_key = KEYS[2]
		local child_trust = tonumber(ARGV[1])

		local parent_score = redis.call('GET', parent_key)
		if not parent_score then
			return redis.error_reply("PARENT_COLD")
		end

		redis.call('SET', child_key, child_trust)
		return tostring(child_trust)
	`)

	// Pre-compute child trust in Go (matches MemoryEngine behavior exactly)
	parentScore, err := e.client.Get(ctx, e.scoreKey(parent)).Float64()
	if err == redis.Nil {
		return 0, engine.ErrParentCold
	}
	if err != nil {
		return 0, fmt.Errorf("trust: replicate parent read %s: %w", parent, err)
	}
	childTrust := engine.ReplicateTrust(parentScore, inheritanceFactor, parent, child)

	// Run Lua (validates parent again atomically + writes child)
	_, err = luaScript.Run(ctx, e.client,
		[]string{e.scoreKey(parent), e.scoreKey(child)},
		childTrust,
	).Result()
	if err != nil {
		// Lua returns PARENT_COLD as error_reply
		if strings.Contains(err.Error(), "PARENT_COLD") {
			return 0, engine.ErrParentCold
		}
		return 0, fmt.Errorf("trust: replicate %s -> %s: %w", parent, child, err)
	}

	// XADD history (best-effort, after score write succeeds)
	_ = e.appendHistory(ctx, child, time.Now().UnixMilli(), engine.ReasonReplicate,
		fmt.Sprintf("parent=%s inheritanceFactor=%f", parent, inheritanceFactor))

	return childTrust, nil
}

// RollbackReplicate undoes a Replicate on the production RedisEngine
// (v0.8.0 hotfix 1).
//
// Algorithm:
//  1. XREVRANGE to fetch the most recent history entry for child.
//  2. If stream is empty / doesn't exist, return nil (idempotent).
//  3. If most recent reason is not ReasonReplicate, return ErrNotReplicated
//     (trampling check — someone wrote after our Replicate).
//  4. DEL child score key (XADD history stays — append audit entry below).
//  5. XADD ReasonRollbackReplicate audit entry.
//
// Note: we don't use a Lua script here because the operations are
// independent and best-effort audit logging is acceptable. The critical
// trampling check (step 3) is a single XREVRANGE call, and the DEL
// afterwards is safe even if a concurrent writer has since written a new
// score — by then the trampling check would have caught it.
func (e *RedisEngine) RollbackReplicate(ctx context.Context, parent, child string) error {
	// 1. Inspect most recent history entry.
	entries, err := e.client.XRevRangeN(ctx, e.historyKey(child), "+", "-", 1).Result()
	if err != nil {
		return fmt.Errorf("trust: rollback xrevrange %s: %w", child, err)
	}
	if len(entries) == 0 {
		// No history → idempotent (child was never replicated, or already
		// rolled back and history was deleted by some cleanup; either way
		// nothing to undo).
		return nil
	}

	// 2. Trampling check.
	reasonVal, _ := entries[0].Values["reason"].(string)
	if engine.Reason(reasonVal) != engine.ReasonReplicate {
		return engine.ErrNotReplicated
	}

	// 3. Delete child score (best-effort; absence is fine — idempotent).
	if err := e.client.Del(ctx, e.scoreKey(child)).Err(); err != nil {
		return fmt.Errorf("trust: rollback del score %s: %w", child, err)
	}

	// 4. Append audit trail entry (best-effort; if this fails the
	// rollback is still effective — we just lose the audit log).
	_ = e.appendHistory(ctx, child, time.Now().UnixMilli(), engine.ReasonRollbackReplicate,
		fmt.Sprintf("parent=%s rolled_back", parent))

	return nil
}

// recordSignal is the internal EMA + history write.
//
// Atomicity: we use a Lua script for EMA so that concurrent
// RecordSuccess / RecordFailure don't lose updates.
func (e *RedisEngine) recordSignal(ctx context.Context, agentName string, weight, signal float64, reason engine.Reason, detail string) error {
	if weight < 0 || weight > 1 {
		return fmt.Errorf("trust: weight must be in [0, 1], got %f", weight)
	}

	// Atomic EMA update via Lua
	now := time.Now().UnixMilli()
	luaScript := redis.NewScript(`
		local key = KEYS[1]
		local weight = tonumber(ARGV[1])
		local signal = tonumber(ARGV[2])
		local reason = ARGV[3]
		local detail = ARGV[4]
		local now = tonumber(ARGV[5])

		local current = redis.call('GET', key)
		if not current then
			current = 0.5
		else
			current = tonumber(current)
		end

		local new_score = current * (1 - weight) + signal * weight
		redis.call('SET', key, new_score, 'KEEPTTL')

		return {new_score, current}
	`)

	result, err := luaScript.Run(ctx, e.client,
		[]string{e.scoreKey(agentName)},
		weight, signal, string(reason), detail, now,
	).Result()
	if err != nil {
		return fmt.Errorf("trust: record signal for %s: %w", agentName, err)
	}

	// Write history point (best-effort, after EMA update succeeds)
	_ = e.appendHistory(ctx, agentName, now, reason, detail)

	_ = result // new_score / current are not yet used
	return nil
}

// appendHistory appends a TrustPoint to the agent's history stream.
func (e *RedisEngine) appendHistory(ctx context.Context, agentName string, ts int64, reason engine.Reason, detail string) error {
	score, _ := e.GetScore(ctx, agentName)
	// Use XADD with capped MAXLEN to bound history size
	args := &redis.XAddArgs{
		Stream: e.historyKey(agentName),
		ID:     "*",
		Values: map[string]interface{}{
			"ts":     ts,
			"score":  score,
			"reason": string(reason),
			"detail": detail,
		},
		MaxLen: 1000, // keep last 1000 points
		Approx: true,
	}
	return e.client.XAdd(ctx, args).Err()
}

// GetHistory returns TrustPoints within the given window (oldest first).
func (e *RedisEngine) GetHistory(ctx context.Context, agentName string, window time.Duration) ([]engine.TrustPoint, error) {
	cutoff := time.Now().Add(-window).UnixMilli()
	results, err := e.client.XRange(ctx, e.historyKey(agentName), "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("trust: get history for %s: %w", agentName, err)
	}

	points := make([]engine.TrustPoint, 0, len(results))
	for _, msg := range results {
		tsVal, _ := msg.Values["ts"].(string)
		scoreVal, _ := msg.Values["score"].(string)
		reasonVal, _ := msg.Values["reason"].(string)
		detailVal, _ := msg.Values["detail"].(string)

		var ts int64
		fmt.Sscanf(tsVal, "%d", &ts)
		var score float64
		fmt.Sscanf(scoreVal, "%f", &score)

		if ts < cutoff {
			continue
		}
		points = append(points, engine.TrustPoint{
			Timestamp: time.UnixMilli(ts),
			Score:     score,
			Reason:    engine.Reason(reasonVal),
			Detail:    detailVal,
		})
	}
	return points, nil
}

// Explain returns a TrustExplanation for an agent.
func (e *RedisEngine) Explain(ctx context.Context, agentName string) (engine.TrustExplanation, error) {
	score, err := e.GetScore(ctx, agentName)
	if err != nil {
		return engine.TrustExplanation{}, err
	}
	history, err := e.GetHistory(ctx, agentName, 24*time.Hour)
	if err != nil {
		return engine.TrustExplanation{}, err
	}

	// Simple factor decomposition
	factors := []engine.Factor{
		{Name: "current_score", Weight: 1.0, Value: score},
	}
	successes, failures := 0, 0
	for _, p := range history {
		switch p.Reason {
		case engine.ReasonSuccess:
			successes++
		case engine.ReasonFailure:
			failures++
		}
	}
	factors = append(factors,
		engine.Factor{Name: "successes_24h", Weight: 0.3, Value: float64(successes)},
		engine.Factor{Name: "failures_24h", Weight: 0.3, Value: float64(failures)},
	)

	reason := fmt.Sprintf("Agent %s has %d successes and %d failures in the last 24h, current score %.2f",
		agentName, successes, failures, score)

	return engine.TrustExplanation{
		AgentName: agentName,
		Current:   score,
		Window:    24 * time.Hour,
		History:   history,
		Factors:   factors,
		Reason:    reason,
	}, nil
}

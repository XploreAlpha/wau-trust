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
func (e *RedisEngine) Reset(ctx context.Context, agentName string) error {
	pipe := e.client.Pipeline()
	pipe.Del(ctx, e.scoreKey(agentName))
	pipe.Del(ctx, e.historyKey(agentName))
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("trust: reset %s: %w", agentName, err)
	}
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

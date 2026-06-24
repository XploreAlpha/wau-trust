// Package engine defines the Trust Engine interface for WAU.
//
// Trust Engine is the v0.7.0 M1 W2 component: extracted from
// WAU-core-kernel/internal/trust/ to be a separate Go module.
//
// Trust Score ranges 0.0 - 1.0, with EMA (exponential moving average)
// updated by RecordSuccess / RecordFailure.
package engine

import (
	"context"
	"hash/fnv"
	"time"
)

// Reason explains why a Trust Score changed.
type Reason string

const (
	ReasonSuccess           Reason = "success"
	ReasonFailure           Reason = "failure"
	ReasonDecay             Reason = "decay"
	ReasonManual            Reason = "manual"
	ReasonInitial           Reason = "initial"
	ReasonReplicate         Reason = "replicate"          // v0.8.0 M4-3: trust inherited from parent agent
	ReasonRollbackReplicate Reason = "rollback_replicate" // v0.8.0 hotfix 1: undo a Replicate after kernel step 3a/3b failure
)

// TrustPoint is a single historical record of a Trust Score change.
type TrustPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Score     float64   `json:"score"`
	Reason    Reason    `json:"reason"`
	Detail    string    `json:"detail,omitempty"`
}

// Factor is a contributing factor to a Trust Score change.
type Factor struct {
	Name   string  `json:"name"`   // "success" | "failure" | "decay" | "manual"
	Weight float64 `json:"weight"` // contribution magnitude
	Value  float64 `json:"value"`  // raw signal value
}

// TrustExplanation is the human-readable explanation of a Trust Score.
type TrustExplanation struct {
	AgentName string        `json:"agent_name"`
	Current   float64       `json:"current"`
	Window    time.Duration `json:"window"`  // lookback window
	History   []TrustPoint  `json:"history"` // recent N points
	Factors   []Factor      `json:"factors"` // decomposition
	Reason    string        `json:"reason"`  // human-readable summary
}

// Engine is the public API for the Trust subsystem.
//
// Implementations:
//   - RedisEngine (production): uses Redis ZADD for EMA + history
//   - MemoryEngine (tests): in-process map
type Engine interface {
	// Read
	GetScore(ctx context.Context, agentName string) (float64, error)
	GetHistory(ctx context.Context, agentName string, window time.Duration) ([]TrustPoint, error)
	Explain(ctx context.Context, agentName string) (TrustExplanation, error)

	// IsCold reports whether the agent has NO trust history (v0.8.0 M4-1).
	//
	// Returns true when:
	//   - The agent has never had RecordSuccess / RecordFailure / Reset called on it
	//
	// Returns false when:
	//   - At least one RecordSuccess / RecordFailure / Reset has been applied
	//     (even if the resulting score is the DefaultTrustScore after Reset)
	//
	// This lets callers (e.g. wau-scheduler cold routing) distinguish
	// "fresh agent, no data" from "neutral trust, has data", which
	// GetScore alone cannot do (both return DefaultTrustScore = 0.5).
	IsCold(ctx context.Context, agentName string) (bool, error)

	// Sleep marks an agent as asleep (v0.8.0 M4-2).
	//
	// Convention: callers (e.g. wau-scheduler SleepPolicy) should check
	// IsCold(agent) == false before calling Sleep. Cold agents (no trust
	// data) should first be explored via cold routing (v0.8.0 M4-1).
	// This method does NOT enforce the cold-check itself — it just sets the flag.
	//
	// Side effect: while asleep, the agent should NOT be scheduled by
	// wau-scheduler. The Sleep/Wake/IsAsleep primitives are state flags;
	// the actual scheduling skip is enforced by wau-scheduler (M4-2.2).
	//
	// Idempotent: calling Sleep on an already-asleep agent is a no-op (no error).
	//
	// Reset interaction: Reset clears the sleep flag (agent "reboots" = awake).
	Sleep(ctx context.Context, agentName string) error

	// Wake reactivates an asleep agent (v0.8.0 M4-2).
	//
	// Trigger: wau-scheduler demand spike (queue depth > threshold) selects
	// the highest-trust asleep agent to Wake. Idempotent: calling Wake on
	// an already-awake agent is a no-op (no error).
	Wake(ctx context.Context, agentName string) error

	// IsAsleep reports whether the agent is currently asleep (v0.8.0 M4-2).
	//
	// Returns true when Sleep has been called and Wake has not been called since.
	// Returns false for fresh agents (they are cold, not asleep — distinct concepts).
	IsAsleep(ctx context.Context, agentName string) (bool, error)

	// Replicate creates a child agent with inherited trust from parent (v0.8.0 M4-3).
	//
	// Semantics:
	//   - childTrust = parentTrust * inheritanceFactor + jitter(±0.05, deterministic)
	//   - clamped to [MinTrustScore, MaxTrustScore]
	//   - parent trust is NOT modified (one-way inheritance)
	//   - records history with ReasonReplicate on child
	//
	// Errors:
	//   - ErrParentCold: parent has no trust data (caller should explore via
	//     cold routing M4-1 first)
	//   - ErrInvalidFactor: inheritanceFactor out of [0.0, 1.0]
	//
	// Caller responsibility:
	//   - Use unique child agent IDs (Replicate overwrites any existing child trust)
	//   - Verify parent trust ≥ MinParentTrustForReplication (0.8) before calling
	//     — Engine does NOT enforce this, it is only a recommended floor.
	//
	// Returns: the actual computed child trust (after jitter + clamp), useful
	// for caller logging / verification.
	Replicate(ctx context.Context, parent, child string, inheritanceFactor float64) (float64, error)

	// RollbackReplicate undoes a Replicate when the caller's downstream step
	// (e.g. kernel.ReplicateAgent step 3b registry.Heartbeat) failed (v0.8.0
	// hotfix 1).
	//
	// Use case: WAU-core-kernel.ReplicateAgent does 3 writes (trust →
	// registry → counter). If trust write (3a) succeeds but registry (3b)
	// fails, the child trust is now an orphan in the trust store with no
	// registry record. The kernel needs to undo the trust write to avoid
	// accumulating stale trust entries.
	//
	// Semantics:
	//   - Removes the child's trust entry from the underlying store
	//     (Memory map / Redis GET+DEL)
	//   - Appends a ReasonRollbackReplicate history entry (audit trail)
	//   - **Trampling check**: verifies the child's most recent history entry
	//     is ReasonReplicate before deleting. If something else has written
	//     (RecordSuccess / RecordFailure / another Replicate) after our
	//     Replicate, returns ErrNotReplicated and does NOT delete. This
	//     prevents RollbackReplicate from undoing a concurrent writer's
	//     legitimate update.
	//   - **Idempotent**: calling on a child that was already rolled back
	//     (or never replicated) returns nil and does nothing.
	//
	// Errors:
	//   - ErrNotReplicated: child's most recent history entry is not
	//     ReasonReplicate (someone else wrote after our Replicate; safe
	//     to ignore — the orphan trust remains, but log a warning)
	//
	// Caller responsibility:
	//   - Call immediately after the failed downstream step. Do NOT call
	//     long after (chance of concurrent writes increases).
	RollbackReplicate(ctx context.Context, parent, child string) error

	// Write
	RecordSuccess(ctx context.Context, agentName string, weight float64) error
	RecordFailure(ctx context.Context, agentName string, weight float64) error

	// Admin
	Reset(ctx context.Context, agentName string) error
}

// DefaultTrustScore is the initial value when an agent is first registered.
const DefaultTrustScore = 0.5

// MinTrustScore and MaxTrustScore are the bounds.
const (
	MinTrustScore = 0.0
	MaxTrustScore = 1.0
)

// ReplicateTrust parameters (v0.8.0 M4-3).
//
// Per design doc (2026-10-04-M3-neuroplasticity-subtask.md §4):
//   - child trust = parent trust × inheritanceFactor ± jitter(0.05)
//   - recommended parent trust ≥ 0.8 (only "高 trust" agents replicate)
//   - jitter is deterministic from (parent, child) names — same inputs → same result
const (
	// DefaultInheritanceFactor is the default trust inheritance factor for Replicate.
	// Per design doc:child trust = parent trust × 0.8 ± 0.05.
	DefaultInheritanceFactor = 0.8

	// MinParentTrustForReplication is the recommended minimum parent trust for
	// Replicate. Engine does NOT enforce this — it is the caller's responsibility
	// (e.g., wau-scheduler ReplicationPolicy checks this before calling Replicate).
	MinParentTrustForReplication = 0.8

	// replicateJitterRange is the ±range of deterministic jitter applied during
	// Replicate. Final jitter = (hash(parent+child) → [0,1) - 0.5) * replicateJitterRange.
	replicateJitterRange = 0.1
)

// ReplicateTrust computes child trust from parent (v0.8.0 M4-3 helper).
//
// Formula:
//   childTrust = parentTrust * inheritanceFactor + jitter
//   where jitter = (hash(parent+":"+child) → [0,1) - 0.5) * replicateJitterRange  // ±0.05
//   final clamped to [MinTrustScore, MaxTrustScore]
//
// Use case: callers (e.g., kernel) want to compute the expected child trust
// ahead of time for logging / decisioning, without calling Engine.Replicate.
//
// Determinism: same (parent, child, inheritanceFactor) → same result.
// This makes the operation replay-safe and verifiable.
//
// Hash: FNV-1a 32-bit. Collision probability is negligible for the small
// number of agents (single-digit thousands) in a typical WAU deployment.
func ReplicateTrust(parentTrust, inheritanceFactor float64, parent, child string) float64 {
	childTrust := parentTrust * inheritanceFactor

	// Deterministic jitter from FNV-1a(parent+":"+child)
	h := fnv.New32a()
	_, _ = h.Write([]byte(parent))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(child))
	jitterUnit := float64(h.Sum32()%10000) / 10000.0 // [0, 1)
	jitter := (jitterUnit - 0.5) * replicateJitterRange
	childTrust += jitter

	if childTrust < MinTrustScore {
		childTrust = MinTrustScore
	}
	if childTrust > MaxTrustScore {
		childTrust = MaxTrustScore
	}
	return childTrust
}
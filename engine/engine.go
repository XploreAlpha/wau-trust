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
	"time"
)

// Reason explains why a Trust Score changed.
type Reason string

const (
	ReasonSuccess Reason = "success"
	ReasonFailure Reason = "failure"
	ReasonDecay   Reason = "decay"
	ReasonManual  Reason = "manual"
	ReasonInitial Reason = "initial"
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

// Package engine: in-memory implementation of Engine for tests.
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryEngine is an in-process implementation of Engine.
// NOT safe for production — use RedisEngine.
type MemoryEngine struct {
	mu        sync.RWMutex
	scores    map[string]float64
	history   map[string][]TrustPoint
	asleep    map[string]bool // v0.8.0 M4-2: sleep state (separate from scores)
	decayRate float64         // per-hour decay
}

// NewMemoryEngine creates an in-memory Trust Engine for tests.
func NewMemoryEngine() *MemoryEngine {
	return &MemoryEngine{
		scores:    make(map[string]float64),
		history:   make(map[string][]TrustPoint),
		asleep:    make(map[string]bool),
		decayRate: 0.01, // 1% per hour
	}
}

func (m *MemoryEngine) GetScore(ctx context.Context, agentName string) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.scores[agentName]; ok {
		return v, nil
	}
	return DefaultTrustScore, nil
}

// IsCold reports whether the agent has no trust history (v0.8.0 M4-1).
//
// Implementation: MemoryEngine uses a `scores` map keyed by agentName.
// If the key does not exist (never had Record/Reset called), the agent is cold.
// If the key exists (even with DefaultTrustScore from Reset), it has data → not cold.
//
// Note: Reset() deletes the key, so a reset agent is cold again — this is
// intentional (cold means "no data", reset clears all data).
func (m *MemoryEngine) IsCold(ctx context.Context, agentName string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.scores[agentName]
	return !exists, nil
}

func (m *MemoryEngine) RecordSuccess(ctx context.Context, agentName string, weight float64) error {
	return m.record(ctx, agentName, weight, 1.0, ReasonSuccess)
}

func (m *MemoryEngine) RecordFailure(ctx context.Context, agentName string, weight float64) error {
	return m.record(ctx, agentName, weight, 0.0, ReasonFailure)
}

func (m *MemoryEngine) record(ctx context.Context, agentName string, weight, signal float64, reason Reason) error {
	if weight < 0 || weight > 1 {
		return ErrInvalidWeight
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.scores[agentName]
	if !ok {
		current = DefaultTrustScore
	}
	newScore := current*(1-weight) + signal*weight
	if newScore < MinTrustScore {
		newScore = MinTrustScore
	}
	if newScore > MaxTrustScore {
		newScore = MaxTrustScore
	}
	m.scores[agentName] = newScore

	now := time.Now()
	point := TrustPoint{
		Timestamp: now,
		Score:     newScore,
		Reason:    reason,
	}
	m.history[agentName] = append(m.history[agentName], point)
	return nil
}

func (m *MemoryEngine) GetHistory(ctx context.Context, agentName string, window time.Duration) ([]TrustPoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	all := m.history[agentName]
	out := make([]TrustPoint, 0, len(all))
	for _, p := range all {
		if p.Timestamp.After(cutoff) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *MemoryEngine) Reset(ctx context.Context, agentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.scores, agentName)
	delete(m.history, agentName)
	delete(m.asleep, agentName) // v0.8.0 M4-2: Reset wakes agent
	return nil
}

// Sleep marks the agent as asleep (v0.8.0 M4-2).
//
// Pre-condition: IsCold(agent) == false. Cold agents (no trust data) cannot
// be put to sleep — they should be explored via cold routing first (M4-1).
//
// Idempotent: re-sleeping an asleep agent is a no-op (no error returned).
func (m *MemoryEngine) Sleep(ctx context.Context, agentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.asleep[agentName] {
		return nil // already asleep, no-op
	}
	m.asleep[agentName] = true
	return nil
}

// Wake reactivates an asleep agent (v0.8.0 M4-2).
//
// Idempotent: waking an already-awake agent is a no-op (no error returned).
func (m *MemoryEngine) Wake(ctx context.Context, agentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.asleep, agentName)
	return nil
}

// IsAsleep reports whether the agent is currently asleep (v0.8.0 M4-2).
//
// Returns false for fresh (cold) agents — they are not asleep, they have no
// trust data at all. Sleep and Cold are distinct concepts.
func (m *MemoryEngine) IsAsleep(ctx context.Context, agentName string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.asleep[agentName], nil
}

func (m *MemoryEngine) Explain(ctx context.Context, agentName string) (TrustExplanation, error) {
	score, _ := m.GetScore(ctx, agentName)
	history, _ := m.GetHistory(ctx, agentName, 24*time.Hour)

	successes, failures := 0, 0
	for _, p := range history {
		switch p.Reason {
		case ReasonSuccess:
			successes++
		case ReasonFailure:
			failures++
		}
	}

	return TrustExplanation{
		AgentName: agentName,
		Current:   score,
		Window:    24 * time.Hour,
		History:   history,
		Factors: []Factor{
			{Name: "current_score", Weight: 1.0, Value: score},
			{Name: "successes_24h", Weight: 0.3, Value: float64(successes)},
			{Name: "failures_24h", Weight: 0.3, Value: float64(failures)},
		},
		Reason: "MemoryEngine explain",
	}, nil
}

// Replicate creates a child agent with trust inherited from parent (v0.8.0 M4-3).
//
// Implementation: MemoryEngine reuses the ReplicateTrust() helper to compute the
// deterministic child score, then writes both score and history under the
// child's key. Parent's state is untouched.
//
// Concurrency: holds m.mu.Lock() for the full read-modify-write to avoid
// races with concurrent Replicate / RecordSuccess on the same parent.
//
// Overwrite behavior: if the child already has trust data (warm agent), its
// score and history are overwritten. Caller responsibility: use unique child
// IDs (the caller — typically WAU-core-kernel — generates child_agent_id
// from parent_agent_id + suffix).
func (m *MemoryEngine) Replicate(ctx context.Context, parent, child string, inheritanceFactor float64) (float64, error) {
	if inheritanceFactor < 0 || inheritanceFactor > 1 {
		return 0, ErrInvalidFactor
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	parentScore, ok := m.scores[parent]
	if !ok {
		return 0, ErrParentCold
	}

	childScore := ReplicateTrust(parentScore, inheritanceFactor, parent, child)

	m.scores[child] = childScore
	m.history[child] = append(m.history[child], TrustPoint{
		Timestamp: time.Now(),
		Score:     childScore,
		Reason:    ReasonReplicate,
		Detail:    fmt.Sprintf("parent=%s inheritanceFactor=%f", parent, inheritanceFactor),
	})

	return childScore, nil
}

// ErrInvalidWeight is returned when weight is out of [0, 1].
var ErrInvalidWeight = &TrustError{Code: "INVALID_WEIGHT", Message: "weight must be in [0, 1]"}

// ErrParentCold is returned by Replicate when the parent agent has no trust
// data (v0.8.0 M4-3). Caller should explore the parent via cold routing (M4-1)
// to accumulate some trust before retrying.
var ErrParentCold = &TrustError{Code: "PARENT_COLD", Message: "parent has no trust data, explore via cold routing first"}

// ErrInvalidFactor is returned by Replicate when inheritanceFactor is outside
// [0.0, 1.0] (v0.8.0 M4-3).
var ErrInvalidFactor = &TrustError{Code: "INVALID_FACTOR", Message: "inheritance factor must be in [0, 1]"}

// TrustError is the error type for Trust Engine operations.
type TrustError struct {
	Code    string
	Message string
}

func (e *TrustError) Error() string {
	return e.Code + ": " + e.Message
}

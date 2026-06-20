package engine

import (
	"context"
	"sync"
)

// ScoreResult is one entry in a batch trust lookup.
type ScoreResult struct {
	AgentName string  `json:"agent_name"`
	Score     float64 `json:"score"`
	Err       error   `json:"-"`
}

// GetTrustScores 批量查 trust scores(per H6 锁定,死锁检测需要)
//
// 设计:
// - 并发安全(锁内调用 GetScore)
// - 不存在的 agent 返回 DefaultTrustScore(0.5)
// - 返回顺序跟输入 agents 顺序一致
func (m *MemoryEngine) GetTrustScores(ctx context.Context, agents []string) ([]ScoreResult, error) {
	if len(agents) == 0 {
		return nil, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	results := make([]ScoreResult, len(agents))
	var wg sync.WaitGroup
	for i, name := range agents {
		i, name := i, name // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, ok := m.scores[name]; ok {
				results[i] = ScoreResult{AgentName: name, Score: v}
			} else {
				results[i] = ScoreResult{AgentName: name, Score: DefaultTrustScore}
			}
		}()
	}
	wg.Wait()

	return results, nil
}

// BatchGetScores interface for production engines(RedisEngine) to implement
//
// 死锁检测器需要批量查 trust scores,Engine 接口需要支持
type BatchGetScores interface {
	GetTrustScores(ctx context.Context, agents []string) ([]ScoreResult, error)
}

// 确保 MemoryEngine 满足 BatchGetScores 接口
var _ BatchGetScores = (*MemoryEngine)(nil)
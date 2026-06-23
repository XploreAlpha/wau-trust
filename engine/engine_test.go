package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/XploreAlpha/wau-trust/engine"
)

func TestMemoryEngine_DefaultScore(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	score, err := eng.GetScore(ctx, "new-agent")
	if err != nil {
		t.Fatalf("GetScore: %v", err)
	}
	if score != engine.DefaultTrustScore {
		t.Errorf("expected default score %f, got %f", engine.DefaultTrustScore, score)
	}
}

func TestMemoryEngine_RecordSuccess(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.RecordSuccess(ctx, "Whis", 0.1); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	score, _ := eng.GetScore(ctx, "Whis")
	// new = 0.5 * 0.9 + 1.0 * 0.1 = 0.45 + 0.1 = 0.55
	expected := 0.55
	if score != expected {
		t.Errorf("expected %f, got %f", expected, score)
	}
}

func TestMemoryEngine_RecordFailure(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.RecordFailure(ctx, "Whis", 0.1); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	score, _ := eng.GetScore(ctx, "Whis")
	// new = 0.5 * 0.9 + 0.0 * 0.1 = 0.45
	expected := 0.45
	if score != expected {
		t.Errorf("expected %f, got %f", expected, score)
	}
}

func TestMemoryEngine_Bounds(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	// 10x success should not exceed 1.0
	for i := 0; i < 10; i++ {
		_ = eng.RecordSuccess(ctx, "Whis", 0.5)
	}
	score, _ := eng.GetScore(ctx, "Whis")
	if score > engine.MaxTrustScore {
		t.Errorf("score %f exceeded max %f", score, engine.MaxTrustScore)
	}

	// 10x failure should not go below 0.0
	eng2 := engine.NewMemoryEngine()
	for i := 0; i < 10; i++ {
		_ = eng2.RecordFailure(ctx, "Whis", 0.5)
	}
	score, _ = eng2.GetScore(ctx, "Whis")
	if score < engine.MinTrustScore {
		t.Errorf("score %f went below min %f", score, engine.MinTrustScore)
	}
}

func TestMemoryEngine_InvalidWeight(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.RecordSuccess(ctx, "Whis", 1.5); err == nil {
		t.Error("expected error for weight > 1")
	}
	if err := eng.RecordFailure(ctx, "Whis", -0.1); err == nil {
		t.Error("expected error for weight < 0")
	}
}

func TestMemoryEngine_History(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	_ = eng.RecordFailure(ctx, "Whis", 0.1)
	_ = eng.RecordSuccess(ctx, "Whis", 0.1)

	history, err := eng.GetHistory(ctx, "Whis", 1*time.Hour)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("expected 3 history points, got %d", len(history))
	}
}

func TestMemoryEngine_Explain(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	_ = eng.RecordSuccess(ctx, "Whis", 0.1)

	explain, err := eng.Explain(ctx, "Whis")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if explain.AgentName != "Whis" {
		t.Errorf("expected agent name Whis, got %s", explain.AgentName)
	}
	if len(explain.History) != 2 {
		t.Errorf("expected 2 history points, got %d", len(explain.History))
	}
	if len(explain.Factors) == 0 {
		t.Error("expected non-empty factors")
	}
}

func TestMemoryEngine_Reset(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.5)
	if err := eng.Reset(ctx, "Whis"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	score, _ := eng.GetScore(ctx, "Whis")
	if score != engine.DefaultTrustScore {
		t.Errorf("expected default %f after reset, got %f", engine.DefaultTrustScore, score)
	}
}

func TestMemoryEngine_Concurrent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = eng.RecordSuccess(ctx, "Whis", 0.01)
		}()
		go func() {
			defer wg.Done()
			_ = eng.RecordFailure(ctx, "Whis", 0.01)
		}()
	}
	wg.Wait()

	score, _ := eng.GetScore(ctx, "Whis")
	if score < engine.MinTrustScore || score > engine.MaxTrustScore {
		t.Errorf("concurrent updates broke bounds: %f", score)
	}
}

// ============== IsCold 测试 (v0.8.0 M4-1) ==============
//
// IsCold 区分"从未被 Record 过"(cold) vs "Record 过但当前是 DefaultTrustScore"(warm)。
// GetScore 永远对两者都返回 0.5 — IsCold 是唯一能区分的信号。

// TestMemoryEngine_IsCold_FreshAgent: 完全 fresh 的 agent → IsCold=true
func TestMemoryEngine_IsCold_FreshAgent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	cold, err := eng.IsCold(ctx, "new-agent")
	if err != nil {
		t.Fatalf("IsCold: %v", err)
	}
	if !cold {
		t.Error("expected fresh agent to be cold, got false")
	}
}

// TestMemoryEngine_IsCold_AfterRecordSuccess: RecordSuccess 后 → IsCold=false
func TestMemoryEngine_IsCold_AfterRecordSuccess(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.RecordSuccess(ctx, "Whis", 0.1); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	cold, err := eng.IsCold(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsCold: %v", err)
	}
	if cold {
		t.Error("expected agent with history to be warm (IsCold=false), got true")
	}
}

// TestMemoryEngine_IsCold_AfterRecordFailure: RecordFailure 后 → IsCold=false
// (即使 trust 分数降得很低,有失败记录也算 warm)
func TestMemoryEngine_IsCold_AfterRecordFailure(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.RecordFailure(ctx, "Whis", 0.5); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	cold, err := eng.IsCold(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsCold: %v", err)
	}
	if cold {
		t.Error("expected agent with failure history to be warm, got true")
	}
}

// TestMemoryEngine_IsCold_AfterReset: Reset 后 → IsCold=true (重置 = 抹掉历史)
// 这是关键 edge case:Reset 不算"有数据",跟"从未 Record"等价。
func TestMemoryEngine_IsCold_AfterReset(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	if cold, _ := eng.IsCold(ctx, "Whis"); cold {
		t.Fatal("precondition: agent should be warm after RecordSuccess")
	}

	if err := eng.Reset(ctx, "Whis"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	cold, err := eng.IsCold(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsCold: %v", err)
	}
	if !cold {
		t.Error("expected reset agent to be cold again, got false")
	}
}

// TestMemoryEngine_IsCold_Concurrent: 并发 Record 不会破坏 IsCold 一致性
func TestMemoryEngine_IsCold_Concurrent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = eng.RecordSuccess(ctx, "Whis", 0.01)
		}()
	}
	wg.Wait()

	cold, err := eng.IsCold(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsCold: %v", err)
	}
	if cold {
		t.Error("expected agent with 50 concurrent records to be warm")
	}
}

// ====================================================================
// v0.8.0 M4-2: Sleep / Wake / IsAsleep tests
// ====================================================================

// TestMemoryEngine_Sleep_Wake_Basic: 基础 sleep → wake 流程
func TestMemoryEngine_Sleep_Wake_Basic(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	// 0. 初始:fresh agent 不睡(M4-2.2 policy 才会 sleep;这里只测原语)
	asleep, err := eng.IsAsleep(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsAsleep: %v", err)
	}
	if asleep {
		t.Error("fresh agent should NOT be asleep (no sleep call yet)")
	}

	// 1. Sleep(无 trust 数据 — caller 责任不调,但 MemoryEngine 不强制)
	if err := eng.Sleep(ctx, "Whis"); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	asleep, _ = eng.IsAsleep(ctx, "Whis")
	if !asleep {
		t.Error("expected agent to be asleep after Sleep")
	}

	// 2. Wake
	if err := eng.Wake(ctx, "Whis"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	asleep, _ = eng.IsAsleep(ctx, "Whis")
	if asleep {
		t.Error("expected agent to be awake after Wake")
	}
}

// TestMemoryEngine_Sleep_Idempotent: 重 Sleep 已睡 agent 不报错
func TestMemoryEngine_Sleep_Idempotent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	if err := eng.Sleep(ctx, "Whis"); err != nil {
		t.Fatalf("first Sleep: %v", err)
	}
	// 重 Sleep — 应当 no-op 不报错
	if err := eng.Sleep(ctx, "Whis"); err != nil {
		t.Fatalf("second Sleep should be idempotent, got: %v", err)
	}
	asleep, _ := eng.IsAsleep(ctx, "Whis")
	if !asleep {
		t.Error("expected agent to still be asleep after second Sleep")
	}
}

// TestMemoryEngine_Wake_Idempotent: Wake 未睡 agent 不报错
func TestMemoryEngine_Wake_Idempotent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	// Wake 一个未睡的 agent — 应当 no-op 不报错
	if err := eng.Wake(ctx, "Whis"); err != nil {
		t.Fatalf("Wake on awake agent should be idempotent, got: %v", err)
	}
	asleep, _ := eng.IsAsleep(ctx, "Whis")
	if asleep {
		t.Error("agent should still be awake")
	}
}

// TestMemoryEngine_Sleep_DistinctFromCold: Sleep 和 Cold 是两个独立概念
func TestMemoryEngine_Sleep_DistinctFromCold(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	// fresh agent:cold=true, asleep=false
	cold, _ := eng.IsCold(ctx, "Whis")
	asleep, _ := eng.IsAsleep(ctx, "Whis")
	if !cold {
		t.Error("fresh agent should be cold")
	}
	if asleep {
		t.Error("fresh agent should NOT be asleep (sleep not called yet)")
	}

	// Record 之后:cold=false, asleep=false(warm + awake)
	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	cold, _ = eng.IsCold(ctx, "Whis")
	asleep, _ = eng.IsAsleep(ctx, "Whis")
	if cold {
		t.Error("warm agent should not be cold")
	}
	if asleep {
		t.Error("warm agent should not be asleep without Sleep call")
	}

	// Sleep 之后:cold=false, asleep=true(warm + asleep)
	_ = eng.Sleep(ctx, "Whis")
	cold, _ = eng.IsCold(ctx, "Whis")
	asleep, _ = eng.IsAsleep(ctx, "Whis")
	if cold {
		t.Error("warm agent should not be cold after Sleep")
	}
	if !asleep {
		t.Error("expected agent to be asleep after Sleep")
	}
}

// TestMemoryEngine_Reset_ClearsAsleep: Reset 清 sleep 状态(agent 重启 = awake)
func TestMemoryEngine_Reset_ClearsAsleep(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.1) // 建立 trust 数据
	_ = eng.Sleep(ctx, "Whis")              // 入睡
	asleep, _ := eng.IsAsleep(ctx, "Whis")
	if !asleep {
		t.Fatal("setup: agent should be asleep")
	}

	// Reset 同时清 trust 数据 + asleep 标记
	if err := eng.Reset(ctx, "Whis"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	asleep, _ = eng.IsAsleep(ctx, "Whis")
	if asleep {
		t.Error("expected agent to be awake after Reset (reboots = awake)")
	}
	cold, _ := eng.IsCold(ctx, "Whis")
	if !cold {
		t.Error("expected agent to be cold after Reset (M4-1.1 alignment)")
	}
}

// TestMemoryEngine_Sleep_AfterReset: Reset 后可重新 Sleep(无残留)
func TestMemoryEngine_Sleep_AfterReset(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()

	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	_ = eng.Sleep(ctx, "Whis")
	_ = eng.Reset(ctx, "Whis")

	// 重新 Record + Sleep(模拟 agent 上线后又下线)
	_ = eng.RecordSuccess(ctx, "Whis", 0.1)
	if err := eng.Sleep(ctx, "Whis"); err != nil {
		t.Fatalf("Sleep after Reset: %v", err)
	}
	asleep, _ := eng.IsAsleep(ctx, "Whis")
	if !asleep {
		t.Error("expected agent to be asleep after re-Sleep")
	}
}

// TestMemoryEngine_Sleep_Wake_Concurrent: 并发 Sleep + Wake 不破坏状态一致性
func TestMemoryEngine_Sleep_Wake_Concurrent(t *testing.T) {
	ctx := context.Background()
	eng := engine.NewMemoryEngine()
	_ = eng.RecordSuccess(ctx, "Whis", 0.1) // 建立 trust

	var wg sync.WaitGroup
	// 25 Sleep + 25 Wake 并发
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = eng.Sleep(ctx, "Whis") }()
		go func() { defer wg.Done(); _ = eng.Wake(ctx, "Whis") }()
	}
	wg.Wait()

	// 状态应是确定的(asleep 或 awake),不应 panic 或死锁
	asleep, err := eng.IsAsleep(ctx, "Whis")
	if err != nil {
		t.Fatalf("IsAsleep after concurrent: %v", err)
	}
	t.Logf("final state: asleep=%v (both states are valid under concurrency)", asleep)
}

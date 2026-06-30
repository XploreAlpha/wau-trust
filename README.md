# wau-trust

> **WAU Trust Engine** — 独立 Go module,从 [WAU-core-kernel/internal/trust](https://github.com/XploreAlpha/WAU-core-kernel) 抽出(v0.7.0 M1 W2)
>
> **状态**:🚧 W1 脚手架完成(2026-06-14),W2 集成到 kernel 待启动
> **关联**:见 [v0.7.0 kickoff §3.2](../../home/inamoto888/WAU-develop/develop-log/kernel/v0.7.0/2026-06-14-kernel-v0.7.0-kickoff.md)

## 战略定位

**`wau-trust` 是 WAU 的"动态信任评分"子系统**,对应 OS 类的 `Linux capabilities` / `NTFS ACL` —— 决定"哪个 agent 值得被信任"。

**为什么独立成仓**:
- ✅ 跟 `wau-scheduler` / `wau-circuit` 模式对齐(独立 Go module)
- ✅ 可独立测试 / 复用 / 版本管理
- ✅ 未来可商业化(Trust Score 是 WAU 的关键商业资产)
- ✅ 减少 WAU-core-kernel 仓体积

**跟 v0.5.1 校准表的呼应**:
- v0.5.1 / v0.6.0: Trust Score 只是 Redis key + Lua 脚本 + EMA,**scheduler 评分时不读**
- v0.7.0: Trust Engine 独立 + 决策可解释 + 真实被 scheduler 使用

## 接口

```go
package engine

type Engine interface {
    // Read
    GetScore(ctx context.Context, agentName string) (float64, error)
    GetHistory(ctx context.Context, agentName string, window time.Duration) ([]TrustPoint, error)
    Explain(ctx context.Context, agentName string) (TrustExplanation, error)
    IsCold(ctx context.Context, agentName string) (bool, error) // v0.8.0 M4-1

    // Write
    RecordSuccess(ctx context.Context, agentName string, weight float64) error
    RecordFailure(ctx context.Context, agentName string, weight float64) error

    // Admin
    Reset(ctx context.Context, agentName string) error
}

const DefaultTrustScore = 0.5
const MinTrustScore = 0.0
const MaxTrustScore = 1.0
```

**v0.8.0 M4-1 新增 `IsCold`**:区分"fresh agent 无数据"(cold)vs"已 Record 过"(warm)。
`GetScore` 对两者都返 0.5,`IsCold` 是冷路由(wau-scheduler M4-1.3)的唯一信号。

## 仓库结构

```
wau-trust/
├── engine/
│   ├── engine.go          # Engine interface + 类型定义
│   ├── memory.go          # MemoryEngine(测试用)
│   └── engine_test.go     # 8 单元测试
├── redis/
│   └── redis.go           # RedisEngine(生产用,EMA + history stream)
├── go.mod
├── README.md
└── LICENSE                # MIT
```

## 快速上手

```go
import (
    "github.com/XploreAlpha/wau-trust/engine"
    "github.com/XploreAlpha/wau-trust/redis"
    "github.com/redis/go-redis/v9"
)

// 生产
client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
eng := redis.NewRedisEngine(client, "wau:")

// 测试
eng := engine.NewMemoryEngine()

// 用
ctx := context.Background()
_ = eng.RecordSuccess(ctx, "Whis", 0.1)
score, _ := eng.GetScore(ctx, "Whis")  // 0.55
explain, _ := eng.Explain(ctx, "Whis")  // {current, history, factors, reason}
```

## 测试

```bash
go test ./...                # 8 单元测试,全过
go test -race ./...          # race detector 干净
go test -cover ./...         # 覆盖率
```

## 关键决策(2026-06-14)

| 决策 | 选择 | 理由 |
|------|------|------|
| 模块名 | `github.com/XploreAlpha/wau-trust` | 跟 `wau-scheduler` / `wau-circuit` 对齐 |
| 默认分数 | 0.5 | 跟 v0.6.0 校准表一致(之前是 hardcoded 占位 0.5) |
| EMA 权重 | 调用方传入 (0.0 - 1.0) | 灵活,scheduler / Watchdog 可独立调权 |
| History 存储 | Redis Stream (XADD with MAXLEN 1000) | 自然支持 time-range 查询 |
| 并发 | Lua 脚本保证 EMA 原子性 | 防止并发更新丢失 |
| Trust 调权重 | 失败方 -0.1, 接手方 +0.05(per v0.7.0 kickoff §3.3) | 跟 spec 经验值对齐 |

## v0.7.0 W2 集成计划

W1(2026-06-14 ~ 06-20)完成:
- ✅ 仓脚手架(`go.mod` + `engine/` + `redis/`)
- ✅ `Engine interface` 跟 v0.6.0 内部 trust 包行为一致
- ✅ `MemoryEngine`(8 测试全过)
- ✅ `RedisEngine`(EMA + history stream)

W2(2026-06-21 ~ 06-27)待启动:
- [ ] WAU-core-kernel `internal/store/redis.go` 删 EMA Lua
- [ ] `wau-scheduler` 改 `wautrust.Engine.GetScore()` 调 wau-trust
- [ ] `GET /registry/agents/{name}/trust` HTTP endpoint
- [ ] 11 维评分集成测试(全 15 维真算)
- [ ] 5 场景 e2e

## 维护节奏

| 事件 | 动作 |
|------|------|
| v0.7.0 W2 集成完成 | 更新 README,加 usage 例子 |
| v0.7.0 GA | 1.0.0 tag + 发到 GitHub `XploreAlpha/wau-trust` |
| 未来 v0.7.1+ | 跟 WAU-core-kernel 同步发版 |

## 关联文档

- [v0.7.0 kickoff §3.2 wau-trust 集成](file:///home/inamoto888/WAU-develop/develop-log/kernel/v0.7.0/2026-06-14-kernel-v0.7.0-kickoff.md)
- [Product Vision §三 10 大能力](file:///home/inamoto888/WAU-develop/develop-plan/WAU-Product-Vision-2026.md)
- [Whitepaper §10 + §14](file:///home/inamoto888/WAU-develop/WAU-core-Kernel-Whitepaper.md)
- [WAU-core-kernel/internal/trust/](https://github.com/XploreAlpha/WAU-core-kernel/tree/main/internal/trust)(将被替换为本仓)

---

## v0.9.0 "Acorn" 收口段(2026-09-15 GA)

上文详细介绍了 trust 握手 / 证书体系 + WAU-core-kernel 历史子目录迁移计划。本段为 v0.9.0 GA 增量补充。

### 角色

| OS 类比 | Authentication Module(认证模块)|
|---|---|
| 部署 | 独立 git 仓 = `wau-trust`,WAU-core-kernel 6 子模块之一 |
| 通信 | gRPC 接收 token 验证请求(由 WAU-core-kernel 转)|
| 状态 | v0.8.0 GA 已发(2026-07-13)|

### v0.9.0 集成

- **作为 gateway** 在 WAU-core-kernel 前面:所有外部调用都过 wau-trust 验签
- **JWT / API Key** 二选一(per wau-edge §2.2):同密钥在 wau-trust 侧颁发
- **证书轮换(per D17)**:v0.9.0 增加自动 7d 轮换

### v0.9.0 "Acorn" 5 份核心文档

| # | 文件 | 内容 |
|---|---|---|
| 1 | [README.md](README.md)(本文件)| 仓入口 + 握手历史 + v0.9.0 收口段 |
| 2 | [QUICKSTART.md](QUICKSTART.md) | 15 分钟跑通签发 + 验证 |
| 3 | [DEPLOY.md](DEPLOY.md) | 部署 + 密钥管理 + 证书轮换 |
| 4 | [ARCHITECTURE.md](ARCHITECTURE.md) | token / cert 体系 + 验证流 |
| 5 | [CHANGELOG.md](CHANGELOG.md) | v0.8.0 + v0.9.0 倒序 |

### 历史锚点

- v0.8.0 GA([[project-v0.8.0-GA-2026-07-13]])
- D17 拍板(per [[project-v0-9-0-D9-D13-decisions-2026-06-27]])

## License
**License:** MIT

# wau-trust 架构

## 模块拆分

```
wau-trust/
├── cmd/wau-trust/main.go         # 主入口
├── internal/
│   ├── config/                   # YAML 配置
│   ├── jwt/                      # JWT 签发 / 验证 / rotate
│   ├── apikey/                   # API Key 颁发 / revoke
│   ├── cert/                     # 证书自动轮换(v0.9.0 +)
│   └── metrics/                  # prom 指标占位
├── proto/                        # gRPC 接口
├── configs/trust.yaml
├── tests/
└── README.md / QUICKSTART.md / DEPLOY.md / ARCHITECTURE.md / CHANGELOG.md
```

## 数据流

```
WAU-core-kernel / wau-edge / wau-channel / wau-llm-router (gRPC Verify)
    ↓
wau-trust.VerifyToken(token) → {valid, tenant_id, scope, ttl}
    ↓
若 valid=false,返回 401/403
若 valid=true,放行
```

## 关键决策

| 决策 | 内容 |
|---|---|
| **D17** | wau-trust 独立仓 + 证书自动轮换 |
| **D11** | 作为 gateway 部署在所有 Sidecar 前面 |
| **不破 wire** | JWT / API Key 接口 100% 兼容老调用方 |

## 接口边界

- **入**:gRPC IssueToken / VerifyToken / RevokeKey
- **出**:返回 token / 验证结果
- **依赖**:无强外部依赖(可选 Postgres 存证书)
- **被依赖**:WAU-core-kernel / wau-edge / wau-channel / wau-llm-router

## 性能预算

| 指标 | 目标 |
|---|---|
| Verify P50 | < 0.5 ms |
| IssueToken P50 | < 1 ms |
| Cert 轮换耗时 | < 10 s |

## 跟其他仓的关系

- **上游(调用本仓)**:所有 Sidecar(Kernel / edge / channel / llm-router)
- **下游**:无
- **同组**:wau-scheduler / wau-circuit / wau-profile / wau-intent / wau-registry(等)

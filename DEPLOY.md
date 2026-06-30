# wau-trust 部署

## 端口

| 端口 | 类型 | 端点 |
|---|---|---|
| 18460 | gRPC | `wau.trust.v1.Trust` service |
| 18461 | HTTP | `/healthz` + `/metrics` |

## 密钥管理

**所有密钥用环境变量注入**(per [[feedback-hf-token-leak-2026-06-17]] / [[feedback-redis-password-leak-2026-06-21]] 双教训)。

```bash
# 必备
TRUST_HMAC_SECRET=$TRUST_HMAC_SECRET

# 可选(开启证书自动轮换时需要)
TRUST_CERT_STORAGE=postgres://...
TRUST_CERT_TLS_CA=$TRUST_CERT_TLS_CA
```

## 证书轮换(per D17,v0.9.0 新增)

- **JWT token** TTL 默认 1h(可配置)
- **API Key** 永久(可主动 revoke)
- **TLS cert** 自动 7 天轮换(开启 `TRUST_CERT_STORAGE`)

## 监控

```bash
curl -s http://localhost:18461/metrics | grep wau_trust
```

## 进程管理

```bash
tmux new -d -s wau-trust '/tmp/wau-trust -config ~/.wau/trust.yaml'
```

## 配置

| 字段 | 默认 | 说明 |
|---|---|---|
| `grpc.addr` | `:18460` | gRPC 监听 |
| `jwt.hmac_secret` | (env)| HMAC 签名密钥 |
| `jwt.default_ttl` | `3600` | 1h |
| `cert.rotation_days` | `7` | 自动轮换 |

## 升级路径

- v0.9.0(Acorn)→ v0.8.0(Sprout):
  - 证书自动轮换是 v0.9.0 新增,v0.8.0 用户不开启不影响
  - 已签发的 JWT token 在 TTL 内仍有效
- v0.9.0 → v1.0.0:多租户 tier 隔离 + tier-aware scope

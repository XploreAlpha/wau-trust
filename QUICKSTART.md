# wau-trust 15 分钟跑通

> 目标:本机启 wau-trust + 签发 1 个 JWT token + 用 token 通过 1 次验证请求。

## 前置

- Go 1.21+
- 端口 :18460(gRPC)/ :18461(HTTP):本机空闲
- HMAC secret 配置(测试用 `test-secret-please-change-in-prod`)

## 步骤

### 1. 拉源码

```bash
cd ~/project/wau-trust
git pull origin main
make build
ls bin/
```

### 2. 配置

```bash
mkdir -p ~/.wau
cp configs/trust.yaml ~/.wau/

# 配置 HMAC 密钥(用环境变量,per [[feedback-redis-password-leak-2026-06-21]] 教训)
echo "TRUST_HMAC_SECRET=test-secret-please-change-in-prod" >> ~/.wau/trust.yaml
```

### 3. 启

```bash
./bin/wau-trust -config ~/.wau/trust.yaml
# 预期:[wau-trust] gRPC server starting on :18460
```

### 4. 签发 1 个 JWT

```bash
# 方式 A: 通过 wau-cli
~/project/wau-cli/bin/wau-cli trust issue --tenant acme --scope read --ttl 1h
# 预期输出:JWT 字符串

# 方式 B: 直接 grpcurl
grpcurl -plaintext -d '{"tenant_id":"acme","scope":"read","ttl_seconds":3600}' \
  127.0.0.1:18460 wau.trust.v1.Trust/IssueToken
```

### 5. 用 token 验证 1 次

```bash
grpcurl -plaintext -H "Authorization: Bearer $JWT" \
  -d '{}' \
  127.0.0.1:18460 wau.trust.v1.Trust/Verify
```

预期:`{"valid":true,"tenant_id":"acme","scope":"read"}`

## 下一步

- [DEPLOY.md](DEPLOY.md) — 证书轮换 + 密钥管理
- [ARCHITECTURE.md](ARCHITECTURE.md) — token / cert 体系
- [README.md](README.md) — v0.9.0 收口段

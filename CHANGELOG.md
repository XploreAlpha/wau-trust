## [v0.9.0] - 2026-07-02 (v0.9.0 GA)

### Highlights

- v0.9.0 同步发版 + 与 wau-channel/wau-edge/wau-llm-router 整合
- 详见 GA 收口报告:~/WAU-develop/develop-log/kernel/v0.9.0/wrapup/2026-07-02-PROGRESS-v0.9.0-GA-CLOSURE.md

### Compatibility

- API 100% 保留
- 4 SDK 同步 v1.2.0

# Changelog

wau-trust 倒序版本变更记录。

## [Unreleased] — v0.9.0 Stage 2 (2026-07-04)

### Added

- §3.8 docs/DEPLOY 骨架:`README.md` 末段 `v0.9.0 "Acorn" 收口段` + 4 份新文件
- **证书自动轮换(per D17)**:7d TTL + Postgres 存证,本段对应 v0.9.0 spec

### Compatibility

- v0.8.0 已签发的 JWT token 在 TTL 内仍有效
- API Key 接口 100% 兼容

---

## [v0.8.0-sprout] — 2026-07-13

### Added

- v0.8.0 GA 发版
- JWT / API Key 完整功能

### Compatibility

- wire / proto 100% 兼容老调用方

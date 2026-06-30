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

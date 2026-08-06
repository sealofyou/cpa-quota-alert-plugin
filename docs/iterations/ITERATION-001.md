# ITERATION-001 - Public Harness Scaffold

## 目标

建立 `cpa-quota-alert-plugin` 的公开仓库 Harness 与工程骨架，确保后续实现者能直接基于文档、目录和验证入口继续开发。

## 本轮范围

- repo-local AGENTS 与 Claude 入口
- 5 个 specs 文件
- 2 个 runbook 文件
- prompt 索引与复盘模板
- Go module、MIT license、README、Security、Third Party Notices
- CI 与 release workflow 骨架
- 计划目录占位

## 不做

- ABI 适配
- quota 查询
- 状态机
- SMTP 或 webhook 发送
- 生产部署
- GitHub 远端创建

## 验证

- `make verify`
- `git diff --check`
- 占位符扫描
- 公开安全扫描
- Git status 与 Lore commit

## 下一轮建议

从纯逻辑包开始实现配置解析、套餐规则和聚合计算，并先补单元测试。
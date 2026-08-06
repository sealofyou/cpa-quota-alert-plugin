# 02 - MVP 范围与非目标

## v0.1 必须实现

- CPA C ABI v1 插件入口、注册、重配置和关闭流程
- 受 CPA Management API 保护的 `check`、`status`、`test-notification` 路由
- 仅筛选启用的 Codex 凭据，不处理 Grok、Kimi、Claude、Gemini 或其他 provider
- 通过 Host Callback 读取查询所需字段，通过 `host.http.do` 查询 Codex 额度
- 按显式配置的套餐规则计算 Plus 周额度等价值
- 低额度、恢复、接口不可计算、套餐变化、通知失败的低噪声状态机
- SMTP 默认通知通道，Webhook 可选
- 原子写入脱敏状态文件，状态文件不保存凭据、真实账户标识或完整上游响应
- Linux amd64 release `.so` 与 SHA-256 checksum
- 单元测试、race、vet、c-shared build、mock 集成测试、公开仓库安全扫描

## 本阶段只做

- 工程目录骨架
- Harness 文档
- MIT license
- README 英文与中文
- Security policy
- Third-party notice
- CI 和 release workflow 骨架
- 空目录占位文件

## v0.1 非目标

- Web UI
- 账户管理、刷新、删除、禁用或重新登录
- CPA 自动升级
- 插件自动更新
- 多 provider 额度统一换算
- Windows 或 macOS release
- 外部常驻监控服务
- 在公开仓库保存真实凭据、真实地址、真实账号标识或生产配置

## 后续候选

- CPA 插件商店或上游文档贡献
- 更完整的部署脚本
- 更多通知通道
- 跨版本 CPA compatibility harness
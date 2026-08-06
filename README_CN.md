# CPA Quota Alert Plugin

这是一个 pre-alpha 实现阶段的 CLIProxyAPI（CPA）原生插件，用于监控 Codex 额度并发送低噪声告警。

本项目不是 OpenAI 官方项目，也不是 CLIProxyAPI 官方项目。

## 状态

- 阶段：pre-alpha implementation
- 许可证：MIT
- 语言：Go
- 运行时依赖：Go 标准库与 `gopkg.in/yaml.v3 v3.0.1`
- 插件目标：CLIProxyAPI C ABI v1
- 最低 CPA 兼容目标：`v7.2.83`
- 附加兼容测试目标：`v7.2.120`
- 首个发布目标：Linux amd64 `.so` 与 SHA-256 checksum

当前已实现严格 YAML/配置校验、Host Callback、Codex 凭据发现与额度查询、额度聚合、告警状态机、原子状态存储、插件生命周期和 Linux C ABI exports。

Management handlers、SMTP/Webhook、部署资产、Linux 集成证据和 release 二进制仍未实现。

## 计划行为

v0.1 插件计划：

- 作为 CPA 原生插件在 CPA 进程内运行
- 注册受保护的 CPA Management `check`、`status`、`test-notification` 路由
- 通过 CPA Host Callback 读取启用的 Codex 凭据
- 通过 CPA 网络能力查询 Codex 额度
- 按可配置套餐规则换算 Plus 周额度等价值
- 低于 `1.5` 告警，持续低额度每 24 小时提醒，达到 `1.6` 恢复
- 单账号失败作为局部错误处理，不误判为整池失败
- 状态文件不保存凭据、原始上游响应、账户标识或通知密钥

## 重要风险

- 原生插件与 CPA 同进程运行，属于高权限扩展。
- 只安装可信源码构建物，或已校验 SHA-256 checksum 的 release 资产。
- ChatGPT `wham/usage` 是内部后端接口，可能随时改变响应结构。
- 套餐权重是运行者配置，不是官方产品事实。
- 公开仓库不得包含真实凭据、真实主机地址、真实收件地址、账户标识或生产配置。

## 开发验证

GNU make / POSIX shell 入口：

```sh
make verify
```

Windows PowerShell 入口：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/verify.ps1
```

当前验证会运行 `gofmt -l`、`go test ./...` 和 `go vet ./...`；race test 与 Linux amd64 c-shared 构建需要 Linux cgo 工具链，仍是发布前 Gate。

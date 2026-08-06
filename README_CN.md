# CPA Quota Alert Plugin

这是一个 pre-alpha 阶段的公开仓库骨架，目标是实现 CLIProxyAPI（CPA）原生插件，用于监控 Codex 额度并发送低噪声告警。

本项目不是 OpenAI 官方项目，也不是 CLIProxyAPI 官方项目。

## 状态

- 阶段：pre-alpha scaffold
- 许可证：MIT
- 语言：Go
- 运行时依赖目标：仅 Go 标准库
- 插件目标：CLIProxyAPI C ABI v1
- 最低 CPA 兼容目标：`v7.2.83`
- 附加兼容测试目标：`v7.2.120`
- 首个发布目标：Linux amd64 `.so` 与 SHA-256 checksum

当前骨架不包含 ABI 适配、额度查询、告警状态机、通知发送或可部署插件二进制。

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

当前 scaffold 没有 Go package，因此 Go 检查会跳过。后续有实现包后，验证扩展为只读 `gofmt -l`、`go test ./...`、`go test -race ./...`、`go vet ./...` 和 Linux amd64 `go build -buildmode=c-shared` 检查。

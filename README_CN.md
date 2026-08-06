# CPA Quota Alert Plugin

这是一个 v0.1 release candidate 阶段的 CLIProxyAPI（CPA）原生插件，用于监控 Codex 额度并发送低噪声告警。

本项目不是 OpenAI 官方项目，也不是 CLIProxyAPI 官方项目。

## 状态

- 阶段：v0.1 release candidate
- 许可证：MIT
- 语言：Go
- 运行时依赖：Go 标准库与 `gopkg.in/yaml.v3 v3.0.1`
- 插件目标：CLIProxyAPI C ABI v1
- 最低 CPA 兼容目标：`v7.2.83`
- 追加兼容目标：`v7.2.120`
- 首个发布目标：Linux amd64 `.so` 与 SHA-256 checksum

当前已实现：严格 YAML/配置校验、类型化 Host Callback、Codex 凭据发现与额度查询、额度聚合、告警状态机、原子状态存储、插件生命周期、Linux C ABI exports、受保护 Management handlers、SMTP/Webhook 发送、公开安全示例配置和 systemd timer 资产。

当前 release Gate 尚未完成。Windows 验证、隔离 Linux 验证、Linux amd64 c-shared 构建、隔离 CPA 加载验证和 root-only SMTP 测试投递已通过。公开 GitHub CI、`v0.1.0` tag/release、VPS1 三轮 production dry-run 和回滚演练仍需完成后，才能进入生产启用。

当前 Linux amd64 `.so` 候选 SHA-256：

```text
c66bb40b9fb80b44a7494105b1a8b93f5b7631d6fde257d23a97cb658123e63a
```

## 计划行为

v0.1 插件设计为：

- 作为 CPA 原生插件在 CPA 进程内运行。
- 注册三条受保护 CPA Management 路由：
  - `POST /v0/management/cpa-quota-alert/check`
  - `GET /v0/management/cpa-quota-alert/status`
  - `POST /v0/management/cpa-quota-alert/test-notification`
- 通过 CPA Host Callback 读取启用的 Codex 凭据。
- 通过 CPA 网络能力查询 Codex 额度。
- 按可配置套餐规则换算 Plus 周额度等价值。
- 低于 `1.5` 告警，持续低额度每 24 小时提醒，达到 `1.6` 恢复。
- 单账号失败作为局部错误处理，不误判为整池失败。
- 状态文件不保存凭据、原始上游响应、账号标识、Management Key 或通知密钥。

## 重要风险

- 原生插件在 CPA 同进程运行，权限高；插件 panic 或内存安全问题可能影响 CPA 进程稳定性。
- 只安装可信源码构建物，或已校验 SHA-256 checksum 的 release 资产。
- ChatGPT `wham/usage` 是内部后端接口，响应结构可能随时变化。
- 套餐权重是运行者配置，不是官方产品事实。
- 本仓库不得包含真实凭据、真实主机地址、真实收件地址、账号标识、Management Key 或生产配置。

## 构建与验证

GNU make / POSIX shell 入口：

```sh
make verify
```

Windows PowerShell 入口：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/verify.ps1
```

当前验证会运行 `gofmt -l`、`go test ./...` 和 `go vet ./...`。Windows `go test`、`go vet` 和 `scripts/verify.ps1` 在使用专用 Go cache 时已通过；隔离 Linux 官方 `golang:1.24-bookworm` 环境中，`go test ./...`、`go test -race ./...`、`go vet ./...` 和 Linux amd64 c-shared 构建已通过。公开 GitHub Actions 尚未作为 release 证据运行。

## 配置

公开保守基线见 `examples/plugin-config.yaml`：

- 默认 `dry_run: true`
- 只包含 Plus 与 Team，均为 `7d` 窗口 `1x`
- Free 忽略
- 终态码：`token_invalidated`、`token_revoked`、`deactivated_workspace`
- 告警与恢复阈值：`1.5` / `1.6`
- systemd 每 5 分钟触发一次
- 24 小时重复提醒
- 连续 3 次失败告警
- 900 秒 stale 窗口
- Webhook 默认关闭

`examples/operator-confirmed-pro20.yaml` 只是 operator 本地假设示例，不是 OpenAI 官方事实。其中 K12 配置为 `5h` 窗口 `0.2x`，模糊 `pro` 配置为 `7d` 窗口 `20x`，并且不提供默认 Pro5 映射。

通知密钥只通过插件 YAML 中的环境变量名间接引用。`deploy/systemd/plugin.env.example` 是示例；复制到 `/etc/cpa-quota-alert-plugin/plugin.env` 这类不入 Git 的 root-only 路径后，设置 owner `root:root`、mode `0600`。本地 root-only 文件中的 `CPA_QUOTA_ALERT_SMTP_PASSWORD` 值就是实际 SMTP 密码；仓库示例值必须保持虚构。将 `deploy/systemd/cpa-service-plugin-env.conf.example` 安装为实际 CPA systemd service 的 drop-in，具体服务名取决于 CPA 的安装方式。该 drop-in 会注入 env 文件，并让 systemd 为实际 CPA service 身份创建 mode `0700` 的 `/var/lib/cpa-quota-alert-plugin`。修改通知值后，需要 `systemctl daemon-reload` 并受控重启 CPA，让同进程插件注册时拿到新环境。oneshot timer 仍不读取 `plugin.env`。

CPA Management Key 不得进入 `ExecStart`、CPA 插件 YAML、环境变量、README 命令行、日志或插件 state。它只能放在从 `deploy/systemd/curl.conf.example` 复制出来的 root-owned `0600` curl config 中。

## systemd 安装

确认 CPA 已能加载插件后，在 Linux 主机安装资产：

```sh
sudo install -d -o root -g root -m 0750 /etc/cpa-quota-alert-plugin
sudo install -o root -g root -m 0600 deploy/systemd/curl.conf.example /etc/cpa-quota-alert-plugin/curl.conf
sudo install -o root -g root -m 0600 deploy/systemd/plugin.env.example /etc/cpa-quota-alert-plugin/plugin.env
sudo install -o root -g root -m 0644 deploy/systemd/cpa-quota-alert-plugin.service /etc/systemd/system/cpa-quota-alert-plugin.service
sudo install -o root -g root -m 0644 deploy/systemd/cpa-quota-alert-plugin.timer /etc/systemd/system/cpa-quota-alert-plugin.timer
```

启用 timer 前，先编辑 `/etc/cpa-quota-alert-plugin/curl.conf`，替换 fake Management Key，并确认 loopback CPA Management 端口。不要把 key 写进 shell history 或 service arguments。

把 CPA service env drop-in 安装到实际 CPA service 名下：

```sh
sudo install -d -o root -g root -m 0755 /etc/systemd/system/<actual-cpa-service>.service.d
sudo install -o root -g root -m 0644 deploy/systemd/cpa-service-plugin-env.conf.example /etc/systemd/system/<actual-cpa-service>.service.d/cpa-quota-alert-plugin-env.conf
sudo systemctl daemon-reload
sudo systemctl restart <actual-cpa-service>.service
sudo systemctl enable --now cpa-quota-alert-plugin.timer
```

将 `<actual-cpa-service>` 替换为该安装环境中真实的 CPA systemd service 名；本仓库不假设固定名称。CPA restart 才会让插件注册看到 `plugin.env`。oneshot service 以 `root` 运行，仅用于读取 root-owned `0600` curl config 中的 Management Key。它的命令仍然只有 `curl --config /etc/cpa-quota-alert-plugin/curl.conf`。curl config 会以 POST 和 `{}` 调用 loopback 上受保护的 Management endpoint，并承载虚构的 `X-Management-Key` 示例值；真实值只替换在 root-only 本地副本里。

CPA restart 后，先验证 `/var/lib/cpa-quota-alert-plugin` 存在、owner 是实际 CPA service 身份、mode 是 `0700`，并且实际 CPA service 对该目录有写权限，再进入 VPS2 或 VPS1 dry-run。插件 state 不得 world-readable。若宿主 systemd 版本或部署策略不支持 `StateDirectory`，手工创建 `/var/lib/cpa-quota-alert-plugin`，赋给实际 CPA 用户和组，并保持 mode `0700`。

unit 保留 `UMask=0077`、`NoNewPrivileges=true` 和不阻断 loopback 网络、可读取 curl config 的 systemd hardening。它不是常驻服务。VPS1 只放插件和 timer 资产，不为此插件额外起常驻服务。

## 上线顺序

1. dry-run 前先验证 state 目录的 owner、mode 和写权限。
2. 以已通过的隔离 CPA 行为作为进入 VPS1 的最低门槛。
3. 公开 release 资产和 checksum 可用后，再把插件放到 VPS1。
4. VPS1 连续三轮 production dry-run，并与旧监控结果对齐。
5. 启用真实通知前先演练回滚。
6. dry-run 和回滚演练稳定后再调用 `test-notification`。
7. operator 确认测试通知后，才启用真实通知发送。

## 回滚

先停用 timer，再在 CPA 配置中禁用插件。如热加载无法干净移除，恢复旧 CPA 配置并受控重启 CPA。回滚不删除、不禁用、不刷新任何 CPA auth，也不升级或降级 CPA。

## 第三方引用

YAML 运行时依赖与协议参考见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

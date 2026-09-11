# CPA Quota Alert Plugin

这是一个 CLIProxyAPI（CPA）原生插件，用于监控 Codex 额度并发送低噪声告警。

本项目不是 OpenAI 官方项目，也不是 CLIProxyAPI 官方项目。

## 状态

- 版本：`0.1.2`
- 许可证：MIT
- 语言：Go
- 运行时依赖：Go 标准库与 `gopkg.in/yaml.v3 v3.0.1`
- 插件目标：CLIProxyAPI C ABI v1
- 兼容：`v7.2.83`、`v7.2.120`、`v7.2.157`
- 发布资产：GitHub Release 上的 Linux amd64 `.so` 与 SHA-256 checksum

插件会在 CPA 内注册、检查 Codex 额度，并可通过 SMTP 或 Webhook 发信。邮件正文是公开安全模板，只含汇总数字。收件人和 SMTP 密钥只放本机 root-only env，不进本仓库。安装前请核对 GitHub Release 里的 checksum。

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

当前验证会运行 `gofmt -l`、`go test ./...` 和 `go vet ./...`。Windows `go test`、`go vet` 和 `scripts/verify.ps1` 在使用专用 Go cache 时已通过。GitHub Actions 的 CI 与 Release 会构建并发布 Linux amd64 `.so`。

## 配置

公开保守基线见 `examples/plugin-config.yaml`：

- 默认 `dry_run: true`。只有 SMTP env 和测试邮件都通了，才把 `dry_run` 改成 `false`
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

### SMTP 和收件人

通知密钥只通过插件 YAML 里的环境变量名间接引用。把 `deploy/systemd/plugin.env.example` 复制到 `/etc/cpa-quota-alert-plugin/plugin.env` 这类不入 Git 的 root-only 路径，设置 owner `root:root`、mode `0600`。真实 SMTP 用户、密码、发件人和收件人只写这个文件，仓库示例必须保持虚构。

把 `deploy/systemd/cpa-service-plugin-env.conf.example` 安装成实际 CPA systemd service 的 drop-in。改完通知值后要 `systemctl daemon-reload` 并受控重启 CPA。oneshot timer 不读 `plugin.env`。

CPA Management Key 不得进入 `ExecStart`、CPA 插件 YAML、环境变量、README 命令行、日志或插件 state。它只能放在从 `deploy/systemd/curl.conf.example` 复制出来的 root-owned `0600` curl config 中。

### 邮件模板

默认邮件是英文，只含汇总字段：剩余 Plus 周等价值、阈值、partial / unresolved 计数、稳定错误码、未识别套餐名。不含账号邮箱、token、Management Key 或主机名。

不写 `mail.templates` 就用内置英文。要改主题或正文，从 `examples/mail-templates.example.yaml` 拷对应块到 CPA 插件 YAML：

```yaml
mail:
  templates:
    low:
      subject: "[CPA额度] 剩余 {{total}}"
      body: |
        当前加权额度 {{total}}，已低于阈值 {{low_threshold}}。
        恢复阈值 {{recovery_threshold}}。
        发生时间（UTC）：{{occurred_at}}
```

支持的事件：`low`、`low_reminder`、`recovery`、`data_error`、`plan_changed`、`test_notification`。

可用占位符：`{{kind}}`、`{{total}}`、`{{low_threshold}}`、`{{recovery_threshold}}`、`{{consecutive_failures}}`、`{{error_code}}`、`{{unknown_plans}}`、`{{partial}}`、`{{unresolved_count}}`、`{{occurred_at}}`。

未知事件名或占位符会在加载配置时被拒绝。主题必须是单行。不要把真实邮箱写进 YAML；收件人只放 `plugin.env`。

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

CPA restart 后，先验证 `/var/lib/cpa-quota-alert-plugin` 存在、owner 是实际 CPA service 身份、mode 是 `0700`，并且实际 CPA service 对该目录有写权限，再启用 timer。插件 state 不得 world-readable。若宿主 systemd 版本或部署策略不支持 `StateDirectory`，手工创建 `/var/lib/cpa-quota-alert-plugin`，赋给实际 CPA 用户和组，并保持 mode `0700`。

unit 保留 `UMask=0077`、`NoNewPrivileges=true` 和不阻断 loopback 网络、可读取 curl config 的 systemd hardening。它不是常驻服务。CPA 主机只放插件和 timer 资产，不要再额外起一套常驻额度告警服务。

## 上线顺序

1. 先验证 state 目录的 owner、mode 和写权限。
2. 用 `dry_run: true` 加载插件，确认 CPA 日志出现 `plugin registered`。
3. 跑几轮 timer 或 Management `check`，和现有额度视图对齐。
4. 填好 root-only `plugin.env` 并重启 CPA。
5. 在 `dry_run: false` 下调用 `test-notification`，确认能收到通用测试邮件。
6. 把插件 YAML 里的 `dry_run` 改成 `false`，再启用 timer。
7. 如果还有另一套额度发信服务，先停掉，避免重复邮件。

## 回滚

先停用 timer，再在 CPA 配置中禁用插件。如热加载无法干净移除，恢复旧 CPA 配置并受控重启 CPA。回滚不删除、不禁用、不刷新任何 CPA auth，也不升级或降级 CPA。

## 第三方引用

YAML 运行时依赖与协议参考见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

# CDT Monitor

CDT Monitor 是一个用于阿里云 CDT 流量监控、ECS 实例自动化控制和费用观察的轻量级单机控制台。HTTP 服务、定时调度器、并发 Worker、SQLite、API 和前端静态资源全部集成在一个 Go 进程中，适合通过 Docker 或独立二进制快速部署。

> 目标平台：Linux / Windows / macOS，amd64 / arm64
> 数据库：SQLite WAL，不需要 MySQL 或 Redis

## 功能

- 多阿里云账号、地域和 ECS 实例集中管理。
- CDT 国内/海外流量分类聚合与用量历史图表。
- 可配置流量阈值，支持“停机并通知”和“仅通知”。
- 普通停机 `KeepCharging` 和节省停机 `StopCharging`。
- 每日定时开关机，任务延迟或进程重启后提供短窗口补偿。
- 多台 ECS 按固定时段轮换，自动更新 Cloudflare 灰云 A 记录；达到全局流量告警阈值立即节省停机，并由下一台未超限机器接班。
- 抢占式实例保活，阈值熔断优先于保活。
- 保活启用时仍可手动关机；关机后自动暂停该实例的保活，到下一次定时开机时恢复。
- Web 控制台手动开机、关机和刷新。
- SMTP、Telegram Bot、自定义 Webhook 通知；Telegram 支持自定义反代和 SOCKS5。
- 中国站和国际站 BSS 端点、CNY/USD 账单与余额缓存。
- 首次安装向导、管理员 Session、登录限速、CSRF 防护。
- AES-GCM 加密 AccessKey Secret 与通知凭据，管理员密码使用 Argon2id。
- 带 scope、可撤销的 API Key，供 Android、iOS 和桌面小组件使用。
- 自动清理日志/统计，健康检查和 JSON 容器日志。
- 原 PHP SQLite 数据库可原地升级；启动前请先备份 `data/data.sqlite`。

## 架构

```text
浏览器 / 小组件
       │
       ▼
Go HTTP API ──► SQLite WAL 状态快照
       │
       ├──► 内置调度器 ──► 持久化 Job ──► Worker Pool ──► 阿里云 RPC
       │                                      │
       └──► 管理操作                           └──► Notification Outbox
                                                        │
                                             SMTP / Telegram / Webhook
```

状态查询只读取 SQLite 快照，不在页面请求内等待阿里云 API。刷新和控制返回 `202 + job_id`，由后台 Worker 执行。调度租约和动作事件键用于防止任务重叠、重复开关机和重复阈值通知。

更完整的现状与技术决策见 [架构分析](docs/architecture-analysis.md)。

## Docker 安装

### Docker Compose

```yaml
services:
  cdt-monitor:
    image: ghcr.io/p0me1oo/cdt-monitor:latest
    container_name: cdt-monitor
    restart: unless-stopped
    init: true
    ports:
      - "43210:8080"
    environment:
      CDT_DATA_DIR: /data
      CDT_LISTEN: :8080
      CDT_WORKERS: 4
      TZ: Asia/Shanghai
    volumes:
      - cdt-data:/data

volumes:
  cdt-data:
```

启动：

```bash
docker compose up -d
docker compose logs -f cdt-monitor
```

浏览器访问 `http://服务器地址:43210`，首次访问会进入安全安装向导。

### Docker CLI

```bash
docker run -d \
  --name cdt-monitor \
  --restart unless-stopped \
  -p 43210:8080 \
  -v cdt-data:/data \
  ghcr.io/p0me1oo/cdt-monitor:latest
```

运行镜像使用非 root 用户和 `scratch` 基础层，不包含 Nginx、PHP-FPM、Cron、shell 或包管理器。HTTPS 请求所需的 CA 证书会单独复制进镜像。

## 二进制安装

从 GitHub Releases 下载对应平台压缩包，解压后运行：

```bash
./cdt-monitor serve --data ./data --listen :8080
```

Windows：

```powershell
.\cdt-monitor.exe serve --data .\data --listen :8080
```

支持的命令：

| 命令 | 用途 |
| --- | --- |
| `serve` | 启动 Web、调度器和 Worker，默认命令 |
| `run-once` | 创建一轮监控任务并等待执行结束 |
| `migrate` | 执行数据库迁移并退出 |
| `version` | 输出版本、Commit 和平台 |
| `healthcheck` | 请求本机 `/healthz`，供容器健康检查使用 |

环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CDT_DATA_DIR` | `./data` | SQLite 和 `master.key` 所在目录 |
| `CDT_LISTEN` | `:8080` | HTTP 监听地址 |
| `CDT_WORKERS` | `4` | 后台任务并发数 |
| `TZ` | 系统时区 | 容器环境时区；业务时区以控制台设置为准 |

## 轮换运行（v2.2.0）

CDT Monitor 部署在独立、持续运行的服务器上。在控制台打开“轮换运行”，填写完整域名、Cloudflare API Token，添加至少两台已配置的机器，并设置每天的开始、结束时间。机器数量不限制为两台；“平均分配时段”可生成初始时间表，再按需要调整。

- 时间使用系统设置中的业务时区，必须连续覆盖 24 小时，不能有重叠或空档。同一机器只能出现一次。结束时间早于开始时间表示跨午夜，午夜填写 `00:00`。
- Token 需要目标 Zone 的 `Zone Read` 和 `DNS Edit` 权限。Token 加密保存，接口不返回原文；编辑时留空保留原 Token。程序自动查找 Zone，不需要手填 Zone ID。
- 当前支持公网 IPv4 和灰云 A 记录。无 A 记录时自动创建；已有唯一 A 记录时更新 IP，TTL 设为 60 秒。多条 A、AAAA、CNAME、NS 或橙云记录会阻止启用，避免部分访问绕过轮换。建议使用专用子域名。
- 阿里云凭据除现有查询和启停权限外，还需 `ecs:DescribeInstances`，用于读取指定实例的状态和公网 IPv4（支持 EIP）。以 `Running` 且已有公网 IPv4 为就绪条件，不检查代理端口。
- 正常轮换会在下一时段前两分钟尝试开机，到时段边界确认新机器就绪后更新 DNS。旧机器至少保留五分钟；原 DNS TTL 更长时，按原 TTL 等待后执行 `StopCharging`。因此实际运行时间包含交接重叠。
- 流量限制沿用“每台配置的流量上限 × 全局告警阈值”，不另设阈值。轮换组即使全局选择“仅通知”或“普通停机”，也会在检测到超限后执行节省停机，优先于 DNS 交接等待。比如上限 200 GB、阈值 95%，达到 190 GB 时跳过该机器。
- 超限时按时间表向后寻找未超限机器，提前接班不移动原时间表；两台及以上均可连续跳过。所有机器都超限时全部节省停机并通知，保留 DNS 记录，后续查询确认流量恢复后按时间表恢复运行。流量查询失败的机器不会被自动启动。
- 轮换接管成员的原定时开关机与保活，阻止直接手动开关机。修改正在运行的轮换计划或成员信息前先停用；停用不会关机或改 DNS，原定时与保活规则恢复生效。
- 下一台启动失败或 DNS 更新失败时，保留未超限的旧机器运行并重试；已超限的机器仍立即发出节省停机指令。状态、交接期限写入数据库，监控进程重启后继续处理。通知使用现有通知渠道。

轮换任务约每 15 秒检查一次，实际间隔受 API 耗时影响；CDT 查询沿用最长约 45 秒的客户端缓存，云端统计也可能延迟。“立即停机”指检测到超限后立即发送停机请求，不代表云端流量实时断流。灰云 DNS 存在客户端缓存，现有代理连接不能迁移，不能保证切换无中断。

轮换只管理所选 ECS 和域名，不同步机器上的服务配置。各台机器需事先部署好相同代理入口并设置开机自启动。提前接班、交接重叠和失败重试可能增加运行时间，本功能不判断账单抹零资格。

接口：`GET /api/v1/rotation` 读取配置与状态；`PUT /api/v1/rotation` 保存或启停计划，均要求管理员登录和现有写请求 CSRF 校验。

实现依据：[Cloudflare DNS 更新接口](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/edit/)、[阿里云 DescribeInstances](https://www.alibabacloud.com/help/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstances)。

## 从 PHP 版本升级

1. 停止旧容器或 PHP Cron，避免两个版本同时执行自动化任务。
2. 完整备份旧 `data/`，尤其是 `data/data.sqlite`。
3. 将同一数据目录挂载到新版 `/data`。
4. 启动新版。程序会补齐 schema，并自动把旧明文 AccessKey Secret、SMTP 密码、Telegram Token 和 Webhook Headers 加密。
5. 使用原管理员密码登录；首次成功登录会把旧明文管理员密码升级为 Argon2id。
6. 检查实例、历史流量、账单、定时计划和通知配置。
7. 创建具有 `cron:run` scope 的专用 API Key，再替换旧的 URL Cron。

新版不会重排账号或日志自增 ID。账号主键保持稳定，历史统计不会因为保存配置而重新绑定。

> `data/master.key` 是解密凭据的必要文件，必须与 SQLite 一起备份。丢失该文件后无法恢复已加密 Secret。

## API Key 与小组件 API

在“设置 → API Key”中创建 Key。Token 只显示一次，数据库只保存 SHA-256 哈希。

权限：

| Scope | 能力 |
| --- | --- |
| `widget:read` | 读取小组件摘要、状态与历史 |
| `instance:control` | 手动刷新、开机和关机 |
| `cron:run` | 调用兼容 URL Cron |

请求示例：

```bash
curl -H "Authorization: Bearer cdt_xxx" \
  http://127.0.0.1:8080/api/v1/widget/summary
```

```bash
curl -X POST \
  -H "Authorization: Bearer cdt_xxx" \
  http://127.0.0.1:8080/api/v1/accounts/1/actions/start
```

主要接口：

```text
GET  /api/v1/widget/summary
GET  /api/v1/status
GET  /api/v1/accounts/{id}/history
POST /api/v1/accounts/{id}/refresh
POST /api/v1/accounts/{id}/actions/start
POST /api/v1/accounts/{id}/actions/stop
GET  /api/v1/jobs/{job_id}
GET  /healthz
GET  /readyz
```

兼容 URL Cron：

```text
GET /monitor.php?key=<具有 cron:run scope 的 API Key>
```

不再支持把管理员密码放在 URL 中。

### Android 桌面小组件

仓库中的 [`android-widget`](android-widget/README.md) 是基于 `/api/v1/widget/summary` 的 Android 小组件原型，源码暂时随主项目保存，供后续研究和完善。当前不作为正式稳定客户端发布。

## 相关链接

- 项目主页：[GitHub](https://github.com/P0me1oo/CDT-Monitor)
- 技术社区：[NodeSeek](https://www.nodeseek.com/) · [Linux.do](https://linux.do/)

## 致谢

本项目 fork 自 [wang4386/CDT-Monitor](https://github.com/wang4386/CDT-Monitor)，原作者为 青柠（[qninq.cn](https://qninq.cn)）。原项目采用 MIT 协议，版权声明保留在 [LICENSE](LICENSE) 中。

## 本地开发

要求 Go 1.24 和 Node.js 22：

```bash
cd web
npm ci
npm run build
cd ..
go test ./...
go run ./cmd/cdt-monitor serve --data ./tmp-data
```

开发前端时可以使用 `npm run dev`，API 请求需要由 Vite 代理或使用同源构建产物测试。

生产构建：

```bash
cd web && npm ci && npm run build && cd ..
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cdt-monitor ./cmd/cdt-monitor
```

## GitHub Actions

| Workflow | 触发条件 | 输出 |
| --- | --- | --- |
| `CI` | `dev`/`main` push、PR | 前端构建、Go race test、vet、Linux build |
| `Automatic Release` | `main` push 或手动 | 优先使用高于现有 tag 的 `web/package.json` 版本，否则递增 patch，创建 tag 并调用正式发布流程 |
| `Release Binaries` | 自动发布、`v*.*.*` tag 或手动 | Linux/Windows/macOS 二进制、SHA-256 校验和及 GitHub Release |
| `Container Images` | `dev`/`main`/tag | 发布 GHCR 的 linux/amd64、linux/arm64 镜像 |
| `Android Widget` | 仅手动触发 | Android 原型 APK/AAB workflow artifact |

镜像标签：

- `dev` 分支：`ghcr.io/p0me1oo/cdt-monitor:dev`
- `main` 分支：`ghcr.io/p0me1oo/cdt-monitor:latest`
- Release：发布 `v2.x.x`、`2.x.x` 与 `2.x` 标签

## 安全建议

- 公网部署必须在反向代理或网关层启用 HTTPS。
- 阿里云 RAM 用户只授予 CDT 查询、ECS 查询/启停和可选 BSS 查询所需的最小权限。
- 小组件只使用 `widget:read` Key；不要把 `instance:control` Key 放入公开脚本。
- 定期撤销不再使用的 Key，并备份整个数据目录。
- 不要把 `data/`、日志或 API Token 提交到 Git。

## License

[MIT](LICENSE)

# DHTSearch

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Co-authored by [Kimi K3](https://www.kimi.com/).

一个自建索引的磁力链接搜索网站：Go 后端通过 DHT 网络爬虫实时采集磁力链接元数据，自动过滤成人内容与垃圾信息，Next.js 前端提供干净的搜索界面。

## 架构

```
┌────────────┐   DHT 网络    ┌──────────────────────────────┐
│ DHT 爬虫    │◄────────────►│ 公共 BitTorrent DHT 节点       │
│ (infohash)  │              └──────────────────────────────┘
└─────┬──────┘
      ▼
┌────────────┐   BEP-9      ┌──────────────────┐   丢弃    ┌──────────────┐
│ 元数据获取  │────────────►│ 过滤引擎          │─────────►│ 成人/垃圾/过小 │
│ (workers)  │             │ (关键词+启发式+体积)│           └──────────────┘
└─────┬──────┘             └────────┬─────────┘
      ▼                             ▼ 通过
┌────────────┐   REST API   ┌─────────────┐
│   SQLite   │◄────────────►│  Next.js 前端│
└─────┬──────┘              └─────────────┘
      │ 每小时增量复审
      ▼
┌──────────────────┐   命中    ┌────────────────────┐
│ LLM 审核          │─────────►│ 删除 + 写入 blocked │
│ (DeepSeek V4 Flash)│          └────────────────────┘
└──────────────────┘
```

- `server/` — Go 后端：DHT 爬虫（anacrolix/dht/v2）、BEP-9 元数据获取（anacrolix/torrent）、过滤引擎（中英文成人词表 + 垃圾启发式）、SQLite 存储（modernc.org/sqlite，无 cgo）、REST API（标准库 net/http）
- `web/` — Next.js 前端（App Router + Tailwind，服务端渲染搜索结果）

## 快速开始

### 后端

```bash
cp env.example .env           # 填入 OPENAI_API_KEY（.env 已 gitignore）

cd server

# 完整模式：启动 DHT 爬虫（需要 UDP 出站，索引随时间增长）
go run ./cmd/server

# 演示模式：不爬 DHT，插入演示数据，便于本地验证 API/前端
CRAWL_ENABLED=false go run ./cmd/server --seed-demo
```

默认监听 `:8080`。配置项（环境变量或同名 flag）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DB_PATH` | `./dhtsearch.db` | SQLite 路径 |
| `CRAWL_ENABLED` | `true` | 是否启动 DHT 爬虫 |
| `DHT_PORT` | `0`（随机） | DHT UDP 端口 |
| `META_WORKERS` | `16` | 元数据并发 worker 数 |
| `META_TIMEOUT` | `45s` | 单个元数据获取超时 |
| `FETCH_METADATA` | `true` | 是否获取元数据（false 时只收 infohash） |
| `MIN_TORRENT_SIZE` | `104857600`（100 MiB） | 低于此总体积的种子不入库 |
| `ENV_FILE` | `.env` | .env 文件路径（相对工作目录） |

LLM 审核相关（见下文「LLM 二次审核」）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `OPENAI_API_KEY` | 无 | **必填**，未设置则审核不启动 |
| `OPENAI_BASE_URL` | `https://api.deepseek.com/v1` | 任意 OpenAI 兼容端点 |
| `OPENAI_MODEL` | `deepseek-v4-flash` | 审核模型 |
| `MODERATION_ENABLED` | `true` | 总开关 |
| `MODERATION_INTERVAL` | `1h` | 审核周期 |
| `MODERATION_BATCH_SIZE` | `50` | 每次请求送审的标题数 |
| `MODERATION_MAX_BATCHES` | `40` | 每轮最多几批（0 = 不限），用于封顶开销 |
| `MODERATION_DRY_RUN` | `false` | 只打日志不删除 |

### 前端

```bash
cd web
cp .env.example .env.local   # NEXT_PUBLIC_API_BASE=http://localhost:8080
npm install
npm run dev                  # 开发
# 或 npm run build && npm run start   # 生产
```

## API

- `GET /api/search?q=xx&page=1&page_size=20` — 搜索（q 为空返回最新收录），结果含拼好的 magnet 链接
- `GET /api/stats` — 收录数、成人/垃圾/体积过滤计数、LLM 审核统计、爬虫状态
- `GET /api/healthz` — 健康检查

## 过滤策略

### 第一道：入库前的静态过滤

- **成人内容**：内置 230+ 中英文关键词（短词用词边界正则防误伤，如 Avatar/Avengers 不会被 "av" 误拦），对资源名和文件名匹配；JAV 番号等弱信号需叠加其他信号才判定
- **垃圾信息**：SEO 关键词堆叠、纯符号/超长名称、零大小或异常大小、超大文件数、随机字符串文件名占比等启发式规则
- **体积下限**：总体积小于 `MIN_TORRENT_SIZE`（默认 100 MiB）的种子直接丢弃，滤掉假种、单图、纯链接/说明文件等垃圾

命中任一即丢弃并计入统计（`adult_filtered` / `spam_filtered` / `size_filtered`）。规则见 `server/internal/filter/`。

### 第二道：LLM 二次审核（每小时）

静态词表只能拦住明写关键词的内容。后台每小时调用一次 OpenAI 兼容 API，对**尚未审核过**的库内记录做批量复审，判定为成人或垃圾的直接删除。

- **增量**：每行有 `reviewed_at` 标记，只为没审过的行付费，不会每小时重扫全库
- **不会复活**：删除的 infohash 写入 `blocked` 表，爬虫与增量同步都不会再把它加回来
- **失败安全**：API 报错时该批不标记已审，下轮重试；模型返回无法解析、标签未知或漏项一律按 `ok` 处理，绝不因不确定而删除
- **开销可控**：`MODERATION_MAX_BATCHES × MODERATION_BATCH_SIZE` 即每小时上限（默认 2000 条／小时）
- **提示词**：明确要求「盗版本身不算垃圾」，否则模型会把整个库都判成垃圾；见 `server/internal/moderator/moderator.go` 的 `systemPrompt`

首次开启建议先跑一轮 dry-run 看日志，确认判定符合预期再放开删除：

```bash
MODERATION_DRY_RUN=true go run ./cmd/server
```

审核统计通过 `GET /api/stats` 的 `moderation` 字段暴露（`reviewed` / `adult_removed` / `spam_removed` / `errors` / `pending` / `blocked`）。

## 部署

`deploy.sh` 一键构建并部署到远程服务器（不包含任何主机/域名/凭证，全部走环境变量）：

```bash
DEPLOY_HOST=user@example.com ./deploy.sh
```

可选变量：`DEPLOY_DIR`（默认 `/opt/dhtsearch`）、`API_PORT`（默认 `8081`）、`GOARCH`（默认 `amd64`）。服务器上需已配置好 `dhtsearch-api` / `dhtsearch-web` systemd 服务和反向代理。

> `deploy.sh` **不会上传 `.env`**（密钥不进部署管道）。服务器上的 API key 需自行放置一次：把 `.env` 放到 `DEPLOY_DIR`（systemd 的 `WorkingDirectory`），或用 systemd 的 `EnvironmentFile=` 指向一个 root-only 的文件。缺少 key 时服务照常运行，只是 LLM 审核不启动。

## 本地爬虫 + 增量同步（可选）

除了在服务器上爬，还可以在本地跑第二个爬虫实例，定时把增量索引合并到服务器：

```bash
echo 'DEPLOY_HOST=user@example.com' > .deploy.env   # gitignored
./scripts/sync-to-remote.sh                          # 手动同步一次
```

原理：本地实例把数据写到 `local.db`，脚本按水位线（`last_sync_ts`）导出新增行到小 delta 文件，scp 到服务器后 `INSERT OR IGNORE` 合并（按 info_hash 去重），只传输增量。

macOS 上可用 launchd 常驻/定时（安装后本地爬虫 API 在 `127.0.0.1:8089`，同步每 30 分钟一次）：

```bash
./scripts/launchd/install.sh   # 构建二进制、安装脚本、加载两个 agent
```

拉取新代码后重新执行同一条命令即可更新（会重新编译并 reload agent）。

运行时文件（二进制、数据库、日志、脚本副本）装在 checkout **之外**：

| 路径 | 内容 |
| --- | --- |
| `~/Library/Application Support/dhtsearch/` | `bin/`、`local.db`、`last_sync_ts`、`.deploy.env`（可用 `DHTSEARCH_STATE_DIR` 覆盖） |
| `~/Library/Logs/dhtsearch/` | `crawler.log`、`sync.log` |

> 这不是洁癖：launchd agent 对非系统卷（`/Volumes/...`）没有 TCC 权限，仓库放在外置盘时 agent **读写都会失败**——spawn 直接以 `EX_CONFIG (78)` 退出，或每次文件操作报 `Operation not permitted`。`$HOME` 下的路径不需要任何授权。

## 注意

- DHT 爬虫从零开始积累索引需要时间（数小时到数天），建议长期运行
- 请遵守所在地区的法律法规，仅用于合法用途

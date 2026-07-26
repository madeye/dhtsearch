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
┌────────────┐   BEP-9      ┌─────────────┐   丢弃    ┌──────────────┐
│ 元数据获取  │────────────►│ 过滤引擎     │─────────►│ 成人/垃圾内容 │
│ (workers)  │             │ (关键词+启发式)│           └──────────────┘
└─────┬──────┘             └──────┬──────┘
      ▼                           ▼ 通过
┌────────────┐   REST API   ┌─────────────┐
│   SQLite   │◄────────────►│  Next.js 前端│
└────────────┘              └─────────────┘
```

- `server/` — Go 后端：DHT 爬虫（anacrolix/dht/v2）、BEP-9 元数据获取（anacrolix/torrent）、过滤引擎（中英文成人词表 + 垃圾启发式）、SQLite 存储（modernc.org/sqlite，无 cgo）、REST API（标准库 net/http）
- `web/` — Next.js 前端（App Router + Tailwind，服务端渲染搜索结果）

## 快速开始

### 后端

```bash
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
- `GET /api/stats` — 收录数、成人/垃圾过滤计数、爬虫状态
- `GET /api/healthz` — 健康检查

## 过滤策略

- **成人内容**：内置 230+ 中英文关键词（短词用词边界正则防误伤，如 Avatar/Avengers 不会被 "av" 误拦），对资源名和文件名匹配；JAV 番号等弱信号需叠加其他信号才判定
- **垃圾信息**：SEO 关键词堆叠、纯符号/超长名称、零大小或异常大小、超大文件数、随机字符串文件名占比等启发式规则

命中任一即丢弃并计入统计。规则见 `server/internal/filter/`。

## 部署

`deploy.sh` 一键构建并部署到远程服务器（不包含任何主机/域名/凭证，全部走环境变量）：

```bash
DEPLOY_HOST=user@example.com ./deploy.sh
```

可选变量：`DEPLOY_DIR`（默认 `/opt/dhtsearch`）、`API_PORT`（默认 `8081`）、`GOARCH`（默认 `amd64`）。服务器上需已配置好 `dhtsearch-api` / `dhtsearch-web` systemd 服务和反向代理。

## 本地爬虫 + 增量同步（可选）

除了在服务器上爬，还可以在本地跑第二个爬虫实例，定时把增量索引合并到服务器：

```bash
echo 'DEPLOY_HOST=user@example.com' > .deploy.env   # gitignored
./scripts/sync-to-remote.sh                          # 手动同步一次
```

原理：本地实例把数据写到 `data/local.db`，脚本按水位线（`data/last_sync_ts`）导出新增行到小 delta 文件，scp 到服务器后 `INSERT OR IGNORE` 合并（按 info_hash 去重），只传输增量。

macOS 上可用 launchd 常驻/定时（安装后本地爬虫 API 在 `127.0.0.1:8089`，同步每 30 分钟一次）：

```bash
./scripts/launchd/install.sh   # 生成并加载 ~/Library/LaunchAgents/com.dhtsearch.{crawler,sync}.plist
```

## 注意

- DHT 爬虫从零开始积累索引需要时间（数小时到数天），建议长期运行
- 请遵守所在地区的法律法规，仅用于合法用途

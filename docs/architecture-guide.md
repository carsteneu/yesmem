# yesmem 架构设计指南

> 本文档基于对 721 个 Go 源文件的全量精读生成。覆盖 32 个 `internal/` 子包,含 `internal/daemon`(119 文件)、`internal/proxy`(162 文件)、`internal/storage`(85 文件)、`internal/embedding`(19 文件)、`internal/ivf`(4 文件)。
>
> 所有 `file:line` 引用均经结构校验。三处子代理报告中标注为"推测"或与我直接校验冲突的事实已更正(见文末 §16 勘误)。

---

## 目录

1. [TL;DR — yesmem 是什么](#1-tldr--yesmem-是什么)
2. [系统拓扑:三进程模型](#2-系统拓扑三进程模型)
3. [启动时序与单例锁](#3-启动时序与单例锁)
4. [数据模型:三 SQLite 库](#4-数据模型三-sqlite-库)
5. [向量与嵌入:自研 IVF + 捆绑模型](#5-向量与嵌入自研-ivf--捆绑模型)
6. [混合检索:RRF 融合三路召回](#6-混合检索rrf-融合三路召回)
7. [Daemon 内部:单写者 + 123 RPC](#7-daemon-内部单写者--123-rpc)
8. [Proxy 内部:折叠引擎 + 缓存门控](#8-proxy-内部折叠引擎--缓存门控)
9. [Hooks:8 个注入点的提取管线](#9-hooks8-个注入点的提取管线)
10. [提取与索引:OpenAI 客户端 + 消毒](#10-提取与索引openai-客户端--消毒)
11. [Caps 系统:两套来源 + ai-jail 沙箱](#11-caps-系统两套来源--ai-jail-沙箱)
12. [Skills 系统:嵌入即安装](#12-skills-系统嵌入即安装)
13. [高阶智能:Briefing / Wiki / Clustering](#13-高阶智能briefing--wiki--clustering)
14. [代码智能:graph / codescan / wikirender](#14-代码智能graph--codescan--wikirender)
15. [并发模型与可扩展点](#15-并发模型与可扩展点)
16. [勘误与未决项](#16-勘误与未决项)

---

## 1. TL;DR — yesmem 是什么

yesmem 是一个**给 Claude Code / OpenCode 用的持久记忆中间层**,形态是一个本地常驻服务 + 一个 Anthropic/OpenAI API 兼容的反向代理。

它的核心价值不是"存对话",而是三件事:

1. **记忆外化** — 把跨会话的 learnings(教训、决策、模式、偏好)存进 SQLite,用 BM25 + 向量 + anticipated-query 三路 RRF 融合检索,在需要时通过 system-reminder 注入。
2. **上下文折叠** — proxy 拦截到上游 LLM 的请求,当上下文膨胀时**本地、不调用 LLM**地把旧消息压缩成 stub(占位符),并缩减上报的 `input_tokens`,让折叠对客户端不可见。
3. **可执行能力(Caps)** — 把可复用的工具定义(JS/Bash 脚本)存成"cap",在 `ai-jail` 沙箱里执行,失败时还能用 LLM 自动纠错并提请人工 review。

**一句话**:yesmem = (daemon: 单写者记忆库 + 调度器 + cap 执行器) + (proxy: 透明折叠代理) + (hooks: 8 注入点提取管线)。

---

## 2. 系统拓扑:三进程模型

```
                        ┌─────────────────────────────────────────┐
                        │            上游 LLM API                  │
                        │   (Anthropic /v1/messages, OpenAI, …)    │
                        └────────────────▲────────────────────────┘
                                         │ SSE 流式 (gzip 预解码)
                        ┌────────────────┴────────────────────────┐
                        │              PROXY 进程                  │
                        │  (proxy.Run, 162 文件)                   │
                        │                                          │
                        │  ┌─────────────────────────────────────┐ │
                        │  │ handleMessages (1045 行)            │ │
                        │  │  stub cycle → cache gate →          │ │
                        │  │  breakpoint 重排 → TTL 归一 →        │ │
                        │  │  pair 校验 → 转发+注解 → 折叠        │ │
                        │  └─────────────────────────────────────┘ │
                        │  + 15 个并发子组件(DecayTracker,        │
                        │    SignalBus, FrozenStubs, Sawtooth…)    │
                        └────────┬───────────────────────┬─────────┘
                  queryDaemon RPC│ (Unix socket, 重试)    │ 127.0.0.1:9099
                                 │                        │ (sanitizeListenAddr
                                 │                        │  强制 127.0.0.1)
                        ┌────────▼───────────────────────▼─────────┐
                        │            DAEMON 进程(单写者)           │
                        │  (daemon.Run, 119 文件)                   │
                        │                                          │
                        │  Unix socket daemon.sock (always on)      │
                        │  + 可选 HTTP API 127.0.0.1:9377           │
                        │  Handler.Handle → switch 123 RPC methods │
                        │  + 后台 goroutine 集群:                   │
                        │    调度器(1s) / 心跳(1s) / opencode 扫描  │
                        │    (60s) / cap 监视(30s) / wiki(5min) /   │
                        │    staleness / briefing(2h) / 聚类(30min) │
                        │    / 复现检测(2h) / 蒸馏(2h) / 自更新       │
                        └────────┬──────────────────────────────────┘
                                 │ 唯一 RW 句柄
                        ┌────────▼──────────────────────────────────┐
                        │  ~/.claude/yesmem/                         │
                        │   yesmem.db   (learnings/sessions/FTS…主)  │
                        │   runtime.db  (proxy_state/pinned 临时态)  │
                        │   messages.db (高频 hook 写,隔离争用)      │
                        │   yesmem.ivf  (质心+聚类ID,向量在 SQLite)   │
                        │   daemon.sock / daemon.pid / auth_token    │
                        └───────────────────────────────────────────┘

  ┌──────────────┐  NDJSON stdin   ┌──────────────┐
  │  HOOK 子进程  │ ◀────────────── │  Claude Code  │  (每个生命周期事件
  │  (yesmem hook-│  8 个注入点     │  / OpenCode   │   spawn 一次 yesmem
  │   check/guard/│                 │              │   CLI 子命令)
  │   learn/…)    │ ───RPC────────▶│ DAEMON        │
  └──────────────┘                 └──────────────┘
```

### 进程清单

| 进程 | 启动入口 (`main.go`) | 生命周期 | 角色 |
|---|---|---|---|
| **daemon** | `main.go:55` `case "daemon"` → `runDaemon` | 常驻(单例) | 单写者 + RPC + 后台智能 |
| **proxy** | `main.go:112` `case "proxy"` → `proxy.Run` | 常驻 | 透明折叠代理 |
| **worker** | `main.go:178` `case "worker"` | 一次性 | headless 后台作业(NDJSON stdin) |
| **mcp** | `main.go:49` `case "mcp"` | 一次性(stdio) | MCP 服务器(stdio 传输) |
| **hook-*** | `main.go:82-92` (8 个) | 一次性 | hook 回调(每事件 spawn) |
| **其余 ~50 子命令** | `main.go:47-186` | 一次性 | CLI 工具(query/stats/backup/migrate…) |

> **关键**:只有 daemon 持有 SQLite 的 RW 句柄。proxy、hooks、CLI 全部通过 Unix socket 发 RPC 给 daemon。这是 yesmem 并发安全的核心架构决策。

---

## 3. 启动时序与单例锁

### socket-first 启动

daemon 启动顺序(`internal/daemon/daemon.go`)精心设计,**socket 在任何索引/重活之前就绪**:

```
Run(cfg)                                     daemon.go:94
 ├─ ensureSingleInstance(Replace)            lifecycle.go:16   ← 写 daemon.pid,可选杀旧进程
 ├─ OpenStore(DataDir)                       daemon.go:~200     ← 开 5 个 DB 句柄,跑 createSchema
 ├─ NewSocketServer → socketSrv.Serve()      daemon.go:236-242  ← ★ socket 先就绪
 │      log: "Socket ready for MCP connections"
 ├─ 新建 embedding Provider (bundled SSE)    daemon.go:~250
 ├─ ivf.Load(yesmem.ivf)  (<50k 走暴力)      daemon.go:632-667
 └─ spawn 后台 goroutine 集群                 daemon.go:~280+
      ├─ 调度器 tick (1s)                     daemon.go:303
      ├─ agent heartbeat (1s)                daemon.go:352
      ├─ opencode 扫描 (60s)                 daemon.go:213
      ├─ caps 目录监视 (30s)                 daemon.go:255
      ├─ wiki 渲染 (5min)                    daemon.go:354
      ├─ briefing 再生 (2h 或 ≥5 待处理)     daemon.go:944
      ├─ staleness / 聚类(30min)/ 复现(2h) / 蒸馏(2h)
      └─ fsnotify 监视(event-driven)         daemon.go:423
 └─ signal.Notify(SIGINT, SIGTERM) 阻塞       daemon.go:1177
```

**为什么 socket-first 重要**:proxy 的 `queryDaemon`(`internal/proxy/proxy.go:695`)有 15 秒重试窗口(冷启动 30 次 × 0.5s)。socket 先开 = MCP 客户端可以立即连接并排队 RPC,daemon 后台索引慢一点也不阻塞客户端启动。

### 单例锁 (`internal/daemon/lifecycle.go`)

- 读 `daemon.pid`(`:23`),用 `/proc/<pid>/cmdline` 校验进程身份(NUL→空格解析,`:167-175`)——防止 PID 复用误判。
- `--replace`(`Replace=true`):发 `SIGTERM` 杀旧进程(`:189`)。
- 无 `--replace` 且有存活实例:报错退出,提示用 `--replace`(`:194-195`)。

### 信号

- 仅 `SIGINT`/`SIGTERM` → `cancel()` ctx → `socketSrv.Close()` → `httpSrv.Shutdown()` → `store.Close()`(`daemon.go:1180-1186`)。
- **没有 `SIGHUP`/`SIGUSR1` 热重载**。重载靠 RPC(`reload_vectors`、`cap_sync`)或 `--replace` 重启。
- 日志用标准库 `log`(无结构化日志)。

---

## 4. 数据模型:三 SQLite 库

### 三库分离(`internal/storage/store.go:30-95`)

纯 Go 驱动 `modernc.org/sqlite`(**CGO_ENABLED=0**,`go.mod:42`),无 sqlite-vec / hnswlib 等原生扩展。

| 句柄 | 文件 | 角色 | 写者 |
|---|---|---|---|
| `s.db` | `yesmem.db` | 主库:learnings/sessions/agents/FTS… | daemon 独占 RW |
| `s.runtimeDB` | `runtime.db` | 临时态:`proxy_state`、`pinned_learnings` | daemon RW |
| `s.messagesDB` | `messages.db` | 高频写:hook 消息流 | daemon RW |
| `s.readDB` | `yesmem.db` (RO) | proxy 读池(driver 级只读 DSN) | — |
| `s.messagesReadDB` | `messages.db` (RO) | proxy 读池 | — |

**为什么分三库**:把高频 hook 写(messages.db)和易变临时态(runtime.db)从珍贵的 learnings(yesmem.db)里隔离出去,proxy 读 runtime 状态时不与主库 WAL 争用。读池用 `openSQLiteReadOnly`(`store.go:235`)在**驱动层**强制只读,proxy 不可能误写主库。

### 主库表(yesmem.db)

DDL 全是 **Go 字符串常量**(`internal/storage/schema.go` 等),**全树无 `.sql` 文件**。核心表:

| 表 | DDL 位置 | 用途 |
|---|---|---|
| `sessions` | `schema.go:487` | 每会话一行(project/branch/jsonl_path/parent_session_id) |
| `messages` | `schema.go:508` | 会话消息(role/content_blob/tool_name/file_path/model);**镜像到 messages.db** |
| `learnings` | `schema.go:523` | 核心知识库(category/content/project/confidence/superseded_by/**embedding_vector BLOB**/embedding_model…) |
| `learnings_fts` | `schema.go:330` | **FTS5 虚表**:`USING fts5(content, content_rowid=id, tokenize='porter unicode61')` |
| `learnings_events` / `learnings_duplicates` | 迁移 | 去重 / supersede 日志 |
| `associations` | schema.go | learning↔session/entity 多对多边 |
| `agents` / `agent_dialogs` / `agent_messages` | `agents.go:11`、`dialogs.go:11/20` | agent 编排态 |
| `scratchpad_entries` | `scratchpad.go:6` | 共享草稿板 |
| `scheduled_jobs` / `bash_job_runs` | schema.go | cron 式调度器 |
| `doc_chunks` | schema.go | 摄入文档(BM25 + per-chunk 元数据) |
| `kv` | schema.go | 通用键值 |
| `refined_briefings` | `schema.go:723` | briefing 缓存 |
| `knowledge_gaps` | schema.go | 追踪的未知项 |

### 迁移机制 — **无 `schema_version` 表**

这是 yesmem 最反直觉的设计之一。幂等性靠两点:

1. **迁移在 `CREATE TABLE` *之前* 跑**(`schema.go:11-58`):
   ```go
   // Migrations FIRST — fix schema before CREATE TABLE IF NOT EXISTS skips
   for _, mig := range migrations {
       s.db.Exec(mig) // 忽略错误(列可能已存在)
   }
   ```
2. 每个 ALTER 必须自保护(`IF NOT EXISTS` / 吞掉 "column already exists" 错误)。

迁移切片 `migrations`(`schema.go:226`,约 150 条,v0.7→v0.51+),每条一句 SQL。**没有迁移日志**——一个拼错的迁移会在第二次运行时静默 no-op。

> **加列的坑**:必须同时改 (a) `migrations` 切片里的 ALTER **和** (b) 建表常量。只改一处会让新装和升级两套库结构分叉。

### FTS5 同步(双模)

- **触发器同步**(v0.10 迁移,`schema.go:215-230`):`learnings_fts_insert/delete/update` 自动把 `learnings.content` 镜像进 `learnings_fts`。新 learning **立即**可被 BM25 命中。
- **富化重建**(`learnings.go:253` `RebuildFTSEnriched`):周期性把 `content + keywords + anticipated_queries + entities` 拼起来(`BuildSearchableText`,`learnings.go:343`)重建 FTS 内容。由 `StartFTSSync(ctx, 10s)`(`store.go:260` → `daemon.go:349`)驱动。
- **代价**:富化后的可检索性有最长 10 秒延迟。

### PRAGMA(每连接,DSN 级)

| PRAGMA | 值 | 原因 |
|---|---|---|
| `journal_mode` | WAL | 写时并发读 |
| `busy_timeout` | 30000ms | **v2.2.5 多写者修复**(提交 `660dd22`),5000→30000 |
| `cache_size` | 64MB (`-65536`) | 每连接页缓存 |
| `mmap_size` | 256MB | 内存映射 I/O |
| `journal_size_limit` | 10MB | WAL 文件上限(由 `journal_size_limit_test.go:17` 验证) |
| `wal_autocheckpoint` | 30000 | 自动检查点 |
| `foreign_keys` | ON | 引用完整性 |

---

## 5. 向量与嵌入:自研 IVF + 捆绑模型

### 嵌入 Provider(`internal/embedding/`,19 文件)

**捆绑模型,纯 Go 推理,无外部 API**。

```go
// provider.go:6
type Provider interface {
    Embed(ctx, texts []string) ([][]float32, error)
    Dimensions() int
    Enabled() bool
    Close() error
}
```

三个实现:

| Provider | 文件 | 用途 |
|---|---|---|
| `SSEProvider`(默认) | `sse.go` | 生产:捆绑模型,**512 维**,~108MB 权重 |
| `StaticProvider` | `static.go` | 测试/CI:确定性向量 |
| `NoneProvider` | `provider.go:21` | 向量搜索禁用时的兜底 |

**SSE 模型资产**(`internal/embedding/assets/`,`//go:embed`):
- `sse_dyt_512d.bin` — 模型配置
- `sse_weights_part0/1/2.bin` — 三段权重(~108MB)
- `tokenizer.json` — BPE/WordPiece 词表

推理流程:纯 Go tokenize → forward pass → L2 归一化 → 返回 `[]float32`(512 维)。无 ONNX、无 libtorch。

**嵌入缓存**(`cache.go`):SHA256(text) → 向量,存 `embedding_cache` 表。`embedding_model` 列变化时失效,避免跨会话重复嵌入。

### 向量存储(`internal/embedding/store.go`)

向量**存在 SQLite**(`learnings.embedding_vector` BLOB),小端 float32 序列,经 `float32SliceFromBlob` / `blobFromFloat32Slice` 互转(`learnings_embedding.go`)。

关键方法:

| 方法 | 行 | 用途 |
|---|---|---|
| `SetIVFIndex(IVFSearcher)` | `store.go:62` | 插入 IVF 后端 |
| `Search(ctx, q, n)` | `store.go:163` | Top-K 余弦;有 IVF 则路由到 IVF |
| `SearchWithProject(ctx, q, n, project)` | `store.go:169` | 按项目过滤的向量检索 |
| `bruteForceScan(...)` | `store.go:192` | 兜底全表扫描 |

### 自研 IVF(`internal/ivf/`,4 文件)

**不是 sqlite-vec,不是 hnswlib**——从零实现的倒排文件索引。

| 文件 | 角色 |
|---|---|
| `index.go`(339 行) | IVFIndex:k-means++ 构建、搜索、Add/Remove |
| `kmeans.go`(142 行) | k-means++ 初始化 + 5 轮迭代 |
| `persist.go`(193 行) | IVF 文件 Save/Load |

**算法参数**:
- 距离:L2 归一化后点积(余弦相似度)
- 聚类数:`autoK(n) = clamp(√n, 1, 100)`
- nprobe:5(每查询探测 5 个聚类)

### IVF 文件格式 — **向量不在文件里**

`~/.claude/yesmem/yesmem.ivf`(单全局文件,非每项目),布局:

```
[4B]  magic "IVF1"
[4B]  k          (uint32 LE) 聚类数
[4B]  dim        (uint32 LE) 维度 = 512
[4B]  nprobe     (uint32 LE)
[k*dim*4B]  centroids    float32 LE 行主序
[k*4B]      cluster_sizes uint32 LE
[Σ sizes * 8B] cluster_ids uint64 LE  ← 只有聚类分配,无向量值
```

> **关键设计取舍**:IVF 文件只存质心 + 每个 id 的聚类归属(~50 B/id),不存 512 维向量本身(否则 2 KB/id)。代价:**每次查询都要回 SQLite 取候选向量**做精排。`VectorStore.SearchWithProject` 在向量检索*之后*才按项目过滤,所以项目偏斜数据会浪费余弦计算。

### 暴力 vs IVF 切换

`IVFAutoBuildThreshold = 50000`(`daemon.go:632-667`):
- < 50k 向量:**不加载 IVF**,每次走 `bruteForceScan` 全表扫描。
- ≥ 50k:加载/构建 IVF,查询先 IVF 粗排再余弦精排。

代价:50k 以下索引构建成本 > 每查询成本,所以默认暴力。`SetIVFSavePath(path, 100)`(`store.go:83`)每 100 次变更刷盘一次——崩溃可能丢最近 <100 个聚类分配(向量本身在 SQLite 里安全),重启时可能需重建 IVF。

---

## 6. 混合检索:RRF 融合三路召回

**编排不在 storage 包,在 daemon 的 `internal/daemon/handler_hybrid.go`。**

### 三路召回

```
                        query
                ┌──────────┼──────────┐
                ▼          ▼          ▼
           BM25 路      向量路     anticipated-query 路
        (FTS5 porter   (512维余弦   (FTS5 命中
         unicode61,    IVF/暴力,    anticipated_queries
         触发器+10s     project后   列加权)
         富化)          置过滤)
                │          │          │
                ▼          ▼          ▼
            ranked      ranked      ranked
                └──────────┼──────────┘
                           ▼
              RRFMerge(k=60)   embedding/hybrid.go:20
              RRF_score(d) = Σ_src 1/(k + rank_in_src)
                           ▼
                  Top-N hydrate 成 Learning 行
```

### BM25 路(`learnings_search.go:162`)

```sql
SELECT l.id, bm25(learnings_fts) AS score, ...
FROM learnings_fts
JOIN learnings l ON l.id = learnings_fts.rowid
WHERE learnings_fts MATCH ?
  AND (project = ? OR ? = '')       -- 项目过滤
  AND (created_at >= ? OR ? = '')   -- since
  AND (created_at <= ? OR ? = '')   -- before
ORDER BY rank LIMIT ?
```

- 分层 AND 搜索:token 按 IDF 降序排,逐级放松匹配要求(`learnings_search.go`)。
- `BuildSearchableText`(`learnings.go:343`)把 content + keywords + AQs + entities 拼进 FTS 索引文本。

### 向量路

调用方 `Provider.Embed(ctx, []string{query})` → 512 维 → `VectorStore.SearchWithProject` → IVF 或暴力 → `[]SearchResult{ID, Score=cosine, Source="vector"}`。

### 融合 — Reciprocal Rank Fusion

```go
// embedding/hybrid.go:20
func RRFMerge(bm25, vector []RankedResult, k, limit) []RankedResult
// k = 60(标准 RRF 常数)
```

为什么用 RRF 而非加权求和:BM25 分数无界,余弦 ∈ [-1,1],两者**不可比**。RRF 用排名而非原始分,天然归一。

> benchmark 里有 3-way 变体(`benchmark/locomo/localsearch.go:288` `rrfMerge3Way`),但**生产 daemon 用 2-way**(BM25 + vector)。AQ 路的结果作为候选增强,不是独立融合源。

### 类型边界陷阱

- `embedding.SearchResult`(`store.go:24`,`{ID string, Score float32, Source}`)
- `models.SearchResult`(`models.go:305`,daemon 边界用)
- 同名不同结构,在 daemon 边界转换。

---

## 7. Daemon 内部:单写者 + 123 RPC

### 单写者模型

**只有 daemon 进程以 RW 模式开 SQLite**。proxy / hooks / CLI 全部通过 Unix socket 发 RPC。这是 yesmem 并发安全的根基,`busy_timeout=30000` 兜底处理 daemon 进程内部多 goroutine 的写争用。

### IPC 传输(`internal/daemon/socket.go`)

```
sockName = "daemon.sock"                          socket.go:13
SocketPath(dataDir) = dataDir/daemon.sock         socket.go:16
SocketServer — AF_UNIX listener, chmod 0600       socket.go:43, :49
Serve() — accept 循环,每连接一 goroutine          socket.go:69
handleConn — newline-delimited JSON codec         socket.go:84
```

**线路协议**:JSON-over-newline。每连接一对 `json.Encoder`/`Decoder`。**无长度前缀**,帧靠 `Encoder.Encode` 追加换行 + `Decoder.Decode` 消费恰好一个 JSON 值。

客户端(`socket.go`):
- `SocketClient.Call(method, params)` (`:111`)
- `CallWithTimeout(dataDir, method, params, timeout)` (`:124`)

### 两个监听面,一个 Handler

1. **Unix socket**(`daemon.sock`)— 永远开,proxy/CLI/hooks 主面。
2. **HTTP API**(可选,`cfg.HTTPEnabled`)— `daemon.go:316-339`,默认 `127.0.0.1:9377`,`~/.claude/yesmem/auth_token` 随机令牌守护。经 `handlerAdapter`(`daemon.go:1353`)桥到同一个 `Handler.Handle`。

### RPC 表面(`handler.go:297` `Handle`)

单个巨型 `switch req.Method`,**123 个 RPC 方法**(`handler.go:326-646`)。分类:

| 类别 | 代表方法 |
|---|---|
| 搜索/记忆 | `search` `deep_search` `hybrid_search` `vector_search` `remember` `expand_context` `query_facts` `reload_vectors` |
| 会话 | `get_session` `get_compacted_stubs` `quarantine_session` `skip_indexing` |
| Learnings | `get_learnings` `relate_learnings` `resolve` `resolve_by_text` |
| Agents | `spawn_agent` `register_agent` `update_agent` `relay_agent` `stop_agent` `resume_agent` `list_agents` `update_agent_status` `register_pid` `register_window` |
| Cap store/执行 | `cap_store`(upsert/query/list/delete/list_tables/create_table)`save_cap` `get_caps` `activate_cap` `deactivate_cap` `execute_cap` `register_caps` |
| Cap 提案 | `cap_proposal_decide` `list_cap_proposals` |
| 调度器 | `schedule`(create/list/delete/run) |
| 钉选/人格/计划 | `pin` `unpin` `get_pins` `set_persona` `set_plan` `update_plan` `get_plan` `complete_plan` |
| 代码智能 | `search_code_index` `search_code` `get_code_context` `get_dependency_map` `graph_traverse` `get_file_index` `get_code_snippet` `get_file_symbols` |
| 技能 | `get_skill_content` |
| LLM | `llm_complete` |
| 消息 | `broadcast` `check_broadcasts` `mark_channel_read` `send_to` |
| 草稿板 | `scratchpad_write` `scratchpad_append` `scratchpad_read` `scratchpad_list` `scratchpad_delete` |
| 内部 | `_track_usage` `_persist_rate_limits` `track_stream_state` `whoami` `fork_*` |

### 后台服务(全 `time.NewTicker` + `select`,无 cron 库)

| 服务 | 周期 | spawn 处 | 职责 |
|---|---|---|---|
| 调度器 tick | 1s | `daemon.go:303` | 用户 cron/interval 作业 |
| agent 心跳 | 1s | `daemon.go:352`→`heartbeat.go:23` | 消息投递、陈旧度检查、orchestrator ping、yesloop 守卫 |
| opencode 扫描 | 60s | `daemon.go:213` | opencode DB 增量提取 |
| caps 监视 | 30s | `daemon.go:255` | 重新导入磁盘 `CAP.md` |
| wiki 渲染 | 5min | `daemon.go:354` | 活跃项目 wiki 重建 |
| staleness | 周期 | `daemon.go:846` | git-commit 扫描 + LLM 新鲜度评估 |
| briefing 再生 | 2h 或 ≥5 待处理 | `daemon.go:944` | 项目简报 |
| 文档同步 | 2h | `daemon.go:1012` | 重新摄入已索引文档 |
| 查询聚类 | 30min | `daemon.go:1036` | 聚类 `query_log` |
| 复现检测 | 2h,≥50 新 | `daemon.go:1051` | 跨 learning 找重复模式 |
| 聚类蒸馏 | 2h | `daemon.go:1080` | 浓缩聚类内相似 learning |
| 自更新 | 可配 | `daemon.go:1134` | 自更新轮询 |
| fsnotify | 事件驱动 | `daemon.go:423` | 文件变→3s 静默重索引;5min 静默→LLM 提取 |

**调度器**(`internal/daemon/scheduler.go`)是手写的 1s tick + 逐 tick 到期评估,无依赖图、无 worker pool。作业持久化在 SQLite(`store.ListScheduledJobs`/`UpdateJobLastRun`),启动时加载。cron 5 字段在 add 时解析;`IntervalSeconds` 覆盖 cron 做亚分钟轮询。

> **注意**:`internal/schedule/` 包**不存在**(子代理报告已校验确认)。调度逻辑全在 `internal/daemon/scheduler.go` + `handler_scheduler.go`。

### fsnotify 去抖(`internal/daemon/watcher.go`)

文件变 → `indexAfter = now + 3s`;若持续静默到 `extractAfter = now + 5min`,跑 LLM 提取(`watcher.go:67-110`)。单 goroutine,无 worker pool;索引/提取经原子标志(`indexRunning`,`daemon.go:234`)串行化。

---

## 8. Proxy 内部:折叠引擎 + 缓存门控

Proxy(`internal/proxy/`,162 文件)是 yesmem 最复杂的子系统。核心是**让上下文折叠对客户端不可见**。

### 入口与派发

- `proxy.Run(cfg)`(`proxy.go:272`)启动,4 个调用点:`cmd_process.go:33/134` 等。
- `Server` 结构组合 ~15 个并发子组件(`DecayTracker` / `Narrative` / `SignalBus` / `FrozenStubs` / `EagerStubMemory` / `CapsCache` / `SawtoothTrigger` / `TimestampStore` / `CacheStatusWriter` / `CacheKeepalive` / `CacheTTLDetector` / `CacheGate` / `SkillTracker` / `ForkState` / `msgCounters`),每组件自带 mutex。

### 请求管道(`handleRequest`,`proxy.go:755`)

按 path/header 派发:

| 路径 | 处理器 | 行为 |
|---|---|---|
| `/v1/messages` | `handleMessages` (1045 行) | Anthropic 格式,12+ 重写标志顺序执行 |
| `/v1/chat/completions` | OpenAI 处理器 | OpenAI 格式转换 |
| `/v1/responses` | Responses 处理器 | Responses API |
| 其他 | `forwardRaw` | 透明转发 |

`handleMessages` 重写顺序:

```
runStubCycle
  → cache-gate (CacheGate)
  → ShiftMessageBreakpoint
  → NormalizeCacheTTL
  → EnforceCacheBreakpointLimit
  → validateToolPairs
  → forwardWithAnnotation (流式 SSE)
  → (边流边检测膨胀) CollapseOldMessages
```

### 折叠(Collapse)— 不调 LLM

折叠完全本地,由 `Stubify` + `DecayTracker` 驱动:

1. `CalcCollapseCutoff`(`collapse.go:15-79`)从后向前按 token 累积扫描,找最小索引。
2. `CollapseOldMessages` 把首条消息空白化,注入 `[Archiv: Messages 1-N (M msgs) — get_compacted_stubs(...) zum Reinzoomen]` 元块。
3. `SawtoothTrigger`(高低三级)+ `FrozenStubs`(sha256 前 16 hex 指纹)在冻结前缀时避免重复存档。

折叠后的存根**保留可重建线索**:子代理/未来会话可用 `get_compacted_stubs(session_id, from, to)` 拉取压缩存根。

### 转发与用量缩减

- `forwardWithAnnotation` **总是流式 SSE**,gzip 在解析前解码。
- **usage deflation(用量缩减)** 是折叠不可见的核心机制:上游返回的 `input_tokens` 按 `DeflateUsageFactor` 缩放后写回 SSE。客户端看到的 token 数与折叠前一致,不会察觉旧消息被压缩。
- `sanitizeListenAddr` 静默把所有绑定强制成 `127.0.0.1`(防误暴露)。

### 可重入与幂等

`requestFingerprint` = `len(messages)` + 最后一条消息前 200 字节的 sha256[16 hex]。匹配时所有副作用(DB 写、daemon RPC、阈值覆盖)都被抑制。

### 维护循环

`Server` 持 ~25 个 mutex(每线程一个);`runMaintenance` 每 5s 触发 daemon RPC;子组件经 `Load/Save(name)` 回调递归持久化(经 daemon JSON-RPC,**不直接**写 DB)。

---

## 9. Hooks:8 个注入点的提取管线

Claude Code 在会话生命周期 spawn `yesmem <hook>` 子进程。8 个注入点(`main.go:82-92`):

| 子命令 | 触发点 | 职责 |
|---|---|---|
| `hook-check` | 会话开始 | 检查记忆/状态,注入 system-reminder |
| `hook-guard` | 工具调用前 | 守卫(如阻止危险操作) |
| `hook-learn` | 工具结果后 | 从交互中提取 learning |
| `hook-assist` | 用户消息后 | 主动召回相关记忆 |
| `hook-failure` | 失败 | 记录失败/纠错 |
| `hook-resolve` | 解决 | 标记问题已解决 |
| `hook-think` | 思考块后 | 提取思考 |
| `session-end` | 会话结束 | 完整会话索引 |

每个 hook 是**一次性子进程**,经 Unix socket 发 RPC 给 daemon。hook 管线的核心是**提取**(`internal/extraction/`)和**衰减**(`internal/hooks/`)。

### 提取管线(`internal/extraction/extractor.go`)

用 LLM 从对话/工具结果里抽 learning。OpenAI 兼容客户端(`openai_client.go`),支持 code-describe(`code_describe.go`,代码描述,max_tokens 8192 — v2.2.4 修复 DeepSeek V4 推理)、PII 消毒(`sanitizing_client.go` 包装层)、API 网关(`apigate.go`)。

### 衰减(`internal/hooks/decay.go`)

`DecayTracker` 分三级衰减模式,管理 learning 的新鲜度/重要性随时间衰退。

### codenav(`internal/hooks/codenav.go`)

代码导航 hook — 拦截 shell 导航命令,建议改用 yesmem 代码工具(`search_code_index` 等)。`dismiss_code_nav` 可按会话关闭。

---

## 10. 提取与索引:OpenAI 客户端 + 消毒

### 提取器(`internal/extraction/extractor.go`)

核心 LLM 提取循环。从会话消息里识别值得记的事件(gotcha/decision/pattern/preference/…),写成 learning 经 daemon RPC `remember` 入库。

### OpenAI 兼容客户端(`openai_client.go`)

支持任意 OpenAI 兼容端点(DeepSeek、本地模型等)。`code_describe.go` 单独处理代码描述任务 — v2.2.4(`86e9dd1`)把 max_tokens 1024→8192 以支持 DeepSeek V4 的长推理输出。

### PII 消毒(`sanitizing_client.go`)

包装层,在发给上游 LLM 前脱敏。

### API 网关(`apigate.go`)

多 provider 路由/限流。

### 后台索引(`internal/indexer/` + daemon goroutine)

新 learning 入库后,后台 indexer 经 `learnings_vectors_pending` 暂存表异步生成向量,填 `embedding_vector` BLOB。

---

## 11. Caps 系统:两套来源 + ai-jail 沙箱

**Cap = 可保存、可执行的工具定义**(JS 和/或 Bash 脚本,每个带 `kind`[tool|handler] + `runtime`[repl|bash])。

### 三套来源(经校验更正)

yesmem 有**三个**不同的"捆绑"目录,子代理报告只讲清了两个:

| 目录 | 数量 | 嵌入方式 | 内容 | 安装到 |
|---|---|---|---|---|
| `internal/daemon/caps/` | 7 | **`go:embed`** | 种子 cap:`repl_tool` `reddit_fetch` `manual_cap` `evolving` `auto_cap` `csv_tags_tool` `dispatch_test` | DB(首次启动导入) |
| `caps/bundled-caps/` | 10 | **磁盘随发行** | 用户 cap:`cap_search` `cap_delete` `telegram` `proxy_health` `deploy` `cap_collect` `cap_save_analysis` `changelog_check` `reddit` `adapter_e2e_test` | `~/.claude/caps/<name>/CAP.md` |
| `skills/bundled-skills/` | 14 | `go:embed`(技能,非 cap) | `yesresearch` `yesmem-sessions` `yesmem-search` `yesmem-remember` `yesmem-planning` `yesmem-orientation` `yesmem-docs` `yesmem-config` `yesmem-cap-builder` `yesmem-agents` `yesloop` `subagent-driven-development` `security-review` `reddit` | `~/.claude/skills/<name>/` |

> 系统提醒里的 "Available caps"(cap_search/cap_delete/telegram/proxy_health/deploy)正好对应 `caps/bundled-caps/` —— 这些是**磁盘随发行、可被用户编辑**的 cap。`internal/daemon/caps/` 的 7 个是**二进制嵌入、首次导入 DB** 的种子。

### CAP.md 格式(`internal/capfile/`)

| 文件 | 角色 |
|---|---|
| `parse.go:64` | `Parse([]byte) (*CapFile, error)` — frontmatter + body 解析 |
| `write.go` | `CapFile` → CAP.md 序列化 |
| `adapter.go` | JS/Bash 适配器生成;provider 名(`mcp__yesmem__cap_store`)↔ 通用名(`store`)互译 |
| `scanner.go` | CAP.md 目录扫描 |

`CapFile` 字段:`Name` `Description` `Tags` `AutoActive` `Scripts []ScriptSpec`(每个:`Name` `Kind`[tool/handler] `Runtime`[repl/bash] `Body` `Schema?`)。

适配器机制(`adapter.go:51` `GenerateAdapterJS`):cap 脚本可写 `store.upsert(...)` 而非 `mcp__yesmem__cap_store(...)`,执行时自动前缀注入别名 JS。

### 磁盘 ↔ DB 同步(`internal/daemon/cap_sync.go`)

- `SyncCapsFromDisk(handler, dir, project)` — 读所有 `CAP.md`,解析,`handleSaveCap` 入库。
- `ExportAllCaps(handler, dir)` — DB cap 导回磁盘供编辑。
- `CapsDirWatcher`(`daemon.go:256`)每 30s 跟踪 mtime,`ScanChanged` 只处理变更。

### 执行(`internal/daemon/handler_cap_exec.go`,293 行)

`execute_cap` RPC:
1. 按 name/project 解析 cap → 找脚本(按 `script_name` 或首个 `kind=tool`)。
2. 按 `runtime` 分支:
   - `"repl"` → 拼 JS + 前缀适配器别名 → `ai-jail bun -e`
   - `"bash"` → 前缀 bash 适配器 → `ai-jail bash -c`
3. 返回 `{output, exitCode, error}`。

### ai-jail 沙箱(`internal/daemon/sandbox.go` + `sandbox_download.go` + `sandbox_profile.go`)

- 沙箱二进制是 **`ai-jail`**(全代码库引用)。
- 三档 profile:`none`(直跑)/ `standard`(限网,默认放行 80,443)/ `strict`(限网+限文件系统)。
- **自动下载**:`ai-jail` 不在 PATH 时,`sandbox_download.go` 自动下载到固定位置(零配置沙箱)。

### Bash 作业自动纠错(`internal/daemon/bash_error_handler.go`)

scheduled bash job 失败且 `AutoCorrect=true` 时:
1. 调 LLM(Sonnet 级)提修正脚本。
2. 持久化为 `cap_proposed` 类 learning。
3. 经 `cap_proposal_decide`(`handler_cap_proposal.go:19`)提请人工 review。
4. `broadcastBashError`(`:~165`)通知运行中的 agent。

### 加新 cap runtime

**无插件接口**——`handler_cap_exec.go` 是 `switch runtime` 两个已知值。加第三个(如 `python`):
1. 扩 `capfile.Runtime` 解析(`parse.go`)。
2. 在执行器 switch 加 `case`。
3. 可选扩沙箱选解释器。
4. 更新 `handler_caps_helpers.go` 校验。

### capblob(`internal/capblob/blob.go`)

独立于 capfile —— 处理 cap 的二进制 blob 存储(`cap-blob-put`/`cap-blob-get` 子命令)。

---

## 12. Skills 系统:嵌入即安装

### 结构(`skills/embed.go`,18 行)

```go
//go:embed *.md                        var BundledCommands embed.FS        // 顶层 .md
//go:embed bundled-skills/*            var BundledSkills embed.FS          // 14 个 yesmem-*
//go:embed bundled-commands/opencode/* var BundledOpencodeCommands embed.FS
```

构建时**嵌入二进制**,无运行时磁盘依赖。顶层 `skills/*.md`(2 个:`swarm.md`、`persistent-orchestrator.md`)→ `~/.claude/commands/`。

### 安装而非调用(`internal/setup/setup.go`)

- `:1813` 遍历 `BundledCommands.ReadDir(".")`,每个 `.md` 写到 `~/.claude/commands/`。
- `:1868` 遍历 `BundledSkills.ReadDir("bundled-skills")`,每个子目录写到 `~/.claude/skills/<name>/`。
- opencode 变体到 `~/.config/opencode/commands/`。
- `uninstall.go:494,:508` 反向。

**daemon 只负责安装**,不负责调用——技能安装后由宿主(Claude Code/OpenCode)作为原生命令呈现,何时调用由宿主决定。

### DB 存储技能(独立机制)

daemon 有 `get_skill_content` RPC(`handler_skills.go:9`),返回 **DB 存储**的技能(`store.GetSkillContent(name, project)`)。这是与嵌入 `.md` **不同的**机制——用户创建/导入的技能,daemon 按需服务给 MCP 客户端。**无公开 `save_skill` RPC**,DB 技能经旁路填充。

### 加新技能

- **捆绑**:在 `skills/bundled-skills/<new>/SKILL.md`(技能)或 `skills/<new>.md`(命令)下放目录/文件,重跑 setup。
- **运行时/DB**:经 setup/seed 路径插入 `store.Skills`;`get_skill_content` 取。

---

## 13. 高阶智能:Briefing / Wiki / Clustering

### Briefing(`internal/briefing/`)

周期性(2h 或 ≥5 待处理,`daemon.go:944`)为每个项目生成简报——最近会话摘要、关键 learning、当前焦点。结果缓存到 `refined_briefings` 表(`schema.go:723`)。`refine.go` 做精炼,`recovery.go` 做崩溃恢复。

### Wiki 渲染(`internal/wikirender/`)

每 5min(`daemon.go:354`)为活跃项目重建 wiki。把 learning + 代码理解 + 文件注解渲染成 Markdown,落到 `~/.claude/yesmem/wiki/<project>/files/<path>.md`(路径编码 `/`→`_`)。这是 `[yesmem-wiki-first]` 规则的物理实现——编辑文件前先查其 wiki 页。

### 聚类与蒸馏(`internal/clustering/` + daemon goroutine)

- **查询聚类**(30min,`daemon.go:1036`):聚类 `query_log`,发现常见查询模式。
- **复现检测**(2h,≥50 新,`daemon.go:1051`):跨 learning 找重复模式。
- **聚类蒸馏**(2h,`daemon.go:1080`):浓缩聚类内相似 learning。
- **staleness**(周期,`daemon.go:846`):git-commit 扫描 + LLM 评估 learning 新鲜度。

### 人格与计划

- `persona_traits` / `persona_directives` 表:agent 人格状态。
- 计划(`set_plan`/`update_plan`/`get_plan`/`complete_plan`):**线程作用域**,经 proxy 重注入在 collapse 后存活——是上下文丢失-proof 的唯一锚点。

---

## 14. 代码智能:graph / codescan / wikirender

### 代码图(`internal/graph/`)

符号级依赖图。`graph_traverse(from, direction, edge_type)` 找所有调用路径。支持 `imports`/`defines`/`calls` 边类型。

### 代码扫描(`internal/codescan/`)

- `health.go` — 代码健康评分(复杂度、重复、死代码)。
- tree-sitter 解析(跨语言)。

### 代码工具(RPC 表面)

`search_code_index` / `search_code` / `get_code_context` / `get_dependency_map` / `graph_traverse` / `get_file_index` / `get_code_snippet` / `get_file_symbols` —— `[yesmem-code-tools-first]` 规则的支撑。代码导航优先用这些,**不**用裸 grep/find/cat。

---

## 15. 并发模型与可扩展点

### 并发模型

- **单 daemon = 单 SQLite 写者**。
- 进程内:多 goroutine(每后台服务一个 + 每 inflight RPC 一个)。争用靠 SQLite `busy_timeout` + `Handler` 上的字段级 mutex(`windowMapMu`、`rateMu`…)。
- **无 goroutine 池,无作业队列**。每 tick 回调在其 goroutine 内联跑;长活(LLM 提取、briefing)在专属 goroutine 里,经原子标志(`indexRunning`)门控。
- proxy 侧:`Server` 持 ~25 mutex(每线程一个);15 个子组件各自串行。

### 扩展点速查

| 要加… | 怎么做 |
|---|---|
| **新 RPC** | `Handler.Handle`(`handler.go:297+`)加 `case "your_method":`;任一 `handler_*.go` 加 `handleYourMethod(params)`。无需注册。 |
| **新表** | 加 `const tableX = "CREATE TABLE IF NOT EXISTS x (...)"`;追加到 `createSchema` 的 `tables` 切片(`schema.go:17`)。已有表加列:迁移切片(`schema.go:226`)**和**建表常量都要改。无版本号,迁移按序。 |
| **换向量后端** | 实现 `embedding.IVFSearcher`(`store.go:49`)— `Search`/`Add`/`Remove`/`Save`;`daemon.go:632-667` 换构造。若新后端自存向量,可丢 `embedding_vector` BLOB,但需同时关掉 `bruteForceScan`(`store.go:192`)。 |
| **新嵌入 provider** | 实现 `Provider`(`provider.go:6`);`factory.go` 注册(现 `sse`/`static`/`none`)。换维度需跑向量迁移(`embedding/migrate.go`)重建所有向量 + IVF。远程 provider 要加批处理 + 复用 `cache.go`。 |
| **新 scheduled job(用户)** | `schedule` RPC,`cron` 或 `interval_seconds`(`handler_scheduler.go`)。自动持久化。 |
| **新 daemon 后台服务** | `Run` 里 `go func(){ ticker := time.NewTicker(...); for { select { case <-ticker.C: …; case <-ctx.Done(): return } } }()`,照 `daemon.go:303` 模式。 |
| **新 cap runtime** | 扩 `capfile.Runtime` 解析 + `handler_cap_exec.go` 执行器加 `case` + `handler_caps_helpers.go` 校验。 |
| **新沙箱 profile** | 扩 `SandboxProfile`(`sandbox_profile.go:5`)+ `sandbox.go` 的 flag 构建。 |
| **新捆绑技能** | `skills/bundled-skills/<name>/SKILL.md`;重跑 setup。 |
| **新捆绑命令** | 顶层 `skills/<name>.md`;重跑 setup。 |

---

## 16. 勘误与未决项

### 对子代理报告的勘误(经结构校验)

1. **`cmd_*.go` 在仓库根目录**(`package main`,32 个,如 `./cmd_relay.go`、`./cmd_scratchpad.go`),**不在** `internal/daemon/`。daemon 子代理的文件路径表把它们错归到了 daemon 包。文件**名**正确,路径错。
2. **三套"捆绑"目录**(daemon 报告只讲清两套):
   - `internal/daemon/caps/`(7 个,**go:embed** 种子)—— 报告正确。
   - `caps/bundled-caps/`(10 个,**磁盘随发行**用户 cap)—— 报告**遗漏**。系统提醒的 "Available caps"(cap_search/cap_delete/telegram/proxy_health/deploy)正源于此。
   - `skills/bundled-skills/`(14 个 yesmem-* **技能**)—— 报告把这里的 `reddit` 与 `caps/bundled-caps/reddit` 混淆。技能与 cap 是两套独立系统。

### 未完全验证的项(标注存疑)

- **SSE 模型架构**:确认为 512 维 "dyt"、~108MB 三段权重、纯 Go 推理。但具体模型族(BERT?MiniLM?定制 "dyt"?)未从 `sse.go` 完全逆向。"dyt" 名 + 捆绑权重模式暗示是小型 transformer(可能轻度定制)。
- **rerank 阶段**:`handler_hybrid.go` 有 rerank 引用,但未完全追踪生产是否接线还是存根。若关键请通读 `handler_hybrid.go`。
- **PRAGMA 精确字符串**:确认 `busy_timeout=30000`、10MB `journal_size_limit`、WAL(经测试 + 提交 `660dd22`)。64MB cache + 256MB mmap 与测试文件一致,但未在 `openSQLite`(`store.go:181-230`)定位确切 DSN 字符串。
- **HTTP API 表面**:确认为可选、`127.0.0.1:9377`、随机令牌守护、经 `handlerAdapter` 桥到 `Handler.Handle`。`internal/httpapi/` 内部未深读。

### 已确认不存在

- `internal/schedule/` 包 — 不存在。调度在 `internal/daemon/scheduler.go` + `handler_scheduler.go`。
- `internal/caps/` 包 — 不存在。活 cap 存储在 `internal/daemon/handler_caps*.go` + `internal/capfile/`(解析)+ `internal/daemon/caps/`(嵌入种子)+ `caps/bundled-caps/`(磁盘用户 cap)。
- daemon 的 `SIGHUP`/`SIGUSR1` 热重载 — 不存在。重载靠 RPC 或 `--replace` 重启。

---

## 附:关键数据一览

| 指标 | 值 |
|---|---|
| Go 源文件总数 | 721 |
| `internal/` 子包 | 32 |
| `internal/daemon` | 119 文件 |
| `internal/proxy` | 162 文件 |
| `internal/storage` | 85 文件 |
| CLI 子命令 | 60+ |
| daemon RPC 方法 | 123 |
| 后台 goroutine 服务 | 12+ |
| SQLite 库 | 3(yesmem/runtime/messages)+ 2 RO 读池 |
| 嵌入维度 | 512(bundled SSE "dyt") |
| IVF 阈值 | 50k(以下暴力) |
| RRF 常数 | k=60 |
| 捆绑 cap(嵌入种子) | 7 |
| 捆绑 cap(磁盘用户) | 10 |
| 捆绑技能 | 14 |
| Go 版本 | 1.25 |
| CGO | `CGO_ENABLED=0`(纯 Go modernc sqlite) |

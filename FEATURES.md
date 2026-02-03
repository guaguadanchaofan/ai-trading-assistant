# AI Trading Assistant - Features (Step 1–8)

本文档总结已完成功能（Step 1–8），包含运行方式、配置说明、接口示例与测试步骤。

## 运行方式

```bash
go run ./cmd/server
```

服务默认监听 `:8080`，可通过环境变量覆盖：

```bash
PORT=8081 go run ./cmd/server
```

健康检查：

```bash
curl http://localhost:8080/healthz
```

## 配置说明（configs/app.yaml）

### server
- `server.port`：监听端口，默认 `8080`

### log
- `log.level`：日志级别，默认 `info`

### push.dingtalk
- `webhook`：钉钉机器人 webhook
- `secret`：加签密钥
- `timeout_ms`：HTTP 超时，默认 `5000`

环境变量覆盖：
- `DINGTALK_WEBHOOK`
- `DINGTALK_SECRET`

### alert
- `rate_limit.per_minute`：全局限流每分钟令牌数
- `rate_limit.burst`：突发上限
- `dedup.window_sec`：去重窗口（秒）
- `merge.window_sec`：合并窗口（秒）
- `digest.low_interval_sec`：低优先级汇总发送周期（秒）

### store.sqlite
- `store.sqlite.path`：SQLite 路径，默认 `data/app.db`

### market
- `market.symbols`：默认行情列表（含 `sh000001` 和自选股）
- `market.poll_interval_sec`：轮询间隔（秒）
- `market.min_request_interval_ms`：最小请求间隔（毫秒）

### engine
规则引擎配置：
- `engine.index_risk.symbol`：指数标的（默认 `sh000001`）
- `engine.index_risk.med_pct / high_pct`
- `engine.panic_drop.window_sec / med_pct / high_pct`
- `engine.volume_spike.ma_points / ratio`
- `engine.key_break_down.levels`：关键价位
- `engine.key_break_down.priority`：med/high
- `engine.window_max_keep`：快照滑窗缓存
- `engine.cooldown_sec.*`：各规则冷却时间

### risk_agent
RiskAgent（LLM）配置：
- `enabled`：是否启用
- `model`、`api_key`、`base_url`、`by_azure`、`api_version`、`timeout_ms`

### plan_agent
PlanAgent（LLM）配置：
- `enabled`：是否启用
- `model`、`api_key`、`base_url`、`by_azure`、`api_version`、`timeout_ms`

## 数据库表

- `alerts`：告警记录（含 dingtalk 状态、payload）
- `events`：事件记录（含规则、证据）
- `market_snapshot`：行情快照
- `plan`：盘前计划

## 功能模块概览（Step 1–8）

### Step 1：最小 HTTP 服务
- Hertz 服务启动
- `/healthz` 返回 `{ "ok": true }`
- YAML 配置 + 环境变量覆盖

### Step 2：钉钉机器人推送（含加签）
- POST `/api/v1/test/push`：测试发送 Markdown

### Step 3：AlertService（限流/去重/合并/low 汇总）
- POST `/api/v1/test/alert`
- POST `/api/v1/test/alert-burst`

### Step 4：SQLite 落库 + 查询接口
- alerts / events 落库
- GET `/api/v1/alerts?date=YYYY-MM-DD&status=&group=&limit=&offset=`
- GET `/api/v1/alerts/dedup/{dedup_key}`

### Step 5：行情接入 + 快照轮询
- 免费行情（东方财富 + 新浪兜底）
- 轮询写入快照
- GET `/api/v1/quotes`（带缓存兜底）
- GET `/api/v1/snapshots?symbol=...&limit=...&offset=...`

### Step 6：事件引擎 v0（4 条规则）
- INDEX_RISK（仅 `sh000001`）
- PANIC_DROP / VOLUME_SPIKE / KEY_BREAK_DOWN（仅股票，不含指数）
- 冷却、防重复、入库、推送
- POST `/api/v1/test/snapshot`
- GET `/api/v1/events?date=YYYY-MM-DD&type=`

### Step 7：RiskAgent（风险决策）
- 事件 -> 风控决策 JSON -> 统一 Markdown 模板
- 失败回退：基于事件 severity 生成决策
- POST `/api/v1/test/risk/ping`
- POST `/api/v1/test/risk/eval`

### Step 8：PlanAgent（盘前计划）
- 从行情生成计划并入库
- LLM 不可用时生成 fallback 模板
- POST `/api/v1/plan/generate?date=YYYY-MM-DD`
- POST `/api/v1/plan/confirm`
- GET `/api/v1/plan?date=YYYY-MM-DD`

## 接口示例

### 基础
```bash
curl http://localhost:8080/healthz
```

### 推送测试
```bash
curl -X POST http://localhost:8080/api/v1/test/push \
  -H 'Content-Type: application/json' \
  -d '{"title":"Hello","markdown":"**Test**"}'
```

### Alert 测试
```bash
curl -X POST http://localhost:8080/api/v1/test/alert \
  -H 'Content-Type: application/json' \
  -d '{"priority":"med","group":"risk","title":"test","markdown":"hello"}'
```

### 行情（带缓存兜底）
```bash
curl "http://localhost:8080/api/v1/quotes?symbols=sh000001,sh600000"
```

### 快照查询
```bash
curl "http://localhost:8080/api/v1/snapshots?symbol=sh000001&limit=50"
```

### 事件注入与查询
```bash
curl -X POST http://localhost:8080/api/v1/test/snapshot \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"sh600000","price":11.3,"change_pct":-5.8,"volume":800000}'

curl "http://localhost:8080/api/v1/events?date=2026-02-03&type=PANIC_DROP"
```

### RiskAgent
```bash
curl -X POST http://localhost:8080/api/v1/test/risk/ping

curl -X POST http://localhost:8080/api/v1/test/risk/eval \
  -H 'Content-Type: application/json' \
  -d '{"event_id":1}'
```

### PlanAgent
```bash
curl -X POST "http://localhost:8080/api/v1/plan/generate?date=2026-02-03"

curl -X POST "http://localhost:8080/api/v1/plan/confirm" \
  -H 'Content-Type: application/json' \
  -d '{"date":"2026-02-03"}'

curl "http://localhost:8080/api/v1/plan?date=2026-02-03"
```

## 测试步骤建议

1. 启动服务：`go run ./cmd/server`
2. 调用 `/healthz` 验证服务可用
3. 先请求 `/api/v1/quotes`，建立缓存
4. 注入 `snapshot` 触发规则（PANIC_DROP 等）
5. 查询 `/api/v1/events` 验证入库
6. 调用 `/api/v1/test/risk/eval` 验证风险决策
7. 调用 `/api/v1/plan/generate` 生成盘前计划并查询

---
如需补充更多接口示例或字段说明，可继续追加。

# 异步图片接口

## 官方新版与现有图片异步的关系

本版本基于官方 `v1.0.0-rc.47`，同时保留本站原有的图片异步扩展。两套接口使用不同任务 ID，不能混用查询地址：

| 用途 | 提交 | 返回 | 查询 |
| --- | --- | --- | --- |
| 现有 GPT 等普通图片渠道 | `/v1/images/generations` 或 `/v1/images/edits`，添加 `Prefer: respond-async` | HTTP 202，`imgtask_...` | `/v1/images/tasks/{id}`，成功后 `/result` |
| 官方任务插件渠道 | `/v1/tasks/{pluginKey}` | HTTP 200，`task_...` | `/v1/tasks/{id}`，成功后 `/artifacts` |

官方任务插件必须已启用，渠道、模型、令牌分组也须配置匹配。模型不会因为升级而自动转换为任务插件。官方图片协议桥接本身仍会等待生成结果；普通图片渠道继续使用下文的 202 异步扩展，原有同步客户端不需修改。

官方任务提交示例（`PLUGIN_KEY` 和 `MODEL` 替换为实际配置）：

```bash
curl "https://nicolessss.com/v1/tasks/$PLUGIN_KEY" \
  -H "Authorization: Bearer $NEWAPI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"MODEL","prompt":"一只坐在窗边的橘猫"}'
```

提交后取响应中的 `task_id`，查询 `GET /v1/tasks/{task_id}`。查询状态为 `NOT_START`、`SUBMITTED`、`QUEUED`、`IN_PROGRESS`、`SUCCESS`、`FAILURE` 或 `UNKNOWN`。成功后查询 `GET /v1/tasks/{task_id}/artifacts`，按返回的产物键下载 `GET /v1/tasks/{task_id}/artifacts/{artifact_key}/content`。所有请求携带原 API 令牌。各插件的附加参数由对应插件规定。

上文官方任务提交成功的 HTTP 200 和下文图片扩展的 HTTP 202 都只表示任务已受理，不等于图片生成成功。不要把下文 `Idempotency-Key` 的保证套用到官方通用任务接口。

官方实现参考：[任务插件 API v1](https://github.com/QuantumNous/new-api/blob/v1.0.0-rc.47/docs/plugin-api/v1.md)。

## 本站图片异步扩展

支持 `POST /v1/images/generations`、`POST /v1/images/edits`（包括 multipart 上传）。原接口默认仍同步返回；异步请求添加 `Prefer: respond-async`，或使用 `?async=true`。不要把 `async` 放进 JSON / multipart 表单，也不要同时启用 `stream=true`。

## 提交

```bash
curl https://nicolessss.com/v1/images/generations \
  -H "Authorization: Bearer $NEWAPI_API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Prefer: respond-async' \
  -H 'Idempotency-Key: my-order-20261025-001' \
  -d '{"model":"gpt-image-2-adobe","prompt":"一只坐在窗边的橘猫，温暖自然光","n":1,"response_format":"b64_json"}'
```

模型必须是令牌当前分组有权调用的模型。编辑图片的例子：

```bash
curl https://nicolessss.com/v1/images/edits \
  -H "Authorization: Bearer $NEWAPI_API_KEY" \
  -H 'Prefer: respond-async' \
  -H 'Idempotency-Key: my-edit-20261025-001' \
  -F 'model=gpt-image-2-adobe' \
  -F 'prompt=将参考图背景改成浅蓝色' \
  -F 'image=@reference.png' \
  -F 'n=1' -F 'response_format=b64_json'
```

请求体已保存后返回 **HTTP 202**，例如：

```json
{
  "id": "imgtask_...",
  "task_id": "imgtask_...",
  "object": "image.task",
  "status": "queued",
  "request_id": "...",
  "status_url": "/v1/images/tasks/imgtask_...",
  "result_url": "/v1/images/tasks/imgtask_.../result",
  "expires_at": 1792944000
}
```

`202` 表示已接收，不代表已经出图或收费成功。请求断开不取消后台任务。使用相同令牌、`Idempotency-Key`、接口、请求内容和路由相关配置重复提交，返回同一个任务；同一键更换内容返回 `409`。JSON 重试应复用相同请求体字节；multipart 重试允许 boundary 变化。幂等记录随任务到期删除，到期后不能依赖旧键去重。客户端不应给一次业务请求换多个键自动重试。

## 查询与取图

使用**提交时相同的 API 令牌**，建议每 3 秒查询一次：

```bash
curl https://nicolessss.com/v1/images/tasks/imgtask_... \
  -H "Authorization: Bearer $NEWAPI_API_KEY"
```

状态为 `queued`（等待）、`running`（执行）、`succeeded`（成功）或 `failed`（失败）。失败信息位于 `error`。需要排查时提供 `task_id` 和 `request_id`，不要提供密钥。

成功后取结果：

```bash
curl https://nicolessss.com/v1/images/tasks/imgtask_.../result \
  -H "Authorization: Bearer $NEWAPI_API_KEY" \
  -o result.json
```

成功返回 **HTTP 200**，响应体与原同步图片接口相同，包括 `data[].b64_json` 或 `data[].url`。仍在执行则返回 `202`，失败返回错误状态及错误对象。重复查询/下载不会再次生成或扣费。查询不消耗生成请求限额，另有每用户每分钟 600 次读取限制。额度耗尽后仍可用原令牌取已生成结果；删除、禁用、过期令牌或封禁账号不能读取，IP 白名单仍生效，其他令牌访问返回 `404`。

结果默认保存 24 小时，到期后返回 `410`（记录清理后 `404`）。上游返回 URL 时，缓存的是响应 JSON，URL 的寿命仍由上游决定；需长期保存图片时及时下载，或使用 `response_format=b64_json`。

## 计费与恢复

- 使用现有模型、分组、用户密价和订阅计费规则。任务**开始执行时**预扣，成功按实际用量结算；等待期间不会占用余额。余额、权限、账号分组发生变化可能导致排队任务失败。
- 普通失败退款；已有违规扣费策略仍适用。提交、预扣、结算及退款采用持久任务记录，重复处理不重复扣款/退款。
- queued 任务重启后继续执行。已经开始、但无法确定上游是否完成的任务不会自动重新生成，避免重复出图和费用；租约超时后标失败并退回未结算预扣。结果已持久保存且已结算的任务可恢复成功。
- 并发由后台 worker 数限制；异步缩短提交连接等待时间，并不使上游模型生成更快。最终取回较大的 base64 响应仍受网络速度影响。

## 部署配置

功能默认关闭；设置环境变量并重启服务后启用。数据目录必须挂载持久卷，不可指向临时缓存目录。

```yaml
environment:
  IMAGE_ASYNC_ENABLED: "true"
  IMAGE_ASYNC_DIR: /data/image-async
  IMAGE_ASYNC_WORKERS: "4"
  IMAGE_ASYNC_TIMEOUT_SECONDS: "600"
  BATCH_UPDATE_ENABLED: "false"
volumes:
  - ./newapi-data:/data
```

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| IMAGE_ASYNC_WORKERS | 4 | 每进程执行并发，1–32 |
| IMAGE_ASYNC_QUEUE_LIMIT | 100 | 全站 queued + running 上限 |
| IMAGE_ASYNC_USER_QUEUE_LIMIT | 20 | 单用户 queued + running 上限 |
| IMAGE_ASYNC_TIMEOUT_SECONDS | 600 | 每任务执行总时限，30–3600 秒 |
| IMAGE_ASYNC_RETENTION_HOURS | 24 | 请求/结果/幂等记录保存时间，2–168 小时 |
| IMAGE_ASYNC_REQUEST_MB | 64 | 单请求原始正文上限；同时受原网关限制 |
| IMAGE_ASYNC_RESULT_MB | 256 | 单响应缓存上限 |
| IMAGE_ASYNC_STORAGE_MB | 10240 | 本地存储准入预算，包含正在写入的预留空间 |

不支持与延迟批量余额写入 `BATCH_UPDATE_ENABLED=true` 同时启用；为防止余额不一致，功能会拒绝启动。磁盘不足、队列满或存储不可写时拒绝接收新任务。

启用后，令牌校验和余额查询以数据库为准，避免同步与异步请求混用时读取旧的 Redis 额度；Redis 会话、限流等其他功能继续使用。这会增加少量数据库查询。多副本部署需统一启用此模式。

多个 NewAPI 实例需要共享数据库、同一个持久任务目录（包含 `node-id` 文件）和一致配置；不能让多个实例分别使用空目录，然后把查询随机负载均衡过去。后台使用 DB 领取与租约避免重复执行。存储故障导致无法取结果时返回 `503`，不会自动重新生图。

worker 在调用上游前再次检查存储预算，已接收的排队任务也可能因存储耗尽而失败，此时不发起生成。多副本的并行上传仍可能短时超过准入预算；需要严格的共享磁盘硬上限时，应另设文件系统/容器卷配额。单个请求和结果的大小上限始终强制执行。

任务目录含用户提示词和上传图片，权限为 `0700`，正文和结果文件为 `0600`；数据库只保存任务元数据与计费账本，不保存 API 密钥。清理任务默认每 30 秒执行。

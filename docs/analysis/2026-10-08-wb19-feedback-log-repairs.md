# wb19：GitHub 反馈与生产日志核对（2026-10-08）

## 范围与基线

- 源码：`juzi8633/workbuddy2api-panel`，基线 `47603a8`；上游只读，不向上游提交。
- 118 生产采样时仍为 `1.13.0-wb18`，二进制 SHA256 前缀 `69c9465b`。
- GitHub 拉取截至 2026-10-08：上游最近 100 条 issue/PR 及重点讨论，fork 的 open issues 和最近 Actions。fork 无 open issue；上游 `main` 没有新提交，**本次是本地修复，不是盲目合并未审查 PR**。
- 修复阶段未修改生产；后续已于 12:53 UTC 部署 `1.13.0-wb19`，见[部署记录](../operations/2026-10-08-wb19-deployment.md)。以下日志统计仍是部署前的固定历史窗口。

## 日志证据与限制

统计截止：2026-10-08 10:32 UTC。数据为生产服务 journal 与随后读取的请求 JSONL；剔除记录中来源为 `127.0.0.1` / `::1` 的本机请求。不能用这一规则识别所有远端人工测试。归档 `time` 为请求开始时间，以下仅计入在窗口内开始且 `time + duration_ms <= 截止时间` 的已完成请求。

| 窗口 | 非本机已完成请求 | HTTP 200 | HTTP 400 |
|---|---:|---:|---:|
| 最近 24h | 1,448 | 1,448 | 0 |
| wb18 首次部署后（05:57:43 UTC 起） | 247 | 247 | 0 |
| wb18 tag 部署后（08:45:06 UTC 起） | 71 | 71 | 0 |
| 10 月 3 日 00:00 CST 起 | 15,378 | 15,375 | 3 |

边界更正：一条请求于 10:31:54.707 UTC 开始，耗时 12,333ms，约 10:32:07.040 UTC 完成并返回 200。初版只按开始时间筛选，四个窗口均多计了这一条；现在单列，不纳入截止时成功率。部署后已完成数 247/71 也与 journal 请求行计数一致。

最近 24h 纳入统计的请求 `outcome` 全为 success；prompt token 加权缓存命中率约 **96.1%**。这不能证明每个会话键都正确，也不能量化本次修复的生产收益。请求归档不包含正文、入站会话 ID 或缓存键，不能从这些记录反推是否触发 PR #133 的具体形态。

历史 3 条非本机 400：

| 时间（CST） | duration_ms | 现有元数据 |
|---|---:|---|
| 10-06 17:08:52 | 9,298 | model/attempts 缺失，outcome=http_error |
| 10-06 19:46:08 | 125,013 | 同上 |
| 10-07 16:07:46 | 45 | 同上 |

wb18 代码的请求体读取失败发生在 `chatStat` 创建之前，这类失败不会生成聊天流水行。历史行没有错误码，无法区分 EOF、连接断开或读取超时；**125 秒不是上游超时/轮转的证据**。本次补观测，不声称已查明这三条的具体 I/O 原因，也不改变已验证的 `read_timeout="0"` 与 300s idle 策略。

## 反馈处理

| 反馈 | 判定与动作 |
|---|---|
| [PR #133](https://github.com/linguo2625469/workbuddy2api-panel/pull/133) 无 conversation_id 缓存键回退 | 本地复现不同消息前缀在同账号撞键；修复并增加流式/非流式真实出站 body 回归。 |
| [#134](https://github.com/linguo2625469/workbuddy2api-panel/issues/134) 配置保存 permission denied | 父目录不可写，不是文件本身 chmod 就能解决。默认 Compose 改为可写配置目录；改善诊断、原子保存和并发安全。不静默降级为截断写入。 |
| [#81](https://github.com/linguo2625469/workbuddy2api-panel/issues/81) 工具 ID 问题 | 完整讨论已将初始“历史重复 ID”归因更正为客户端截断并行 ID 后冲突。当前生产日志无相关 11148 证据；不猜测结果配对或重写历史。 |
| [PR #123](https://github.com/linguo2625469/workbuddy2api-panel/pull/123) 原生工具标记还原 | 反馈明确承认尚未证实“中转丢 tools”。本次不把未声明工具的正文提升为可执行调用，不引入弱名单模式。 |
| [#135](https://github.com/linguo2625469/workbuddy2api-panel/issues/135) 跨域回退 | 功能/扣费策略选择，不是裸名默认 CN 的实现 bug；不自动消耗另一域额度。 |
| [#136](https://github.com/linguo2625469/workbuddy2api-panel/issues/136) 生图模型 | 功能请求；本轮不扩张协议和模型目录。 |
| #121/#125/#128、企业版相关已关闭反馈 | wb18 已包含对应上游代码，不重复移植。 |

另核实 fork 的 [Actions 37735574388](https://github.com/juzi8633/workbuddy2api-panel/actions/runs/37735574388)：test 和五平台 build 均成功，publish 的“tag 与源码版本一致性断言”失败。原脚本去掉源码 `-wbN` 后缀，却不去掉 tag 后缀，必然拒绝自己的 tag，反而允许把 wb 产物标成纯上游版本。现保留 wb 后缀，旧 `-panel` 兼容与 `-ci` 演练规则不变；测试执行工作流里的原始 shell 块。

## 实现边界

### 缓存键

- 优先保留显式会话 ID；没有时使用原始请求派生的稳定会话键。客户端显式 `prompt_cache_key` 原样保留。
- `user_id` 请求按既有契约不参与粘性绑定，但现在可独立派生**仅用于缓存**的 `system + 首条 user` 键；不会以 user_id 充当会话 ID，也不会哈希整段历史导致每轮变化。
- 派生在 prompt 改写前完成；追加历史不改键；缺少可派生用户内容仍保留旧空键行为。
- 审查补修：`ChatMeta.ConversationID` 原来同时驱动缓存与 `X-Conversation-ID`，初版回退会给无显式 ID 请求新增会话头。现增加独立 `PromptCacheSessionKey`，仅交给请求体缓存键派生；会话头继续只透传显式值。实际 HTTP 出站测试同时断言 key 与头的存在/缺失，覆盖流式/非流式及 user_id。
- 内容派生不是绝对唯一的会话身份；相同初始前缀可能共用键。有精确隔离需求的客户端应传显式会话 ID/缓存键。

### 配置落盘

- 普通文件：同目录随机唯一临时文件，0600，写完整并 Sync/Close 后 Rename；保存全过程串行化，包含读、合并、落盘和热应用。
- 非 EBUSY 的 rename 失败不会截断原文件；临时文件清理。权限错误说明父目录要求。
- 旧单文件 Docker bind mount 的 EBUSY 兼容保留，明确不具备原子性。原位写入/Sync/Close 失败保留完整临时副本用于人工恢复。
- 默认 Compose 与 README 迁移到 `./config:/app/config`，配置为 `/app/config/config.json`。`login.sh` 支持 CONFIG_PATH，否则优先新目录再兼容旧文件。
- 118 使用 systemd 二进制部署，本轮没有把它迁移到 Docker。

### 请求观测与测试可靠性

- JSONL 新增可选 `error_code` / `error_stage`，仅记录网关固定分类，不保存请求体、原始错误文本或凭证。
- 读取失败区分 `body_read_error` / `body_read_timeout`，stage 为 `read_body`；客户端原有 HTTP 400 / invalid_request 协议不变。
- 早期失败 journal 带 request ID、分类、状态和耗时；成功聊天流水也关联同一 request ID。成功事件省略错误字段。
- 修复模型列表测试的全局负缓存串扰；改用离线假上游；补齐 `cn:` case、reasoning 字段名与第二次请求的真实缓存命中断言。

## 验证证据

- 缓存派生测试：旧代码不同前缀撞键（red）；修复后普通/metadata/user_id、显式 key、流式/非流式、passthrough/custom 全通过（green）。
- 会话头副作用：增加实际出站头断言后，初版回退出现“无显式 ID 却发送派生 X-Conversation-ID”的 red；拆分缓存元数据后 green，显式会话头仍保留，旧只传 ConversationID 的 upstream 调用仍兼容。
- 配置并发/固定临时文件：旧实现 overlay 回归失败，新实现通过。错误注入覆盖 create、write、rename、fallback open/write/short-write/Sync/Close。
- 真实非 root API 烟测（UID/GID 65534、空账号、无生产凭证）：旧布局保存 400 且原文件未变；可写目录保存 200，8 个并发更新全部保留，未知键不丢、权限0600，重复 -config 参数最后一个生效。**这是本地进程/文件权限测试，不声称运行了真实 Docker bind mount**；EBUSY 使用故障注入覆盖。
- 观测测试：旧实现缺 error_code/stage（red）；新实现归档及 journal 可关联、敏感文本不入日志、成功不带错误字段（green）。
- tag 工作流：旧实现正确 wb tag 被拒、纯上游 tag 错误接受（red）；新实现均按预期处理（green）。
- 父代理最终验证：`go vet ./...`、`go test -race -count=3 ./...`（15 个有测试包、4 个无测试包）、模型测试乱序重复 10 次全部通过；Linux amd64/arm64、Windows amd64、macOS amd64/arm64 五平台编译通过，`bash -n login.sh` 通过。
- 原始快照/测试详细输出留在控制端 `/tmp/wb19-logs/` 与 `/tmp/wb19-research/`（敏感快照目录700，不入仓库）；PR与配置测试输出另见 `/tmp/pr133-*.log`、`/tmp/issue134-*.log`。这些临时路径不是永久归档承诺。

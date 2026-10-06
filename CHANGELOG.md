# 变更日志

本 fork 基于上游 [linguo2625469/workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel)
v1.11.11。版本号规则 `<上游版本>-wb<本 fork 第几个发版>`，例如 `1.11.11-wb8`。

- `version` 字段（面板 / `/panel/api/overview`）= 批次身份，同时是静态资源
  `app.js?v=<它>` 的 cache-busting 键 → **每批发版必须变化**。
- `build` 字段 = 日期 + 二进制自身 sha256 前 8 位，由程序自读 `/proc/self/exe`
  算出（见 `internal/panel/buildid.go`）→ 部署后可 `sha256sum wb2api` 逐位核对。

> 为什么不再把特性名堆进版本号：一个串同时当"版本号 / 缓存键 / 变更日志"三件事，
> 结果三件都不合格（太长、同一批内所有构建同值、必须手打维护）。特性清单归本文件。

## wb17 fork 导入与上游同步 · 2026-10-06

保留原本地提交历史及完整 wb17 源码、回归测试、彩排工具，合并上游 main
`a222882`（通过本账号 fork 的 `origin/main`，不向上游推送或提交 PR）。

- 保留低积分冻结、构建指纹与运行时长、积分曲线、模型可用性及 effort 日志去重。
- 纳入上游「全部历史」修复：用量页明确发送 `hours=0`，请求记录仍不设起点。
- 保留上游暂停选号与保号任务口径、模型锁池及配置表单一致性测试。
- 接通上游 `server.read_timeout`：默认 `300s` 总读取上限；显式 `0` 关闭总上限，
  并保留本 fork 的 300 秒读间空闲保护。新增真实连接回归，防空闲续期覆盖配置上限。
- 三方合并去除重复路由、超时辅助函数及测试桩分支，保留两侧回归覆盖。

## wb17 · 2026-10-06 · 跟上游 main 的「模型锁池」视图

上游 `v1.12.0` 之后 main 又走了 4 个提交（`47613d8`），**没有新 tag**，
所以又是「main 领先发版」的常态。其中一个是真实功能：

### b3f92dd 模型锁池（feat）

新增 `internal/pool/modelview.go`：`Pool.ModelLockView()` 按「域 + 模型」聚合
**未过期的模型级限流**，输出 `servable/total`、`locked`、`unlock_at`（最早解锁，
= "何时值得重试"）、`fully_unlock_at`、以及三态：

| state | 含义 |
|---|---|
| `locked` | 全部参与选号的账号都被该模型冷却挡住 → 换模型或等解锁才有用 |
| `starved` | 此刻没号能服务，但**不是**模型冷却造成的（账号级冷却/熔断/在途占满）→ 等一下就好 |
| `partial` | 仍有号能服务，只是部分号被锁 |

与既有 `ModelBlocked`（请求失败时的**单模型**判定）互补：后者回答「这一个模型现在
是不是全池被挡」，本视图是**全清单**，运维不必先失败一次才知道哪个模型不能用。

- `internal/server/handler.go` → `/status` 增 `model_locks` 键（**只增键，零回归**）
- `internal/panel/panel.go` → `/panel/api/overview` 增同键
- `internal/panel/index.html` + `app.js` → 账号池视图内新增「模型锁池」表，无锁时显示「当前没有模型级限流 —— 所有模型均可选」
- 10 个用例（`modelview_test.go`）：空池、三态判定、AuditOnly 剔除、过期剔除、禁用/暂停剔除（**注意这条与 #113 同口径**）、域隔离、排序

### 7339a3c 面板文档 + CFG_MAP 一致性测试（docs/test）

README「Web 管理面板」段与实际界面脱节（写「四个视图」，实际七个），按现状重写并
补 `/panel/api/*` 五组列表。另一个有价值的是 `TestConfigFormMatchesCFGMap`：钉住
「配置表单字段 ↔ `CFG_MAP`」一一对应——这套映射断掉时**既无编译期也无运行期报错**
（表单多字段会被保存时静默丢弃，`CFG_MAP` 多键则回填/保存空转），只能靠人点开配置页发现。

### 移植方式

`git apply --include=<file>` 逐文件应用（`panel.go` 因本 fork 多了 `frozen`/`build`/
`uptime_sec` 等键而冲突，手工插入 `model_locks` 一行）。**注意上游 `overview` 没有
`frozen` 与 `build` 键**——本 fork 的「低积分冻结」卡片与 build 自证依赖它们，不能
被上游版本覆盖。

### 未跟的部分

- `v1.12.0..47613d8` 里除上述两个提交外只剩 merge commit，无其它改动。
- 仍未提交工作区（见 CHANGELOG 末尾的批次说明）。

### 验证

- `go test ./...` 15/15 包全绿；`TestModelLock*` 10 个用例 + `TestConfigFormMatchesCFGMap` 都实跑通过。
- 彩排与生产验证见部署记录。

---

## wb16 · 2026-10-06 · 降档日志去重（纯日志，行为零变化）

### 问题

`normalizeReasoningEffort` 每次改写档位都打一行日志。降档是按**模型能力**决定的，
档位组合是有限小集合，于是生产上呈现出「同一句话刷满 journal」：

```
$ journalctl -u workbuddy2api --since "13h ago" | grep -c "reasoning_effort downgraded"
1368            # 同期请求 1373 条 → 99.6%
$ ... | grep -oE "medium -> low" | sort -u
medium -> low   # 唯一的组合
```

claude-cli 对 `deepseek-v4.1-flash` 固定发 `medium`，而该模型 `supportedEfforts`
不含 medium → 每请求降一次。**降档本身是对的**（正是 `normalizeReasoningEffort`
的职责，也是上游要求的档位合规），要改的只是日志：1368 行噪音把有价值的**请求行**
挤出视野，排查真问题时得先翻过几千行。

### 改动

`internal/upstream/payload.go` 新增进程级去重表与 `logEffortRewriteOnce`：

```go
var effortRewriteLogged sync.Map   // key = kind|model|from|to

func logEffortRewriteOnce(kind, model, from, to string) { ... LoadOrStore ... }
```

- 去重键是「**方向 + 模型 + 请求档 + 实际档**」四元组，所以换模型、换请求档、
  换实际档、或上游调整 `supportedEfforts` 导致映射变化，**都会重新打印一次**；
  丢掉的只有同一组合的**重复**。有信息量的变化一条都不丢。
- 同组合**首次出现必然打印**，不存在「静默改写」。
- `downgraded` 与 `floored`（全部支持档高于请求档）两条路径都走它。
- 日志文案补一句「（同组合本进程内只打印一次）」，免得运维误以为降档只发生过一次。

### 测试

`TestEffortRewriteLoggedOnce`（`payload_test.go`）捕获标准 logger 输出，跑 5 轮，
断言四件事：

| 模型 | supportedEfforts | 请求档 | 期望打印次数 |
|---|---|---|---|
| m-a | `[low]` | medium | **1**（降级，去重） |
| m-b | `[low]` | medium | **1**（同映射不同模型 → 各自打） |
| m-c | `[high]` | low | **1**（floored 路径同样去重且要打） |
| m-d | `[low, medium]` | medium | **0**（档位被支持 → 不改写不打印） |

**红测确认**：把 `logEffortRewriteOnce` 换回裸 `log.Printf` 后三行断言立即 FAIL
（`m-a 打印 5 次, want 1`）。

配套加了测试辅助 `logCapture`（`log.SetOutput` 到带锁 buffer）——该包此前没有
任何捕获日志的手段。

**踩坑记录**：去重表是**进程级**的（这正是它的目的：跨请求生效），所以测试必须
自己清空它，否则 `go test -count=2` 第二轮一条都不打，「必须打印」的反向断言会
假失败。用 `-count=3` 复现并修复。

### 影响

- **行为零变化**：出站请求体逐字节不变，只是日志行数从「每请求一行」变成
  「每组合一行」。生产 13h 实测从 1368 行降到 **1 行**。
- 无配置项，无导出 API 变化。

### 验证

- `go test ./...` 15/15 包全绿；`go test -count=3 ./internal/upstream/` 亦绿。
- 彩排 + 生产验证见部署记录。

---

## wb15 · 2026-10-06 · 跟进上游 v1.12.0

上游在 v1.11.11 之后又发了 **v1.12.0**（tag `v1.12.0` = `9478287`）。wb14 的基线是
`ea3a51c`（= PR #113 的 merge），`v1.12.0` 只比它多两个提交，其中一个正是对 #113 的修正。

### 唯一的真实功能缺口：夜猫子仍会跑暂停号

`b1a2284` 的精髓不在「旅行/成长照常」（本树早就没给它们加跳过），而在这一行：

```go
// internal/scheduler/blackcat.go
-		if st.Disabled {
+		if st.Disabled || st.Paused {
```

`RunNightChats` 逐条 `ChatStream` 发真实 **glm-5.2 对话**，是任务体系中唯一
「整任务都是模型对话」的。暂停号参与它，等于「让位防风控」的账号仍在同一出口 IP
发模型对话——正是 #113 要消灭的东西。wb14 漏掉了这里，wb15 补上。

判定方法（值得记）：**不看自己的源码，去看上游 tag 的源码**。
`git show v1.12.0:internal/scheduler/blackcat.go` 与工作区逐字节对比即可定位缺口；
反过来只看自己的 diff 会误以为「上游 cherry-pick 时把 paused 都跳过了」。

### 其余「差异」都是文档/注释，功能已等价

逐文件对比 `v1.12.0` 与本树，除 blackcat 外无功能差异：

| 文件 | 结论 |
|---|---|
| `internal/panel/taskcenter.go` | **逐字节相同**（两处 `st.Disabled || st.Paused` 在本树已是 `st.Disabled`） |
| `internal/scheduler/travel.go` | 仅函数头注释（本树未给 paused 加跳过） |
| `internal/scheduler/scheduler_test.go` | 本树缺 `travelCalls` 字段与 `TestPausedStillTravels`（已补） |
| `README.md` | 本树 README 停在 v1.11.11 口径（已补三处，见下） |
| `internal/pool/*` | `realm.go` 与 v1.12.0 逐字节相同 → **`prefer_expiring` 在本树本来就是加权（×3 虚拟实例）而非排序**，是 v1.11.11 的 README 文案陈旧；本树 README 同处文案也陈旧，已顺手更正 |

### README 补三处

1. **「暂停选号」章节**（v1.12.0 原文）：此前 README 只描述了全局开关
   `include_disabled_in_tasks`，没写账号级 `paused`，面板上的按钮无从对照。
2. **`include_disabled_in_tasks` 章节末补一段**：明确 `paused` **不**受该开关约束
   （保号四任务无条件照跑，旅行/成长照常，仅夜猫子跳过）——否则读者会把两个开关混为一谈。
3. **`server.read_timeout` 行**（v1.12.0 有、本树缺）：`300s`，`0` = 不限制，改动需重启（#100）。

### 刻意未跟的两处

- `v1.12.0` 的 `internal/pool/pick.go` 与 `README.md` 里 `prefer_expiring` 的**文案**：
  已确认本树实现就是加权版，只补了 README 表格与算法段落的描述，不动代码。
- 本树 `prefer_expiring` 的 `pickEarliestExpiryLocked` 里多一个 `frozen` 排除
  （wb2 冻结态引入）：**本 fork 特有、比上游更严**，保留。

### 验证

- `go test ./...` 15/15 包全绿。
- 新增 `TestBlackcatSkipsPausedAccount`（fake 对每号下发 `black_cat` 待办 + 统计
  `/v2/chat/completions` 次数）。**做过红测确认**：把 `blackcat.go` 的过滤改回
  `if st.Disabled {` 后立即 FAIL——`夜猫子对话次数=2 want 1`，u1 与暂停号 u2 都被
  拉出来发对话，正是本批要消灭的行为。禁用号 u3 作对照，两版都不出现。
- 彩排实例（生产机端口 7872，真实 5 账号）：`version=1.12.0-wb15`、18 个模型、断言全过、回收净空。

---

## wb14 · 2026-10-05 · 并入上游 v1.11.11 之后合并的 8 个 PR

上游 release 停在 v1.11.11（2026-10-01），但 main 已合并 8 个 PR 未进 release。
本批把它们补进 wb13 分支（`/tmp/work/S`，remote `origin/main` = `ea3a51c`）。

| PR | 提交 | 内容 | 落地方式 |
|---|---|---|---|
| #113 | `fd835df` | 池新增 `paused`「暂停选号」状态（退出选号但照常保号） | `patch -p1 --fuzz=3`，1 个 hunk 手工落位（见下） |
| #111 | `c9986f9` | `include_disabled_in_tasks` 保号任务覆盖已禁用账号（issue #110） | 干净应用 |
| #115 | `8b18ab8` | 吐字速率扣除首 token 等待（issue #34） | 干净应用 |
| #108 | `1c96d5e` | GPT 系 `max_tokens` 抬到下限 16，修 Claude Code 切 gpt-6-sol 必 503 | 干净应用 |
| #107 | `44ca8ff` | global 目录补桌面端 UA 主路（gpt-6-sol 等不再漏） | `global_models.go` + 新测试文件；`client.go` 已等价（见下） |
| #117 | `10e17ef` | 覆盖型配置字段可清空 + `upstream.user_agent` 重启提示 | `main.go`/`app.js` + 新前端测试 |
| #114 | `7d462bb` | reqlog `ReadArchive` 按事件时间排序 | **wb13 已含**，不重复应用 |
| #106 | `20bde29` | `#keyInput` 放独立 form 修 Chromium 凭证表单 | **wb13 已等价**（`form autocomplete="on"` 包裹 + `name=wb2api_key`，见下） |

已等价、不重复移植的上游项：#99 bad_params fail-fast（wb8）、#116 model-blocked
（wb13 自研等价，`internal/pool/cooldown.go` 的 `ModelBlocked()`）、#93（wb1）。

### #113 的一处落位修正

`patch` 只对着 `disabled || !e.until.IsZero()` 这行做上下文匹配，而本 fork 的
`modelExempt()` 早已是 `disabled || e.frozen || ...`（wb2 加的冻结态）——模糊匹配
把 hunk 塞进了函数体之外，`go build` 直接报
`entry.go:445: syntax error: non-declaration statement outside function body`。
修正后 `modelExempt()` 为 `disabled || paused || frozen || ...`，`Paused` 字段落回
`stateAccount` 内 `Disabled` 之后。教训：`--fuzz` 接受的是上下文相似，不是语义正确；
每个 hunk 落位后必须编译。

### #107 与 #106 为什么不按上游原文落

- `client.go`：本 fork 的 `nonChatModel` 已独立实现同一件事，且比上游更严——
  按标签逐个 `strings.ToLower(strings.TrimSpace(t))` 后 `switch`，覆盖
  `text-to-image` / `text-to-video` / `image-to-video`（上游是 `t == "text-to-image"`）。
  重复移植会把本 fork 的 trim/大小写归一化退回原样，故保留本地实现。
- `index.html`：上游把 `#keyInput` 包进 `<form style="display:contents" onsubmit="return false">`
  且 `autocomplete="off"`；本 fork 的 wb5（issue #105）已经包了
  `<form autocomplete="on" onsubmit="return false">` 并加了 `name="wb2api_key"`，
  是同一问题的更完整解法（显式 name 让 Chromium 不再去"找"搜索框配对）。保持现状。

### 验证

- `go test ./...` 15/15 包全绿。
- 彩排实例（生产机隔离端口 7871，真实 5 账号）：
  - `build=wb2026.10.05+a1f0eced` 与二进制 sha256 前 8 位一致；
  - `/v1/models` 19 个（目录口径未变）、`Cache-Control: no-store` 仍在；
  - `POST .../pause` → `/healthz` healthy 5 → **4** → `resume` → 恢复 **5**
    （paused 退出选号但不影响 `total`，正是设计语义）；
  - `paused` 号在 state.json 里只落 `paused` 标志，不写冷却/熔断字段。

---


## wb13 · 2026-10-05 · 模型级阻塞不再伪装成「没有可用账号」

**上游 PR #116（未合并）的等价实现 + 两处按本 fork 纪律的修正。**

### 问题（上游 issue #102 附带发现 1）

选号返回 nil 有两种**成因相反**的情况，此前客户端拿到的都是同一个
`503 no_healthy_account`：

| 真实成因 | 客户端该怎么办 | 此前显示 |
|---|---|---|
| 池子真没可用号（账号级冷却/在途占满/积分保底） | **等** | `no_healthy_account` ✅ |
| 号都在，但**每个号都对这个模型**处于模型级冷却（11102/6004） | **换模型**（换账号无用） | `no_healthy_account` ❌ |

后者被读成「服务端过载」，客户端据此无脑重试（#81 就是 503 → 自动重试 5 次），
而正确处置是换模型。hint 也把排查引向账号池。

**丢信息的时序**：首次请求还能看到上游原文（`lastErr` 非空）；一旦负缓存写入，
后续请求 `lastErr` 为空，就只剩「池子没号」——上游原文与解封时间全丢。

### 改动

- `internal/pool/cooldown.go`：新增 `ModelBlocked()` 查询（此前只有写入没有查询）
  + `ModelBlockStatus`。口径与选号一致，用 `modelCooled` 以尊重 `AuditOnly`；
  **任一账号仍能服务即返回 `Blocked=false`**（防误报把用户赶去换模型）。
- `internal/server/handler.go`：`acct==nil` 时取一次快照；末端据此输出
  `400 model_unavailable`，并在 `ue.Kind == ErrModelBlocked` 时同样归类（覆盖
  上游直接返回 11102 的路径，此前该分支的 `code` 停在 `no_healthy_account`）。
- `internal/upstream/hint.go`：新增 `ModelBlockedHint()`。

### 与上游 PR #116 的两处差异（本 fork 纪律）

1. **hint 用中文**。PR #116 写的是英文
   （`model_blocked: %d account(s) cooling down this model; earliest unblock at ...`），
   违反 `hint.go` 文件头明写的纪律（「全部 hint 用中文」——面板与错误文案的主要
   读者是中文用户）。改为「该模型在所有账号上均处于冷却（模型级不可用，换账号无用）；
   N 个账号被挡；最早解封 MM-DD HH:MM」。
2. **不把上游原文塞进 hint**。PR #116 在 hint 末尾追加 `; upstream: <原文>`，
   但 `message` 已经透传了上游原文（含 requestId），重复一遍既冗余又违反
   「hint 只做并列补充说明」的纪律。

时间格式也从 `RFC3339`（`2026-10-05T13:20:00+08:00`）改为 `01-02 15:04`
（`10-05 13:20`）：hint 是给人看的，秒与年份在这里没有信息量。

### 测试

- `internal/pool/modelblocked_test.go`（6 例，沿自 PR #116）：全池冷却 / 有一号仍可服务 /
  禁用号不计 / AuditOnly 不拦 / 已过期不拦 / 空输入。
- `internal/server/model_blocked_test.go`（4 例，本 fork 新写）：
  ① 全池冷却 → **400 + model_unavailable**，且 hint 不含「池中没有可用的健康账号」，
     且**上游零触达**（选号阶段就拦下）；② hint 带出账号数与最早解封；
  ③ 还有账号能服务时**不得**报 model_unavailable（反向用例）；
  ④ hint 与 `NoHealthyAccountHint()` 必须不同且为中文。
- **红测确认**：把 `ModelBlocked()` 快照改回恒零值（= 修复前行为）后，
  ① ② 两例立即 FAIL，失败信息正是 `code=503 want 400` +
  `gateway_hint="池中没有可用的健康账号"` —— 即本批要消灭的那个 bug。

### 验证

- `go test ./...` 15/15 包全绿（无 flaky）。
- 三次构建 sha256 一致：`1f3acdfcab7ab4e8`（+17456 字节）。
- `scripts/rehearse.py` 隔离彩排（3 假账号）：

```
[断言] /v1/models 共 12 个；面板标记 ['cn:glm-5.2']
       rate_limited(model_unavailable) 条目 3 条
✓ 面板已标记: cn:glm-5.2        全部断言通过
```

真实请求（彩排实例，glm-5.2 全池被注入 11102 冷却）：

```json
{"error":{"code":"model_unavailable",
 "message":"model is unavailable on every account (per-model cooldown), try another model",
 "gateway_hint":"该模型在所有账号上均处于冷却（模型级不可用，换账号无用）；3 个账号被挡；最早解封 10-05 13:20"}}
```

对照组（未被注入冷却的模型）仍走 `no_healthy_account` —— 未误伤。

---

## wb12 · 2026-10-05 · 上游超时的 gateway_hint 不再指错排查方向

`internal/server/handler.go` 的末端错误映射里，`errors.Is(lastErr, errUpstreamTimeout)`
分支只改了 `code` 与 `msg`，**`gateway_hint` 仍是默认的
`NoHealthyAccountHint()`（「池中没有可用的健康账号；请查看 /status 或稍后重试」）**。

于是上游停摆时，客户端/运维拿到的提示把排查引向**账号池**，而真实原因在上游侧 ——
而且 `isUpstreamTimeout` 的止损分支**不冷却、不熔断、不 NoteError**，池里
healthy/cooling 都是干净的，看 `/status` 只会看到"一切正常"。

修复：`upstream` 包新增 `UpstreamTimeoutHint()`
（「上游响应超时，网关已停止轮转（换号会撞上同一个慢上游）；账号未被罚分，请稍后重试或查看 /status」），
末端在超时分支优先使用它；其余 Kind 走 `hintOf`，本地调度错误仍用
`NoHealthyAccountHint()`。

两条测试钉住：`hint_test.go` 断言新 hint 中文化且**不与** `NoHealthyAccountHint()`
相同；`stream_outcome_test.go` 的端到端用例断言 `upstream_timeout` 响应体里
**不含**「池中没有可用的健康账号」，且带 `gateway_hint` 字段。

---

## wb11 · 2026-10-05 · 网关 JSON 响应也显式禁缓存

wb10 给面板（`/panel/*`）统一加了 `Cache-Control: no-store`，但漏了**网关自己的
JSON 出口**：实测 `/v1/models` 与 `/v1/chat/completions` 的响应**完全没有
`Cache-Control`**。

这两类都是客户端会直接消费的实时数据，尤其 `/v1/models` 的内容随上游目录与
可用性筛选变化，缓存住就是"模型列表过期"。修复位置：`internal/server/handler.go`
的 `writeJSON`（`writeOpenAIError` / `writeOpenAIErrorHint` 都经它）。

新增 `TestGatewayResponsesDisabledCaching` 覆盖 `/v1/models` 的 200 与非法 JSON 的
错误响应两条出口。

**实测 CF 行为**（我们经 Cloudflare Tunnel 暴露，tunnel 用 token 运行、入口规则在
CF 控制台，本地无配置文件）。逐个路径实测，CF 的处理是**分路径的**：

| 路径 | 本地发出 | 经 CF 客户端收到 | cf-cache-status |
|---|---|---|---|
| `/panel/*`（含 app.js、页面） | `no-store` | **`no-cache`** | DYNAMIC |
| `/v1/models` | `no-store` | **`no-store`**（原样透传） | DYNAMIC |

（同一路径连测 3 次结果稳定，不是抖动。`panel/*` 被改写而对 `/v1/*` 原样透传，
指向 CF 控制台里按路径配置的规则。）

**这不构成问题**：`no-cache` 与 `no-store` 一样要求"使用前必须回源校验"，而我们的
响应没有 ETag/Last-Modified，实际效果就是每次都回源 —— 即"不可缓存"的语义成立。
本项的价值是**消除"完全无指令"这个可被启发式缓存的窗口**，而非对抗 CF。

真正保证面板不吃旧资源的仍是版本化 `app.js?v=<version>`（wb5 引入），缓存头只是
补充 —— 两者都在，互为冗余。

---

## wb10.1 · 2026-10-05 · 彩排工具回收后断言净空

`scripts/rehearse.py` 的 `down` 原本**只打印警告就返回**：PID 文件丢失、端口被别的
进程接管、或 SIGTERM 被忽略时，工具会"看起来回收成功"却留下占号野进程 —— 正是这个
工具当初要消灭的问题。现在改为断言：

- 端口未释放 / PID 文件残留 / `--rm` 后目录仍在 → 逐条 `fail` 并 **`sys.exit(1)`**；
- 回收干净时打印 `✓ 回收净空`；
- `down` 结束时删除本次运行的 PID 文件（否则 `restart` 路径 down→up 会自己踩到断言）；
- `cmd_run` 的 `finally` 捕获这个 `SystemExit`：不吞掉已得到的断言结果，但把"回收不净"
  记为退出码 2（环境问题），与断言失败（1）区分。

四个场景实测（本机模拟）：

| 场景 | 期望 | 实测 |
|---|---|---|
| 正常回收 | 0 | 0 ✓ |
| PID 文件被删、进程还活着（端口兜底救回） | 0 | 0 ✓ |
| PID 文件残留（进程已死） | 1 | 1 ✓ |
| 端口被忽略 SIGTERM 的进程占住 | 1 | 1 ✓ |

另：**验证了工具本身可用**。此前我以为孤儿实例是工具缺陷，实际是我手写的一次性
`/tmp/*.sh` 脚本在用 `ss … | grep pid= | kill` 取空 PID 时静默跳过造成的。工具实测
自动取生产 auths/state、隔离 listen/data_dir/auth_dir/**state_file**、关全部排程、
PID 落盘精确回收，全对。

---

## wb10 · 2026-10-05 · 归档排序修复 / 视频模型过滤 / 面板禁用缓存

三件独立的小修，都是"上游已修而我们没有"或"同类问题的漏项"。

### ① 归档读取按事件时间排序（移植上游 PR #114）

`internal/reqlog/reqlog.go` 的 `read()` 原本把**归档文件的 mtime 升序**当作事件时间
升序，再"取尾部 limit 条 + 反转"。三个原因使它不可靠：

1. `writeEvent` 经 64KB `bufio` 缓冲，轮转时才 `closeFile` 落盘；同一内核时间戳粒度
   （Linux 常见 ~1ms）内写出的多个文件 mtime 可能**完全相同**，而 `sort.Slice` 非稳定。
2. 字典序上 `requests-<day>.1.jsonl < … < requests-<day>.jsonl`，装着最早事件的基准
   文件排在读取序列**最末**，于是"最旧"被当成最近记录。
3. 归档目录被整体拷贝 / 恢复备份后 mtime 更不可信。

修复：文件读取顺序不再参与语义，读完一律
`sort.SliceStable(out, 按 Time 倒序)` 再取前 limit 条。

**这同时修掉了仓库里那个长期红着的测试** `TestArchiveRotationReadAndFilter`。
此前我们在多轮报告里把它记为"上游历来就坏的 flaky"——**这个判断是错的**，
它是真 bug（在本机和官方 v1.11.11 基线上 3/3 必失败）。本次新增
`TestReadArchiveOrdersByEventTimeNotFileMtime`：把所有归档文件的 mtime 改成与事件
时间**相反**的顺序，钉住"只能按事件时间排序"。

生产未受影响：面板「运行日志」走 `/panel/api/request_logs`，其顺序实测本就正确。

### ② 生成类模型过滤补视频标签（取自上游 PR #107 的非 UA 部分）

`internal/upstream/nonChatModel` 原本只按 `text-to-image` 过滤生成模型。上游桌面端
目录实测另有 `text-to-video` / `image-to-video`（seedance 系），作为对话模型选上去
只会报 11102。本次一并过滤，并用 `EqualFold` 语义容错（标签大小写/空白不保证规范）。
本函数 CN 与 global 共用，两域同时生效。

**注意**：PR #107 的主体（global 目录补桌面端 UA 主路）**未采用** —— 与 wb9 的决定
一致（cn 域账号、且目录 UA 只控制"看得见"而账号权益控制"用得了"）。

### ③ 面板全部响应显式 `Cache-Control: no-store`

**问题**：`/panel/api/*` 的所有响应都没有 `Cache-Control`。静态资源
（`index.html` / `app.js`）早在 wb5 修 issue #105 时就设了 `no-store`，**API 这条漏了**
——而 API 返回的是余额 / 用量 / 积分 / 模型目录等实时数据，URL 固定且无
ETag/Last-Modified，浏览器会按启发式规则自行缓存，表现为「已清理的条目仍在面板上
显示 / 余额是旧值」。

**修复位置**：统一设在 `Panel.ServeHTTP`，而不是 `writeJSON`。因为 mux 自产的
**404 不经过 `writeJSON`**（测试实测 `Cache-Control=""`），只在 `writeJSON` 加会漏。

新增 `TestPanelAPIResponsesDisabledCaching` 覆盖 401 与 404 两个非 200 出口。

---

**影响面**：`/v1/models` 与目录不变（仍 19 个）；无配置项变化；前三项的运行时行为
只有"更高"没有"更宽"（多的排序是修正、多的过滤是收紧、多的响应头只禁缓存）。

## wb9 · 2026-10-04 · 模型目录回退到官方 IDE UA（19 个）

**动机**：wb6 把 `/v3/config` 的目录探测从"只用官方 IDE UA"改成"IDE + 桌面端 + CLI
三路并发取并集"，目录 19 → 48，用以回应 issue #43/#102/#26/#15 的「模型目录不全」。
代价是目录里必然混入一批**任何 UA、任何账号都调不通**的条目。本次回退。

**实测依据**（隔离实例，5 个真实账号 + 真实余额，逐条 `max_tokens=5` 调用；
负缓存先清空以免干扰）：

| 探测组合 | 目录数 | 实测可用 | 可用率 |
|---|---|---|---|
| **只留 IDE**（本次采纳） | 19 | **14** | **100%** |
| IDE + CLI | 38 | 25 | 66% |
| 三路并集（wb6～wb8） | 48 | 36 | 75% |

三路并集多出 22 个可用模型，但恒定混入这 12 个必然 503 的条目：`glm-4.6` /
`glm-4.6v` / `glm-4.7` / `glm-5.0` / `kimi-k2-thinking` / `kimi-k2-instruct-taiji` /
`minimax-m2.5` / `default-1.1` / `default-1.2` / `deepseek-v3-1-volc` /
`deepseek-r1-0528` / `hunyuan-image-alpha-edit`。

**A/B 排除"换个 UA 就能用"**：同一份 state，聊天出站 UA 分别钉成官方 IDE UA 与
桌面端 UA，上述 12 个逐条结果**完全相同**（全部 503 `no_healthy_account`）。结论：
它们的可用性由**账号权益**决定，与出站身份无关 —— 目录 UA 只控制"看得见"。

**改动**：`internal/upstream/client.go` 的 `fetchV3Models` 恢复单路
`fetchV3ConfigModelMap(a, codeBuddyIDEUA)`，并保留稳定排序（map 遍历无序）。
`desktopUA` 常量**不删** —— `/v2/report` 等桌面行为上报仍需它（`desktop.go`）。
global 侧（`global_models.go`）的 IDE+CLI 合并不动。

**测试**：`TestFetchV3ModelsUnionsUserAgentFamilies` → `TestFetchV3ModelsProbesIDEFamilyOnly`
（把"只探一路"钉成契约，并断言桌面端/CLI 的模型 id 即使出现在响应里也不得进入结果）；
`TestFetchV3ModelsToleratesSingleUAFailure` → `TestFetchV3ModelsIDEFailureDegradesToEnterprise`
（单路语义下 IDE 失败只能降级到企业端点）；`TestFetchV3ModelsAllUAFail` →
`TestFetchV3ModelsAllSourcesFail`；`TestFetchModelsUsesConfiguredUA` 与
`TestModelsNegativeCacheOnFetchFailure` 的探针计数 4 → 2。

**影响**：`/v1/models` 与面板目录 48 → 19（面板其中 5 个非对话条目由 `nonChatModel`
过滤，实际输出 14）。**可用模型 36 → 14**，这是本次回退主动接受的代价。
若将来上游真按账号权益下发桌面/CLI 目录（而非当前的一刀切 11102），可重新评估。

---

## wb8 · 2026-10-04 · 模型可用性判据 / 选项持久化 / 彩排工具

**模型可用性（`/v1/models` 剔除 + 面板标记）**

- 判据从"池内**全员**有 11102 证据"放宽为 `Blocked*2 >= Total && Healthy == 0`。
  原判据只能覆盖 4/13：负缓存覆盖面由 `MaxRotate`（=3）决定，一次失败尝试只写
  3 个账号的条目，5 号池永远凑不齐"全员"。实测 11102 是**模型级**（补测未命中的
  账号全部 11102），故"过半数"已是足够证据。
- 新增正面证据否决项 `Healthy`：任何账号与上游**成功**交互过该模型
  （`modelCost` 只在成功路径写入、6h TTL）即不剔除。这是放宽阈值的安全阀。
- 注意不要加 `Available == 0`：`Healthy==0` 时 `Available ≡ Total-Blocked`，
  要求它等于要求 `Blocked==Total`，即把判据退回"全员"（第一版实际写错过）。
- 修复 `stateModelCooldown` 未持久化 `Hits`：每次重启把 11102 指数退避打回第一档
  （6h），死模型永远停在最低档、退避形同虚设。
- `Pool.load()` 解析失败时打 WARN（原先静默 `return` → 整个 state 被丢弃而日志
  只留一行正常的"恢复来源=本地 state.json"）。

**CN 模型目录**

- `/v1/models` 列出三路 UA 并集（修复上游 issue #102/#43）：IDE UA 只给 19 个，
  桌面端 54 个、CLI 32 个。

**面板**

- 静态资源 `Cache-Control: no-store` + `app.js?v=<version>`（修 issue #105：
  发版后浏览器仍用旧 app.js）。
- 路由路径到期判据优先 `DeductionEndTime`（新增 `packageExpiry()`），与面板路径
  统一口径（issues #23/#101）。
- 用量时序图带逐桶积分与橙色积分折线（issue #58）。

**网关**

- 11101 `ErrBadParams` 改为轮转循环内 fail-fast 400 透传（上游 PR #99），
  不再轮转全部账号后才 503。
- `readBody` + `ReadTimeout=0`（修 issue #100：大请求体 60s 超时）。
- 低积分自动冻结 `freeze_threshold`（移植 Smirk1921 fork）：frozen 是与 disabled
  正交的第三态，冻结账号**仍参与签到**，因此余额恢复即自动解冻 —— 比"禁用账号
  跳过所有保号任务"（issue #110）更安全。

**工具**

- `scripts/rehearse.py`：彩排工具（独立实例 + 预检），把"脚本写错→误判产品 bug"
  变成启动前硬失败。`scripts/rehearse_fixture.py` 生成离线假账号 fixture。

## wb2–wb7 · 2026-10-03 ~ 10-04 · 内部批次

（早期批次未逐版记录，改动已并入 wb8 的条目；这些批次的成员时点见
`/root/wb2api-backup/` 下的 bundle README。）

- wb7 = `+filter+hits`：首次实现模型可用性剔除（全员判据）+ `Hits` 持久化。
- wb6 = `+catalog`：三路 UA 并集目录。
- wb5 = `+credits`：用量图积分折线。
- wb4 = `+expiry`：路由路径到期口径统一。
- wb3 = `+cache`：静态资源禁缓存 + 版本化。
- wb2 = `+freeze`：低积分自动冻结。
- wb1 = `panel`：官方 v1.11.11 + 自研 fix2（`readBody` / `ReadTimeout=0` 等）。
  **注**：wb1 与"wb1 + PR #93"两个**不同**的二进制曾共用同一版本串
  `1.11.11-panel` —— 这正是本次改名的动因之一。

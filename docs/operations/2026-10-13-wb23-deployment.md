# wb23 部署记录（2026-10-13）

## 部署方式

本次按用户要求**从 CI 镜像取产物**部署（`ghcr.io/juzi8633/workbuddy2api-panel@sha256:02830e91…`，
由 `.github/workflows/docker-ghcr.yml` 在 `c07902c` 上构建，run 结论 success）。该镜像
内含 `/app/wb2api`，本机 `docker create` + `docker cp` 取出后按既有 systemd 方式替换——
**不是**用 compose 起容器：目标主机 118.145.237.3 没有 docker/containerd（已确认无
`/usr/bin/docker`、无 `/var/lib/docker`），生产一直是 systemd 直接跑二进制。

校验了两者语义一致：CI 产物 `18310bf6…`（`-trimpath`）与本地编译 `dddba8e5…` 内容不同
属预期，`strings` 均报告 `1.13.0-wb23`；CI 产物在服务器上以生产用户跑过隔离烟测。

> 顺带核对：**emby-lite-go 没有可部署的新镜像**——`v0.1.31` 与 `latest` 都解析到
> `sha256:f252c62e7f…`，正是 159 当前运行的 digest（见记忆 #1325）。故本批只涉及
> workbuddy2api。

## 部署结果

- 主机：118.145.237.3，systemd `workbuddy2api`，运行用户 `wb2api`。
- 2026-10-13 14:56:54 UTC / 22:56:54 CST 切换。
- 源码提交：`c07902c7f755ce9df8a7f742d0e26a5688eb0d89`，已推送 fork `main`。
- 版本：`1.13.0-wb23`；build：`wb2026.10.13+18310bf6`。
- 二进制 SHA256：`18310bf65884db418a6cbf7c52172b613afc76f8666a5ccea69bb46329f63993`；
  磁盘程序与 `/proc/<MainPID>/exe` 一致（MainPID 420529）。
- 配置 SHA256 前后一致（`7f283d0b…`），保留 `server.read_timeout="0"`；账号与状态保留。
- 备份/回滚目录 `/root/wb2api-backup/wb23-20261013T145654Z/`，旧程序为 `wb2api.pre-wb23`
  （SHA256 `a9555fc10c17fdf437406af696389cf0a035bbf0514066dd56b7420c1a52765b`，即 wb22）。

**本次切换瞬间在途 = 0**（8/8 可用、无在途），是罕见的理想窗口，未截断任何长流——
与前两次（wb21/wb22 在途均为 3）不同，本批**可以**说没有因切换中断在途请求。

## 本批内容

三项实测问题，详见 CHANGELOG「wb23」段：过期积分偏好反向（#160）、请求体读取失败无法
归因、只有 access token 的账号无法导入（#165，含上游建议四条）。

## 部署后验证

- 真实上游烟测（合成，`X-Smoke: wb23-ci-deploy`）：HTTP 200、`finish_reason=stop`、
  正文非空、约 26s（该模型思考较长，见记忆 #856）。
- **新归因字段在生产上生效**：合成一次「客户端声明大 body 后立刻断开」，journal 与归档
  均为
  `WARN: [server] request=req-a1fb0f803fa3354c stage=read_body code=body_read_error detail=peer_closed status=400 duration_ms=404`
  ——这正是此前缺失的那一层（56 次失败全部只有 `body_read_error`，无法区分谁断的）。
  归档同时确认**未泄漏**原始错误文本（无 `connection reset by peer` / `i/o timeout` / 地址）。
- 切换后 22 条事件：成功 19、失败 3（一条是合成断连烟测，一条缺 key 的 401，见下）。
- 账号 8/8 可用，`NRestarts=0`，journal 无 panic。

## 未解决：axonhub 的 503 被误报为 no_healthy_account（**先于本次部署存在**）

切换后仍有一条真实失败：

```
22:57:59  axonhub/1.0  503  code=no_healthy_account  duration_ms=29649
```

排查结论：**与本批改动无关，是既有缺陷**，且与已修的读取归因同源。

- 按天统计（axonhub 专门一类客户端）：失败同时出现在 `2026-10-11`（42+8）与
  `2026-10-13`（9+6），远早于 22:56:54 的切换；部署日 6 次 `no_healthy_account` 中 5 次在切换前。
- 形态高度一致：`no_healthy_account` 14 条时长 **29.3–30.1s**（中位 29.8），
  `body_read_error` 51 条 **23.2–30.1s**（中位 29.5）——同一客户端、同一 ~30s 量级。
- 机制推断：`axonhub/1.0` 自身有约 30s 超时。超时落在**上传请求体**阶段 → 记
  `body_read_error`；落在**等响应**阶段 → 客户端断开使 `r.Context()` 取消，请求 ctx 取消
  又**不是** `isUpstreamTimeout` 的「客户端已离开」态……最终走到末端，`lastErr` 非
  `errUpstreamTimeout` 也非 `*upstream.Error`，于是落到默认的
  `code=no_healthy_account` / 「池中没有可用的健康账号」文案。
- 危害：文案把排查引向账号池（余额、熔断），而真实原因是**该客户端自己放弃**
  （服务端 `context canceled`、账号未被罚）。这与刚刚修好的读取归因是**同一类问题**：
  末态分类没有区分「谁先走的」。

**若要修**（未做，避免在部署记录里夹带未验证改动）：给该路径加一条与
`error_detail` 同族的分类——`client_canceled`（`r.Context().Err() != nil`）应返回可区分
的 code（如 `client_canceled`，HTTP 499 语义或 503 但换文案），而不是
`no_healthy_account`。需要先写复现用例，再动 `handler.go` 末端分类。

## 已知限制

- 短期样本（切换后约 4 分钟、22 条事件），不扩大为长期稳定性结论。
- 上述 axonhub 误报仍在，属既有问题。

# wb22 部署记录（2026-10-13）

## 部署结果

- 主机：118.145.237.3，systemd `workbuddy2api`，运行用户 `wb2api`。
- 2026-10-13 13:26:18 UTC / 21:26:18 CST 开始切换，13:26:18 UTC 完成验证。
- 源码提交：`13ee3345fb609864ddd6a5cfc1458335b15b05ef`，已推送到 fork `main`。
- 版本：`1.13.0-wb22`；build：`wb2026.10.13+a9555fc1`。
- 二进制 SHA256：`a9555fc10c17fdf437406af696389cf0a035bbf0514066dd56b7420c1a52765b`；磁盘程序与 `/proc/<MainPID>/exe` 均匹配。
- 构建：Go 1.23.4，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，`-ldflags '-s -w'`，`vcs.modified=false`。
- 配置 SHA256 前后一致，保留 `server.read_timeout="0"`；账号、用量桶与运行状态保留。

## 本批内容

修复「可用」标签旁挂着旧限流文案的问题。

生产实测：账号 `28f60c37` 的 `state.json` 中 `until` 停在 `2026-10-08T10:33:51+08:00`、`reason` 仍为 `429 rate limit`，而 `cooling=false`、`last_success` 是当天——界面因此同时显示「可用」和「429 rate limit」。

根因是 `e.reason` 没有过期钩子：冷却到期只是 `now.Before(e.until)` 的时间比较，不是事件；`NoteSuccess` 也刻意不清理 `reason`（它清的是失败/熔断计数）。因此冷却结束后原文长期残留，而面板标签只按倒计时字段判定，两者口径不一致。

修法：`pool.Status` 只在**这一行不是「可用」**时透出 `Reason`——禁用（终态）、冷却中（until/熔断期未过）、暂停选号、低积分冻结、连败降权期未过。冷却自然到期后不再对外暴露。内部 `e.reason` 保留供排障；`last_err` / `err_total` / 限额台账不受影响；禁用号同时保留 `reason` 与 `disabled_reason`，兼容既有读取方。

## 验证

部署前全量 `go test ./...`、`go vet ./...`、`go build ./...` 通过。

回归测试 `internal/pool/status_reason_test.go`：修复前必红（实测 `冷却到期后 Reason 必须为空` 失败），修复后通过；含反面钉桩（暂停/禁用/冷却中仍必须透出原因），并固定「`NoteSuccess` 不清 `reason` 但也不外泄」的组合。

VPS 上使用生产同一运行用户、随机 loopback 端口、独立配置与空 auths 目录启动候选程序：版本与构建指纹、提前结束请求体的 400、固定错误码与 request ID 关联、敏感标记不入日志，均通过；未复制生产凭证。

部署后真实上游烟测标记 `wb22-deploy-smoke`，不计作自然流量：HTTP 200、正文非空、`finish_reason=stop`、0.98s，request ID `req-d89c5d1db30324d0`。

部署后校验：`/status` 中**不再有任何「可用且带 reason」的账号**（此前正是该组合造成困惑）。截至 2026-10-13 13:29 UTC，切换后归档 13 条请求：12 条自然请求全部 success、无失败；1 条合成烟测。账号健康 8/8，自动重启 0，journal 无 panic。短期样本，不扩大为长期稳定性结论。

## 部署安全门的变化

本次第一次执行**按设计中止，未停止服务**：脚本原用「等 `in_flight` 连续两次为 0」的安全门，而该服务已有持续真实流量，该门永远无法满足。

改为：等待低流量窗口（`in_flight <= 2`）并记录切换瞬间在途数，依赖 systemd `stop` 的优雅停机（`srv.Shutdown` 5s 宽限）。本次切换瞬间 `in_flight=3`。中止产生的空备份目录已删除；正式备份为上表所示目录。个别长流在该窗口被截断属已知权衡，**不能声称零中断**。

## 备份与回滚

服务器备份目录：`/root/wb2api-backup/wb22-20261013T132548Z`（校验和见其中 `backup-checksums.json`）。

包含旧程序 `wb2api.pre-wb22`、新程序 `wb2api.wb22`、配置、auths 与运行状态归档、部署与烟测脚本及结果。凭证与状态仅留服务器 root 私有目录，不入仓库。

旧 wb21 SHA256：`7edf0decf0e947b8281690717c643480ca6081758f65f1454bed4ab053d24985`。

回滚：确认在途请求后停止服务，将 `wb2api.pre-wb22` 原子替换到 `/opt/workbuddy2api/wb2api` 并启动，核对 wb21 版本、旧指纹与健康。默认只回滚程序；不要把备份的 auths/配置/状态直接覆盖到运行服务。服务器 `/root/wb2api-backup/README.md` 为当前总览。

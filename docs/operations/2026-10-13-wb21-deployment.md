# wb21 部署记录（2026-10-13）

## 部署结果

- 主机：118.145.237.3，systemd `workbuddy2api`，运行用户 `wb2api`。
- 2026-10-13 12:46:38 UTC / 20:46:38 CST 开始切换，12:46:39 UTC 完成验证。
- 源码提交：`799f47bada8eaee70534bd2babf9bcba1e63aea5`，已推送到 fork `main`。
- 版本：`1.13.0-wb21`；build：`wb2026.10.13+7edf0dec`。
- 二进制 SHA256：`7edf0decf0e947b8281690717c643480ca6081758f65f1454bed4ab053d24985`；磁盘程序与 `/proc/<MainPID>/exe` 均匹配。
- 构建：Go 1.23.4，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，`-ldflags '-s -w'`，`vcs.modified=false`。
- 配置 SHA256 前后一致，保留 `server.read_timeout="0"`；账号、用量桶与运行状态保留。

## 本批内容

合并上游 main `6b31db6`（含 #133、#148、#152），并移植到期快照：

- #133（无 `conversation_id` 时缓存键回退）与 #148（空工具名裁剪）本 fork 已在 wb19/wb20 实现，合并保留本 fork 版本——缓存身份与会话请求头解耦，不伪造 `X-Conversation-ID`。
- #152 的正体：`renderExpirySnapshot` 用 `/overview` 已有的 `credits_earliest_expiry` / `credits_earliest_remaining` 在进入面板时即渲染最早到期批次，零上游请求。此前只补了「卡片可见」，卡片可见但无内容是残留问题。
- 已有逐包明细缓存时不被快照覆盖；「检查」按钮继续作为拉完整明细的增强入口。

冲突解决：`app.js`、`index.html`、`handler.go`、`tool_pairing.go`、`tool_pairing_test.go` 五个文件。前两组是重复实现（保留本 fork 更完整版本）；`handler.go` 是两套缓存键机制并存的语义冲突。

## 验证

部署前全量 Go 测试、vet、build 通过；保留既有 gofmt 未格式化文件（与本次改动无关）。

VPS 上使用生产同一运行用户、随机 loopback 端口、独立配置与空 auths 目录启动候选程序：版本与构建指纹、提前结束请求体的 400、固定错误码与 request ID 关联、敏感标记不入日志，均通过；未复制生产凭证。

部署后真实上游烟测明确标记 `wb21-deploy-smoke`，不计作自然流量：HTTP 200、正文非空、`finish_reason=stop`、1.27s，request ID `req-e9610e35584a59c2`。

截至 2026-10-13 12:53 UTC，切换后归档 15 条请求：**14 条自然请求全部 200/success，无失败**；1 条合成烟测。账号健康 8/8，自动重启 0，journal 无 panic、无 11133/11101。这是短期样本，不扩大为长期稳定性结论。

## 与 wb20 部署的差异

wb20 的安全门是「等 in_flight 连续两次为 0」。本批执行时该服务已有**持续真实流量**（3–4 个在途流式会话，来自 `pi` / `claude-cli` 客户端），该门永远无法满足，脚本按设计中止且**未停止服务**。

改为：等待一个在途数较低窗口（≤2）并记录切换瞬间在途数，依赖 systemd `stop` 触发的优雅停机（`srv.Shutdown` 5s 宽限）让多数流式请求自然收尾。本次切换瞬间 in_flight=4。切换过程没有观察到失败请求，但**该窗口内若有个别长流被截断，属于可接受的权衡**，不能声称零中断。

## 备份与回滚

服务器备份目录：`/root/wb2api-backup/wb21-20261013T124608Z`（校验和见其中 `backup-checksums.json`）。

包含旧程序 `wb2api.pre-wb21`、新程序 `wb2api.wb21`、配置、auths 与运行状态归档、部署与烟测脚本及结果。凭证与状态仅留服务器 root 私有目录，不入仓库。

旧 wb20 SHA256：`ce17e73c04dff670072c05d03cc9242d5085088fb590a2d4aee727f600b64177`。

回滚：确认在途请求后停止服务，将 `wb2api.pre-wb21` 原子替换到 `/opt/workbuddy2api/wb2api` 并启动，核对 wb20 版本、旧指纹与健康。默认只回滚程序；不要把备份的 auths/配置/状态直接覆盖到运行服务。服务器 `/root/wb2api-backup/README.md` 为当前总览。

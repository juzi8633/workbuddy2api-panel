# wb20 部署记录（2026-10-11）

## 部署结果

- 主机：118.145.237.3，systemd `workbuddy2api`，运行用户 `wb2api`。
- 2026-10-11 00:20:34 UTC / 08:20:34 CST 开始切换，00:20:35 UTC 完成验证。
- 源码提交：`f46869485276eac08a7cd2f4cb4cd682b5f60343`，已推送到我们的 fork `main`。
- 版本：`1.13.0-wb20`；build：`wb2026.10.11+ce17e73c`。
- 二进制 SHA256：`ce17e73c04dff670072c05d03cc9242d5085088fb590a2d4aee727f600b64177`；磁盘程序与 `/proc/<MainPID>/exe` 均匹配。
- 构建：Go 1.23.4，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，`-ldflags '-s -w'`，`vcs.modified=false`。
- 配置 SHA256 前后一致，保留 `server.read_timeout="0"`；账号及运行状态保留。

## 验证

部署前全量 Go 测试、vet、build 通过；upstream/server/panel 三包 race 检查通过。最新源码的 GitHub 五平台构建通过。

在 VPS 上使用生产同一运行用户、随机 loopback 端口、独立配置与空 auths 目录启动候选程序。验证版本与构建指纹、提前结束请求体时的 400、固定错误码和 request ID 关联，以及敏感标记不进入日志。没有复制生产凭证。

等待连续两次请求计数 `in_flight=0` 后停止服务，备份落盘数据，再原子替换程序并启动。自动检查失败会恢复旧程序，本次没有触发回滚。部署后健康为 5/5。

真实上游烟测均明确标记为 `wb20-deploy-smoke`，不计作自然流量：

- 第一条：`cn:deepseek-v4.1-flash`，`max_tokens=16`，HTTP 200，1.433s，`finish_reason=length`，没有正文。该预算不足以验证正常完成，不把它计作通过的对话烟测。
- 第二条：同模型，独立会话，`max_tokens=256`、`reasoning_effort=low`，HTTP 200，1.135s，有正文且 `finish_reason=stop`。request ID `req-c34c0e90d4d19aa3`。

截至 2026-10-11 00:24:54 UTC，部署后已完成并归档20条请求：18条自然请求全部success，2条合成烟测也按HTTP结果记为success（首条仍不能算正常完成验证）。服务自动重启0、journal panic行0、账号健康5/5，配置指纹未变化。这是短期样本，不扩大为长期稳定性结论。

这两条合成请求不能证明长期真实流量稳定性，也不证明原生 Codex subagent 支持。混合流中已输出合成调用的去重限制见[分析记录](../analysis/2026-10-11-wb20-upstream-integration.md)。

## 备份与回滚

服务器备份目录：`/root/wb2api-backup/wb20-20261011T002009Z/`。

包含旧程序 `wb2api.pre-wb20`、新程序 `wb2api.wb20`、配置、auths 与运行状态归档、源码、构建信息、部署和烟测脚本、验证结果及校验和。凭证与状态仅留在服务器 root 私有目录，不入仓库。

旧 wb19 SHA256：`9ac727dd073fe86a0899e137063b08d479f0444494e771a6cf3743c772593c95`。

确认没有在途请求后，停止服务，将 `wb2api.pre-wb20` 原子替换到 `/opt/workbuddy2api/wb2api` 并启动，核对 wb19 版本、旧指纹与健康。默认只回滚程序；不要把备份的 auths、配置或状态直接覆盖到运行服务，以免丢失新数据或已刷新的凭证。具体命令留在服务器 `/root/wb2api-backup/README.md`。

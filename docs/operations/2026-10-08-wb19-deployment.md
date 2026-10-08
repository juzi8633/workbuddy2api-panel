# wb19 部署记录（2026-10-08）

## 产物与目标

- 主机：118.145.237.3，systemd `workbuddy2api`，用户 `wb2api`。
- 2026-10-08 **12:53:36 UTC / 20:53:36 CST** 完成 wb18 → wb19 切换。
- 源码 tag：[`v1.13.0-wb19`](https://github.com/juzi8633/workbuddy2api-panel/releases/tag/v1.13.0-wb19)。
- 源码提交：`93025a94f5fa7e618c9d0363d2c696ecf310c2d0`，包含缓存身份/会话头解耦补修，不是初版 `85d444d`。
- 版本：`1.13.0-wb19`，build：`wb2026.10.08+9ac727dd`。
- 部署二进制 SHA256：`9ac727dd073fe86a0899e137063b08d479f0444494e771a6cf3743c772593c95`。
- 构建：Go 1.23.4，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w'`，`vcs.modified=false`。本地构建自 tag 所指干净源码；不是声称与 CI 不同构建环境的二进制逐字节相同。

最终提交的 go-binaries / docker-ghcr CI 均成功。tag 发布校验及 publish 现已成功，正式 Release 包含五平台压缩包和 checksums.txt。

## 部署前验证

- 再次执行 `go vet ./...`、`go test ./...`，全通过；修复阶段已完成全量 race 三轮和五平台编译。
- 在 VPS 上用新二进制、生产同一运行用户、随机 loopback 端口、独立配置/state/空 auths 目录启动候选实例。没有复制生产凭证；本次使用独立小脚本，不声称执行了 `scripts/rehearse.py` 的整套场景。
- 通过原始 TCP 提前结束请求体：返回 400，归档为 `error_stage=read_body` / `error_code=body_read_error`，响应、归档与日志的 request ID 相同；日志不含注入的私密标记，账号仍为0、请求在途归零。
- 精确终止候选 PID，并删除隔离目录。

## 切换与备份

备份目录：`118:/root/wb2api-backup/wb19-20261008T125335Z/`。

主要内容：

- `wb2api.pre-wb19`：原 wb18 tag 构建，SHA256 `69c9465bb4961bafbd4bc757466d9f0159fd0330defd1c53baef64c0c8ef775e`。
- `wb2api.wb19`：本次部署产物。
- `config.json`、`auths.tar.gz`、`runtime-state.tar.gz`：敏感快照，仅留 root 可访问目录，不入仓库。
- `source-v1.13.0-wb19.tar.gz`、`build-info.txt`：源码与构建溯源。
- `deployment.json`、`candidate-smoke.json`、`production-smoke.json`、`post-deploy-audit.json`、`backup-checksums.json`：验证记录。
- `README.pre-wb19.md`、`README.production.md`、部署/烟测脚本。

等待连续两次请求计数 `in_flight=0` 后停止服务，待落盘后备份账号及 state/usage；在相同文件系统内替换预先准备的二进制，再启动。自动检查失败会恢复旧二进制，本次没有触发回滚。

配置文件前后 SHA256 一致，`server.read_timeout="0"` 保留。不修改 prompt 模式、不迁移 Docker、不覆盖账号或运行状态。备份归档可读取，全部文件校验和核验通过。

## 部署后结果与限制

截至 **13:00:26 UTC**：

- `/panel/api/overview`：wb19，build 与二进制 SHA256 前8位一致；`/proc/285453/exe` 与磁盘二进制哈希也一致。
- `/healthz`：5/5 healthy；自动重启0；配置未漂移。
- 做了**一条明确标记的合成对话**：`cn:deepseek-v4.1-flash`，`max_tokens=16`，单独会话 ID；非流式 OpenAI 响应 HTTP 200，耗时约1.113s。
- 合成请求的 `rid` 在 journal 中可关联，成功归档没有错误字段。
- 部署后请求计数1成功/0失败，**这1条全是上述烟测，自然远端样本为0**，不能声称已完成真实流量稳定性验证。
- 服务 journal 中 ERR/ERROR/panic/read-body 错误计数均0。

未再次运行分钟级大 body 上游请求；慢上传/300s停滞守卫已在 wb18 实测，本轮未修改其实现。

## 回滚

确认没有在途请求后按 VPS `/root/wb2api-backup/README.md` 操作，将本批 `wb2api.pre-wb19` 替换回运行路径并启动服务。默认只回滚二进制；不要将历史 auths/config/state 直接解包覆盖到运行服务，避免回退刷新后的凭证和新状态。

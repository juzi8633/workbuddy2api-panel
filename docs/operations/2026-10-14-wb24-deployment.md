# wb24 部署记录（调用方断开不再被记成服务端/上游故障）

- **日期**：2026-10-14
- **主机**：118.145.237.3（systemd `workbuddy2api`，非容器；该机无 docker/containerd）
- **版本**：`1.13.0-wb24`
- **代码提交**：`63a4754`（`5c04536` 为首次实现，`63a4754` 为部署后自查发现的自身遗漏修正）
- **build**：`wb2026.10.14+385125d9`
- **二进制 SHA256**：`385125d90ceb011901c8f07b6b4e8f3c8b2b2ab800776919fd7a5e0e5a95ff21`
- **回滚目录**：`/root/wb2api-backup/wb24-20261014T100345Z/`（内含 `wb2api.pre-wb24`）
- **被替换的旧程序**：首个 wb24 候选 `f15e47fa…`（当天 17:55 部署），之前的 wb23 为 `18310bf6…`

## 部署过程

1. 部署前连续 5 次采样在途：`in_flight_full=0`，8/8 账号健康，近 60s 完成请求 0 → 空闲窗口。
2. 备份旧程序 + config.json + state.json，并记录 config 的 SHA256。
3. `install` 新程序到 `wb2api.new` → `mv` 就位 → `systemctl restart`。
4. 校验：磁盘 SHA256 == `/proc/<MainPID>/exe` SHA256，config SHA256 未变，`NRestarts=0`，无 panic。

**切换瞬间在途 = 0**，因此本批没有因切换中断任何在途请求。

## 生产验证

| 检查 | 结果 |
| --- | --- |
| 版本 / build | `1.13.0-wb24` / `wb2026.10.14+385125d9` |
| 磁盘 == 进程 | 一致（`385125d9…`） |
| config | SHA256 未变，`server.read_timeout="0"` 保留 |
| 账号 | 8/8 健康，无连败/冷却 |
| 正常上游烟测（合成） | 200，耗时 1.14s |
| 掐断长请求（合成） | `499 client_canceled` / `interrupted` |
| 进程指标 | `completed=2 succeeded=1 failed=0 interrupted=1 success_rate=100` |
| panic / 自动重启 | 0 / 0 |

## 部署后自查发现的自身遗漏（已在 63a4754 修正并重新部署）

首次 wb24（`f15e47fa`）部署后，我到生产上核对「三项修复是否真的成立」，发现**第三项
是假的**：

我在 CHANGELOG 里写了「调用方中断不计入服务端失败率」，但实际算法是
`Failed = completed - succeeded` —— 它**根本没看 `outcome`**。499 既不是 2xx 也不是
`success`，于是照样被计入失败、照样拉低面板成功率。实测：`completed=3 succeeded=2
failed=1 success_rate=66.7`，那条 499 就在 `failed` 里面。

只改分类不改度量，等于把「把调用方超时算成我们的故障」换了个说法。

修正内容：

- `Recorder` 增加 `interrupted` 计数，`Snapshot` 透出 `Interrupted`；
- 失败率分母改为**可归因于服务端的请求数 = `completed − interrupted`**；
- `completed` 不减（总数不能凭空少一条），`interrupted` 单独可见；
- `denom=0`（全是被调用方中断）时保持零值，前端显示「—」而非 NaN 或假 0；
- 面板请求摘要增加「调用方中断 N」。

回归测试 `internal/reqlog/reqlog_test.go:TestInterruptedNotCountedAsFailure`
（修复前编译期红：字段缺失；实现后绿）。修正后重新走了一遍备份 → 切换 → 校验。

## 遗留

- 本批只覆盖「调用方断开」这一类。`global:` 域仍只有 1 个账号且被手动暂停选号
  （见记忆），与本批无关。
- 生产归档里 `axonhub/1.0` 的约 30s 超时是**客户端自身**的超时，不是我们的配置；
  本批只是让它的记录口径正确，没有、也无法消除该客户端自身的超时。

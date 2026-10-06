#!/usr/bin/env python3
"""生成最小可跑彩排 fixture（3 个假账号 + state.json），用于自测 rehearse.py。

为什么需要：rehearse.py 需要 auths + state 才能起实例。拿生产 auths 彩排有两个问题
——(1) 账号是真实凭证，(2) state 里有真实余额与成本台账，注入合成证据会混淆。
本脚本生成同构但全假的 fixture，让彩排变成**离线可重复**的。

字段名必须与 internal/auth 的 Parse() 一致（camelCase 扁平形，不是 snake_case）——
写错的话实例会"0 账号"，表现为产品 bug。本脚本用对的形式，避免这个坑。

用法
  python3 rehearse_fixture.py /tmp/fx        # 生成 /tmp/fx/{auths,data,config.json}
  python3 rehearse.py run --auths /tmp/fx/auths --state /tmp/fx/data/state.json \
      --prod-config /tmp/fx/config.json --bin ./wb2api ...

注意
  - accessToken 是假串：彩排**不会**向上游发请求（/v1/models 走目录缓存，
    /v1/chat/completions 不在彩排断言范围）。若要测真实推理，请用生产 auths。
  - schedule.* 全关：fixture 的假账号不能做签到（会 401 并污染日志）。

与生产 state.json 同构（字段名/时间格式一致），但账号与 token 是假的 ——
彩排只验证"网关读不读得到 state / 判据对不对"，不碰上游。
"""
import json, os, sys, datetime

out = sys.argv[1] if len(sys.argv) > 1 else "/tmp/fx"
os.makedirs(out + "/auths", exist_ok=True)
os.makedirs(out + "/data", exist_ok=True)
tz = datetime.datetime.now().astimezone()

UIDS = ["aaaaaaaa-0000-0000-0000-000000000001",
        "bbbbbbbb-0000-0000-0000-000000000002",
        "cccccccc-0000-0000-0000-000000000003"]

for i, uid in enumerate(UIDS):
    auth = {
        "accessToken": "fake-access-token-%d" % i,
        "refreshToken": "fake-refresh-token-%d" % i,
        "expiresAt": 9999999999,
        "domain": "copilot.tencent.com",
        "realm": "cn",
        "uid": uid,
        "nickname": "dry%d" % i,
    }
    json.dump(auth, open("%s/auths/workbuddy-%s.json" % (out, uid), "w"), indent=2)

state = {"accounts": {}}
for i, uid in enumerate(UIDS):
    state["accounts"][uid] = {
        "credits": 5000 + i,
        "credits_total": 5000 + i,
        "cool_kind": 0,
        "updated": tz.isoformat(),      # 带时区
    }
json.dump(state, open(out + "/data/state.json", "w"), ensure_ascii=False, indent=2)
print("fixture 已生成:", out)

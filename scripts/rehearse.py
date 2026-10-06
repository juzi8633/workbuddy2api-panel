#!/usr/bin/env python3
"""workbuddy2api 彩排（独立实例 + 预检），把"脚本写错 → 误判产品 bug"的循环掐掉。

背景（为什么要这个工具）
  2026-10-04 我为验证新判据写了 8 个临时彩排脚本，其中 dry8 **连续 4 轮**报
  「判据没生效」——查了半小时，根因是我的脚本两处低级错误，而非产品：

  1. `last_seen` 写成无时区的 naive 时间（`datetime.now().isoformat()`）→
     `time.Time.UnmarshalJSON` 失败 → `json.Unmarshal` 整体报错 → 网关
     **静默丢弃整个 state.json**（余额/冷却/成本台账全没），于是判据当然不生效。
  2. `set -u` 下 `$K`（api_key）在定义前被引用 → 所有 curl 带空 Bearer →
     API 返回 403/空，输出看起来像"功能坏了"。

  两次都表现为"产品 bug"，实际都是脚本 bug。本工具把这两类错误变成**启动前的
  硬失败**，而不是事后的误判。

设计原则
  - **预检失败即拒绝启动**（不是警告）——错误必须挡在"看结果"之前。
  - **一切走 RFC3339（带时区）**：所有时间字段统一生成，杜绝 naive。
  - **状态注入带 schema 校验**：注入后先本地重读、断言字段类型，再放行。
  - **隔离三件套强制**：listen / data_dir / auth_dir **和 state_file** 必须全改
    （漏 state_file 会让彩排污染生产请求日志 —— 踩过）。
  - **端口占用即拒绝**（而不是 bind 失败后静默用旧实例 —— 踩过）。
  - **进程按 PID 精确回收**（`pkill -f "<dir>/wb2api"` 匹配不到相对路径 cmdline
    —— 踩过，会留下占号野进程）。

用法
  # 起彩排实例（默认 7867，自动预检 + 隔离 + 关排程）
  python3 rehearse.py up --bin ./wb2api --auths /opt/workbuddy2api/auths \
      --state /opt/workbuddy2api/data/state.json --dir /root/dry

  # 注入合成证据（模拟 MaxRotate 留下的 11102 形态；时间字段自动带时区）
  python3 rehearse.py inject --dir /root/dry \
      --block "glm-4.6=ALL" --block "kimi-k2-thinking=3" \
      --veto "glm-5.2=u4" --block "deepseek-v4.1-flash=1"

  # 断言判据（期望哪些被 /v1/models 剔除、哪些在面板被标记）
  python3 rehearse.py verify --dir /root/dry \
      --expect-hidden "glm-4.6,kimi-k2-thinking" --expect-kept "glm-5.2,deepseek-v4.1-flash"

  # 或一条龙：up(注入) → verify → down
  python3 rehearse.py run --bin ./wb2api --dir /root/dry \
      --block "glm-4.6=ALL" --expect-hidden "glm-4.6" --expect-kept "glm-5.2"

  python3 rehearse.py down --dir /root/dry

退出码
  0 = 全部符合期望；1 = 预检失败或断言不符；2 = 环境问题（端口占用/进程残留等）
"""
import argparse
import datetime as dt
import glob
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time

# ---------------------------------------------------------------- 输出 helpers

RED = "\033[31m" if sys.stdout.isatty() else ""
GREEN = "\033[32m" if sys.stdout.isatty() else ""
YELLOW = "\033[33m" if sys.stdout.isatty() else ""
BOLD = "\033[1m" if sys.stdout.isatty() else ""
OFF = "\033[0m" if sys.stdout.isatty() else ""


def ok(msg):
    print(f"  {GREEN}✓{OFF} {msg}")


def warn(msg):
    print(f"  {YELLOW}!{OFF} {msg}")


def fail(msg):
    print(f"  {RED}✗{OFF} {msg}")


def head(msg):
    print(f"\n{BOLD}{msg}{OFF}")


def die(msg, code=1):
    fail(msg)
    sys.exit(code)


# ------------------------------------------------------------ 时间/schema 预检


def now_rfc3339(hours=5):
    """本工具**唯一**的时间生成入口：永远带时区。

    为什么单列一个函数：彩排注入的 naive 时间会让网关丢弃整个 state.json，
    而失败表现是"功能没生效"（极难反查）。集中到一处 + 下面的
    validate_state_times 双保险。
    """
    return (dt.datetime.now().astimezone() + dt.timedelta(hours=hours)).isoformat()


# 期望是 RFC3339（带时区偏移或 Z）的时间字段。Go 的 time.Time 反序列化要求
# 时区；naive 字符串会让**整个文件**解析失败。
TIME_FIELDS = {
    "until",
    "reset_at",
    "last_seen",
    "last_success",
    "last_err",
    "credits_earliest_expiry",
}
RFC3339_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|z|[+-]\d{2}:?\d{2})$"
)


def walk_times(obj, path="", out=None):
    """递归找出所有看起来像时间字段的值（(path, value)）。"""
    if out is None:
        out = []
    if isinstance(obj, dict):
        for k, v in obj.items():
            p = f"{path}.{k}" if path else k
            if isinstance(v, str) and k in TIME_FIELDS:
                out.append((p, v))
            else:
                walk_times(v, p, out)
    elif isinstance(obj, list):
        for i, v in enumerate(obj):
            walk_times(v, f"{path}[{i}]", out)
    return out


def validate_state_times(path):
    """断言 state.json 里所有时间字段都是 RFC3339（带时区）。

    这是本工具的核心防线：naive 时间会让 `json.Unmarshal` 整体失败 →
    `Pool.load()` 丢弃**全部**持久化状态，而日志只留一行正常的
    "恢复来源=本地 state.json"，零线索（实测 2026-10-04）。
    """
    try:
        raw = open(path, encoding="utf-8").read()
    except OSError as e:
        return [f"读取失败: {e}"]
    try:
        d = json.loads(raw)
    except json.JSONDecodeError as e:
        return [f"JSON 解析失败: {e}"]

    bad = []
    for p, v in walk_times(d):
        if p.endswith("reset_at") and v == "0001-01-01T00:00:00Z":
            continue  # 零值哨兵，合法
        if not RFC3339_RE.match(v):
            bad.append(f"{p} = {v!r} 不带时区（Go 的 time.Time 会拒绝，"
                       f"导致整个 state.json 被丢弃）")
    return bad


def validate_state_schema(path):
    """校验关键字段类型（注入后调用）。类型错同样会让 Unmarshal 整体失败。"""
    d = json.load(open(path, encoding="utf-8"))
    errs = []
    if not isinstance(d.get("accounts"), dict):
        errs.append("accounts 不是 dict")
        return errs
    for uid, acct in d["accounts"].items():
        if not isinstance(acct, dict):
            errs.append(f"accounts[{uid}] 不是 dict")
            continue
        for key in ("credits",):
            if key in acct and not isinstance(acct[key], int):
                errs.append(f"accounts[{uid}].{key} 应为 int，实为 "
                            f"{type(acct[key]).__name__}")
        mc = acct.get("model_cooldowns")
        if mc is not None and not isinstance(mc, dict):
            errs.append(f"accounts[{uid}].model_cooldowns 应为 dict")
            continue
        for m, entry in (mc or {}).items():
            if not isinstance(entry, dict):
                errs.append(f"accounts[{uid}].model_cooldowns[{m}] 应为 dict")
                continue
            for k in ("until", "reset_at"):
                if k in entry and not isinstance(entry[k], str):
                    errs.append(f"accounts[{uid}].model_cooldowns[{m}].{k} 应为 str")
        costs = acct.get("model_costs")
        if costs is not None and not isinstance(costs, dict):
            errs.append(f"accounts[{uid}].model_costs 应为 dict")
            continue
        for m, entry in (costs or {}).items():
            if not isinstance(entry, dict):
                errs.append(f"accounts[{uid}].model_costs[{m}] 应为 dict")
                continue
            if "last_seen" in entry and not isinstance(entry["last_seen"], str):
                errs.append(f"accounts[{uid}].model_costs[{m}].last_seen 应为 str")
            if "cost_per_1k" in entry and not isinstance(
                entry["cost_per_1k"], (int, float)
            ):
                errs.append(f"accounts[{uid}].model_costs[{m}].cost_per_1k 应为数字")
    return errs


def port_free(port):
    """端口空闲才允许启动（否则会连到别人的实例，输出看似产品 bug）。"""
    with socket.socket() as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind(("127.0.0.1", port))
            return True
        except OSError:
            return False


def port_pid(port):
    """占用某端口的 PID（用于精确回收）。"""
    out = subprocess.run(
        ["ss", "-ltnp"], capture_output=True, text=True
    ).stdout
    for line in out.splitlines():
        if f":{port} " in line:
            m = re.search(r"pid=(\d+)", line)
            if m:
                return int(m.group(1))
    return None


# ------------------------------------------------------------------ 目录与配置


def dry_paths(d):
    return {
        "dir": d,
        "data": os.path.join(d, "data"),
        "auths": os.path.join(d, "auths"),
        "state": os.path.join(d, "data", "state.json"),
        "config": os.path.join(d, "config.json"),
        "log": os.path.join(d, "run.log"),
    }


def load_prod_config(prod_config=None):
    """取生产配置作模板（彩排继承上游参数、api_key 等，只改隔离项）。"""
    if prod_config and os.path.exists(prod_config):
        return json.load(open(prod_config, encoding="utf-8"))
    for cand in ("/opt/workbuddy2api/config.json", "config.json"):
        if os.path.exists(cand):
            return json.load(open(cand, encoding="utf-8"))
    return {}


def safe_copy(src, dst):
    """拷贝；src 与 dst 是同一文件时跳过（restart 路径会传入彩排目录内的路径）。

    这里不只是防崩溃：restart 场景下 `--state` 可能正是彩排目录里的 state.json，
    若照拷就会**把刚注入的合成证据覆盖掉**，让彩排结果变成假绿。
    """
    if not src or not os.path.exists(src):
        return False
    if os.path.abspath(src) == os.path.abspath(dst):
        return False
    if os.path.exists(dst) and os.path.samefile(src, dst):
        return False
    shutil.copy2(src, dst)
    return True


# 生产默认路径：文档承诺"默认取生产"，必须在代码里落实——否则会起出一个
# 0 账号的空实例，而"credits 已加载"检查因为列表为空反而看起来通过（假绿）。
DEFAULT_AUTHS = "/opt/workbuddy2api/auths"
DEFAULT_STATE = "/opt/workbuddy2api/data/state.json"
DEFAULT_CONFIG = "/opt/workbuddy2api/config.json"


def cmd_up(a):
    p = dry_paths(a.dir)
    # 落实默认值（--auths/--state 未给时取生产；生产不存在则明说，不静默空跑）
    if not getattr(a, "auths", None) and os.path.isdir(DEFAULT_AUTHS):
        a.auths = DEFAULT_AUTHS
    if not getattr(a, "state", None) and os.path.exists(DEFAULT_STATE):
        a.state = DEFAULT_STATE
    if not getattr(a, "prod_config", None) and os.path.exists(DEFAULT_CONFIG):
        a.prod_config = DEFAULT_CONFIG

    head(f"[预检] 彩排目录 {p['dir']}")
    errs = []

    if a.port and not port_free(a.port):
        other = port_pid(getattr(a, "port", 0))
        errs.append(f"端口 {a.port} 已被占用（PID={other}）——"
                    f"若它属于别的彩排实例，先 rehearse.py down，否则会读到别人的状态")
    if not shutil.which("ss"):
        errs.append("缺少 ss 命令（无法校验端口与回收进程）")
    if not os.path.exists(a.bin):
        errs.append(f"二进制不存在: {a.bin}")
    # 隔离三件套 + state_file：漏 state_file 会让彩排把请求日志写进生产
    # （request-logs 路径由 stateSibling(StateFile) 派生）。下面显式设置，此处只提示。
    for label, path in (("auths", a.auths), ("state", a.state)):
        if path and not os.path.exists(path):
            warn(f"{label} 不存在，跳过: {path}")

    # auths 必须真有账号：0 账号实例会让"credits 已加载"看起来通过（假绿），
    # 而后续所有判据都无从验证。
    if not a.auths:
        errs.append(f"未提供 --auths，且默认 {DEFAULT_AUTHS} 不存在"
                    f"（0 账号实例无法验证任何判据）")
    else:
        n_auth = len(glob.glob(os.path.join(a.auths, "workbuddy*.json")))
        if n_auth == 0:
            errs.append(f"{a.auths} 下没有 workbuddy*.json（0 账号）")

    if errs:
        for e in errs:
            fail(e)
        die("预检未通过，拒绝启动（避免把环境问题误判成产品 bug）", 2)
    ok("端口空闲 / 二进制存在")

    os.makedirs(p["data"], exist_ok=True)
    safe_copy(a.bin, os.path.join(p["dir"], "wb2api"))
    os.chmod(os.path.join(p["dir"], "wb2api"), 0o755)

    # auths 拷贝：源与目标同路径时必须**跳过**。restart 路径会把彩排目录内的
    # auths 当源传回来，若照抄就会先 rmtree 源、再 copytree 已删的源 → 崩。
    if a.auths and os.path.isdir(a.auths):
        if os.path.abspath(a.auths) == os.path.abspath(p["auths"]):
            ok("auths 已在彩排目录内，跳过拷贝（restart 路径）")
        else:
            if os.path.exists(p["auths"]):
                shutil.rmtree(p["auths"])
            shutil.copytree(a.auths, p["auths"])
    safe_copy(a.state, p["state"])
    for extra in ("usage.json",):
        src = os.path.join(os.path.dirname(a.state or ""), extra)
        safe_copy(src, os.path.join(p["data"], extra))

    cfg = load_prod_config(a.prod_config)
    cfg["listen"] = f"127.0.0.1:{a.port}"
    cfg["data_dir"] = p["data"]
    cfg["auth_dir"] = p["auths"]
    cfg["state_file"] = p["state"]  # ← 漏了就污染生产请求日志（踩过）
    sched = cfg.setdefault("schedule", {})
    for k in list(sched):
        if isinstance(sched[k], bool):
            sched[k] = False  # 彩排一律关排程：避免与生产抢账号做签到
    json.dump(cfg, open(p["config"], "w", encoding="utf-8"),
              ensure_ascii=False, indent=2)

    head("[预检] state.json 时间字段（naive 时间会导致整个文件被丢弃）")
    if os.path.exists(p["state"]):
        bad = validate_state_times(p["state"])
        if bad:
            for b in bad:
                fail(b)
            die("state.json 含非 RFC3339 时间，网关会静默丢弃全部持久化状态；"
                "请先修正（或用 --no-state 从空状态起）", 1)
        ok("全部时间字段带时区")
    else:
        warn("无 state.json（空状态起）")

    head("[启动]")
    with open(p["log"], "w") as lf:
        proc = subprocess.Popen(
            ["./wb2api", "-config", "config.json"],
            cwd=p["dir"], stdout=lf, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    for _ in range(150):
        if not port_free(a.port):
            break
        if proc.poll() is not None:
            print(open(p["log"], encoding="utf-8", errors="replace").read())
            die("实例启动即退出（见上方日志）", 2)
        time.sleep(0.1)
    else:
        print(open(p["log"], encoding="utf-8", errors="replace").read())
        die("等待端口监听超时", 2)

    # 记录 PID 供 down 精确回收（cmdline 是相对路径 ./wb2api，pkill -f 匹配不到）
    with open(os.path.join(p["dir"], "pid"), "w") as f:
        f.write(str(proc.pid))
    ok(f"实例已启动 PID={proc.pid} 端口 {a.port}")

    ver = api(a.dir, "/panel/api/overview").get("version")
    ok(f"version={ver}")
    head("[预检] 实例是否真的读到了 state")
    o = api(a.dir, "/panel/api/overview")
    accounts = o.get("accounts", [])
    credits = [x.get("credits") for x in accounts]
    if not accounts:
        fail("实例 0 账号：auths 路径不对或文件不是 Parse 认识的形状"
             "（flat 形须用 camelCase accessToken/refreshToken/expiresAt）")
        die("拒绝继续（0 账号时任何判据断言都会假绿）", 1)
    if os.path.exists(p["state"]) and all(c in (0, None) for c in credits):
        fail("所有账号 credits=0：实例很可能丢弃了 state.json（时间格式/结构问题）")
        die("拒绝继续（这正是 dry8 的失败模式：看起来像功能没生效）", 1)
    ok(f"credits 已加载: {credits}")


def api(d, path, method="GET"):
    cfg = json.load(open(os.path.join(d, "config.json"), encoding="utf-8"))
    key = cfg.get("api_key", "")
    import urllib.error
    import urllib.request

    req = urllib.request.Request(
        f"http://127.0.0.1:{cfg['listen'].split(':')[-1]}{path}",
        method=method,
        headers={"Authorization": "Bearer " + key},
    )
    try:
        return json.loads(urllib.request.urlopen(req, timeout=30).read())
    except urllib.error.HTTPError as e:
        body = e.read(300).decode("utf-8", "replace")
        hint = ""
        if e.code in (401, 403):
            hint = ("（api_key 不匹配：确认彩排 config.json 里的 api_key 与请求一致；"
                    "若服务端 api_key 为空则鉴权被关闭）")
        die(f"{path} → HTTP {e.code}: {body}{hint}")
    except Exception as e:
        die(f"{path} 请求失败: {e}")


# ------------------------------------------------------------------- 状态注入


def parse_spec(spec):
    """`model=N` / `model=ALL` / `model=u4` 解析。N 是账号数，uN 是第 N 个账号。"""
    if "=" not in spec:
        die(f"注入规格应为 model=COUNT|ALL|uN，实为 {spec!r}")
    model, val = spec.split("=", 1)
    return model.strip(), val.strip()


def cmd_inject(a):
    p = dry_paths(a.dir)
    head(f"[注入] {p['state']}")
    if not os.path.exists(p["state"]):
        die("无 state.json；先 up（或 --no-state 起实例）", 2)

    d = json.load(open(p["state"], encoding="utf-8"))
    uids = sorted(d.get("accounts", {}))
    if not uids:
        die("state.json 里没有账号", 2)

    def pick(val):
        if val.upper() == "ALL":
            return uids
        if val.lower().startswith("u") and val[1:].isdigit():
            n = int(val[1:])
            if not (1 <= n <= len(uids)):
                die(f"账号下标越界: {val}（共 {len(uids)} 个）")
            return [uids[n - 1]]
        if val.isdigit():
            n = int(val)
            if n > len(uids):
                die(f"账号数 {n} 超过池大小 {len(uids)}")
            return uids[:n]
        hits = [u for u in uids if u.startswith(val)]
        if not hits:
            die(f"账号前缀无匹配: {val}")
        return hits

    n_block = n_veto = 0
    for spec in getattr(a, "block", []):
        model, val = parse_spec(spec)
        for u in pick(val):
            d["accounts"][u].setdefault("model_cooldowns", {})[model] = {
                "until": now_rfc3339(getattr(a, "hours", 5)),
                "reason": "11102 model not available",
            }
            n_block += 1
    for spec in getattr(a, "raw_block", []):
        # 任意 reason（测 6004 限流等场景时不该被当成"模型不可用"）
        model, val = parse_spec(spec)
        for u in pick(val):
            d["accounts"][u].setdefault("model_cooldowns", {})[model] = {
                "until": now_rfc3339(getattr(a, "hours", 5)),
                "reason": getattr(a, "raw_reason", "6004 rate limit"),
            }
            n_block += 1
    for spec in getattr(a, "veto", []):
        # 正面证据（成功过）→ model_costs 有新鲜观测 → Healthy>0 → 否决剔除
        model, val = parse_spec(spec)
        for u in pick(val):
            d["accounts"][u].setdefault("model_costs", {})[model] = {
                "cost_per_1k": getattr(a, "veto_cost", 0.1),
                "last_seen": now_rfc3339(0),  # 带时区！
                "samples": 1,
            }
            n_veto += 1

    ok(f"注入 {n_block} 条 11102 证据 / {n_veto} 条成功证据（否决项）")

    head("[注入后校验]")
    errs = validate_state_schema(p["state"]) + validate_state_times(p["state"])
    # 写回前先校验内容，避免把坏文件落盘
    tmp = p["state"] + ".tmp"
    json.dump(d, open(tmp, "w", encoding="utf-8"), ensure_ascii=False, indent=2)
    errs += validate_state_times(tmp) + validate_state_schema(tmp)
    if errs:
        os.unlink(tmp)
        for e in errs:
            fail(e)
        die("注入结果不合法，未落盘（这正是会让网关丢弃 state 的那类错误）", 1)
    os.replace(tmp, p["state"])
    ok("schema 与时间格式均合法（已重读校验）")

    if getattr(a, "restart", False):
        head("[重启] 让实例加载新 state")
        port = json.load(open(p["config"], encoding="utf-8"))["listen"].split(":")[-1]
        cmd_down(argparse.Namespace(dir=a.dir, port=int(port)))
        a2 = argparse.Namespace(**vars(a))
        a2.port = int(port)
        a2.auths = getattr(a, "auths", None) or p["auths"]
        # 首次 up 若走默认值（生产 auths），此时 a.auths 已是生产路径；若它是彩排
        # 目录内的副本，cmd_up 的同路径守卫会跳过拷贝。
        a2.state = p["state"]
        a2.prod_config = getattr(a, "prod_config", None) or DEFAULT_CONFIG
        a2.bin = os.path.join(p["dir"], "wb2api")
        cmd_up(a2)


# --------------------------------------------------------------------- 断言


def cmd_verify(a):
    p = dry_paths(a.dir)
    head(f"[断言] {a.dir}")
    models = [m["id"] for m in api(a.dir, "/v1/models").get("data", [])]
    listed = set(models)
    panel = api(a.dir, "/panel/api/models").get("models", [])
    marked = {m["id"] for m in panel if m.get("unavailable")}
    o = api(a.dir, "/panel/api/overview")
    rl = {
        (acct["uid"][:8], r["model"])
        for acct in o.get("accounts", [])
        for r in (acct.get("rate_limited_models") or [])
        if r.get("kind") == "model_unavailable"
    }

    print(f"    /v1/models 共 {len(models)} 个；面板标记 {sorted(marked) or '无'}")
    print(f"    rate_limited(model_unavailable) 条目 {len(rl)} 条")

    bad = 0
    for m in getattr(a, "expect_hidden", []):
        full = m if ":" in m else "cn:" + m
        if full in listed:
            fail(f"期望剔除，但仍在目录里: {full}")
            bad += 1
        else:
            ok(f"已剔除: {full}")
            if full not in marked and getattr(a, "expect_marked", []):
                fail(f"面板未标记（剔除了但面板看不到?）: {full}")
                bad += 1
    for m in getattr(a, "expect_kept", []):
        full = m if ":" in m else "cn:" + m
        if full not in listed:
            fail(f"期望保留，但被剔除: {full}")
            bad += 1
        else:
            ok(f"已保留: {full}")

    if getattr(a, "expect_marked", []):
        for m in a.expect_marked:
            full = m if ":" in m else "cn:" + m
            if full not in marked:
                fail(f"期望面板标记，未标记: {full}")
                bad += 1
            else:
                ok(f"面板已标记: {full}")

    if getattr(a, "expect_no_marked", False):
        if marked:
            fail(f"期望面板无标记，实为 {sorted(marked)}")
            bad += 1
        else:
            ok("面板无标记")

    if bad:
        die(f"{bad} 项断言不符", 1)
    head("全部断言通过")


# ---------------------------------------------------------------------- 回收


def cmd_down(a):
    p = dry_paths(a.dir)
    pidf = os.path.join(p["dir"], "pid")
    killed = False
    if os.path.exists(pidf):
        try:
            pid = int(open(pidf).read().strip())
            os.kill(pid, signal.SIGTERM)
            killed = True
            ok(f"已终止 PID={pid}")
        except (ValueError, ProcessLookupError):
            warn("PID 文件存在但进程已不在")
        except PermissionError:
            fail(f"无权限终止 PID（试 sudo）")
    # 兜底：按端口找（PID 文件丢失时）——不按 cmdline 模式匹配，因为彩排进程的
    # cmdline 是相对路径 `./wb2api -config config.json`，pkill -f "<dir>/wb2api"
    # 匹配不到，会留下占号野进程（踩过）。
    if getattr(a, "port", None):
        for _ in range(20):
            if port_free(a.port):
                break
            time.sleep(0.1)
        other = port_pid(getattr(a, "port", 0))
        if other and other != getattr(a, "_self_pid", None):
            warn(f"端口 {a.port} 仍被 PID={other} 占用")
            try:
                os.kill(other, signal.SIGTERM)
                ok(f"已终止占端口的 PID={other}")
            except (ProcessLookupError, PermissionError):
                pass
    if not killed and not getattr(a, "keep_dir", False):
        warn("无 PID 文件（可能未曾启动）")
    if getattr(a, "rm", False):
        if os.path.exists(p["dir"]):
            shutil.rmtree(p["dir"])
            ok(f"已删除 {p['dir']}")
    else:
        # PID 文件属于"上一次运行的产物"，回收后必须清掉：否则 restart 路径
        # （cmd_inject 里 down→up）会在 down 的净空断言上误报，且下次 down 会
        # 对着一个已死的 PID 空跑。用户信号处理只删"本次这一轮"的产物。
        try:
            os.remove(pidf)
        except OSError:
            pass
    # 回收后**断言**净空。此前这里只打印警告就返回：若 PID 文件丢失、端口已被
    # 别的进程接管、或 SIGTERM 被忽略，工具会"看起来回收成功"却留下占号野进程
    # ——正是本工具要消灭的问题。断言失败以非零码退出，调用方（含 cmd_run 的
    # finally）能立刻发现，而不是几小时后才由一个孤儿实例暴露。
    stale = []
    if getattr(a, "port", None) and not port_free(a.port):
        stale.append(f"端口 {a.port} 仍被 PID={port_pid(a.port)} 占用")
    if os.path.exists(pidf):
        stale.append(f"PID 文件未清理: {pidf}")
    if getattr(a, "rm", False) and os.path.exists(p["dir"]):
        stale.append(f"彩排目录未删除: {p['dir']}")
    if stale:
        for s in stale:
            fail(s)
        sys.exit(1)
    ok("回收净空（端口/进程/目录均无残留）")

def cmd_run(a):
    """一条龙：up → inject → verify → down。

    **无论成败都必须回收实例**：断言失败走 die() → sys.exit，若不捕获就会留下
    占端口的野进程——这正是本工具要消灭的问题（曾经 pkill -f 匹配不到相对路径
    cmdline，野进程一直占着账号跑）。所以这里用 try/finally 兜住。
    """
    started = False
    rc = 0
    try:
        cmd_up(argparse.Namespace(**vars(a)))
        started = True
        if a.block or a.veto or getattr(a, "raw_block", None):
            cmd_inject(argparse.Namespace(**vars(a), restart=True))
        if (a.expect_hidden or a.expect_kept or a.expect_marked
                or a.expect_no_marked):
            cmd_verify(a)
    except SystemExit as e:
        rc = e.code or 0
    finally:
        if started:
            head("[回收]")
            try:
                cmd_down(argparse.Namespace(dir=a.dir, port=a.port, rm=a.rm))
            except SystemExit:
                # down 现在会在回收不净时 sys.exit(1)。那属于环境问题，不能吞掉
                # rc=0 的成功结果——但也绝不掩盖：记为失败码 2（环境问题）。
                if rc == 0:
                    rc = 2
    if rc:
        sys.exit(rc)
    head("彩排完成")


# ---------------------------------------------------------------------- CLI


def main():
    ap = argparse.ArgumentParser(
        description="workbuddy2api 彩排：独立实例 + 预检，避免脚本错误被误判成产品 bug",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    def common(sp, with_bin=True):
        if with_bin:
            sp.add_argument("--bin", required=True, help="被测二进制路径")
        sp.add_argument("--dir", default="/root/rehearse",
                        help="彩排目录（默认 /root/rehearse，可复用→dir 已存在时会覆盖）")
        sp.add_argument("--port", type=int, default=7867, help="监听端口")
        sp.add_argument("--auths", help="auths 目录（默认取生产）")
        sp.add_argument("--state", help="state.json（默认取生产）")
        sp.add_argument("--prod-config", help="生产 config.json 作模板")

    sp = sub.add_parser("up", help="起彩排实例（预检 + 隔离 + 关排程）")
    common(sp)

    sp = sub.add_parser("inject", help="注入合成证据到彩排 state")
    sp.add_argument("--dir", default="/root/rehearse")
    sp.add_argument("--block", action="append", default=[],
                    help="11102 证据：model=COUNT|ALL|uN（可重复）")
    sp.add_argument("--raw-block", action="append", default=[],
                    help="任意 reason 的冷却（测 6004 等；可重复）")
    sp.add_argument("--raw-reason", default="6004 rate limit")
    sp.add_argument("--veto", action="append", default=[],
                    help="成功证据（否决项）：model=COUNT|ALL|uN（可重复）")
    sp.add_argument("--veto-cost", type=float, default=0.1)
    sp.add_argument("--hours", type=float, default=5, help="冷却有效小时数")
    sp.add_argument("--restart", action="store_true", help="注入后重启实例")

    sp = sub.add_parser("verify", help="断言目录剔除/面板标记")
    sp.add_argument("--dir", default="/root/rehearse")
    sp.add_argument("--expect-hidden", default="", help="期望被剔除（逗号分隔）")
    sp.add_argument("--expect-kept", default="", help="期望保留（逗号分隔）")
    sp.add_argument("--expect-marked", default="", help="期望面板标记（逗号分隔）")
    sp.add_argument("--expect-no-marked", action="store_true")

    sp = sub.add_parser("down", help="回收实例（按 PID 精确；回收后断言净空，不净则退出码 1）")
    sp.add_argument("--dir", default="/root/rehearse")
    sp.add_argument("--port", type=int, default=7867)
    sp.add_argument("--rm", action="store_true", help="同时删除彩排目录")

    sp = sub.add_parser("run", help="一条龙：up → inject → verify → down")
    common(sp)
    sp.add_argument("--block", action="append", default=[])
    sp.add_argument("--raw-block", action="append", default=[])
    sp.add_argument("--raw-reason", default="6004 rate limit")
    sp.add_argument("--veto", action="append", default=[])
    sp.add_argument("--veto-cost", type=float, default=0.1)
    sp.add_argument("--hours", type=float, default=5)
    sp.add_argument("--expect-hidden", default="")
    sp.add_argument("--expect-kept", default="")
    sp.add_argument("--expect-marked", default="")
    sp.add_argument("--expect-no-marked", action="store_true")
    sp.add_argument("--rm", action="store_true")

    a = ap.parse_args()
    for attr in ("expect_hidden", "expect_kept", "expect_marked"):
        if hasattr(a, attr):
            v = getattr(a, attr)
            setattr(a, attr, [x for x in v.split(",") if x.strip()] if isinstance(v, str) else v)

    {"up": cmd_up, "inject": cmd_inject, "verify": cmd_verify,
     "down": cmd_down, "run": cmd_run}[a.cmd](a)


if __name__ == "__main__":
    main()

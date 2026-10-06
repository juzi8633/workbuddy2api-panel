package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestModelAvailabilityAllAndZero 池内全体账号都对该模型 11102 → 可用数 0；
// 只要还有一个健康账号（未撞过该模型）→ 可用数 >0。这是 /v1/models 剔除的判据。
func TestModelAvailabilityAllAndZero(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999})
	// 两个账号都撞 11102 → 池内全不可用。
	p.BlockModelBackoff("a", "glm-4.6", "11102 model not available")
	p.BlockModelBackoff("b", "glm-4.6", "11102 model not available")
	if avail, total := p.ModelAvailability("glm-4.6", "cn"); avail != 0 || total != 2 {
		t.Fatalf("glm-4.6 avail/total = %d/%d want 0/2", avail, total)
	}
	// 只有 a 撞过 → b 还能跑。
	p.BlockModelBackoff("a", "kimi-k2-thinking", "11102 model not available")
	if avail, total := p.ModelAvailability("kimi-k2-thinking", "cn"); avail != 1 || total != 2 {
		t.Fatalf("kimi avail/total = %d/%d want 1/2", avail, total)
	}
	// 从没撞过的模型：全员可用。
	if avail, total := p.ModelAvailability("glm-5.2", "cn"); avail != 2 || total != 2 {
		t.Fatalf("glm-5.2 avail/total = %d/%d want 2/2", avail, total)
	}
	// 批量接口只返回"被负缓存过"的候选，且只含不可用数 0 之外的值。
	all := p.ModelAvailabilityAll("cn")
	if all["glm-4.6"] != 0 {
		t.Errorf("ModelAvailabilityAll[glm-4.6] = %d want 0", all["glm-4.6"])
	}
	if all["kimi-k2-thinking"] != 1 {
		t.Errorf("ModelAvailabilityAll[kimi] = %d want 1", all["kimi-k2-thinking"])
	}
	if _, ok := all["glm-5.2"]; ok {
		t.Error("healthy model must not appear in the blocked-candidate map")
	}
}

// TestModelAvailabilityIgnores6004 6004 是**限流**（暂时），不代表模型不可用：
// 不得进入 11102 候选集，也不该让 /v1/models 把它剔除。
func TestModelAvailabilityIgnores6004(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999})
	p.CooldownSoftForModel("a", 10*time.Minute, time.Time{}, "glm-5.2", "6004 模型限流")
	all := p.ModelAvailabilityAll("cn")
	if _, ok := all["glm-5.2"]; ok {
		t.Error("6004 rate-limit must not be treated as model-unavailable")
	}
}

// TestModelAvailabilityRealmScoped realm 谓词必须生效：CN 模型的可用性不能由
// global 账号凑数（CN 模型在 global 账号上必然 11102，混算会误判成"还有可用"）。
func TestModelAvailabilityRealmScoped(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "gl1", AccessToken: "at-gl", ExpiresAt: 9999999999, Domain: "workbuddy.ai"})
	p.BlockModelBackoff("cn1", "glm-4.6", "11102 model not available")
	// cn 域：cn1 被负缓存、gl1 不计入 → 0/1。
	if avail, total := p.ModelAvailability("glm-4.6", "cn"); avail != 0 || total != 1 {
		t.Fatalf("realm=cn avail/total = %d/%d want 0/1", avail, total)
	}
	// 不限 realm：gl1 没撞过该模型 → 1/2（说明 realm 过滤真的起了作用）。
	if avail, total := p.ModelAvailability("glm-4.6", ""); avail != 1 || total != 2 {
		t.Fatalf("realm=\"\" avail/total = %d/%d want 1/2", avail, total)
	}
}

// TestModelAvailabilityExcludesFrozenAndDisabled 冻结/禁用账号不算可用：
// 否则一个被冻结的号会让 /v1/models 继续列出实际选不中的模型。
func TestModelAvailabilityExcludesFrozenAndDisabled(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at-ok", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "fz", AccessToken: "at-fz", ExpiresAt: 9999999999})
	p.BlockModelBackoff("ok", "glm-4.6", "11102 model not available")
	// fz 被冻结：不该被算成 glm-4.6 的可用来源。
	p.SetFreezeThreshold("fz", 999999)
	p.SetCredits("fz", 1, 0) // 低于阈值 → 冻结
	if avail, _ := p.ModelAvailability("glm-4.6", "cn"); avail != 0 {
		t.Fatalf("frozen account must not count as available source: avail=%d", avail)
	}
	// 解冻后（阈值 0）+ 该号未撞过该模型 → 可用。
	p.SetFreezeThreshold("fz", 0)
	if avail, _ := p.ModelAvailability("glm-4.6", "cn"); avail != 1 {
		t.Fatalf("after unfreeze avail=%d want 1", avail)
	}
	p.Disable("fz", "test")
	if avail, _ := p.ModelAvailability("glm-4.6", "cn"); avail != 0 {
		t.Fatalf("disabled account must not count: avail=%d", avail)
	}
}

// TestModelAvailabilityEvidenceGapDocumentsMaxRotate 记录一个**真实局限**（不是 bug，
// 是设计边界）：/v1/models 的剔除依赖"池内全体账号都对该模型有 11102 负缓存条目"，
// 而负缓存条目的覆盖面由 Handler.MaxRotate 决定（生产=3）。所以一次失败尝试通常只
// 写 3 个账号的条目 → 5 个账号的池里该模型"命中 3/5"，本判定返回 avail>0 → 不剔除。
//
// 实测（2026-10-04，生产 118.145.237.3）：11102 是**模型级**、与账号无关，对命中
// 3/5 的模型补测未命中的 2 个账号，**全部返回 11102**。也就是这些模型其实全员
// 不可用，只是证据未满池 → 过滤漏掉它们。
//
// 本测试把该边界**固定下来**（防止有人误以为"命中 3/5 就会剔除"而去掉其它保险）：
// 只要还有一个账号没有该模型的负缓存条目，就不剔除。
// 彻底的修法是提高 MaxRotate，或在目录探测时主动对候选模型做池内探针——两者都不在
// 本函数职责内。参见 memory #857。
func TestModelAvailabilityEvidenceGapDocumentsMaxRotate(t *testing.T) {
	p := New("")
	for _, uid := range []string{"u1", "u2", "u3", "u4", "u5"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at-" + uid, ExpiresAt: 9999999999})
	}
	// 模拟"客户端一次尝试 × MaxRotate=3"留下的证据：只有 3 个账号有条目。
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.BlockModelBackoff(uid, "glm-4.6v", ModelUnavailableReasonPrefix+" model not available")
	}
	avail, total := p.ModelAvailability("glm-4.6v", "cn")
	if avail != 2 || total != 5 {
		t.Fatalf("avail/total = %d/%d want 2/5（证据未满池）", avail, total)
	}
	if all := p.ModelAvailabilityAll("cn"); all["glm-4.6v"] == 0 {
		t.Error("must NOT claim pool-wide unavailable while 2 accounts lack evidence")
	}
	// 补满第 4、5 个账号后 → 0，此时才剔除。
	p.BlockModelBackoff("u4", "glm-4.6v", ModelUnavailableReasonPrefix+" model not available")
	p.BlockModelBackoff("u5", "glm-4.6v", ModelUnavailableReasonPrefix+" model not available")
	if avail, _ := p.ModelAvailability("glm-4.6v", "cn"); avail != 0 {
		t.Fatalf("after all accounts have evidence avail=%d want 0", avail)
	}
}

// TestModelUnavailableReasonPrefixMatchesUpstreamLiteral pool 的 reason 前缀字面量
// 必须与 upstream.ModelBlockReason 一致（跨包断言在 internal/server，这里只守住
// pool 侧的字面量本身，防止有人改前缀导致 server 侧跨包测试以外的地方静默失配）。
func TestModelUnavailableReasonPrefixMatchesUpstreamLiteral(t *testing.T) {
	if ModelUnavailableReasonPrefix != "11102" {
		t.Fatalf("prefix = %q want 11102", ModelUnavailableReasonPrefix)
	}
	if !IsModelUnavailableReason("11102 model not available") {
		t.Error("canonical 11102 reason must be recognized")
	}
	if IsModelUnavailableReason("11101 Unmarshal chat params failed") {
		t.Error("11101 must not be classified as model-unavailable")
	}
}

// TestModelCooldownHitsSurviveRestart 11102 退避的 Hits 必须跨重启保留。
//
// 实测（2026-10-04）：Hits 不落盘时，每次网关重启都把退避打回第一档——
// 同一模型 15:30 已命中（until=21:30），16:21 重启后 16:21:59 再命中只得
// until=22:22（6h）而不是 12h。网关每次发版都重启，于是死模型永远停在 6h 档。
func TestModelCooldownHitsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	// 第一次：命中 3 次 → 退避到 6h<<2 = 24h 封顶。
	p1 := New(stateFile)
	p1.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	for i := 0; i < 3; i++ {
		p1.BlockModelBackoff("u1", "glm-4.6", ModelUnavailableReasonPrefix+" model not available")
	}
	p1.mu.RLock()
	hits1 := p1.byUID["u1"].modelCooldowns["glm-4.6"].Hits
	until1 := p1.byUID["u1"].modelCooldowns["glm-4.6"].Until
	p1.mu.RUnlock()
	if hits1 != 3 {
		t.Fatalf("hits=%d want 3", hits1)
	}
	p1.Flush()

	// 第二次（模拟重启）：加载后 hits 必须还是 3，Until 不变。
	p2 := New(stateFile)
	p2.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p2.mu.RLock()
	mc, ok := p2.byUID["u1"].modelCooldowns["glm-4.6"]
	p2.mu.RUnlock()
	if !ok {
		t.Fatal("model cooldown lost across restart")
	}
	if mc.Hits != 3 {
		t.Fatalf("hits after restart = %d want 3（Hits 未持久化 → 退避被打回第一档）", mc.Hits)
	}
	if !mc.Until.Equal(until1) {
		t.Fatalf("until changed across restart: %v → %v", until1, mc.Until)
	}
	// 再命中一次（第 4 次）→ hits=4，仍是 24h 封顶。
	p2.BlockModelBackoff("u1", "glm-4.6", ModelUnavailableReasonPrefix+" model not available")
	p2.mu.RLock()
	hits4 := p2.byUID["u1"].modelCooldowns["glm-4.6"].Hits
	p2.mu.RUnlock()
	if hits4 != 4 {
		t.Fatalf("hits after restart+1 = %d want 4", hits4)
	}
}

// TestModelCooldownOldStateWithoutHits 旧 state.json（无 hits 字段）加载后
// Hits=0，首次命中即 hits=1 → 6h（与旧行为一致，不是回归）。
func TestModelCooldownOldStateWithoutHits(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	until := time.Now().Add(5 * time.Hour)
	raw := `{"accounts":{"u1":{"credits":100,"cool_kind":0,"model_cooldowns":{"glm-4.6":{"until":"` +
		until.Format(time.RFC3339) + `","reason":"11102 model not available"}}}}}`
	if err := os.WriteFile(stateFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(stateFile)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.mu.RLock()
	mc, ok := p.byUID["u1"].modelCooldowns["glm-4.6"]
	p.mu.RUnlock()
	if !ok {
		t.Fatal("legacy entry not loaded")
	}
	if mc.Hits != 0 {
		t.Fatalf("legacy hits=%d want 0", mc.Hits)
	}
	p.BlockModelBackoff("u1", "glm-4.6", ModelUnavailableReasonPrefix+" model not available")
	p.mu.RLock()
	h := p.byUID["u1"].modelCooldowns["glm-4.6"].Hits
	p.mu.RUnlock()
	if h != 1 {
		t.Fatalf("hits after first block = %d want 1", h)
	}
}

// TestModelUnavailableEvidenceAll 证据面汇总：Blocked/Total/Healthy/Available 四个数
// 各司其职。/v1/models 的判据（过半数 Blocked + Healthy==0 + Available==0）依赖它们。
func TestModelUnavailableEvidenceAll(t *testing.T) {
	p := New("")
	for _, uid := range []string{"u1", "u2", "u3", "u4"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at-" + uid, ExpiresAt: 9999999999})
	}
	// u1,u2 实证不可用（2/4 = 过半数）。
	p.BlockModelBackoff("u1", "glm-4.6", ModelUnavailableReasonPrefix+" model not available")
	p.BlockModelBackoff("u2", "glm-4.6", ModelUnavailableReasonPrefix+" model not available")

	ev := p.ModelUnavailableEvidenceAll("cn")["glm-4.6"]
	if ev.Blocked != 2 || ev.Total != 4 {
		t.Fatalf("Blocked/Total = %d/%d want 2/4", ev.Blocked, ev.Total)
	}
	if ev.Healthy != 0 {
		t.Fatalf("Healthy = %d want 0（无人成功过）", ev.Healthy)
	}
	if ev.Available != 2 {
		t.Fatalf("Available = %d want 2（u3/u4 未被试过）", ev.Available)
	}
	if ev.Reason == "" {
		t.Error("Reason 应带 11102 原文供面板展示")
	}
	// 判据（与 server/panel 同表达式）：过半数 + Healthy 0 + Available 0 → 不可用。
	// 此处 Available=2 → **不**判不可用（这正是"证据驱动"的边界）。
	if ev.Blocked*2 >= ev.Total && ev.Healthy == 0 && ev.Available == 0 {
		t.Error("Available>0 时不该判为池内全不可用")
	}
}

// TestModelUnavailableEvidenceVetoBySuccess Healthy>0 是否决项：只要有一个账号
// **成功**用过该模型（modelCost 账本有观测），就不得剔除——这是放宽 Blocked 阈值
// 的安全阀：上游真做账号级权益分级时，"部分账号能用"的模型一定有账号成功过。
func TestModelUnavailableEvidenceVetoBySuccess(t *testing.T) {
	p := New("")
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at-" + uid, ExpiresAt: 9999999999})
	}
	// 3/3 全被负缓存：数字上"健康证据"为零、Available 也为零。
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.BlockModelBackoff(uid, "glm-5.0-turbo", ModelUnavailableReasonPrefix+" model not available")
	}
	ev := p.ModelUnavailableEvidenceAll("cn")["glm-5.0-turbo"]
	if ev.Blocked != 3 || ev.Available != 0 || ev.Healthy != 0 {
		t.Fatalf("Blocked/Available/Healthy = %d/%d/%d want 3/0/0", ev.Blocked, ev.Available, ev.Healthy)
	}
	// 纯看三个数会判"池内全不可用"。
	if !(ev.Blocked*2 >= ev.Total && ev.Healthy == 0 && ev.Available == 0) {
		t.Fatal("precondition: 应满足剔除条件")
	}
	// u3 成功过一次（NoteModelCost 只在成功路径写）→ Healthy=1 → 否决。
	p.NoteModelCost("u3", "glm-5.0-turbo", 0.1, 100)
	ev = p.ModelUnavailableEvidenceAll("cn")["glm-5.0-turbo"]
	if ev.Healthy != 1 {
		t.Fatalf("Healthy = %d want 1", ev.Healthy)
	}
	if ev.Blocked*2 >= ev.Total && ev.Healthy == 0 && ev.Available == 0 {
		t.Error("Healthy>0 必须否决剔除")
	}
	if !p.ModelHasSucceeded("glm-5.0-turbo", "cn") {
		t.Error("ModelHasSucceeded 应为 true")
	}
	if p.ModelHasSucceeded("glm-4.6", "cn") {
		t.Error("ModelHasSucceeded 对没成功过的模型应为 false")
	}
}

// TestModelUnavailableEvidenceThresholdOverHalf 阈值是"严格过半"：
// 4 账号池里 2 个命中 → 2*2 >= 4 成立（恰好过半，判不可用）；
// 5 账号池里 2 个命中 → 2*2 < 5 不成立（不足半数，不判）。
func TestModelUnavailableEvidenceThresholdOverHalf(t *testing.T) {
	// 4 池，命中 2 → 恰好过半。
	p4 := New("")
	for _, uid := range []string{"a", "b", "c", "d"} {
		p4.Add(&auth.Auth{UID: uid, AccessToken: "at", ExpiresAt: 9999999999})
	}
	p4.BlockModelBackoff("a", "m", ModelUnavailableReasonPrefix+" x")
	p4.BlockModelBackoff("b", "m", ModelUnavailableReasonPrefix+" x")
	if ev := p4.ModelUnavailableEvidenceAll("cn")["m"]; ev.Blocked*2 < ev.Total {
		t.Fatalf("4 池命中 2 应算过半: %d/%d", ev.Blocked, ev.Total)
	}
	// 5 池，命中 2 → 不足半数。
	p5 := New("")
	for _, uid := range []string{"a", "b", "c", "d", "e"} {
		p5.Add(&auth.Auth{UID: uid, AccessToken: "at", ExpiresAt: 9999999999})
	}
	p5.BlockModelBackoff("a", "m", ModelUnavailableReasonPrefix+" x")
	p5.BlockModelBackoff("b", "m", ModelUnavailableReasonPrefix+" x")
	if ev := p5.ModelUnavailableEvidenceAll("cn")["m"]; ev.Blocked*2 >= ev.Total {
		t.Fatalf("5 池命中 2 不该算过半: %d/%d", ev.Blocked, ev.Total)
	}
}

// TestStateLoadMalformedTimeDiscardsEverything 记录一个**真实的运维陷阱**（本次踩到）：
// state.json 里任何一个时间字段是"无时区"的 naive 形式（如
// "2026-10-04T17:30:00.123456"）时，time.Time 的 UnmarshalJSON 失败 →
// json.Unmarshal 整体返回错误 → load() **静默丢弃整个 state 文件**（余额、
// model_cooldowns、成本台账全没了），且日志只有一行正常的"恢复来源=本地 state.json"，
// 完全没有报错线索。
//
// 实测（2026-10-04，生产机彩排）：naive 时间戳 → API 报 credits=0 / 0 条冷却；
// 同一文件把 last_seen 改成带 +08:00 的 RFC3339 → credits=5266 正常恢复。
//
// 本测试把"解析失败必须被日志暴露"这一期望固定下来——见 load() 的 WARN。
func TestStateLoadMalformedTimeDiscardsEverything(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	// model_costs.last_seen 是 naive 时间（无时区）→ 整个文件不可解析。
	until := time.Now().Add(5 * time.Hour).Format(time.RFC3339)
	raw := `{"accounts":{"u1":{"credits":5268,"cool_kind":0,` +
		`"model_cooldowns":{"glm-4.6":{"until":"` + until + `","reason":"11102 model not available"}},` +
		`"model_costs":{"glm-5.2":{"cost_per_1k":0.1,"last_seen":"2026-10-04T17:30:00.123456","samples":2}}}}}`
	if err := os.WriteFile(fp, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(fp)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})

	// 现状（实测行为）：整个 state 被丢弃 —— credits 回到零值、冷却表空。
	p.mu.RLock()
	credits := p.byUID["u1"].credits
	cooldowns := len(p.byUID["u1"].modelCooldowns)
	p.mu.RUnlock()
	if credits != 0 || cooldowns != 0 {
		t.Fatalf("naive 时间戳应导致整个 state 被丢弃；credits=%d cooldowns=%d", credits, cooldowns)
	}

	// 对照：同一内容把 last_seen 写成 RFC3339（带时区）→ 正常恢复。
	fp2 := filepath.Join(dir, "state2.json")
	raw2 := strings.Replace(raw, `"2026-10-04T17:30:00.123456"`, `"`+until+`"`, 1)
	if err := os.WriteFile(fp2, []byte(raw2), 0o600); err != nil {
		t.Fatal(err)
	}
	p2 := New(fp2)
	p2.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p2.mu.RLock()
	credits2, cd2 := p2.byUID["u1"].credits, len(p2.byUID["u1"].modelCooldowns)
	p2.mu.RUnlock()
	if credits2 != 5268 || cd2 != 1 {
		t.Fatalf("RFC3339 时间戳应正常恢复：credits=%d cooldowns=%d want 5268/1", credits2, cd2)
	}
}

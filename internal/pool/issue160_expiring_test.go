package pool

import (
	"fmt"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestIssue160ExpiringPreferenceDistribution 用**生产同配置**测量「快过期优先」到底
// 是否生效：issue #160 报告勾选后仍像负载均衡。
//
// 生产配置（118.145.237.3 config.json，2026-10-13）：
//
//	prefer_expiring=true, expiring_soon=168h,
//	idle_weight_per_hour=0.5, idle_weight_max=5, credit_floor=100
//
// 生产 8 个账号余额：5724/4096/5030/5823/4094/4060/5766/5683。
func TestIssue160ExpiringPreferenceDistribution(t *testing.T) {
	withNoPickGap(t)
	credits := []int64{5724, 4096, 5030, 5823, 4094, 4060, 5766, 5683}

	measure := func(t *testing.T, markExpiring bool) map[int]int {
		t.Helper()
		p := New("")
		p.SetWeights(0.5, 5.0)
		p.SetPreferExpiring(true)
		p.SetCreditFloor(100)
		for i, c := range credits {
			uid := fmt.Sprintf("u%d", i)
			p.Add(&auth.Auth{UID: uid})
			p.SetCredits(uid, c, 0)
		}
		if markExpiring {
			p.mu.Lock()
			// 账号 3（5823 分）有一批 3 天后到期的积分——在 168h 窗口内。
			p.byUID["u3"].creditsExpiring = 1200
			p.byUID["u3"].creditsEarliestExpiry = time.Now().Add(72 * time.Hour)
			p.byUID["u3"].creditsEarliestRemaining = 1200
			p.mu.Unlock()
		}
		counts := map[int]int{}
		const n = 20000
		for i := 0; i < n; i++ {
			a := p.Pick()
			if a == nil {
				t.Fatal("pick returned nil")
			}
			counts[int(a.UID[1]-'0')]++
		}
		return counts
	}

	base := measure(t, false)
	exp := measure(t, true)

	t.Logf("无快过期账号（基线）：")
	for i := range credits {
		t.Logf("  u%d credits=%-5d  %5.2f%%", i, credits[i], float64(base[i])*100/20000)
	}
	t.Logf("u3 标记快过期（窗口内）：")
	for i := range credits {
		t.Logf("  u%d credits=%-5d  %5.2f%%", i, credits[i], float64(exp[i])*100/20000)
	}
	share := float64(exp[3]) * 100 / 20000
	t.Logf("u3 占比：基线 %.2f%% → 快过期 %.2f%%", float64(base[3])*100/20000, share)
	if share < 30 {
		t.Errorf("issue #160：窗口内快过期账号占比仅 %.2f%%，×3 加权未形成可见偏好", share)
	}
}

// TestIssue160LowCreditsExpiringAccount 复现 #160 的典型形态：**快过期的账号恰恰
// 是余额少的那个**（正因为没用完才要过期）。此时 ×3 加在「credits 比例 ×10」主导
// 的权重上，是否还够？
//
// 三个账号：快过期号只剩 200 分（3 天后到期），另两个各 5000 分。
func TestIssue160LowCreditsExpiringAccount(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.SetWeights(0.5, 5.0)
	p.SetPreferExpiring(true)
	p.SetCreditFloor(100)
	for _, u := range []string{"low", "hi1", "hi2"} {
		p.Add(&auth.Auth{UID: u})
	}
	p.SetCredits("low", 200, 0)
	p.SetCredits("hi1", 5000, 0)
	p.SetCredits("hi2", 5000, 0)
	p.mu.Lock()
	p.byUID["low"].creditsExpiring = 200
	p.byUID["low"].creditsEarliestExpiry = time.Now().Add(72 * time.Hour)
	p.byUID["low"].creditsEarliestRemaining = 200
	p.mu.Unlock()

	counts := map[string]int{}
	const n = 30000
	for i := 0; i < n; i++ {
		counts[p.Pick().UID]++
	}
	lowShare := float64(counts["low"]) * 100 / n
	t.Logf("low(200分,3天后到期)=%.2f%%  hi1=%.2f%%  hi2=%.2f%%",
		lowShare, float64(counts["hi1"])*100/n, float64(counts["hi2"])*100/n)
	// 修复后：奖励是加法（+3 个满额积分单位），低余额不再把偏好吃掉。
	// 实测约 58.8%，与哈希路径（realm.go 的 3 槽位 → 60%）同口径。
	if lowShare <= 33.4 {
		t.Errorf("issue #160 未修复：快过期但低余额的账号占比 %.2f%% ≤ 均分 33.33%%", lowShare)
	}
	if lowShare < 50 || lowShare > 70 {
		t.Errorf("快过期偏好应接近哈希路径的 60%%，实测 %.2f%%", lowShare)
	}
}

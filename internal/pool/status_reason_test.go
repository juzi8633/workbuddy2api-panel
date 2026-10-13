package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestStatusReasonHiddenAfterCooldownExpires 钉桩「过期即不再展示」：冷却到期后
// Status.Reason 必须为空串，面板才不会在「可用」标签旁挂一个旧的 429 文案。
//
// 背景（2026-10-13 生产实测）：账号 28f60c37 的 state.json 里 until 停在 10-08 10:33、
// reason 仍是 "429 rate limit"，而 cooling=false、last_success 是当天——界面因此同时
// 显示「可用」和「429 rate limit」。原因是 reason 没有过期钩子：到期只是时间比较，
// NoteSuccess 也刻意不碰 reason。
func TestStatusReasonHiddenAfterCooldownExpires(t *testing.T) {
	t.Run("冷却是隐藏边界", func(t *testing.T) {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.Cooldown("u1", CoolSoft, time.Hour, "429 rate limit")

		// 冷却中：原因必须可见，否则运维看不到「为什么这个号不参与选号」。
		st, ok := p.Status("u1")
		if !ok || !st.Cooling || st.Reason != "429 rate limit" {
			t.Fatalf("冷却中应透出原因: %+v ok=%v", st, ok)
		}

		// 把冷却截止推到过去，模拟自然到期（不经过任何清理入口）。
		p.mu.Lock()
		p.byUID["u1"].until = time.Now().Add(-time.Minute)
		p.mu.Unlock()

		st, ok = p.Status("u1")
		if !ok {
			t.Fatal("no status")
		}
		if st.Cooling {
			t.Fatalf("冷却已到期，不应仍报 cooling: %+v", st)
		}
		if st.Reason != "" {
			t.Errorf("冷却到期后 Reason 必须为空（否则面板「可用」旁挂旧 429 文案）: %q", st.Reason)
		}
	})

	t.Run("成功请求不清 reason 但也不再展示", func(t *testing.T) {
		// NoteSuccess 刻意不碰 reason（成功清的是失败/熔断计数）。本用例固定这一组合
		// 不会把旧文案漏出去：过期 + 成功之后对外必须是空的。
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.Cooldown("u1", CoolSoft, time.Minute, "429 rate limit")
		p.NoteSuccess("u1")

		p.mu.Lock()
		p.byUID["u1"].until = time.Now().Add(-time.Second)
		p.mu.Unlock()

		st, _ := p.Status("u1")
		if st.Cooling || st.Reason != "" {
			t.Errorf("过期+成功后对外必须干净: cooling=%v reason=%q", st.Cooling, st.Reason)
		}
		// 内部状态仍保留原文，便于排障（不要求清空，只要求不外泄）。
		p.mu.RLock()
		internal := p.byUID["u1"].reason
		p.mu.RUnlock()
		if internal != "429 rate limit" {
			t.Errorf("内部 reason 不应被本改动清掉（仅供排障）: %q", internal)
		}
	})
}

// TestStatusReasonVisibleWhilePenalized 反面钉桩：仍是惩罚态时原因**必须**透出
// （禁用/冷却/暂停/冻结/连败降权），门控不能把有效信息一起藏掉。
func TestStatusReasonVisibleWhilePenalized(t *testing.T) {
	t.Run("暂停", func(t *testing.T) {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.Cooldown("u1", CoolSoft, time.Hour, "429 rate limited")
		p.Pause("u1")
		st, _ := p.Status("u1")
		if st.Reason != "429 rate limited" {
			t.Errorf("暂停号仍应透出原因: %q", st.Reason)
		}
	})

	t.Run("禁用", func(t *testing.T) {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.Disable("u1", "12153 session dead")
		st, _ := p.Status("u1")
		if !st.Disabled || st.Reason != "12153 session dead" || st.DisabledReason != "12153 session dead" {
			t.Errorf("禁用号应同时透出 reason 与 disabled_reason: %+v", st)
		}
	})

	t.Run("冷却中", func(t *testing.T) {
		p := New("")
		p.Add(&auth.Auth{UID: "u1"})
		p.CooldownUntilTomorrow4AM("u1", "余额不足")
		st, _ := p.Status("u1")
		if !st.Cooling || st.Reason != "余额不足" {
			t.Errorf("冷却中应透出原因: %+v", st)
		}
	})
}

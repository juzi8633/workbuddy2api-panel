package panel

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestUnavailableModelsMatrix 面板标记判据（与 /v1/models 同一阈值表达式）：
// 过半数 11102 实证 + 无账号成功过 → 标记；任一不满足 → 不标记。
// 跨包一致性（面板 vs server）由 internal/server 的
// TestUnavailableModelsCriteriaMatchesServer 断言。
func TestUnavailableModelsMatrix(t *testing.T) {
	p := pool.New("")
	for _, uid := range []string{"u1", "u2", "u3", "u4", "u5"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at-" + uid, ExpiresAt: 9999999999})
	}
	// 3/5 实证 + 无人成功 → 标记。
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.BlockModelBackoff(uid, "majority-dead", upstream.ModelBlockReason)
	}
	// 3/5 实证，但 u4 成功过 → 否决，不标记。
	for _, uid := range []string{"u1", "u2", "u3"} {
		p.BlockModelBackoff(uid, "vetoed", upstream.ModelBlockReason)
	}
	p.NoteModelCost("u4", "vetoed", 0.1, 100)
	// 1/5 实证 → 不足半数，不标记。
	p.BlockModelBackoff("u1", "minority", upstream.ModelBlockReason)

	pn := New(Config{Pool: p})
	got := pn.unavailableModels()
	if !got["majority-dead"] {
		t.Error("过半数实证 + 无人成功 → 应标记不可用")
	}
	if got["vetoed"] {
		t.Error("有账号成功过（Healthy>0）→ 不得标记（否决项）")
	}
	if got["minority"] {
		t.Error("1/5 不足半数 → 不得标记")
	}
}

// TestUnavailableModelsNoPool 无 Pool 时不 panic、返回空。
func TestUnavailableModelsNoPool(t *testing.T) {
	pn := New(Config{})
	if got := pn.unavailableModels(); len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

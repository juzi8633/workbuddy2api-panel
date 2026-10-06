package upstream

import (
	"encoding/json"
	"testing"
)

// TestPrepareBodyMergesAdjacentToolCalls 端到端（出站管线级）钉住 11148 修复：
// 背靠背的两条 assistant.tool_calls 必须在出站前合成一条。
//
// 这是 PR #93 记录的真实事故形态：部分 OpenAI 兼容 agent 客户端回放历史时把同一批
// 并行工具调用拆成多条紧邻的 assistant 消息，上游 deepseek 系模型判
// 400 code=11148（tool_call_sequence_broken），整条会话报废且换号无效。
func TestPrepareBodyMergesAdjacentToolCalls(t *testing.T) {
	// 客户端报文形状：两条紧邻的 assistant(各带一个 tool_call) + 两条结果
	raw := []byte(`{"model":"deepseek-v4.1-flash","messages":[
	  {"role":"assistant","content":null,"tool_calls":[{"id":"c00","type":"function","function":{"name":"f","arguments":"{}"}}]},
	  {"role":"assistant","content":null,"tool_calls":[{"id":"c01","type":"function","function":{"name":"g","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c00","content":"r0"},
	  {"role":"tool","tool_call_id":"c01","content":"r1"}
	]}`)

	out := PrepareBodyOpt(raw, false)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	// 期望：两条 assistant 合成一条（tool_calls 各一个），随后两条 tool = 共 3 条。
	if len(msgs) != 3 {
		t.Fatalf("messages=%d want 3（背靠背 assistant 未合并）: %s", len(msgs), out)
	}
	first, _ := msgs[0].(map[string]any)
	if role, _ := first["role"].(string); role != "assistant" {
		t.Fatalf("首条 role=%v want assistant", first["role"])
	}
	tcs, _ := first["tool_calls"].([]any)
	if len(tcs) != 2 {
		t.Fatalf("合并后 tool_calls=%d want 2: %s", len(tcs), out)
	}
	// 顺序必须保持原相对顺序 c00 → c01。
	for i, want := range []string{"c00", "c01"} {
		tc, _ := tcs[i].(map[string]any)
		if id, _ := tc["id"].(string); id != want {
			t.Errorf("tool_calls[%d].id=%v want %s", i, tc["id"], want)
		}
	}
}

// TestPrepareBodyLeavesNonAdjacentToolCallsAlone 反向守卫：不是「紧邻」的
// assistant(tool_calls) 不得被合并——隔着消息说明不是同一批声明，凭猜测合并会改语义。
func TestPrepareBodyLeavesNonAdjacentToolCallsAlone(t *testing.T) {
	raw := []byte(`{"model":"deepseek-v4.1-flash","messages":[
	  {"role":"assistant","content":null,"tool_calls":[{"id":"c00","type":"function","function":{"name":"f","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c00","content":"r0"},
	  {"role":"user","content":"next"},
	  {"role":"assistant","content":null,"tool_calls":[{"id":"c01","type":"function","function":{"name":"g","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c01","content":"r1"}
	]}`)

	out := PrepareBodyOpt(raw, false)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages=%d want 5（非紧邻调用被误合并）: %s", len(msgs), out)
	}
}

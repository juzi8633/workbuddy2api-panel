// claim_stuck_test.go 钉住上游 issue #174：任务**已达标但未领奖**时，自动化链路
// 必须能把它领掉，而不是当作「已完成」跳过。
//
// 生产实证（上游报告）：新号跑完成长任务后积分停在 2000/2100/2300 上不去 4000。
// 任务本身达标（progress.current >= progress.target、accept_status 已 accepted），
// 差的只是没人去 claim。三条相关链路各自差一步：
//
//  1. growthPending 要求任务在 autoActions 白名单里 —— 不在白名单的任务即使达标也
//     永远扫不进队列；
//  2. runGrowthQueued 会领奖，但要**先重跑一遍行为动作**（达标任务重跑动作是浪费，
//     且一次性任务的动作重跑可能直接失败 → 永远领不到）；
//  3. runAutoAll（面板「一键完成」）对 `current >= target` 直接 skip，**根本不领**。
//
// 本 fork 的既有实现里 1、2 已按「达标也入队」处理（见 growthPending 注释），
// 但 3 明确是「已完成」跳过 —— 用户点「一键完成」时，达标未领的任务会被跳过，
// 积分就停在那里。本测试钉住 3 的修复：达标未领必须走**只领奖、不重跑动作**的路径。
package panel

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// fakeGrowthUpstream 造一个只回任务列表 + 记录 claim 调用的假上游。
// tasksJSON 为 GET /activity/growth/tasks 的响应体。
func fakeGrowthUpstream(t *testing.T, tasksJSON string) (*upstream.Client, *[]string) {
	t.Helper()
	var claimed []string
	c := &upstream.Client{
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch {
			case strings.Contains(r.URL.Path, "/claim"):
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				// .../tasks/{code}/claim
				for i, p := range parts {
					if p == "tasks" && i+1 < len(parts) {
						claimed = append(claimed, parts[i+1])
					}
				}
				body = `{"code":0,"data":{"already_claimed":false,"credit":300,"energy":5}}`
			case strings.Contains(r.URL.Path, "/tasks"):
				body = tasksJSON
			default:
				body = `{"code":0,"data":{}}`
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})},
	}
	return c, &claimed
}

func testPanelWithUpstream(t *testing.T, up *upstream.Client, accounts ...*auth.Auth) *Panel {
	t.Helper()
	p := pool.New("")
	for _, a := range accounts {
		p.Add(a)
	}
	return New(Config{Pool: p, Upstream: up})
}

// TestStuckClaimIsNotSkipped 是本次修复的核心回归。
//
// 场景：`chat_5` 已达标（5/5）、未领奖。点「一键完成」时必须真的调用 claim，
// 而不是回一句「已完成」然后什么都不做。
func TestStuckClaimIsNotSkipped(t *testing.T) {
	const tasksJSON = `{"code":0,"data":{"tasks":[
		{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":5,
		 "accept_status":"accepted","reward_credit":300,"reward_energy":5}
	]}}`
	up, claimed := fakeGrowthUpstream(t, tasksJSON)
	a := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPanelWithUpstream(t, up, a)

	results := p.runAutoAll(a)
	if len(*claimed) == 0 {
		var msgs []string
		for _, r := range results {
			if r["task_code"] == "chat_5" {
				msgs = append(msgs, r["status"].(string)+": "+r["message"].(string))
			}
		}
		t.Fatalf("达标未领的任务没有被领奖（claim 调用 0 次）；runAutoAll 对 chat_5 的结果=%v", msgs)
	}
	if (*claimed)[0] != "chat_5" {
		t.Fatalf("领奖的任务码不对: %v", *claimed)
	}
}

// TestClaimedTaskStillSkipped 反面钉桩：**已领取**的任务不得重复调用 claim
// （上游会返回 already_claimed；重复调用是无效流量，且会让日志失真）。
func TestClaimedTaskStillSkipped(t *testing.T) {
	const tasksJSON = `{"code":0,"data":{"tasks":[
		{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":5,
		 "accept_status":"claimed","reward_credit":300,"reward_energy":5}
	]}}`
	up, claimed := fakeGrowthUpstream(t, tasksJSON)
	a := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPanelWithUpstream(t, up, a)

	p.runAutoAll(a)
	if len(*claimed) != 0 {
		t.Fatalf("已领取的任务不应再调用 claim，实际调用了 %v", *claimed)
	}
}

// TestUnfinishedTaskStillRunsAction 反面钉桩：**未达标**的任务必须照常跑行为动作
// （不能因为新增「只领奖」路径就把正常动作吞掉）。
func TestUnfinishedTaskStillRunsAction(t *testing.T) {
	const tasksJSON = `{"code":0,"data":{"tasks":[
		{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":1,
		 "accept_status":"accepted","reward_credit":300,"reward_energy":5}
	]}}`
	up, claimed := fakeGrowthUpstream(t, tasksJSON)
	a := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPanelWithUpstream(t, up, a)

	results := p.runAutoAll(a)
	if len(*claimed) != 0 {
		t.Fatalf("未达标的任务不应领奖，实际调用了 %v", *claimed)
	}
	// 必须尝试过动作（未达标 → 走 act.run，最终因进度仍不足而不 claim）。
	found := false
	for _, r := range results {
		if r["task_code"] == "chat_5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未达标任务应出现在结果里: %v", results)
	}
}

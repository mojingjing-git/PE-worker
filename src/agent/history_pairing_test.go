// agent/history_pairing_test.go —— S2-2/S2-3 回归门禁：ClipHistory 的配对修复。
//
// 背景：S2-1 把 agent.NewLoop 从 runWorker 的 for 循环**内**提到循环**外**，
// 于是 history 第一次真正跨轮累积。随之而来的是原来被"每次重建 Loop"
// 掩盖的两个 400 洞 —— 它们在生产路径上从未跑过，一上线就会炸。
//
// 这两个洞的共同点：**显式报错但无解** —— 不是返回错内容，而是上游直接 400，
// 且本会话之后每次请求都 400，用户只能重启进程。
package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"peagent/src/tools"
)

// toolPairHistory 造 [system, user, (assistant+tool)×n] 的历史。
func toolPairHistory(n int) []Message {
	m := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "q0"},
	}
	for i := 0; i < n; i++ {
		m = append(m, Message{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "c" + string(rune('a'+i%26)), Name: "ls", Arguments: `{"input":"."}`}},
		})
		m = append(m, Message{
			Role:       RoleTool,
			ToolCallID: "c" + string(rune('a'+i%26)),
			Content:    "result",
		})
	}
	return m
}

// checkNoOrphanTool 断言：每条 tool 消息前面必须紧邻一条带 tool_calls 的
// assistant。这正是 OpenAI/Anthropic 的硬要求。
func checkNoOrphanTool(t *testing.T, m []Message, tag string) {
	t.Helper()
	for i, msg := range m {
		if msg.Role != RoleTool {
			continue
		}
		// 往前找最近的 assistant
		found := false
		for j := i - 1; j >= 0; j-- {
			if m[j].Role == RoleAssistant {
				found = len(m[j].ToolCalls) > 0
				break
			}
			if m[j].Role == RoleUser {
				break
			}
		}
		if !found {
			roles := roleSeq(m)
			t.Fatalf("[%s] 第 %d 条 tool 消息的父 assistant 没有 tool_calls —— OpenAI 会报 "+
				"\"messages with role 'tool' must be a response to a preceding message with 'tool_calls'\""+
				"\n实际序列: %s", tag, i, roles)
		}
	}
}

// checkStartsWithUser 断言第一条非 system 消息必须是 user。
func checkStartsWithUser(t *testing.T, m []Message, tag string) {
	t.Helper()
	for _, msg := range m {
		if msg.Role == RoleSystem {
			continue
		}
		if msg.Role != RoleUser {
			t.Fatalf("[%s] 第一条非 system 消息是 %s，应为 user\n实际序列: %s",
				tag, msg.Role, roleSeq(m))
		}
		return
	}
}

func roleSeq(m []Message) string {
	parts := make([]string, 0, len(m))
	for _, msg := range m {
		parts = append(parts, string(msg.Role))
	}
	return strings.Join(parts, ",")
}

// TestClipHistory_ToolFirstIsRepaired 是 S2-2 的核心门禁：
// 削窗切在 tool 中间时，必须回退到合法起点，绝不能留下孤儿 tool。
func TestClipHistory_ToolFirstIsRepaired(t *testing.T) {
	// 40 条滑窗；构造足够长的历史让切点落在 tool 上
	for _, n := range []int{1, 5, 14, 30, 45} {
		m := toolPairHistory(n)
		got := ClipHistory(m)
		if len(got) == 0 {
			t.Errorf("n=%d: ClipHistory 返回空 —— 会导致发空 messages → 上游 400", n)
			continue
		}
		checkStartsWithUser(t, got, "toolFirst")
		checkNoOrphanTool(t, got, "toolFirst")
	}
}

// TestClipHistory_NeverEmpty 是 S2-3 的门禁：全 assistant 输入时不能返空。
func TestClipHistory_NeverEmpty(t *testing.T) {
	m := []Message{{Role: RoleSystem, Content: "sys"}}
	for i := 0; i < 60; i++ {
		m = append(m, Message{Role: RoleAssistant, Content: "chatter"})
	}
	got := ClipHistory(m)
	if len(got) == 0 {
		t.Fatalf("ClipHistory 返回空 —— 发空 messages 会被上游 400")
	}
	if got[0].Role != RoleSystem {
		t.Errorf("空场景下 system 段应保留，实际首条 role = %s", got[0].Role)
	}
}

// TestClipHistory_EmptyInput 边界：空输入不该 panic。
func TestClipHistory_EmptyInput(t *testing.T) {
	got := ClipHistory(nil)
	if len(got) != 0 {
		t.Errorf("ClipHistory(nil) = %v, want empty", got)
	}
}

// TestClipHistory_PreservesSystem 保证 system 段始终在最前。
func TestClipHistory_PreservesSystem(t *testing.T) {
	m := toolPairHistory(20)
	got := ClipHistory(m)
	if len(got) == 0 || got[0].Role != RoleSystem || got[0].Content != "sys" {
		t.Errorf("system 段丢失或不在最前: %s", roleSeq(got))
	}
}

// TestLoopHistory_AccumulatesAcrossRuns 验证 S2-1 的效果：
// 同一个 Loop 连跑两轮，history 必须增长（而不是每轮重置）。
//
// 用项目现有的 httptest mock 模式（与 loop_test.go 一致）而不是自造 fake。
func TestLoopHistory_AccumulatesAcrossRuns(t *testing.T) {
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"答一"},"finish_reason":"stop"}]}`,
		`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"答二"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()

	c, err := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 5, "sys")
	before := len(l.History())
	if before != 1 {
		t.Fatalf("初始 history 应只有 system，实际 %d 条", before)
	}
	if _, err := l.Run(context.Background(), "问一"); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	afterFirst := len(l.History())
	if _, err := l.Run(context.Background(), "问二"); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	afterSecond := len(l.History())

	// S2-1 修好后：第二轮之后 history 必须比第一轮更长（跨轮累积）
	if afterSecond <= afterFirst {
		t.Errorf("第二轮后 history 未增长（%d -> %d）—— 说明每轮仍在重建 Loop，"+
			"多轮对话记忆仍然是坏的", afterFirst, afterSecond)
	}
	if afterFirst <= before {
		t.Errorf("第一轮后 history 未增长（%d -> %d）", before, afterFirst)
	}
}

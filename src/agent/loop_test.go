// Package agent — loop_test.go
//
// 测三件事：
//   1. Loop.Run() 走完一条 user → assistant(text) 的最短路径
//   2. Loop.Run() 走 user → assistant(tool_call) → user(tool_result) → assistant(text) 的多轮
//   3. Loop.Run() max turns 上限 + ctx cancel
//   4. history.go ClipHistory / ClipImages 的边界
//   5. verdict.go 的 step 记录 + String 输出
//
// mock LLM 走 httptest；tool 走真实 tools.RunByName（用 help 这种无副作用的）。
package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"peagent/src/tools"
)

// mockLLMScript 是按顺序返回的 canned 响应（每个元素是一次 Chat() 的返回值）。
type mockLLMScript struct {
	calls int32
	steps []string // JSON body，每次 Chat 用一个
}

func (m *mockLLMScript) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		idx := int(atomic.AddInt32(&m.calls, 1)) - 1
		if idx >= len(m.steps) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"script exhausted"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(m.steps[idx]))
	})
}

// ---------- Loop 最短路径（text-only） ----------

func TestLoop_TextOnly(t *testing.T) {
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 5, "sys")
	got, err := l.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "hi" {
		t.Errorf("got %q, want hi", got)
	}
	if atomic.LoadInt32(&script.calls) != 1 {
		t.Errorf("LLM called %d times, want 1", script.calls)
	}
}

// ---------- Loop tool_call 路径 ----------

func TestLoop_ToolCall(t *testing.T) {
	// 轮 1: 模型说调 help 工具
	// 轮 2: 模型看完结果给纯文本
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"help","arguments":"{\"input\":\"\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"我已查过 help, 没啥用"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 5, "sys")
	got, err := l.Run(context.Background(), "列出工具")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "我已查过 help, 没啥用" {
		t.Errorf("got %q", got)
	}
	if atomic.LoadInt32(&script.calls) != 2 {
		t.Errorf("LLM called %d times, want 2", script.calls)
	}
	// history 应该有 1 system + 1 user + 1 assistant(tool_calls) + 1 tool + 1 assistant(text) = 5
	if h := l.History(); len(h) != 5 {
		t.Errorf("history len = %d, want 5; h=%+v", len(h), h)
	}
}

// ---------- Loop 工具错误不中断 ----------

func TestLoop_ToolErrorBubbledNotAborted(t *testing.T) {
	// 调一个不存在的工具 → 工具返 err → loop 把 err 当 tool result 回灌 → 模型下一步
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"nonexistent","arguments":"{\"input\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"我换成 help 试试"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 5, "sys")
	got, err := l.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "我换成 help 试试" {
		t.Errorf("got %q", got)
	}
	// tool result 里应该有 ERROR 文本（错误透传，L5）
	h := l.History()
	var foundErrTool bool
	for _, m := range h {
		if m.Role == RoleTool && strings.Contains(m.Content, "ERROR:") {
			foundErrTool = true
		}
	}
	if !foundErrTool {
		t.Errorf("history 应含 tool 错误结果: %+v", h)
	}
}

// ---------- Loop max turns ----------

func TestLoop_MaxTurns(t *testing.T) {
	// 模型永远调工具不收尾
	toolCallResp := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"help","arguments":"{\"input\":\"\"}"}}]},"finish_reason":"tool_calls"}]}`
	script := &mockLLMScript{steps: []string{
		toolCallResp, toolCallResp, toolCallResp, toolCallResp, toolCallResp,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 3, "sys")
	_, err := l.Run(context.Background(), "loop")
	if err == nil {
		t.Fatal("want err on max turns")
	}
	if !strings.Contains(err.Error(), "max turns") {
		t.Errorf("err should mention max turns, got: %v", err)
	}
	if atomic.LoadInt32(&script.calls) != 3 {
		t.Errorf("LLM called %d times, want 3 (== MaxTurns)", script.calls)
	}
}

// ---------- Loop ctx cancel ----------

func TestLoop_ContextCancel(t *testing.T) {
	// 第一次调用立刻 cancel
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, nil, 5, "sys")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Run(ctx, "hi")
	if err == nil {
		t.Fatal("want err on cancelled ctx")
	}
}

// ---------- Loop Reset ----------

func TestLoop_Reset(t *testing.T) {
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"}]}`,
		`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	l := NewLoop(c, nil, 5, "sys-prompt")
	_, _ = l.Run(context.Background(), "q1")
	if len(l.History()) != 3 { // system + user + assistant
		t.Errorf("after 1st run, hist = %d, want 3", len(l.History()))
	}
	l.Reset()
	if len(l.History()) != 1 { // only system
		t.Errorf("after reset, hist = %d, want 1", len(l.History()))
	}
	if l.History()[0].Role != RoleSystem {
		t.Errorf("after reset, first should be system, got %s", l.History()[0].Role)
	}
}

// ---------- history.go ----------

func TestClipHistory_TruncatesLongContent(t *testing.T) {
	long := strings.Repeat("a", MaxContentBytes+10)
	m := []Message{{Role: RoleUser, Content: long}}
	out := ClipHistory(m)
	if len(out[0].Content) <= MaxContentBytes {
		t.Errorf("content not truncated: len=%d", len(out[0].Content))
	}
	if !strings.HasSuffix(out[0].Content, "[truncated]") {
		t.Errorf("truncation marker missing: %q", out[0].Content[len(out[0].Content)-20:])
	}
}

func TestClipHistory_KeepsSystem(t *testing.T) {
	msgs := []Message{}
	msgs = append(msgs, Message{Role: RoleSystem, Content: "sys"})
	for i := 0; i < maxHistoryMessages+5; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf("u%d", i)})
	}
	out := ClipHistory(msgs)
	// system 必须保留
	if len(out) == 0 || out[0].Role != RoleSystem {
		t.Errorf("system not kept: out[0]=%v", out[0])
	}
	// 总数 ≤ maxHistoryMessages
	if len(out) > maxHistoryMessages {
		t.Errorf("out len = %d > %d", len(out), maxHistoryMessages)
	}
}

func TestClipHistory_RepairsAssistantFirst(t *testing.T) {
	// 极端情况：削后第一条变成 assistant（不应该，但防御一下）
	msgs := []Message{
		{Role: RoleSystem, Content: "s"},
	}
	for i := 0; i < maxHistoryMessages+5; i++ {
		// 交替 user/assistant
		if i%2 == 0 {
			msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf("u%d", i)})
		} else {
			msgs = append(msgs, Message{Role: RoleAssistant, Content: fmt.Sprintf("a%d", i)})
		}
	}
	out := ClipHistory(msgs)
	if out[0].Role == RoleAssistant {
		t.Errorf("out[0] should not be assistant after clip, got: %+v", out[0])
	}
}

func TestClipImages_RemovesOldImages(t *testing.T) {
	mk := func(role Role, imgs []Image, txt string) Message {
		return Message{Role: role, Images: imgs, Content: txt}
	}
	img1 := Image{MediaType: "image/png", Data: []byte{1, 2, 3}}
	img2 := Image{MediaType: "image/png", Data: []byte{4, 5, 6}}
	img3 := Image{MediaType: "image/png", Data: []byte{7, 8, 9}}
	msgs := []Message{
		mk(RoleUser, []Image{img1}, "u1"),
		{Role: RoleAssistant, Content: "a1"},
		mk(RoleUser, []Image{img2}, "u2"),
		{Role: RoleAssistant, Content: "a2"},
		mk(RoleUser, []Image{img3}, "u3"),
	}
	ClipImages(msgs)
	// u1 应被裁掉
	if len(msgs[0].Images) != 0 {
		t.Errorf("u1 images should be cleared, got %d", len(msgs[0].Images))
	}
	if !strings.Contains(msgs[0].Content, "[截图已省略") {
		t.Errorf("u1 content 应含 [截图已省略], got: %q", msgs[0].Content)
	}
	// u3 保留
	if len(msgs[4].Images) != 1 {
		t.Errorf("u3 images should be kept, got %d", len(msgs[4].Images))
	}
}

// ---------- verdict.go ----------

func TestVerdict_AllOK(t *testing.T) {
	v := NewVerdict("test")
	v.Step("a", true)
	v.Step("b", true)
	if !v.OK() {
		t.Error("should be OK")
	}
	s := v.String()
	if !strings.Contains(s, "[OK]  a") {
		t.Errorf("missing OK a: %s", s)
	}
	if !strings.Contains(s, "=====> PASS") {
		t.Errorf("missing PASS: %s", s)
	}
}

func TestVerdict_FailIncluded(t *testing.T) {
	v := NewVerdict("test")
	v.Step("a", true)
	v.Step("b", false, "want=1 got=2")
	v.StepErr("c", fmt.Errorf("boom"))
	if v.OK() {
		t.Error("should not be OK")
	}
	fails := v.Failures()
	if len(fails) != 2 {
		t.Errorf("failures = %d, want 2 (%v)", len(fails), fails)
	}
	s := v.String()
	if !strings.Contains(s, "=====> FAIL") {
		t.Errorf("missing FAIL: %s", s)
	}
	if !strings.Contains(s, "want=1 got=2") {
		t.Errorf("missing note: %s", s)
	}
	if !strings.Contains(s, "boom") {
		t.Errorf("missing StepErr note: %s", s)
	}
}

// ---------- 集成：loop + verdict（演示 L4 用法） ----------

func TestLoop_L4VerdictUsage(t *testing.T) {
	// 演示 v1 第四轮 L4：loop 跑的每一步都进 VERDICT，最后总评。
	script := &mockLLMScript{steps: []string{
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"help","arguments":"{\"input\":\"\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
	}}
	srv := httptest.NewServer(script.handler())
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})

	vd := NewVerdict("loop_l4_demo")
	l := NewLoop(c, &tools.Context{Confirm: func(string) bool { return true }}, 5, "sys")
	got, err := l.Run(context.Background(), "test")
	vd.Step("no_error", err == nil, errString(err))
	vd.Step("text", got == "ok", got)
	vd.Step("llm_called_twice", atomic.LoadInt32(&script.calls) == 2, fmt.Sprintf("calls=%d", atomic.LoadInt32(&script.calls)))
	if !vd.OK() {
		t.Fatal(vd.String())
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

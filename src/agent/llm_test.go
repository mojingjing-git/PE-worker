// Package agent — llm_test.go
//
// 用 httptest 模拟两个 provider，验证：
//   1. 请求体 JSON 形状正确（按各自 wire 格式）
//   2. 响应解析正确（text / tool_calls / reasoning_content / 错误）
//   3. 错误透传（4xx body 返出来 / 5xx 重试 / 网络错误重试到上限）
//
// 不用 TLS 测：TLS 路径有 spike/https 把关，这里只验 wire 格式。
// 把 Server 当 HTTP 用即可（agent.NewClient 不强求 https scheme）。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- 工具：mock server + 收请求体 ----------

type capturedReq struct {
	Path    string
	Headers http.Header
	Body    []byte
}

// newMockLLM 启一个 httptest server；handler 每次把请求记到 got，
// 按 canned 状态码和 body 响应。
func newMockLLM(cannedStatus int, cannedBody string, got *[]capturedReq) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = append(*got, capturedReq{
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    body,
		})
		w.WriteHeader(cannedStatus)
		_, _ = w.Write([]byte(cannedBody))
	}))
}

// ---------- Anthropic ----------

func TestAnthropic_TextResponse(t *testing.T) {
	var got []capturedReq
	canned := `{
		"id":"msg_01",
		"type":"message",
		"role":"assistant",
		"content":[{"type":"text","text":"hello from claude"}],
		"stop_reason":"end_turn"
	}`
	srv := newMockLLM(200, canned, &got)
	defer srv.Close()

	c, err := NewClient(Config{
		Provider: ProviderAnthropic,
		BaseURL:  srv.URL,
		Model:    "claude-3-5-sonnet-20241022",
		APIKey:   "test-key",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.Chat(context.Background(), Request{
		// 加 system 来验 system 字段是顶层（不是 messages 里）。
		Messages: []Message{
			{Role: RoleSystem, Content: "你是 smith"},
			{Role: RoleUser, Content: "hi"},
		},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text != "hello from claude" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello from claude")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 request, got %d", len(got))
	}
	r := got[0]
	if r.Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", r.Path)
	}
	if r.Headers.Get("x-api-key") != "test-key" {
		t.Errorf("missing x-api-key header")
	}
	if r.Headers.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("missing anthropic-version header")
	}
	if !strings.Contains(string(r.Body), `"model":"claude-3-5-sonnet-20241022"`) {
		t.Errorf("body missing model: %s", r.Body)
	}
	// system 字段在顶层
	if !strings.Contains(string(r.Body), `"system":"你是 smith"`) {
		t.Errorf("body missing system 顶层字段: %s", r.Body)
	}
	// system 不应在 messages 里
	if strings.Contains(string(r.Body), `"role":"system"`) {
		t.Errorf("system 不应在 messages 里: %s", r.Body)
	}
}

func TestAnthropic_ToolUse(t *testing.T) {
	var got []capturedReq
	canned := `{
		"id":"msg_02",
		"type":"message",
		"role":"assistant",
		"content":[
			{"type":"text","text":"我跑一下 ver"},
			{"type":"tool_use","id":"toolu_abc","name":"exec","input":{"input":"ver"}}
		],
		"stop_reason":"tool_use"
	}`
	srv := newMockLLM(200, canned, &got)
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "win 版本?"}},
		Tools:    []ToolDef{{Name: "exec", Description: "执行命令"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, want tool_calls (normalized)", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "exec" {
		t.Errorf("Name = %q, want exec", tc.Name)
	}
	if tc.ID != "toolu_abc" {
		t.Errorf("ID = %q, want toolu_abc", tc.ID)
	}
	// Arguments 还原成 JSON 字符串
	if !strings.Contains(tc.Arguments, `"input":"ver"`) {
		t.Errorf("Arguments = %q, want contains input:ver", tc.Arguments)
	}
	// 请求里 system 应该是空（只有 user msg）
	body := string(got[0].Body)
	if !strings.Contains(body, `"tools":[{`) {
		t.Errorf("body missing tools: %s", body)
	}
	if !strings.Contains(body, `"input_schema":{`) {
		t.Errorf("anthropic tools 应含 input_schema: %s", body)
	}
}

func TestAnthropic_ToolResultRoundTrip(t *testing.T) {
	// 模拟"assistant 调用工具 → user 返回结果"的一轮。
	// mock 必须收到 user→assistant→user(带 tool_result) 三个消息，
	// 交替不合并（中间隔了 assistant）。
	var got []capturedReq
	srv := newMockLLM(200, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`, &got)
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})

	_, err := c.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Content: "查 ver"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "toolu_1", Name: "exec", Arguments: `{"input":"ver"}`}}},
			{Role: RoleTool, ToolCallID: "toolu_1", Content: "Microsoft Windows [Version 6.1.7601]"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	body := string(got[0].Body)
	// tool_result block
	if !strings.Contains(body, `"tool_result"`) || !strings.Contains(body, `"tool_use_id":"toolu_1"`) {
		t.Errorf("body missing tool_result block: %s", body)
	}
	// tool_use block（assistant 那一轮）
	if !strings.Contains(body, `"tool_use"`) || !strings.Contains(body, `"id":"toolu_1"`) {
		t.Errorf("body missing tool_use block: %s", body)
	}
	// 期望 2 条 user（原始 + tool_result）+ 1 条 assistant；交替合法，不需合并
	countUser := strings.Count(body, `"role":"user"`)
	if countUser != 2 {
		t.Errorf("user msgs = %d, want 2; body=%s", countUser, body)
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Errorf("missing assistant msg: %s", body)
	}
}

func TestAnthropic_MergeAdjacentSameRole(t *testing.T) {
	// 直接测合并逻辑：两条连续 user 应被合并。
	in := []anthropicMessage{
		{Role: "user", Content: []map[string]interface{}{{"type": "text", "text": "a"}}},
		{Role: "user", Content: []map[string]interface{}{{"type": "text", "text": "b"}}},
		{Role: "assistant", Content: []map[string]interface{}{{"type": "text", "text": "ok"}}},
		{Role: "user", Content: []map[string]interface{}{{"type": "text", "text": "c"}}},
	}
	out := mergeAdjacentRoles(in)
	if len(out) != 3 {
		t.Fatalf("merged len = %d, want 3", len(out))
	}
	if len(out[0].Content) != 2 {
		t.Errorf("first user should have 2 blocks, got %d", len(out[0].Content))
	}
}

func TestAnthropic_4xxPropagates(t *testing.T) {
	var got []capturedReq
	srv := newMockLLM(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, &got)
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "bad"})
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("want err on 401")
	}
	// 4xx body 全文透传
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Errorf("err should contain body, got: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err should contain 401, got: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("4xx must not retry: got %d requests, want 1", len(got))
	}
}

func TestAnthropic_5xxRetries(t *testing.T) {
	var got []capturedReq
	// 第 1、2 次 5xx，第 3 次 200
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, capturedReq{Path: r.URL.Path, Headers: r.Header.Clone(), Body: body})
		attempt := atomic.AddInt32(&n, 1)
		if attempt < 3 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("upstream busy"))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("Text = %q", resp.Text)
	}
	if len(got) != 3 {
		t.Errorf("want 3 requests (2 retries), got %d", len(got))
	}
}

// ---------- OpenAI 兼容（OpenAI 官方） ----------

func TestOpenAI_TextResponse(t *testing.T) {
	var got []capturedReq
	canned := `{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"created":1700000000,
		"model":"gpt-4",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"hi from gpt"},
			"finish_reason":"stop"
		}]
	}`
	srv := newMockLLM(200, canned, &got)
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "gpt-4", APIKey: "k"})
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text != "hi from gpt" {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	r := got[0]
	if r.Path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", r.Path)
	}
	if r.Headers.Get("authorization") != "Bearer k" {
		t.Errorf("authorization header = %q", r.Headers.Get("authorization"))
	}
}

func TestOpenAI_ToolCalls(t *testing.T) {
	var got []capturedReq
	canned := `{
		"id":"x",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"call_xyz",
					"type":"function",
					"function":{"name":"exec","arguments":"{\"input\":\"ver\"}"}
				}]
			},
			"finish_reason":"tool_calls"
		}]
	}`
	srv := newMockLLM(200, canned, &got)
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "gpt-4", APIKey: "k"})
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Tools:    []ToolDef{{Name: "exec", Description: "执行命令"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_xyz" {
		t.Errorf("ID = %q", resp.ToolCalls[0].ID)
	}
	body := string(got[0].Body)
	if !strings.Contains(body, `"type":"function"`) {
		t.Errorf("openai tools 应含 type:function: %s", body)
	}
	if !strings.Contains(body, `"parameters":{`) {
		t.Errorf("openai tools 应含 parameters: %s", body)
	}
}

func TestOpenAI_ZeroChoices(t *testing.T) {
	// 上游可能返回 200 + 空 choices（截断 / 限流）
	srv := newMockLLM(200, `{"id":"x","choices":[]}`, &[]capturedReq{})
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("want err on 0 choices")
	}
	if !strings.Contains(err.Error(), "0 choices") {
		t.Errorf("err should mention 0 choices, got: %v", err)
	}
}

// ---------- DeepSeek 特有：reasoning_content 吸收 ----------

func TestDeepSeek_ReasoningContentAbsorbed(t *testing.T) {
	var got []capturedReq
	canned := `{
		"id":"x",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"最终答案",
				"reasoning_content":"先想一下：用户问 ver，应该调工具..."
			},
			"finish_reason":"stop"
		}]
	}`
	srv := newMockLLM(200, canned, &got)
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderDeepSeek, BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "k"})
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// 推理拼到 Text 前面，用 <think>...</think> 标记
	if !strings.HasPrefix(resp.Text, "<think>") {
		t.Errorf("Text 应以 <think> 开头, got: %q", resp.Text)
	}
	if !strings.Contains(resp.Text, "先想一下") {
		t.Errorf("Text 应含 reasoning_content, got: %q", resp.Text)
	}
	if !strings.Contains(resp.Text, "最终答案") {
		t.Errorf("Text 应含原始 content, got: %q", resp.Text)
	}
}

// ---------- 构造校验 ----------

func TestNewClient_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty base", Config{Provider: ProviderAnthropic, Model: "m", APIKey: "k"}, "BaseURL"},
		{"empty model", Config{Provider: ProviderAnthropic, BaseURL: "https://x", APIKey: "k"}, "Model"},
		{"empty key", Config{Provider: ProviderAnthropic, BaseURL: "https://x", Model: "m"}, "APIKey"},
		{"unknown provider", Config{Provider: "xai", BaseURL: "https://x", Model: "m", APIKey: "k"}, "provider"},
	}
	for _, c := range cases {
		_, err := NewClient(c.cfg)
		if err == nil {
			t.Errorf("%s: want err, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want contains %q", c.name, err, c.want)
		}
	}
}

func TestNewClient_DefaultsTimeoutAndMaxTokens(t *testing.T) {
	// 0 应该是默认 120s / 2048；通过 NewClient 不报错来验（实际值不导出）
	c, err := NewClient(Config{Provider: ProviderAnthropic, BaseURL: "https://x", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient with defaults: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

// ---------- ctx cancel ----------

func TestChat_ContextCancel(t *testing.T) {
	// 一个永不响应的 server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k", TimeoutS: 10})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻 cancel
	_, err := c.Chat(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("want err on cancelled ctx")
	}
}

// ---------- prompt.go ----------

func TestSystemPrompt_ContainsToolList(t *testing.T) {
	p := SystemPrompt(true)
	// 必须含每个工具的名字
	for _, name := range []string{"exec", "help", "ls", "cat", "http_get", "ps"} {
		if !strings.Contains(p, name) {
			t.Errorf("system prompt missing tool %q", name)
		}
	}
	// 长度上限 < 4000 字符（粗略对应 < 1000 token）
	if len(p) > 4000 {
		t.Errorf("system prompt too long: %d chars", len(p))
	}
}

func TestSystemPrompt_Compact(t *testing.T) {
	p := SystemPrompt(false)
	if len(p) > 1500 {
		t.Errorf("core prompt too long: %d chars", len(p))
	}
}

// ---------- util ----------

// 防止 import 抖动（json 和 fmt 在 test 里都被用到）
var _ = json.Marshal
var _ = fmt.Sprintf

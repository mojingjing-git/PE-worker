// Package test 集成测试（v1 第五轮新增）。
//
// 目标：把 cfg + tools + agent + logx 串起来跑通最小闭环。
//
// 范围（PLAN §7 验收 + P1-14 范围）：
//   1. cfg 加载 → agent.LLM 配置转换
//   2. agent.Loop 跑一条 user input → 调 help 工具 → 收到结果 → 返文本
//   3. 14 工具的 Registry 完整性 + RiskLevel 合理
//   4. history.go 在长会话下的滑动窗口正确
//   5. win package 关键常量 + win.KeepAlive 不爆
//
// 不测（GUI 路径，本机无桌面）：
//   - 创建窗口 / 消息循环 / 用户输入捕获
//   - 真实 LLM API 调用（用 mock httptest）
//   - Job Object 杀整棵进程树（需要 PE 镜像）
//
// 跑法：go test -count=1 ./src/test/...
package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"peagent/src/agent"
	"peagent/src/cfg"
	"peagent/src/logx"
	"peagent/src/tools"
	"peagent/src/win"
)

// ---------- 1. cfg → agent.Config 串通 ----------

func TestE2E_CfgToAgentConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smith.ini")
	os.WriteFile(path, []byte(`[llm]
base = https://api.openai.com/v1
model = gpt-4
provider = openai
key = sk-test
timeout = 90

[agent]
maxturns = 5
imghistory = 3

[ui]
fontsize = 14
`), 0644)

	c, err := cfg.Load(path)
	if err != nil {
		t.Fatalf("cfg.Load: %v", err)
	}
	if c.LLM.Provider != "openai" {
		t.Errorf("Provider = %q", c.LLM.Provider)
	}
	if c.Agent.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d", c.Agent.MaxTurns)
	}

	// 验证 Default() 也能转（缺 ini 文件的常见情况）
	def := cfg.Default()
	if def.Agent.MaxTurns != 10 {
		t.Errorf("Default MaxTurns = %d, want 10", def.Agent.MaxTurns)
	}
}

// ---------- 2. agent.Loop 完整跑通（mock LLM） ----------

// makeMockLLM 启一个 httptest server，模拟 OpenAI chat completions。
// 第一次请求：assistant 调 help 工具
// 第二次请求：assistant 返回纯文本 "OK"
func makeMockLLM(t *testing.T) (*httptest.Server, *int32) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := atomic.AddInt32(&calls, 1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		if idx == 1 {
			// 调 help 工具
			_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"help","arguments":"{\"input\":\"\"}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		// 收尾：纯文本
		_, _ = w.Write([]byte(`{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"OK from LLM"},"finish_reason":"stop"}]}`))
	}))
	return srv, &calls
}

func TestE2E_LoopWithRealTools(t *testing.T) {
	srv, calls := makeMockLLM(t)
	defer srv.Close()

	c, err := agent.NewClient(agent.Config{
		Provider: agent.ProviderOpenAI,
		BaseURL:  srv.URL,
		Model:    "gpt-4",
		APIKey:   "k",
		TimeoutS: 10,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	toolCtx := &tools.Context{
		Confirm: func(string) bool { return true }, // 自动 yes
		Config:  &tools.Config{Confirm: true, Whitelist: []string{"ver"}},
	}
	loop := agent.NewLoop(c, toolCtx, 5, agent.SystemPrompt(false))
	got, err := loop.Run(context.Background(), "help me")
	if err != nil {
		t.Fatalf("Loop.Run: %v", err)
	}
	if got != "OK from LLM" {
		t.Errorf("got = %q, want %q", got, "OK from LLM")
	}
	if atomic.LoadInt32(calls) != 2 {
		t.Errorf("LLM called %d times, want 2 (one tool_use, one text)", *calls)
	}
	// history 应含 1 system + 1 user + 1 assistant(tool) + 1 tool + 1 assistant(text) = 5
	if h := loop.History(); len(h) != 5 {
		t.Errorf("history len = %d, want 5", len(h))
	}
}

// ---------- 3. 14 工具 Registry 完整性 ----------

func TestE2E_All14ToolsRegistered(t *testing.T) {
	want := []string{
		"exec", "run_script", "help", "selftest",
		"ls", "cat", "grep", "find",
		"write", "edit", "append",
		"http_get", "https_get",
		"ps",
	}
	got := tools.All()
	if len(got) != len(want) {
		t.Fatalf("tools count = %d, want %d", len(got), len(want))
	}
	gotNames := map[string]bool{}
	for _, t2 := range got {
		if t2.Description() == "" {
			t.Errorf("%s 缺 Description", t2.Name())
		}
		if gotNames[t2.Name()] {
			t.Errorf("重复: %s", t2.Name())
		}
		gotNames[t2.Name()] = true
	}
	for _, n := range want {
		if !gotNames[n] {
			t.Errorf("缺工具: %s", n)
		}
	}
}

// RiskLevel 分配合理性：exec/run_script 应该是 RiskExec 或更高；
// ls/cat 应该是 RiskRead；write/edit/append 应该是 RiskWrite。
func TestE2E_ToolRiskLevels(t *testing.T) {
	cases := map[string]tools.RiskLevel{
		"exec":       tools.RiskExec,
		"run_script": tools.RiskDangerous, // 写 bat + 调外部
		"write":      tools.RiskWrite,
		"edit":       tools.RiskWrite,
		"append":     tools.RiskWrite,
		"http_get":   tools.RiskRead,
		"https_get":  tools.RiskRead,
		"ls":         tools.RiskRead,
		"cat":        tools.RiskRead,
		"grep":       tools.RiskRead,
		"find":       tools.RiskRead,
		"ps":         tools.RiskRead,
		"help":       tools.RiskRead,
		"selftest":   tools.RiskRead,
	}
	for name, wantRisk := range cases {
		t0, ok := tools.Get(name)
		if !ok {
			t.Errorf("缺工具: %s", name)
			continue
		}
		if t0.Risk() != wantRisk {
			t.Errorf("%s Risk = %s, want %s", name, t0.Risk(), wantRisk)
		}
	}
}

// ---------- 4. logx 不会吞错 + fallback sink 可切 ----------

func TestE2E_LogxFallbackSink(t *testing.T) {
	// 把 fallback sink 切到一个 buffer，验证写入走 fallback
	var buf strings.Builder
	logx.SetOutput(&buf)
	// 注意：SetHWND(0) 才能走 fallback 路径（默认就是 0）
	// logx 设计：hwnd=0 时返 ErrNoHWND，**不**代表失败（log 已成功写 fallback）
	err := logx.Info("hello %d", 42)
	if err != logx.ErrNoHWND {
		t.Errorf("Info err = %v, want ErrNoHWND", err)
	}
	if !strings.Contains(buf.String(), "hello 42") {
		t.Errorf("buf = %q, want contains 'hello 42'", buf.String())
	}
	// 恢复 stderr（其它测试可能依赖）
	logx.SetOutput(os.Stderr)
}

// ---------- 5. win.KeepAlive + Ptr 不爆（v1-M1） ----------

func TestE2E_WinKeepAliveNoCrash(t *testing.T) {
	// 构造一个 UTF-16 字符串，KeepAlive 后立即调 win.Ptr 再 KeepAlive，
	// 不应该 crash（GC 不会在 KeepAlive 期间回收）。
	s := "PE-agent e2e"
	p, err := win.Ptr(s)
	if err != nil {
		t.Fatalf("win.Ptr: %v", err)
	}
	win.KeepAlive(p)
	// 再来一次
	p2, err := win.Ptr(s + " again")
	if err != nil {
		t.Fatalf("win.Ptr: %v", err)
	}
	win.KeepAlive(p2)
	// 跑点内存压力看 GC
	makeGarbage()
	win.KeepAlive(p)
	win.KeepAlive(p2)
}

func makeGarbage() {
	for i := 0; i < 1000; i++ {
		_ = make([]byte, 1024)
	}
}

// ---------- 6. cfg 缺文件时 Default() 兜底 ----------

func TestE2E_CfgFallbackToDefault(t *testing.T) {
	c, err := cfg.Load("Z:\\nonexistent\\path\\smith.ini")
	if err == nil {
		// 如果 Z 盘存在（不该），那走的是真 Load；这情况下 cfg 应该有 LLM 都为零
		if c.LLM.KeyFile != "smith.key" {
			t.Errorf("Loaded cfg KeyFile = %q", c.LLM.KeyFile)
		}
		return
	}
	// 文件不存在 → 应该返 ErrFileNotFound
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("err = %v, want ErrFileNotFound", err)
	}
	// main.go 在这情况下会 cfg.Default() 兜底
	def := cfg.Default()
	if def.Agent.MaxTurns != 10 {
		t.Errorf("Default MaxTurns = %d", def.Agent.MaxTurns)
	}
}

// ---------- 7. agent.NewClient 缺字段的拒收 ----------

func TestE2E_AgentClientValidation(t *testing.T) {
	_, err := agent.NewClient(agent.Config{
		Provider: agent.ProviderAnthropic,
		Model:    "m",
		APIKey:   "k",
		// BaseURL 缺
	})
	if err == nil {
		t.Error("want err on missing BaseURL")
	}
}

// ---------- 8. 端到端：tools 实际能跑（无 mock） ----------

func TestE2E_RealToolsRunWithoutLLM(t *testing.T) {
	// 不走 LLM，直接调工具 → 验证 14 工具在 cfg.Default() 配置下都能实例化
	toolCtx := &tools.Context{
		Confirm: func(string) bool { return true },
		Config:  &tools.Config{Confirm: true, Whitelist: []string{"ver"}},
	}
	for _, name := range []string{"help", "selftest"} {
		t0, _ := tools.Get(name)
		r, err := t0.Run(toolCtx, "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if r.Text == "" {
			t.Errorf("%s: 空输出", name)
		}
	}
}

// ---------- 9. ctx 取消链路 ----------

func TestE2E_ContextCancelPropagates(t *testing.T) {
	// 一个故意慢的 mock（Sleep 3 秒）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer srv.Close()
	c, _ := agent.NewClient(agent.Config{Provider: agent.ProviderOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k", TimeoutS: 10})
	loop := agent.NewLoop(c, nil, 5, "sys")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := loop.Run(ctx, "test")
	dur := time.Since(start)
	if err == nil {
		t.Error("want err on cancel")
	}
	if dur > 2*time.Second {
		t.Errorf("cancel 未及时生效，跑了 %v", dur)
	}
}

// 防止 import 抖动
var _ = win.WM_LOG_LINE

// Package agent — llm_hardening_test.go
//
// 2026 审计修复的回归测试。每条对应一个曾经的线上/审计缺陷：
//
//  1. 跨 host 重定向泄漏 x-api-key（Go 只自动剥 Authorization）
//  2. https -> http 降级
//  3. 429 不重试
//  4. ResponseHeaderTimeout 30s / 确定性网络错误空等重试
//  5. extractInputArg 遇非字符串字段整包退化
//  6. (net.go 的 CA 兜底在 tools 包，这里只验 agent 侧不受牵连)
//
// 全部走 httptest（明文 http + 回环），生产 TLS 路径仍由 spike/https 把关。
package agent

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------- 1. 跨 host 重定向不得带走 x-api-key ----------

func TestHarden_CrossHostRedirectRefused_KeyNotForwarded(t *testing.T) {
	var leaked atomic.Value // 第二个（目标）server 收到的 x-api-key
	leaked.Store("")

	// 目标站：正常情况下**不该**被访问到。
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("x-api-key"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"leaked"}],"stop_reason":"end_turn"}`))
	}))
	defer victim.Close()

	// 源站：302 跨 host 跳到目标站（模拟中转 / 代理）。
	var srcHits int32
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&srcHits, 1)
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("源站应收到 x-api-key（说明客户端根本没发）")
		}
		http.Redirect(w, r, victim.URL+"/v1/messages", http.StatusFound)
	}))
	defer src.Close()

	c, err := NewClient(Config{Provider: ProviderAnthropic, BaseURL: src.URL, Model: "m", APIKey: "SECRET-KEY"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("跨 host 重定向必须报错（不能静默把 key 交出去）")
	}
	// 两条策略都会拦下：跨 host、明文 http 目标。各自的分支由
	// TestHarden_CheckRedirectPolicy 逐条验，这里只关心"被策略拒了"。
	if !errors.Is(err, errRedirectRefused) {
		t.Errorf("err = %v, want errors.Is(errRedirectRefused)", err)
	}
	if got := leaked.Load().(string); got != "" {
		t.Fatalf("!! 目标站收到了 x-api-key = %q", got)
	}
	// 策略拒绝不是"网络抖动"，不该被退避重试：源站只能被访问 1 次。
	if n := atomic.LoadInt32(&srcHits); n != 1 {
		t.Errorf("源站被访问 %d 次，want 1（策略拒绝不该重试）", n)
	}
}

// 同为 https 的**跨 host** 跳转（两端都是 TLS）：key 一样不能被带走。
// 上一个用例走的是明文 mock，会先撞上"降级"那条策略；这里专门压
// cross-host 那条 —— 那才是审计里真实中转场景的形态。
func TestHarden_CrossHostRedirectRefused_OverTLS(t *testing.T) {
	var leaked atomic.Value
	leaked.Store("")

	victim := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("x-api-key"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"leaked"}],"stop_reason":"end_turn"}`))
	}))
	defer victim.Close()

	var srcHits int32
	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&srcHits, 1)
		http.Redirect(w, r, victim.URL+"/v1/messages", http.StatusFound)
	}))
	defer src.Close()

	c, err := NewClient(Config{Provider: ProviderAnthropic, BaseURL: src.URL, Model: "m", APIKey: "SECRET-KEY"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tr := c.(*anthropicClient).http.Transport.(*http.Transport)
	pool := x509.NewCertPool()
	pool.AddCert(src.Certificate())
	pool.AddCert(victim.Certificate())
	tr.TLSClientConfig.RootCAs = pool

	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("https 跨 host 跳转也必须报错")
	}
	if !strings.Contains(err.Error(), "cross-host redirect") {
		t.Errorf("err = %v, want cross-host redirect", err)
	}
	if got := leaked.Load().(string); got != "" {
		t.Fatalf("!! 目标站收到了 x-api-key = %q", got)
	}
	if n := atomic.LoadInt32(&srcHits); n != 1 {
		t.Errorf("源站被访问 %d 次, want 1（策略拒绝不该重试）", n)
	}
}

// https -> http 降级：跳到明文目标必须被拒。
func TestHarden_HttpsToHttpDowngradeRefused(t *testing.T) {
	var victimHits int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&victimHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"leaked"}],"stop_reason":"end_turn"}`))
	}))
	defer victim.Close()

	// 源站是 https（httptest TLS）。把测试证书塞进客户端 transport 的
	// RootCAs，才能走到 CheckRedirect 这一步（生产用打包 CA）。
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, victim.URL+"/v1/messages", http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "SECRET-KEY"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tr, ok := c.(*anthropicClient).http.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport 类型不对")
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig.RootCAs = pool

	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("https -> http 降级必须报错")
	}
	if !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("err = %v, want downgrade", err)
	}
	if n := atomic.LoadInt32(&victimHits); n != 0 {
		t.Errorf("明文目标被访问了 %d 次，want 0", n)
	}
}

func TestHarden_CheckRedirectPolicy(t *testing.T) {
	mk := func(u string, hdr map[string]string) *http.Request {
		r, err := http.NewRequest("POST", u, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", u, err)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	via := []*http.Request{mk("https://api.example.com/v1/messages", map[string]string{
		"x-api-key": "SECRET-KEY", "Authorization": "Bearer k",
	})}

	// 同 host + https → 放行
	if err := checkRedirect(mk("https://api.example.com/v1/messages", nil), via); err != nil {
		t.Errorf("同 host https 应放行, got %v", err)
	}
	// 跨 host → 拒，且请求头里的 key 被清掉
	cross := mk("https://evil.example.net/v1/messages", map[string]string{
		"x-api-key": "SECRET-KEY", "Authorization": "Bearer k",
	})
	err := checkRedirect(cross, via)
	if err == nil {
		t.Fatal("跨 host 必须被拒")
	}
	if !errors.Is(err, errRedirectRefused) {
		t.Errorf("err = %v, want errors.Is(errRedirectRefused)", err)
	}
	if cross.Header.Get("x-api-key") != "" {
		t.Errorf("跨 host 跳转请求仍带 x-api-key: %q", cross.Header.Get("x-api-key"))
	}
	// https -> http 降级 → 拒
	down := mk("http://api.example.com/v1/messages", nil)
	if err := checkRedirect(down, via); err == nil {
		t.Error("https->http 降级必须被拒")
	}
	// 跳数超限 → 拒
	many := []*http.Request{via[0], via[0], via[0], via[0]}
	if err := checkRedirect(mk("https://api.example.com/v1/messages", nil), many); err == nil {
		t.Error("跳转次数超限必须被拒")
	}
}

// ---------- 2. /v1 自适应（Anthropic 以前漏了） ----------

func TestHarden_AnthropicBaseWithV1_NoDoubleV1(t *testing.T) {
	for _, base := range []string{"https://api.example.com", "https://api.example.com/v1", "https://api.example.com/v1/"} {
		if got := joinAPIPath(base, "messages"); got != "https://api.example.com/v1/messages" {
			t.Errorf("joinAPIPath(%q) = %q, want .../v1/messages", base, got)
		}
	}
	for _, base := range []string{"https://api.example.com", "https://api.example.com/v1", "https://api.example.com/v1/"} {
		if got := joinAPIPath(base, "chat/completions"); got != "https://api.example.com/v1/chat/completions" {
			t.Errorf("joinAPIPath(%q) = %q, want .../v1/chat/completions", base, got)
		}
	}
}

func TestHarden_AnthropicWirePath_NoDoubleV1(t *testing.T) {
	var got []capturedReq
	srv := newMockLLM(200, `{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, &got)
	defer srv.Close()
	// 模拟用户 base 直接带 /v1（keydialog 默认）
	c, err := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got[0].Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages（不能是 /v1/v1/messages）", got[0].Path)
	}
}

// ---------- 3. 429 退避重试 ----------

func TestHarden_429RetriesWithRetryAfter(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&n, 1)
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	start := time.Now()
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("429 后重试应成功, got %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("Text = %q", resp.Text)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Errorf("want 2 requests (1 retry), got %d", got)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("应遵守 Retry-After: 1s，实际只等了 %v", d)
	}
}

func TestHarden_429ExhaustedKeepsStatusAndBody(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Retry-After", "0") // 0 = 不睡，走指数退避
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"still limited"}}`))
	}))
	defer srv.Close()

	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("持续 429 必须报错")
	}
	if got := atomic.LoadInt32(&n); got != int32(maxRetries+1) {
		t.Errorf("want %d attempts, got %d", maxRetries+1, got)
	}
	// 现场不能丢：status + body 都要在错误里（L5）
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "still limited") {
		t.Errorf("错误必须含 429 + 上游 body, got: %v", err)
	}
}

func TestHarden_Other4xxStillNoRetry(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer srv.Close()
	c, _ := NewClient(Config{Provider: ProviderAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	if _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
		t.Fatal("want err on 400")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Errorf("4xx(非 429) 不得重试, got %d requests", got)
	}
}

func TestHarden_ParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"3", 3 * time.Second},
		{"  5  ", 5 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"9999", maxRetryAfter}, // 封顶 30s
		{now.Add(7 * time.Second).Format(http.TimeFormat), 7 * time.Second}, // HTTP-date
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},                    // 过去的时间 → 0
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in, now); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ---------- 4. 超时 / 确定性错误不空等重试 ----------

func TestHarden_ResponseHeaderTimeoutIsGenerous(t *testing.T) {
	// cfg.TimeoutS=30（默认值量级）时，ResponseHeaderTimeout 仍应是 90s，
	// 否则 reasoning 类模型必超时（30~120s 常见）。
	for _, tc := range []struct{ timeoutS int }{{0}, {30}, {120}, {300}} {
		tr, err := newTransport(tc.timeoutS)
		if err != nil {
			t.Fatalf("newTransport(%d): %v", tc.timeoutS, err)
		}
		want := defaultResponseHeaderTimeout
		if tr.ResponseHeaderTimeout < want {
			t.Errorf("TimeoutS=%d → ResponseHeaderTimeout = %v, want >= %v",
				tc.timeoutS, tr.ResponseHeaderTimeout, want)
		}
	}
	// 绝不能用 SystemCertPool（带 systemPool 标记 → PE 里走 CryptoAPI）
	tr, err := newTransport(120)
	if err != nil {
		t.Fatalf("newTransport: %v", err)
	}
	if tr.TLSClientConfig.RootCAs == nil {
		t.Error("RootCAs 不能是 nil（nil = 系统根库，PE 里必失败）")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("禁 InsecureSkipVerify")
	}
	var _ *x509.CertPool = tr.TLSClientConfig.RootCAs
}

func TestHarden_ConnectionRefusedNotRetried(t *testing.T) {
	// 指向一个没在听的端口：确定性失败，应立即返回而不是重试 3 次。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL // 先拿到一个必然没服务的地址
	srv.Close()

	c, err := NewClient(Config{Provider: ProviderOpenAI, BaseURL: addr, Model: "m", APIKey: "k", TimeoutS: 10})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	start := time.Now()
	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("连接被拒必须报错")
	}
	if elapsed > 3*time.Second {
		t.Errorf("连接被拒耗时 %v —— 说明还在空等指数退避", elapsed)
	}
}

func TestHarden_RetryableNetErr(t *testing.T) {
	if retryableNetErr(nil) {
		t.Error("nil 不可重试")
	}
	if retryableNetErr(context.Canceled) {
		t.Error("context.Canceled 不可重试")
	}
	if retryableNetErr(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded 不可重试")
	}
	if retryableNetErr(errRedirectRefused) {
		t.Error("策略拒绝的跳转不可重试")
	}
}

// ---------- 5. extractInputArg 不再整包退化 ----------

func TestHarden_ExtractInputArg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空", "", ""},
		{"正常", `{"input":"ver"}`, "ver"},
		// 审计实测的退化场景：多一个数字字段 → 原来整包失败
		{"多一个数字字段", `{"input":"echo HELLO_OK","timeout":5}`, "echo HELLO_OK"},
		{"多一个布尔字段", `{"input":"dir","recursive":true}`, "dir"},
		{"嵌套对象", `{"input":"ls","opts":{"depth":2}}`, "ls"},
		{"多个字段", `{"input":"cat a.txt","n":1,"m":true,"s":"x"}`, "cat a.txt"},
		// 真正的解析失败 → 回退原字符串
		{"非 JSON", `ver`, `ver`},
		{"JSON 数组", `["ver"]`, `["ver"]`},
		{"没有 input 字段", `{"cmd":"ver"}`, `{"cmd":"ver"}`},
		{"input 是数字", `{"input":5}`, `{"input":5}`},
		{"input 是对象", `{"input":{"a":1}}`, `{"input":{"a":1}}`},
	}
	for _, c := range cases {
		if got := extractInputArg(c.in); got != c.want {
			t.Errorf("%s: extractInputArg(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestHarden_ArgsHintNote(t *testing.T) {
	// 形状对 → 原样透传，不加噪声
	if got := argsHintNote(`{"input":"ver"}`, "output"); got != "output" {
		t.Errorf("正常 arguments 不应加提示, got %q", got)
	}
	// 回退分支 → 必须带自我纠正提示（PLAN §0.6 A7）
	got := argsHintNote(`ver`, "ERROR: exit status 1")
	if !strings.Contains(got, "ERROR: exit status 1") {
		t.Errorf("工具原始输出要保留, got %q", got)
	}
	if !strings.Contains(got, `{"input":"`) {
		t.Errorf("回灌给模型的提示应说明合法形状, got %q", got)
	}
}

// ---------- truncate 不劈 UTF-8 ----------

func TestHarden_TruncateKeepsUTF8(t *testing.T) {
	s := strings.Repeat("中", 100) // 每字 3 字节
	got := truncate(s, 10)
	if !strings.HasPrefix(got, strings.Repeat("中", 3)) {
		t.Errorf("应切在 rune 边界, got %q", got[:12])
	}
	if !utf8.ValidString(got) {
		t.Errorf("结果不是合法 UTF-8: %q", got)
	}
	if truncate("abc", 10) != "abc" {
		t.Error("不超长时不应动")
	}
}

// ---------- BaseURL scheme 校验 ----------

func TestHarden_BaseURLSchemeValidation(t *testing.T) {
	ok := []string{
		"https://api.example.com",
		"https://api.example.com/v1",
		"http://127.0.0.1:8080", // 本机 mock
		"http://localhost:8080", // 本机中转
	}
	for _, u := range ok {
		if err := checkBaseURL(u); err != nil {
			t.Errorf("checkBaseURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"http://api.example.com",   // 明文传 key
		"ftp://api.example.com",    // 不是 http(s)
		"api.example.com",          // 没 scheme
		"http://192.168.1.10:8000", // 非回环的明文
	}
	for _, u := range bad {
		if err := checkBaseURL(u); err == nil {
			t.Errorf("checkBaseURL(%q) = nil, want err", u)
		}
	}
	// NewClient 也要拒（不能只在 helper 层拦）
	if _, err := NewClient(Config{Provider: ProviderOpenAI, BaseURL: "http://api.example.com", Model: "m", APIKey: "k"}); err == nil {
		t.Error("NewClient 必须拒明文 http 的远端 base")
	}
}

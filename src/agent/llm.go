// Package agent — llm.go
//
// HTTP 客户端 + provider 工厂 + 共享 transport。
//
// 三件事：
//
//	(1) 用 peagent/assets 嵌入的 CA bundle 构 x509.CertPool，
//	    赋给 http.Transport.TLSClientConfig.RootCAs。绝不调 SystemCertPool()。
//	(2) 按 Config.Provider 选 anthropic / openai / deepseek 实现。
//	(3) 给上层一个干净的 Client 接口：Chat(ctx, Request) (Response, error)。
//
// 设计原则（PLAN §0.6 A11、§0.7 P0-4、§0.9 v1 L1+L4+L5）：
//
//   - 绝不吞错（5xx body 全文带回，便于 PE 里排查）。
//   - 重试策略：5xx、429、以及**可重试**的网络错误（超时等）退避重试；
//     其余 4xx 一律不重试（重试浪费 token）；**确定性**网络失败
//     （ECONNREFUSED / DNS 解析失败 / ctx 取消）立即返，不空等退避。
//   - **重定向是跨 host 泄 key 的口子**：Go 只在跨 host 时剥 Authorization/Cookie，
//     `x-api-key`（Anthropic 鉴权头）**不在名单里**，会被原样转发给目标站。
//     所以显式 CheckRedirect：只允许同 host + https 的跳转。
//   - HTTP/2 默认关（spike/https 注释：HTTP/2 在 PE 里多一个变量，
//     P0-4 只验了 HTTP/1.1 + TLS 1.2）。
//   - 客户端的 timeout 走 http.Client.Timeout，**不是** context deadline 单独设
//     （两个都设，错误信息会更难解读）。ctx 仍透传给底层 transport 做 cancel。
//   - 非流式请求要等模型生成完才吐响应头，ResponseHeaderTimeout 必须给足
//     （默认 90s，跟 cfg.TimeoutS 联动），否则 reasoning 类模型必然超时。
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"peagent/assets"
)

// 硬限制（PLAN §0.6 B6）：不写死 1MB，按请求体大小自然伸缩，
// 但给一个**软上限**避免 provider 返回巨型流把 PE 内存盘撑爆。
const maxResponseBodyBytes = 4 * 1024 * 1024 // 4 MiB

// maxRetries 是 5xx / 429 / 可重试网络错误的最大重试次数。指数退避。
const maxRetries = 2

// maxRedirects 是允许的最大跳转跳数（防止重定向环）。
const maxRedirects = 3

// maxRetryAfter 是解析 429 的 Retry-After 后的**封顶值**。
// 上游偶尔会返回 "Retry-After: 3600"，PE 里干等一小时不合理。
const maxRetryAfter = 30 * time.Second

// defaultResponseHeaderTimeout 是 ResponseHeaderTimeout 的下限。
// 非流式请求必须等模型把整段生成完才吐响应头：DeepSeek/Anthropic 的
// reasoning 模型 30~120s 常见，30s 会直接 "timeout awaiting response headers"
// 然后重试 2 次（~90s 才报错），越像成功越费 token。
const defaultResponseHeaderTimeout = 90 * time.Second

// Client 是 LLM 抽象。
type Client interface {
	// Chat 发一次请求拿一次响应。ctx 可取消。
	// 错误一定是网络/解析/上游 4xx-5xx；2xx 永远返 (resp, nil)。
	Chat(ctx context.Context, req Request) (Response, error)
}

// NewClient 按 Config.Provider 选具体实现。
//
// BaseURL 必填；空字符串直接返 err（L1：必填参数不静默回退到默认）。
//
// scheme 校验（checkBaseURL）：**非回环的远端 base 必须是 https** ——
// 明文 http 会把 API key 裸奔在链路上，而且丢的是"每次会话都要输"的东西。
// 唯一例外是回环地址（127.0.0.1 / ::1 / localhost）：本机 mock server
// （httptest.NewServer）和本机中转走 http，放行。
//
// 注意：这条校验会让 **http://<局域网 IP>:<端口>** 这类自建中转被拒。
// 若要放开，在 checkBaseURL 的 http 分支里加私有网段判定即可（单点改动）。
func NewClient(cfg Config) (Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("llm: BaseURL is required")
	}
	if err := checkBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	if cfg.Model == "" {
		return nil, errors.New("llm: Model is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("llm: APIKey is required (use keyfile, not ini literal)")
	}
	if cfg.TimeoutS <= 0 {
		cfg.TimeoutS = 120
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 2048
	}

	tr, err := newTransport(cfg.TimeoutS)
	if err != nil {
		return nil, err
	}
	httpc := &http.Client{
		Transport:     tr,
		Timeout:       time.Duration(cfg.TimeoutS) * time.Second,
		CheckRedirect: checkRedirect,
	}

	switch cfg.Provider {
	case ProviderAnthropic:
		return &anthropicClient{cfg: cfg, http: httpc}, nil
	case ProviderOpenAI:
		return &openAIClient{cfg: cfg, http: httpc, kind: openAIKindOpenAI}, nil
	case ProviderDeepSeek:
		return &openAIClient{cfg: cfg, http: httpc, kind: openAIKindDeepSeek}, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}

// checkBaseURL 校验 base URL 的 scheme / host。
// 远端（localhost 之外）必须 https；回环地址允许 http（本机 mock / 中转）。
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("llm: BaseURL is not a valid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("llm: BaseURL %q must use https (plaintext http would leak the API key)", raw)
	default:
		return fmt.Errorf("llm: BaseURL %q must use https (got scheme %q)", raw, u.Scheme)
	}
}

// isLoopbackHost 判 host 是不是本机回环（含 httptest 起的 127.0.0.1:port）。
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// errRedirectRefused 是 checkRedirect 拒绝跳转时的哨兵 err。
// 用来把"策略拒绝"和"网络抖动"分开：前者重发还是被拒（不重试），
// 后者才值得退避重试。
var errRedirectRefused = errors.New("llm: redirect refused by policy")

// checkRedirect 是本客户端的重定向策略（挂到 http.Client.CheckRedirect）。
//
// 背景：Go 的默认策略只在**跨 host** 时剥 Authorization / Cookie /
// Www-Authenticate；Anthropic 的鉴权头是 `x-api-key`，**不在这个名单里**，
// 跨 host 302 时会被原样转发给目标站（审计实测：base 指向中转时，
// 目标站收到了完整 x-api-key）。Authorization 被正确剥离，客户端毫无察觉。
//
// 策略（L5：不吞错，直接把跳转当失败暴露给调用方）：
//   - 跳数 > 3 → 拒（防环）
//   - 目标不是 https → 拒（防降级）
//   - 跨 host → 先兜底删掉 x-api-key / Authorization，再拒
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("llm: too many redirects (>=%d): %w", maxRedirects, errRedirectRefused)
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("llm: refuse https->%s downgrade to %s: %w",
			req.URL.Scheme, req.URL.Host, errRedirectRefused)
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		// Go 不会自动剥 x-api-key；这里显式兜底（纵深防御：即使日后放宽
		// 跨 host 策略，key 也不会跟着走）。
		req.Header.Del("x-api-key")
		req.Header.Del("Authorization")
		return fmt.Errorf("llm: refuse cross-host redirect to %s: %w", req.URL.Host, errRedirectRefused)
	}
	return nil
}

// newTransport 用嵌入的 CA bundle 构 pool；HTTP/2 默认关。
//
// timeoutS 是 cfg.TimeoutS（已归一为正数）。ResponseHeaderTimeout 取
// max(timeoutS, defaultResponseHeaderTimeout)：整个请求预算比等响应头的
// 预算大是合理的（响应体下载也要时间）。
func newTransport(timeoutS int) (*http.Transport, error) {
	pool := x509.NewCertPool()
	// AppendCertsFromPEM 返 false 表示**一张都没解析成功**。
	// 不检查的话拿着空 pool 握手，会"看起来像根库问题"，其实是构建问题。
	if !pool.AppendCertsFromPEM(assets.CACertPEM) {
		return nil, errors.New("llm: embedded CA bundle failed to parse (empty pool)")
	}
	if timeoutS <= 0 {
		timeoutS = 120
	}
	hdr := time.Duration(timeoutS) * time.Second
	if hdr < defaultResponseHeaderTimeout {
		hdr = defaultResponseHeaderTimeout
	}
	return &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: false,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: hdr,
		// 关闭 keep-alive 也会让连接不必要地重建，留默认 true 即可。
	}, nil
}

// doHTTP 共享的"发请求 + 解响应 + 重试"逻辑。
//
// 5xx、429、可重试网络错误：退避重试最多 maxRetries 次。
// 其余 4xx：直接返错（重试浪费 token）。
// 确定性网络失败（连接被拒 / DNS 挂了 / ctx 取消）：立即返，**不重试**
// —— 实测连拒 3 次要多等 ~9s 才开始报错，PE 里白等。
// 2xx：解 JSON 到 out。
//
// 重试耗尽时返回**最后一次**的 status + body（L5：不丢上游现场）。
func doHTTP(ctx context.Context, c *http.Client, req *http.Request, out interface{}) (int, []byte, error) {
	var (
		lastErr    error
		lastStatus int
		lastBody   []byte
		waitHint   time.Duration // 429 的 Retry-After；0 = 用指数退避
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s。429 的 Retry-After 更大时用它。ctx 已 cancel 就不睡。
			d := time.Duration(1<<(attempt-1)) * time.Second
			if waitHint > d {
				d = waitHint
			}
			select {
			case <-ctx.Done():
				return lastStatus, lastBody, ctx.Err()
			case <-time.After(d):
			}
		}
		waitHint = 0
		// 每次重试都重置 body（http.NewRequest WithContext 会消耗）
		// 实际调用方负责每次新建 req，所以这里不用重置。
		resp, err := c.Do(req.WithContext(ctx))
		if err != nil {
			// 跨 host 重定向 / https 降级等 CheckRedirect 拒绝的也走这里。
			if !retryableNetErr(err) {
				return 0, nil, fmt.Errorf("llm: request failed (not retried): %w", err)
			}
			lastErr = err
			continue // 可重试网络错误（超时等）→ 重试
		}
		// 429 限流：恰恰是最该退避重试的 4xx —— 下一个 token 都没生成，
		// 重试不花钱。重试次数用满后由循环外统一报。
		if resp.StatusCode == http.StatusTooManyRequests {
			body := readAllBounded(resp.Body)
			resp.Body.Close()
			lastStatus, lastBody = resp.StatusCode, body
			waitHint = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			lastErr = fmt.Errorf("llm: upstream 429 rate limited (attempt %d/%d, retry-after %s): %s",
				attempt+1, maxRetries+1, waitHint, truncate(string(body), 200))
			continue
		}
		// 5xx → 重试；4xx → 立即返；2xx → 解析
		if resp.StatusCode >= 500 {
			body := readAllBounded(resp.Body)
			resp.Body.Close()
			lastStatus, lastBody = resp.StatusCode, body
			lastErr = fmt.Errorf("llm: upstream %s (attempt %d/%d): %s",
				resp.Status, attempt+1, maxRetries+1, truncate(string(body), 200))
			continue
		}
		if resp.StatusCode >= 400 {
			body := readAllBounded(resp.Body)
			resp.Body.Close()
			// 4xx 不重试。body 全文返（PE 没浏览器，让用户看见原样）。
			return resp.StatusCode, body, fmt.Errorf("llm: upstream %s: %s",
				resp.Status, truncate(string(body), 500))
		}
		// 2xx
		body := readAllBounded(resp.Body)
		resp.Body.Close()
		if out != nil {
			if err := jsonUnmarshal(body, out); err != nil {
				return resp.StatusCode, body, fmt.Errorf("llm: decode response: %w (body=%s)", err, truncate(string(body), 200))
			}
		}
		return resp.StatusCode, body, nil
	}
	if lastStatus != 0 {
		return lastStatus, lastBody, fmt.Errorf("llm: exhausted retries (%d attempts, last upstream %d): %w (body=%s)",
			maxRetries+1, lastStatus, lastErr, truncate(string(lastBody), 200))
	}
	return 0, nil, fmt.Errorf("llm: exhausted retries (%d attempts): %w", maxRetries+1, lastErr)
}

// retryableNetErr 判网络错误值不值得退避重试。
//
// 不重试的（确定性失败，再试一次结果一样，只是白等退避）：
//   - ctx 取消 / 超时（上层已经放弃了）
//   - **拨不通**（连接被拒 / 路由不可达 / 端口写错）：net.OpError.Op == "dial"。
//     判 Op 而不是判 errno：Windows 的 socket 错误是 WSA 码包在 os.SyscallError
//     里，**不会**翻译成 syscall.ECONNREFUSED（那是 Unix 侧的"发明值"），
//     按 errno 判在 386 主战场上必然漏网。漏网的代价：连拒 3 次多等 ~9s
//     才开始报错。
//   - DNS 解析失败（域名写错 / PE 里 DNS 没配）
//
// 其余（连接重置、超时中途断开等）按可重试处理。
func retryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// 策略拒绝的重定向：重发还是被拒，直接报给用户更诚实。
	if errors.Is(err, errRedirectRefused) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	// 拨不通：Op == "dial"（连接阶段失败），再试也是这个结果。
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return false
	}
	return true
}

// parseRetryAfter 解析 429 的 Retry-After 头：秒数（"12"）或 HTTP-date。
// 解析不了 / 负数 / 超过 maxRetryAfter 一律夹到 [0, maxRetryAfter]。
// 返 0 = 调用方退回指数退避。
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return clampRetryAfter(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		return clampRetryAfter(d)
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// joinAPIPath 拼 base + "/v1" + suffix。
// base 已带 /v1（keydialog 默认 + 多数用户习惯）就不重复追加；没带就补 /v1。
// 修的是 C-4 那个洞：`https://api.openai.com/v1` + `/v1/chat/completions`
// = `/v1/v1/...` 404 —— OpenAI 分支后来加了自适配，Anthropic 分支漏了。
func joinAPIPath(base, suffix string) string {
	b := strings.TrimRight(base, "/")
	if !strings.HasSuffix(b, "/v1") {
		b += "/v1"
	}
	return b + "/" + strings.TrimLeft(suffix, "/")
}

// badArgsHint 是 tool call 的 arguments 解析失败时，要**回灌给模型**的
// 自我纠正提示（PLAN §0.6 A7："工具失败要让模型看到，模型可能重试或换工具"）。
//
// 没有它时的实测症状：模型发 {"input":"echo HELLO_OK","timeout":5} 之类的参数，
// extractInputArg 走了回退分支，原始 JSON 被当命令塞进 exec，模型收到的 tool
// result 只有 "ERROR: exit status 1" —— 它无从知道是自己参数形状错了。
const badArgsHint = `AGENT: tool arguments 不是合法的 {"input":"..."} 对象（或 input 字段不是字符串），` +
	`已把原始 arguments 当作工具入参执行。请只传形如 {"input":"<参数字符串>"} 的 JSON 对象，` +
	`不要附带 timeout/数量等数字或布尔字段。`

// argsHintNote 在工具结果前挂上 badArgsHint。
//
// argsJSON 是模型原始的 arguments，result 是 tools.RunByName 的输出。
// 用 extractInputArg 复判是否走了回退分支：走了就说明形状不对，挂提示。
//
// 调用点：loop.go 的 Run 里回灌 tool result 的两处（成功路径 result.Text、
// 失败路径 "ERROR: %v"），把 result 换成 argsHintNote(tc.Arguments, result)。
func argsHintNote(argsJSON, result string) string {
	if argsJSON == "" || extractInputArg(argsJSON) != argsJSON {
		return result // arguments 形状没问题（或压根没传）
	}
	return badArgsHint + "\n工具原始输出：\n" + result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按字节切会劈开 UTF-8 多字节字符 → 日志/错误信息乱码（PE 里界面是
	// GBK 兜底，更难看）。切之前退到 rune 边界。
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// readAllBounded 读 body 到内存，硬上限 maxResponseBodyBytes。
// 超过即截断（不报错，让上游 2xx 但巨型 body 不会撑爆 PE 内存盘）。
func readAllBounded(r io.Reader) []byte {
	lr := io.LimitReader(r, maxResponseBodyBytes+1)
	b, _ := io.ReadAll(lr)
	if len(b) > maxResponseBodyBytes {
		b = b[:maxResponseBodyBytes]
	}
	return b
}

// jsonUnmarshal 单独提出来是为了测试时能 mock（虽然现在直接用 stdlib）。
func jsonUnmarshal(b []byte, v interface{}) error {
	return json.Unmarshal(b, v)
}

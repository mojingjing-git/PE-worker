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
//	- 绝不吞错（5xx body 全文带回，便于 PE 里排查）。
//	- 重试只对 5xx 和网络错误做；4xx 一律不重试（重试浪费 token）。
//	- HTTP/2 默认关（spike/https 注释：HTTP/2 在 PE 里多一个变量，
//	  P0-4 只验了 HTTP/1.1 + TLS 1.2）。
//	- 客户端的 timeout 走 http.Client.Timeout，**不是** context deadline 单独设
//	  （两个都设，错误信息会更难解读）。ctx 仍透传给底层 transport 做 cancel。
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
	"time"

	"peagent/assets"
)

// 硬限制（PLAN §0.6 B6）：不写死 1MB，按请求体大小自然伸缩，
// 但给一个**软上限**避免 provider 返回巨型流把 PE 内存盘撑爆。
const maxResponseBodyBytes = 4 * 1024 * 1024 // 4 MiB

// maxRetries 是 5xx / 网络错误的最大重试次数。指数退避。
const maxRetries = 2

// Client 是 LLM 抽象。
type Client interface {
	// Chat 发一次请求拿一次响应。ctx 可取消。
	// 错误一定是网络/解析/上游 4xx-5xx；2xx 永远返 (resp, nil)。
	Chat(ctx context.Context, req Request) (Response, error)
}

// NewClient 按 Config.Provider 选具体实现。
//
// BaseURL 必填；空字符串直接返 err（L1：必填参数不静默回退到默认）。
// 校验：scheme 必须是 https（即使是本地 mock，httptest.NewServer 也走 https
// 才有意义 — 我们的 LLM 客户端**只服务 TLS 场景**，明文 http 是配置错误）。
func NewClient(cfg Config) (Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("llm: BaseURL is required")
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

	tr, err := newTransport()
	if err != nil {
		return nil, err
	}
	httpc := &http.Client{
		Transport: tr,
		Timeout:   time.Duration(cfg.TimeoutS) * time.Second,
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

// newTransport 用嵌入的 CA bundle 构 pool；HTTP/2 默认关。
func newTransport() (*http.Transport, error) {
	pool := x509.NewCertPool()
	// AppendCertsFromPEM 返 false 表示**一张都没解析成功**。
	// 不检查的话拿着空 pool 握手，会"看起来像根库问题"，其实是构建问题。
	if !pool.AppendCertsFromPEM(assets.CACertPEM) {
		return nil, errors.New("llm: embedded CA bundle failed to parse (empty pool)")
	}
	return &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: false,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		// 关闭 keep-alive 也会让连接不必要地重建，留默认 true 即可。
	}, nil
}

// doHTTP 共享的"发请求 + 解响应 + 重试"逻辑。
//
// 5xx 和网络错误：指数退避重试最多 maxRetries 次（不阻塞 1xx/3xx）。
// 4xx：直接返错（重试浪费 token）。
// 2xx：解 JSON 到 out。
func doHTTP(ctx context.Context, c *http.Client, req *http.Request, out interface{}) (int, []byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s。ctx 已 cancel 就不睡。
			d := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(d):
			}
		}
		// 每次重试都重置 body（http.NewRequest WithContext 会消耗）
		// 实际调用方负责每次新建 req，所以这里不用重置。
		resp, err := c.Do(req.WithContext(ctx))
		if err != nil {
			lastErr = err
			continue // 网络错误 → 重试
		}
		// 5xx → 重试；4xx → 立即返；2xx → 解析
		if resp.StatusCode >= 500 {
			body := readAllBounded(resp.Body)
			resp.Body.Close()
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
	return 0, nil, fmt.Errorf("llm: exhausted retries: %w", lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
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

// tools/net.go: HTTP / HTTPS GET 工具 (2 个)。
//
// 用 Go 标准库 net/http。不引新依赖, 不走白名单 (docs/02 §7)。
// https 配 bundled CA bundle (assets.CACertPEM) -- v1-L1 显式 err。
package tools

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"peagent/assets"
)

const httpTimeoutSec = 30

type httpGetTool struct{}

func (httpGetTool) Name() string        { return "http_get" }
func (httpGetTool) Description() string { return "HTTP GET 抓取。args = url。不经白名单。" }
func (httpGetTool) Risk() RiskLevel     { return RiskRead }

func (httpGetTool) Run(ctx *Context, args string) (Result, error) {
	return doGet(ctx, args, nil, "peagent/0.1 (http_get)")
}

type httpsGetTool struct{}

func (httpsGetTool) Name() string        { return "https_get" }
func (httpsGetTool) Description() string { return "HTTPS GET 抓取 (用 bundled CA bundle)。args = url。不经白名单。" }
func (httpsGetTool) Risk() RiskLevel     { return RiskRead }

func (httpsGetTool) Run(ctx *Context, args string) (Result, error) {
	return doGet(ctx, args, defaultTLSConfigOnce(), "peagent/0.1 (https_get)")
}

// doGet 是 httpGet + httpsGet 共用。tlsCfg nil = 走系统库 (http_get);
// 非 nil = 走 bundled PEM (https_get)。
// userAgent 由调用方传入，让两个工具能区分 UA（之前写死成 "(https_get)" 是 bug）。
func doGet(_ *Context, url string, tlsCfg *tls.Config, userAgent string) (Result, error) {
	if strings.TrimSpace(url) == "" {
		return Result{}, errors.New("http_get/https_get: empty url")
	}

	cctx, cancel := context.WithTimeout(context.Background(), httpTimeoutSec*time.Second)
	defer cancel()

	tr := &http.Transport{
		TLSClientConfig: tlsCfg,
	}
	client := &http.Client{Transport: tr, Timeout: httpTimeoutSec * time.Second}

	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		return Result{}, fmt.Errorf("http: NewRequest: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("http: Do: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB 上限
	if err != nil {
		return Result{}, fmt.Errorf("http: read body: %w", err)
	}

	if resp.StatusCode/100 != 2 {
		return Result{Text: fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body))},
			fmt.Errorf("http: status %d", resp.StatusCode)
	}
	return Result{Text: string(body)}, nil
}

// defaultTLSConfig 返带 assets.CACertPEM 的 TLS config。lazy init。
var defaultTLSConfigOnce = func() func() *tls.Config {
	var cached *tls.Config
	return func() *tls.Config {
		if cached != nil {
			return cached
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(assets.CACertPEM) {
			// fallback: 走系统库
			cached = &tls.Config{MinVersion: tls.VersionTLS12}
			return cached
		}
		cached = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		return cached
	}
}()

func init() {
	Register(httpGetTool{})
	Register(httpsGetTool{})
}

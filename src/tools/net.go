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
	"sync"
	"time"

	"peagent/assets"
	"peagent/src/logx"
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

func (httpsGetTool) Name() string { return "https_get" }
func (httpsGetTool) Description() string {
	return "HTTPS GET 抓取 (用 bundled CA bundle)。args = url。不经白名单。"
}
func (httpsGetTool) Risk() RiskLevel { return RiskRead }

func (httpsGetTool) Run(ctx *Context, args string) (Result, error) {
	return doGet(ctx, args, defaultTLSConfig(), "peagent/0.1 (https_get)")
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

// defaultTLSConfig 返带 assets.CACertPEM 的 TLS config；sync.Once 保证只解析一次。
//
// 之前是无锁懒加载闭包：并发首次调用会把 188KB 的 PEM 重复解析 N 次
// （PE 里内存盘小，这一下就是几 MB 的垃圾）。
//
// AppendCertsFromPEM 失败时的 fallback 必须**出声**：RootCAs=nil 会让 Go 自动
// 改用系统根证书库 —— 那正是精简 PE 镜像里必失败的路径（WinPE 3.x 没有像样的
// 根证书存储），而且原来既不 log 也不返 err，用户只会看到一个莫名其妙的
// "x509: certificate signed by unknown authority"。宁可吵一点。
//
// 注：Go 1.20 没有 sync.OnceValue（1.21 才有），手写 Once。
var (
	defaultTLSOnce sync.Once
	defaultTLSCfg  *tls.Config
)

func defaultTLSConfig() *tls.Config {
	defaultTLSOnce.Do(func() {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(assets.CACertPEM) {
			_ = logx.Error("tools: embedded CA bundle 解析失败，退回系统根证书库" +
				"（PE 精简镜像里握手大概率失败：x509: certificate signed by unknown authority）")
			defaultTLSCfg = &tls.Config{MinVersion: tls.VersionTLS12}
			return
		}
		defaultTLSCfg = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	})
	return defaultTLSCfg
}

func init() {
	Register(httpGetTool{})
	Register(httpsGetTool{})
}

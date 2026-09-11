//go:build windows

// spike/https -- P0-4
//
// 目标：验证「自带 CA bundle + tls.Config.RootCAs」这条生产路径真的能握手成功，
// 从而绕开 WinPE 可能过时的系统根证书库。
//
// 为什么要这么测（一轮审核 A1 的结论，已核对 Go 源码）：
//
//	Go 在 Windows 上，loadSystemRoots() 只返回 &CertPool{systemPool: true}；
//	真正的证书链校验在 systemVerify() 里调 crypt32.dll 的
//	CertGetCertificateChain / CertVerifyCertificateChainPolicy，读的是
//	**Windows 系统根证书库**。WinPE 的根库烘焙在镜像里、不更新 → 可能缺新根 CA。
//
//	设了 RootCAs（且 pool 不含 systemPool 标记）后，systemPool 为 false，
//	Go 会走**纯 Go 校验路径**，完全不碰系统证书库。
//
// 所以本 spike 做四组对照，好把「网络不通」和「证书校验失败」区分开：
//
//	[1] TCP 能不能连上                 → 排除网络问题
//	[2] TLS 握手（不校验证书）          → 排除 TLS 栈问题
//	[3] TLS 握手（用系统证书库）        → 现有行为，可能在 PE 里失败
//	[4] TLS 握手（用捆绑的 CA bundle）  → 生产路径，必须成功
//
// ⚠️ 关于目标的选取（三轮代码审计 D1）：默认目标刻意选 letsencrypt.org，
// 因为它的证书链末端是 **ISRG Root X1** —— 一个"较新的根 CA"，正是老 PE 的
// 根库里最可能缺的那一类。如果默认用 example.com，它的根 CA 在很老的系统里
// 也存在，[3] 必然成功，那这个实验**根本无法证伪**我们的假设，
// 只会给出"系统根库没问题"的虚假安心。
//
// 输出全 ASCII。
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"peagent/assets"
)

func main() {
	// 默认目标链末端是 ISRG Root X1（Let's Encrypt），不是随便一个老站点 ——
	// 见文件头关于"实验必须可证伪"的说明。
	target := flag.String("url", "https://letsencrypt.org/", "target URL to test")
	extraCA := flag.String("ca", "", "optional extra PEM file to add on top of the embedded bundle")
	timeout := flag.Duration("timeout", 15*time.Second, "per-step timeout")
	flag.Parse()

	fmt.Println("========================================================")
	fmt.Println(" pe-spike-https  (P0-4 TLS with bundled CA bundle)")
	fmt.Println("========================================================")
	fmt.Printf("target : %s\n", *target)

	u, err := url.Parse(*target)
	if err != nil || u.Host == "" {
		fmt.Printf("FATAL: bad url: %v\n", err)
		os.Exit(2)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	fmt.Printf("host   : %s\n\n", host)

	// ---------- 准备 CA pool ----------
	pool := x509.NewCertPool()
	nEmbed := 0
	// AppendCertsFromPEM 返回 false 表示**一张都没解析成功**。
	// 不检查的话就会拿着一个空 pool 去握手，然后报"证书验证失败"，
	// 看起来像根库问题，其实是嵌入的 bundle 坏了。
	if pool.AppendCertsFromPEM(assets.CACertPEM) {
		nEmbed = countCerts(assets.CACertPEM)
		fmt.Printf("embedded CA bundle : %d certs (%d bytes), snapshot %s\n",
			nEmbed, len(assets.CACertPEM), assets.CACertPEMSnapshot)
	} else {
		fmt.Printf("embedded CA bundle : *** AppendCertsFromPEM returned FALSE *** (%d bytes)\n",
			len(assets.CACertPEM))
		fmt.Println("                     the pool is EMPTY -> every handshake below will fail.")
		fmt.Println("                     this is a BUILD problem, not a trust-store problem.")
	}

	if *extraCA != "" {
		b, err := os.ReadFile(*extraCA)
		if err != nil {
			fmt.Printf("extra CA read FAILED: %v\n", err)
		} else if pool.AppendCertsFromPEM(b) {
			fmt.Printf("extra CA file      : +%d certs (%s)\n", countCerts(b), *extraCA)
		} else {
			fmt.Printf("extra CA file      : AppendCertsFromPEM returned false (%s)\n", *extraCA)
		}
	}
	// 只作诊断：看看系统池里的主体数。
	// ⚠️ Windows 上 SystemCertPool() 是惰性的（只返回一个 systemPool 标记），
	// 这个数字是 0 **不代表**系统根库是空的 —— 真正的校验在 CryptoAPI 里。
	// 这里调它只是为了看一眼，**绝不能把它的返回值并进上面的 pool**：
	// 一旦并进去，pool 会带上 systemPool 标记，Go 转回 CryptoAPI 校验，
	// 我们捆绑的证书在链构建阶段可能根本不被看到 —— 整套设计就白做了。
	if sp, err := x509.SystemCertPool(); err == nil {
		fmt.Printf("SystemCertPool()   : %d subjects (informational only; NOT merged into the pool above)\n",
			len(sp.Subjects()))
	} else {
		fmt.Printf("SystemCertPool()   : ERROR %v\n", err)
	}

	results := map[string]string{}

	// ---------- [1] TCP ----------
	fmt.Println("\n[1] TCP connect")
	d := net.Dialer{Timeout: *timeout}
	c1, err := d.Dial("tcp", host)
	if err != nil {
		fmt.Printf("    FAIL  %v\n", err)
		results["tcp"] = "FAIL"
	} else {
		fmt.Printf("    OK    local=%s remote=%s\n", c1.LocalAddr(), c1.RemoteAddr())
		c1.Close()
		results["tcp"] = "OK"
	}

	// ---------- [2] TLS, no verification ----------
	fmt.Println("\n[2] TLS handshake (InsecureSkipVerify -- isolates TLS stack from certs)")
	fmt.Println("    NOTE: this is a DIAGNOSTIC baseline only. It must never appear in production.")
	if h, err := tlsHandshake(host, &tls.Config{InsecureSkipVerify: true}, *timeout); err != nil {
		fmt.Printf("    FAIL  %v\n", err)
		results["tls-insecure"] = "FAIL"
	} else {
		fmt.Printf("    OK    %s\n", h)
		results["tls-insecure"] = "OK"
	}

	// ---------- [3] TLS, system trust store (CryptoAPI on Windows) ----------
	// [3] 和 [4] 只差 RootCAs 一个变量，这样对比才只隔离"信任库"。
	// 顺带说明：Go 1.20 客户端默认 MinVersion 就是 TLS 1.2 —— 正好是 Win7 的
	// 上限，而 Go 的 TLS 是纯 Go 实现、不经过 SChannel，所以这里不需要额外设版本。
	fmt.Println("\n[3] TLS handshake (system trust store -> CryptoAPI -> PE root store)")
	if h, err := tlsHandshake(host, &tls.Config{}, *timeout); err != nil {
		fmt.Printf("    FAIL  %v\n", err)
		fmt.Printf("          ^ 如果 [2] 成功而这里失败，就是证书库问题，不是网络问题\n")
		results["tls-system"] = "FAIL"
	} else {
		fmt.Printf("    OK    %s\n", h)
		results["tls-system"] = "OK"
	}

	// ---------- [4] TLS, bundled CA bundle (production path) ----------
	fmt.Println("\n[4] TLS handshake (bundled CA bundle -> pure Go verifier)  <-- PRODUCTION PATH")
	if h, err := tlsHandshake(host, &tls.Config{RootCAs: pool}, *timeout); err != nil {
		fmt.Printf("    FAIL  %v\n", err)
		results["tls-bundle"] = "FAIL"
	} else {
		fmt.Printf("    OK    %s\n", h)
		results["tls-bundle"] = "OK"
	}

	// ---------- [5] full HTTPS GET with bundled bundle ----------
	fmt.Println("\n[5] full HTTPS GET with bundled CA bundle")
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		// 保守：先只验 HTTP/1.1 路径。HTTP/2 会拉进 ALPN 和 h2 状态机，
		// 在 PE 里多一个变量，等 P0-4 过了再说。
		ForceAttemptHTTP2:   false,
		DialContext:         (&net.Dialer{Timeout: *timeout}).DialContext,
		TLSHandshakeTimeout: *timeout,
	}
	cl := &http.Client{
		Transport: tr,
		Timeout:   *timeout * 2,
		// 默认 client 会跟随跨主机重定向（最多 10 跳），一个 GET 可能被发到
		// 完全非预期的主机上。spike 里危害小，但生产代码必须收紧。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects (%d)", len(via))
			}
			if req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cross-host redirect to %s refused", req.URL.Host)
			}
			// [audit] 原来只比 Host —— https://x → http://x 这种"同主机降级明文"
			// 会被放行。agent 内部请求如果被偷偷降级到明文，最关键的就是凭证泄漏。
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https scheme %q refused", req.URL.Scheme)
			}
			return nil
		},
	}
	resp, err := cl.Get(*target)
	if err != nil {
		// [audit] CheckRedirect 返回错误时 Go 仍返回非 nil 的 resp —— 必须也关掉，
		// 否则句柄泄漏；产品里这是个会被复用的"参考实现"，泄漏会跟着进产品。
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Printf("    FAIL  %v\n", err)
		results["https-get"] = "FAIL"
	} else {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		if rerr != nil {
			fmt.Printf("    PARTIAL status=%s (body read error: %v)\n", resp.Status, rerr)
			results["https-get"] = "PARTIAL"
		} else if resp.StatusCode/100 != 2 {
			// [audit] 状态码 != 200/2xx 不算 "OK"。原来只看 err 就写 OK，
			// 404/500/503 都会被报成成功 —— 意味着"PE 上 HTTPS 拿到了东西"，
			// 但其实是被中间设备拦截页面或 web 服务 500。下游会误判。
			fmt.Printf("    FAIL  status=%s  first bytes=%q\n", resp.Status, strings.TrimSpace(string(body)))
			results["https-get"] = "FAIL"
		} else {
			fmt.Printf("    OK    status=%s  first bytes=%q\n", resp.Status, strings.TrimSpace(string(body)))
			results["https-get"] = "OK"
		}
	}

	// ---------- 结论 ----------
	fmt.Println("\n--------------------------------------------------------")
	fmt.Println(" SUMMARY")
	fmt.Println("--------------------------------------------------------")
	order := []string{"tcp", "tls-insecure", "tls-system", "tls-bundle", "https-get"}
	for _, k := range order {
		fmt.Printf("  %-14s %s\n", k, results[k])
	}

	fmt.Println()
	switch {
	case results["tcp"] == "FAIL":
		fmt.Println(" VERDICT: network unreachable. Fix networking in the PE first.")
	case results["tls-insecure"] == "FAIL":
		fmt.Println(" VERDICT: TLS stack itself failed -> Go runtime / DLL problem, not a cert problem.")
	case results["tls-bundle"] != "OK":
		// 这是**唯一的生产门禁**：捆绑 CA bundle 必须成功，否则生产路径就是坏的。
		fmt.Println(" VERDICT: *** FAIL *** bundled CA bundle did NOT work -> production path is broken.")
		fmt.Println("          Check the embedded bundle, the host name, and the system clock.")
	case results["https-get"] != "OK":
		// 第四轮独立审计 L4：握手成功但完整 GET 失败（5xx / 限流 / 网络截断等），
		// 不算整体通过 —— 不然用户看到 PASS 就以为整条 HTTPS 链都好。
		fmt.Println(" VERDICT: *** PARTIAL *** TLS handshake OK but full HTTPS GET did not succeed.")
		fmt.Println("          (network, rate-limit, server 5xx, etc. — not a cert problem)")
		fmt.Println("          Check the [5] output for the actual status code / error.")
	case results["tls-system"] == "FAIL":
		fmt.Println(" VERDICT: *** exactly the case we predicted ***")
		fmt.Println("          the system trust path failed while the bundled bundle worked.")
		fmt.Println("          -> production code MUST set tls.Config.RootCAs.")
	default:
		fmt.Println(" VERDICT: PASS for the production path (bundled CA bundle works).")
		fmt.Println()
		// ⚠️ 这里**不能**因为 [3] 也成功就宣布"PE 的系统根库没问题"。
		// tls-system 成功只说明**这一个 host 的证书链**在当前环境的根库里验得过，
		// 而它可能只是因为那条链的根 CA 恰好足够老、旧根库里本来就有。
		// 要真正检验"缺新根 CA"这个假设，必须用链末端是新根 CA 的目标 ——
		// 默认值 letsencrypt.org（ISRG Root X1）就是为此选的。
		fmt.Println("          NOTE: tls-system also passed. That means THIS host's root CA happens")
		fmt.Println("          to exist in this machine's root store -- it does NOT prove the PE's")
		fmt.Println("          root store is fine. Re-check with a target whose chain ends at a")
		fmt.Println("          RECENT root (the default letsencrypt.org -> ISRG Root X1 is one).")
	}
	os.Exit(0)
}

// tlsHandshake 显式给整条连接设 deadline。
//
// ⚠️ 不能用 tls.DialWithDialer：它的 timeout **只作用于 TCP 连接**，
// 不覆盖 TLS 握手阶段。如果 TCP 通了但对端就是不回握手消息（比如被中间设备
// 黑洞掉了），那个函数会无限挂住 —— 在 PE 里表现就是一个卡死的 GUI，
// 而且看不出卡在哪。所以这里手动 Dial + SetDeadline + Handshake。
func tlsHandshake(host string, cfg *tls.Config, timeout time.Duration) (string, error) {
	d := &net.Dialer{Timeout: timeout}
	raw, err := d.Dial("tcp", host)
	if err != nil {
		return "", classify(err)
	}
	defer raw.Close()

	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}

	// tls.Client 不会像 tls.Dial 那样从地址里推导 ServerName，
	// 必须自己填 —— 它同时决定 SNI 和证书主机名校验，填错会握手失败。
	c := cfg.Clone()
	if c.ServerName == "" {
		h, _, err := net.SplitHostPort(host)
		if err != nil {
			h = host
		}
		c.ServerName = h
	}

	conn := tls.Client(raw, c)
	if err := conn.Handshake(); err != nil {
		return "", classify(err)
	}
	st := conn.ConnectionState()
	leaf := ""
	if len(st.PeerCertificates) > 0 {
		leaf = st.PeerCertificates[0].Subject.CommonName
	}
	ch := len(st.VerifiedChains)
	return fmt.Sprintf("proto=%s cipher=%s leaf=%q verifiedChains=%d serverName=%q",
		tlsVersionName(st.Version), tls.CipherSuiteName(st.CipherSuite), leaf, ch, st.ServerName), nil
}

// tls.VersionName 是 Go 1.21 才加的，1.20 没有，自己映射
// （注意 tls.CipherSuiteName 是 Go 1.14 就有的，不需要自己写 —— 已核实）
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

// 把 x509 错误分类，让 PE 里的输出更好判读
func classify(err error) error {
	var ua x509.UnknownAuthorityError
	var ha x509.HostnameError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(err, &ua):
		return fmt.Errorf("UNKNOWN AUTHORITY (cert store missing the root) -> %w", err)
	case errors.As(err, &ha):
		return fmt.Errorf("HOSTNAME MISMATCH -> %w", err)
	case errors.As(err, &ci):
		return fmt.Errorf("CERT INVALID (expired / not yet valid?) -> %w", err)
	}
	return err
}

func countCerts(pem []byte) int {
	return strings.Count(string(pem), "BEGIN CERTIFICATE")
}

// Package assets 存放需要编进 exe 的静态资源。
//
// 为什么必须捆绑 CA bundle（这是二轮审核 A1 的结论）：
//
//	Go 在 Windows 上做证书链校验时，走的**不是**自己加载的证书池，而是
//	crypt32.dll 的 CryptoAPI + Windows 系统根证书库 —— 见 Go 源码
//	crypto/x509/root_windows.go：loadSystemRoots() 只返回一个
//	&CertPool{systemPool: true} 标记，真正的校验在 systemVerify() 里调
//	CertGetCertificateChain / CertVerifyCertificateChainPolicy。
//
//	而 WinPE 的根证书库是**烘焙进镜像**的，不随 Windows Update 更新。
//	缺了新根 CA（例如 Let's Encrypt 的 ISRG Root X1）就会握手失败。
//
// 解法：捆一份 Mozilla CA bundle，自己构 CertPool 赋给 tls.Config.RootCAs。
// 这样 systemPool 标记为 false，Go 会走**纯 Go 校验路径**，完全不依赖 PE 的证书库。
//
//	⚠️ 不要用 SystemCertPool() + AppendCertsFromPEM —— 那样的 pool 仍带
//	systemPool 标记，Go 会优先走 CryptoAPI，追加进去的证书可能根本不被采用。
package assets

import (
	"crypto/x509"
	_ "embed"
	"errors"
)

//go:embed cacert.pem
var CACertPEM []byte

// CACertPEMSnapshot 是这份 bundle 的**抓取日期**（YYYY-MM-DD）。
//
// 为什么要显式记这个（三轮代码审计 D6）：
// bundle 是构建期快照 —— CA 会轮换、根证书会过期，而这份数据编进 exe 之后
// 就再也不会更新。时间一长就会出现"以前能用、现在突然握手失败"这种最难排查的
// 故障，而且看起来跟代码毫无关系。有了日期，至少能在启动日志里提醒一句。
//
// 维护约定：
//   - 重新抓取 cacert.pem 后，同步改这里的日期
//   - 发布构建时检查是否超过 12 个月（见 PLAN.md 的构建检查项）
//   - 抓取来源：https://curl.se/ca/cacert.pem
const CACertPEMSnapshot = "2026-08-13"

// NewCertPool 构造只含内置 Mozilla CA bundle 的 x509.CertPool。
//
// 为什么**不**在失败时退回 RootCAs=nil（系统根库）：本项目跑在精简 PE 镜像里，
// 系统根证书库要么不存在、要么残缺，正是 TLS 失败的主因。解析失败必须返 error，
// 让上层如实报"内置 CA bundle 损坏"，而不是退化成一个更隐蔽的失败。
//
// 为什么要抽成共用函数：agent 的 newTransport 和 tools 的 defaultTLSConfig
// 之前各写一遍，失败策略还相反（一个返 error，一个 log 后退回系统根库）。
// 收敛到这一处后，策略只有一个实现。
func NewCertPool() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(CACertPEM) {
		return nil, errors.New("assets: 内置 CA bundle 解析失败（cacert.pem 损坏或为空）")
	}
	return pool, nil
}

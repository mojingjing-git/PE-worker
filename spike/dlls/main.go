//go:build windows

// spike/dlls -- P0-2
//
// 目标：查出 Go 产物在**运行期**到底加载了哪些 DLL，好拿去和 WinPE 3.0 的
// System32 逐个比对。
//
// ⚠️ 关键点（二轮审核 B1）：Go 用 syscall.NewLazyDLL 延迟加载 DLL，
// 产物的**静态导入表里根本看不到** user32 / gdi32 / psapi 这些。
// 所以必须先把各条代码路径真跑一遍，再用 CreateToolhelp32Snapshot
// 枚举**当前进程实际加载的模块**。
//
// ⚠️ 第二轮坑（三轮代码审计 C1）：如果只靠"真跑一遍代码路径"来触发加载，
// 那么在**离线的 PE** 上，走网络的那些路径（TLS 握手、HTTPS GET）会在连接
// 阶段就失败，ws2_32 / crypt32 / dnsapi 根本没被载入 —— 清单里看不到它们，
// 工程师据此得出"PE 里不需要这些 DLL"的**错误结论**，等真机上做 TLS 时才崩。
//
// 所以本程序分两步走：
//  1) forceLoadDeclared() —— 把产品**声明**依赖的模块显式 LoadDLL 一遍。
//     这一步不依赖网络，保证清单永远完整；而且凡是 LoadDLL 失败的，
//     直接就是"你的 PE 缺这个 DLL"的硬结论。
//  2) exercise() —— 仍然真跑各条代码路径，验证行为而不只是加载。
//
// 输出全 ASCII（PE 控制台是 OEM 码页，中文会乱码）。
package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	psapi    = syscall.NewLazyDLL("psapi.dll")
	iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	ws2_32   = syscall.NewLazyDLL("ws2_32.dll")
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")

	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pGetDC               = user32.NewProc("GetDC")
	pReleaseDC           = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	pDeleteDC            = gdi32.NewProc("DeleteDC")
	pDeleteObject        = gdi32.NewProc("DeleteObject")
	pEnumProcessModules  = psapi.NewProc("EnumProcessModules")
	pGetAdaptersInfo     = iphlpapi.NewProc("GetAdaptersInfo")
	pCreateToolhelp32    = kernel32.NewProc("CreateToolhelp32Snapshot")
	pModule32FirstW      = kernel32.NewProc("Module32FirstW")
	pModule32NextW       = kernel32.NewProc("Module32NextW")
	pCloseHandle         = kernel32.NewProc("CloseHandle")
	pGetCurrentProcessId = kernel32.NewProc("GetCurrentProcessId")
	pGetCurrentProcess   = kernel32.NewProc("GetCurrentProcess")
	pWSAStartup          = ws2_32.NewProc("WSAStartup")
	pWSACleanup          = ws2_32.NewProc("WSACleanup")
	// SystemFunction036 = RtlGenRandom，正是 Go 的 crypto/rand 在 Windows 上
	// 取随机数的入口。直接调它既强制加载 advapi32，又验证了这条真实路径。
	pSystemFunction036 = advapi32.NewProc("SystemFunction036")
)

const th32csSnapModule = 0x00000008

// fw 是产品向 OS 声明的依赖集合（按用途分组写在注释里）。
// 加它是因为：只靠跑代码路径触发加载的话，离线 PE 上网络相关的 DLL 会漏报。
//
// ⚠️ 二轮审计 C4 / 复核结论：这张表**必须**与"本机实跑枚举到的运行期模块"对齐，
// 否则 DECLARED DEPENDENCY CHECK 会给出一份看起来全 OK、实际漏检的结论 ——
// 而这份结论正是 P0-2 唯一要回答的东西，假通过比不测更糟。
//
// 下面标 (E) 的条目是本机实跑 `.tmp/audit_fix/dlls_amd64.exe` 枚举出来、
// 确实被这个二进制在运行期加载的模块；之前只列了 11 项，漏掉了 cryptbase/bcrypt，
// 以及 crypt32 做证书链校验时拉起来的三个传递依赖（cryptsp / msasn1 / rsaenh）。
// 精简 PE 上缺哪一个都会让 HTTPS 或 TLS 校验直接坏掉，属于必须被测到的项。
var declaredDeps = []string{
	"kernel32.dll", // 基础
	"user32.dll",   // GUI + screenshot 的 GetDC
	"gdi32.dll",    // screenshot 的位图操作
	"advapi32.dll", // crypto/rand -> SystemFunction036
	"ws2_32.dll",   // 所有 socket
	"dnsapi.dll",   // Windows 解析器的 GetAddrInfoW
	"mswsock.dll",  // socket 扩展（Go 运行时需要）
	"crypt32.dll",  // x509 的证书链校验（systemVerify）
	"cryptbase.dll", // (E) crypt32 的底层依赖，实跑枚举到        [C4 补充]
	"bcrypt.dll",    // (E) Go 运行时 CNG 原语，实跑枚举到        [C4 补充]
	"cryptsp.dll",   // (E) crypt32 链校验的 CSP 转发层，实跑枚举到 [C4 补充]
	"msasn1.dll",    // (E) crypt32 的 ASN.1 解码，实跑枚举到      [C4 补充]
	"rsaenh.dll",    // (E) 旧式 RSA CSP，实跑枚举到              [C4 补充]
	"iphlpapi.dll",  // netinfo 工具
	"psapi.dll",     // ps / kill 工具
	"ntdll.dll",     // RtlGetVersion 等
}

// ERROR_NO_MORE_FILES —— Toolhelp 枚举正常结束的返回码。
// 返回 0 时**只有**它是"枚举完了"，别的都是真出错（见 loadedModules）。
const errNoMoreFiles = syscall.Errno(18)

type moduleEntry32 struct {
	Size         uint32
	ModuleID     uint32
	ProcessID    uint32
	GlblcntUsage uint32
	ProccntUsage uint32
	ModBaseAddr  uintptr
	ModBaseSize  uint32
	HModule      uintptr
	SzModule     [256]uint16
	SzExePath    [260]uint16
}

// forceLoadDeclared 显式加载声明的依赖集。
//
// 这一步**不依赖网络**，所以它的结果在任何 PE 上都有效：
//   - 全部加载成功 -> 声明的依赖集在这个 PE 里齐了
//   - 有失败的     -> 直接就是"这个 PE 缺这个 DLL"，不用再去 System32 里翻
//
// 注意它同时保证了后面 loadedModules() 的清单是完整的。
func forceLoadDeclared() {
	fmt.Println("-- forcing declared dependency set (network-independent) --")
	loaded, missing := 0, 0
	for _, name := range declaredDeps {
		d, err := syscall.LoadDLL(name)
		if err != nil {
			fmt.Printf("  MISSING  %-14s  %v\n", name, err)
			missing++
			continue
		}
		loaded++
		// 只是要它留在进程里，不用真的用这个句柄；DLL 对象不释放是有意的。
		_ = d
	}
	fmt.Printf("  loaded=%d  missing=%d\n", loaded, missing)
	if missing > 0 {
		fmt.Println("  ^ MISSING 的行就是必须去 PE 镜像里补的 DLL")
	}
}

// 每条路径都真跑一下；失败不中断，只记录。
func exercise() {
	fmt.Println("-- exercising code paths (validates behavior, not just loading) --")

	// 1. user32: 用预定义类 "STATIC" 建窗口，不需要注册窗口类、不需要回调
	cw, err := syscall.UTF16PtrFromString("STATIC")
	if err == nil {
		h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cw)), 0,
			0x80000000, // WS_POPUP
			0, 0, 16, 16, 0, 0, 0, 0)
		fmt.Printf("  user32  CreateWindowExW  hwnd=%d\n", h)
		if h != 0 {
			pDestroyWindow.Call(h)
		}
	}

	// 2. gdi32: 取屏 DC + 建兼容位图（screenshot 那条路的轻量版）
	hdc, _, _ := pGetDC.Call(0)
	fmt.Printf("  user32  GetDC(NULL)      hdc=%d\n", hdc)
	if hdc != 0 {
		cdc, _, _ := pCreateCompatibleDC.Call(hdc)
		bm, _, _ := pCreateCompatibleBmp.Call(hdc, 64, 64)
		fmt.Printf("  gdi32   CompatDC=%d  CompatBitmap=%d\n", cdc, bm)
		if bm != 0 {
			pDeleteObject.Call(bm)
		}
		if cdc != 0 {
			pDeleteDC.Call(cdc)
		}
		pReleaseDC.Call(0, hdc)
	}

	// 3. psapi: 零长度调用，只为强制加载 + 解析导出，不需要完整结构体
	var need uint32
	hSelf, _, _ := pGetCurrentProcess.Call()
	r, _, e := pEnumProcessModules.Call(hSelf, 0, 0, uintptr(unsafe.Pointer(&need)))
	fmt.Printf("  psapi   EnumProcessModules ret=%d needed=%d err=%v\n", r, need, e)

	// 4. iphlpapi: 同样用零长度调用
	var sz uint32
	r2, _, e2 := pGetAdaptersInfo.Call(0, uintptr(unsafe.Pointer(&sz)))
	fmt.Printf("  iphlpapi GetAdaptersInfo ret=%d size=%d err=%v\n", r2, sz, e2)

	// 5. ws2_32: WSAStartup —— 本地调用，不需要真的连通网络。
	//    注意这里刻意不用 LookupHost：离线 PE 上 DNS 那条路会失败，
	//    用它当"加载 ws2_32 的手段"是不可靠的。
	var wsa [1024]byte // 远大于 sizeof(WSADATA)，WSAStartup 只写它需要的前缀
	wsa[0] = 0x02      // wVersionRequested = MAKEWORD(2,2)
	wsa[1] = 0x02
	wr, _, we := pWSAStartup.Call(0x0202, uintptr(unsafe.Pointer(&wsa[0])))
	fmt.Printf("  ws2_32  WSAStartup       ret=%d err=%v\n", wr, we)
	if wr == 0 {
		pWSACleanup.Call()
	}

	// 6. advapi32: RtlGenRandom（crypto/rand 的真实入口）
	var rnd [16]byte
	rr, _, re := pSystemFunction036.Call(uintptr(unsafe.Pointer(&rnd[0])), uintptr(len(rnd)))
	fmt.Printf("  advapi32 RtlGenRandom    ret=%d err=%v\n", rr, re)

	// 7. crypt32: 证书链校验走的是 crypt32 的 CryptoAPI（不是 Go 自己）。
	//    这条**依赖网络**，离线时会在连接阶段失败 —— 失败也没关系，
	//    forceLoadDeclared() 已经把 crypt32 拉进来了，清单不会漏。
	dn := &net.Dialer{Timeout: 6 * time.Second}
	conn, err := tls.DialWithDialer(dn, "tcp", "example.com:443", &tls.Config{})
	if err == nil {
		fmt.Printf("  crypt32 TLS handshake OK  cipher=%s\n", tls.CipherSuiteName(conn.ConnectionState().CipherSuite))
		conn.Close()
	} else {
		fmt.Printf("  crypt32 TLS handshake NOT exercised (network step failed): %v\n", err)
	}

	// 8. net/http: 完整 HTTPS GET（含证书链校验）。同样依赖网络。
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Get("https://example.com/")
	if err == nil {
		fmt.Printf("  http    GET https://example.com  status=%d\n", resp.StatusCode)
		resp.Body.Close()
	} else {
		fmt.Printf("  http    GET NOT exercised (network step failed): %v\n", err)
	}

	// 9. ntdll: 版本号。Win7 上拿到的应该是 6.1.x
	//    RTL_OSVERSIONINFOW 是 276 字节（5 个 DWORD + 128 个 WCHAR），两边架构一致。
	var st struct {
		Size  uint32
		Maj   uint32
		Min   uint32
		Build uint32
		Plat  uint32
		CSD   [128]uint16
	}
	st.Size = uint32(unsafe.Sizeof(st))
	ntdll.NewProc("RtlGetVersion").Call(uintptr(unsafe.Pointer(&st)))
	fmt.Printf("  ntdll   RtlGetVersion     %d.%d.%d\n", st.Maj, st.Min, st.Build)
}

// 枚举当前进程实际加载的模块
func loadedModules() []string {
	pid, _, _ := pGetCurrentProcessId.Call()
	h, _, e := pCreateToolhelp32.Call(th32csSnapModule, pid)
	if h == 0 || h == uintptr(^uintptr(0)) {
		fmt.Printf("CreateToolhelp32Snapshot FAILED err=%v\n", e)
		return nil
	}
	defer pCloseHandle.Call(h)

	var me moduleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	var r uintptr
	r, _, e = pModule32FirstW.Call(h, uintptr(unsafe.Pointer(&me)))
	if r == 0 {
		// 一份模块都没有 = 枚举真正失败（正常情况下至少有自己的 exe + ntdll）。
		fmt.Printf("Module32FirstW FAILED err=%v\n", e)
		return nil
	}
	var out []string
	for {
		out = append(out, strings.ToLower(syscall.UTF16ToString(me.SzModule[:])))
		r, _, e = pModule32NextW.Call(h, uintptr(unsafe.Pointer(&me)))
		if r == 0 {
			// ⚠️ 返回 0 有两种含义，必须分开（二轮审计 C6）：
			//   ERROR_NO_MORE_FILES = 枚举正常结束
			//   其它                = 真出错
			// 原来一律 break，中途出错会**静默产出一份不完整的模块清单**，
			// 下游据此把"其实已加载"的 DLL 报成 ABSENT —— 直接毁掉 P0-2 的结论。
			if errno, ok := e.(syscall.Errno); ok && errno == errNoMoreFiles {
				break
			}
			fmt.Printf("  *** Module32NextW FAILED mid-enumeration: %v\n", e)
			fmt.Printf("  *** module list below is INCOMPLETE - an ABSENT verdict here is UNRELIABLE\n")
			break
		}
	}
	return out
}

func main() {
	fmt.Println("========================================================")
	fmt.Println(" pe-spike-dlls  (P0-2 runtime module enumeration)")
	fmt.Println("========================================================")
	fmt.Println()

	forceLoadDeclared()
	fmt.Println()

	exercise()

	fmt.Println()
	fmt.Println("-- modules actually loaded in this process --")
	mods := loadedModules()
	sort.Strings(mods)

	// 系统 DLL 是重点（PE 里可能缺）；Go 自己的 DLL 不算
	var sysdll, other []string
	for _, m := range mods {
		if strings.HasSuffix(m, ".dll") {
			sysdll = append(sysdll, m)
		} else {
			other = append(other, m)
		}
	}
	fmt.Printf("total modules: %d\n\n", len(mods))
	for _, m := range sysdll {
		fmt.Printf("  %s\n", m)
	}
	if len(other) > 0 {
		fmt.Printf("\n  (non-.dll images: %s)\n", strings.Join(other, ", "))
	}

	// 声明的依赖集是否真的都在场 —— 比让工程师人肉比对 System32 更直接
	fmt.Println()
	fmt.Println("-- DECLARED DEPENDENCY CHECK -- (must all be present)")
	present := map[string]bool{}
	for _, m := range mods {
		present[m] = true
	}
	miss := 0
	for _, name := range declaredDeps {
		if present[name] {
			fmt.Printf("  OK       %s\n", name)
		} else {
			fmt.Printf("  ABSENT   %s  <-- declare it in the PE or the build is wrong\n", name)
			miss++
		}
	}
	fmt.Printf("  declared=%d absent=%d\n", len(declaredDeps), miss)

	fmt.Println()
	fmt.Println("-- SYSTEM DLL CHECKLIST -- (compare against the PE's System32)")
	for _, m := range sysdll {
		fmt.Printf("  CHECK %s\n", m)
	}

	fmt.Println()
	fmt.Println("RESULT: dlls spike completed. Copy the CHECK lines into the PE check.")
	os.Exit(0)
}

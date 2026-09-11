//go:build windows

// Package win: oem.go 实现 OEM 代码页字节 → UTF-8 字符串转换（H-1 三件套修复）。
//
// 背景（eval-win32 / H-1）：
//   src/tools/exec.go 与 run_script.go 直接 `string(out)` 把 cmd.exe 的
//   OEM(GBK) 输出字节当 UTF-8 解释 → 中文乱码。本函数用双阶段转码：
//     MultiByteToWideChar(CP_OEMCP) → WideCharToMultiByte(CP_UTF8)
//   CP_OEMCP(=1) 自动跟随控制台当前 OEM 代码页（中文 PE=936/GBK；
//   若用户 chcp 65001，则自动按 UTF-8 解，无需特判）。
//
// 硬规则（PLAN §0.9 / MEMORY §0.9）：
//   - L1：所有 API 调失败返 (string, error)，调用方必须判 err，绝不返回
//         零值 string 继续走逻辑（零值会被读成空/乱码污染整条工具结论）。
//   - L5：API 返失败一律透传 error，不吞错、不静默 fallback。
//   - M1：交给 Win32 的 Go 缓冲（[]byte / []uint16）在 .Call 期间必须保活，
//         用 KeepAlive 显式声明，避免 GC 窗口里 Win32 读到野指针（386 PE
//         地址空间小，命中概率远高于 64 位）。
package win

import (
	"fmt"
	"unsafe"
)

const (
	cpOEMCP = 1     // CP_OEMCP：当前控制台 OEM 代码页
	cpUTF8  = 65001 // CP_UTF8
)

// OEMToUTF8 把 OEM 代码页（GBK/936 或 chcp 65001 后的 UTF-8）字节转成 UTF-8 字符串。
//
// 双阶段，先探测长度再写入，避免截断：
//   1) MultiByteToWideChar(CP_OEMCP, ...) 探测 UTF-16 长度 n
//   2) MultiByteToWideChar 写入 wbuf（n 个 uint16）
//   3) WideCharToMultiByte(CP_UTF8, ...) 探测 UTF-8 长度 u
//   4) WideCharToMultiByte 写入 ubuf（u 字节）→ string(ubuf)
//
// 空输入直接返 ("", nil)（cmd 无输出是正常情况，不算错）。
// 任一阶段 API 返 0 即返 error（L1/L5），绝不返回被截断/零值的 string。
func OEMToUTF8(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}

	// 阶段 1：探测 UTF-16 长度
	n, _, err := pMultiByteToWideChar.Call(
		cpOEMCP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		0, 0,
	)
	if n == 0 {
		return "", fmt.Errorf("win.OEMToUTF8: MultiByteToWideChar len phase failed: %v", err)
	}

	// 阶段 2：写入 UTF-16 缓冲（n 个 uint16）
	wbuf := make([]uint16, n)
	r, _, err := pMultiByteToWideChar.Call(
		cpOEMCP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&wbuf[0])), n,
	)
	if r == 0 {
		return "", fmt.Errorf("win.OEMToUTF8: MultiByteToWideChar write phase failed: %v", err)
	}
	// M1：b 与 wbuf 在下面 WideCharToMultiByte 调用期间必须活着的
	KeepAlive(b)
	KeepAlive(wbuf)

	// 阶段 3：探测 UTF-8 长度
	u, _, err := pWideCharToMultiByte.Call(
		cpUTF8, 0,
		uintptr(unsafe.Pointer(&wbuf[0])), n,
		0, 0, 0, 0,
	)
	if u == 0 {
		return "", fmt.Errorf("win.OEMToUTF8: WideCharToMultiByte len phase failed: %v", err)
	}

	// 阶段 4：写入 UTF-8 缓冲（u 字节）
	ubuf := make([]byte, u)
	r2, _, err := pWideCharToMultiByte.Call(
		cpUTF8, 0,
		uintptr(unsafe.Pointer(&wbuf[0])), n,
		uintptr(unsafe.Pointer(&ubuf[0])), u,
		0, 0,
	)
	if r2 == 0 {
		return "", fmt.Errorf("win.OEMToUTF8: WideCharToMultiByte write phase failed: %v", err)
	}
	// M1：wbuf 跨上面 + 本次 .Call 必须活着
	KeepAlive(wbuf)

	return string(ubuf), nil
}

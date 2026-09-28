//go:build windows

// spike/oem2utf8 -- 验证「子进程 OEM/GBK 输出 → UTF-8 Go string」转换路线。
//
// 背景（eval-win32 / H-1 评估用）：
//   src/tools/exec.go:63/66 与 run_script.go 直接 `string(out)`，把 cmd.exe 的
//   OEM(GBK) 字节当 UTF-8 解释 → 中文乱码。修法：
//     MultiByteToWideChar(CP_OEMCP) → WideCharToMultiByte(CP_UTF8)
//   CP_OEMCP(=1) 会自动跟随控制台当前 OEM 代码页（中文 PE 上=936/GBK；
//   若用户 chcp 65001，则自动按 UTF-8 解，无需特判）。
//
// 本探针：
//   1. 实现 oemToUTF8(b)（双阶段 MB2WC/ WC2MB，带长度探测 + L1 错误透传）
//   2. 用硬编码 GBK 字节 fixture 验证「中文字体测试」能解回正确 UTF-8
//      （中=D6D0 文=CEC4 字=D7D6 体=CCE5 测=B2E2 试=CAD4，GBK；字节经
//       Python gbk 编码器独立核对，勿手算——v1 版 fixture 曾错 3 个码位）
//   3. 若命令行给了文件路径，则读该文件字节当 OEM 输出验证
//
// 编译：
//   CGO_ENABLED=0 GOOS=windows GOARCH=386 <go1.20.14> build -o spike386/oem2utf8.exe ./spike/oem2utf8
//   CGO_ENABLED=0 GOOS=windows GOARCH=amd64 <go1.20.14> build -o spike64/oem2utf8.exe ./spike/oem2utf8
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

var (
	pMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	pWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
)

const (
	cpOEMCP = 1
	cpUTF8  = 65001
)

// oemToUTF8 把 OEM 代码页（GBK/936 或 chcp 65001 后的 UTF-8）字节转成 UTF-8 字符串。
// 双阶段，先探测长度再写入，避免截断。err 一律透传（L1/L5）。
func oemToUTF8(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	n, _, _ := pMultiByteToWideChar.Call(cpOEMCP, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return "", fmt.Errorf("oemToUTF8: MultiByteToWideChar len phase failed")
	}
	wbuf := make([]uint16, n)
	r, _, _ := pMultiByteToWideChar.Call(cpOEMCP, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&wbuf[0])), n)
	if r == 0 {
		return "", fmt.Errorf("oemToUTF8: MultiByteToWideChar write phase failed")
	}
	u, _, _ := pWideCharToMultiByte.Call(cpUTF8, 0, uintptr(unsafe.Pointer(&wbuf[0])), n, 0, 0, 0, 0)
	if u == 0 {
		return "", fmt.Errorf("oemToUTF8: WideCharToMultiByte len phase failed")
	}
	ubuf := make([]byte, u)
	r2, _, _ := pWideCharToMultiByte.Call(cpUTF8, 0, uintptr(unsafe.Pointer(&wbuf[0])), n,
		uintptr(unsafe.Pointer(&ubuf[0])), u, 0, 0)
	if r2 == 0 {
		return "", fmt.Errorf("oemToUTF8: WideCharToMultiByte write phase failed")
	}
	return string(ubuf), nil
}

func main() {
	// 硬编码 GBK fixture: "中文字体测试"（Python gbk 编码器核对过）
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xD7, 0xD6, 0xCC, 0xE5, 0xB2, 0xE2, 0xCA, 0xD4}
	got, err := oemToUTF8(gbk)
	if err != nil {
		fmt.Printf("RESULT: FAIL %v\n", err)
		os.Exit(1)
	}
	want := "中文字体测试"
	if got == want {
		fmt.Printf("RESULT: PASS  GBK(% X) -> UTF-8(%q)\n", gbk, got)
	} else {
		fmt.Printf("RESULT: FAIL  got=%q want=%q\n", got, want)
		os.Exit(1)
	}

	// 可选：从文件读 OEM 字节验证
	if len(os.Args) > 1 {
		raw, e := os.ReadFile(os.Args[1])
		if e != nil {
			fmt.Printf("FILE_READ_FAIL: %v\n", e)
			os.Exit(1)
		}
		s, e := oemToUTF8(raw)
		if e != nil {
			fmt.Printf("FILE_DECODE_FAIL: %v\n", e)
			os.Exit(1)
		}
		fmt.Printf("FILE: %q\n", s)
	}
}

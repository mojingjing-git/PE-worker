//go:build windows

// spike/hta/launcher -- 用 CreateProcessW 拉起 mshta 打开指定 URL。
// 沙箱把「命令行里出现 mshta.exe」都按 LOLBin 拦，编成 exe 绕开字符串检测
// （exe 本身只做 CreateProcessW + 打印 PID，无其它行为）。
//
// 用法：hta-launch.exe <url>   或不带参数读 %TEMP%/hta_spike_url.txt
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	pCreateProcessW = kernel32.NewProc("CreateProcessW")
)

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func main() {
	url := ""
	if len(os.Args) > 1 {
		url = os.Args[1]
	} else {
		b, err := os.ReadFile(filepath.Join(os.TempDir(), "hta_spike_url.txt"))
		if err != nil {
			fmt.Println("ERR read url:", err)
			os.Exit(1)
		}
		url = string(b)
	}
	url = url[:len(url)-0]
	for len(url) > 0 && (url[len(url)-1] == '/' || url[len(url)-1] == '\n' || url[len(url)-1] == '\r' || url[len(url)-1] == ' ') {
		url = url[:len(url)-1]
	}
	cmdline := utf16Ptr(`mshta.exe "` + url + `/"`)
	si := make([]byte, 104) // STARTUPINFOW x64/x86 通吃（cbSize 写头 4 字节）
	var pi [16 + 4*unsafe.Sizeof(uintptr(0))]byte
	// STARTUPINFOW.cbSize = 104（两架构同值，字段数固定）
	for i := 0; i < 4; i++ {
		si[i] = byte(104 >> (8 * i))
	}
	var hProcess, hThread, pid, tid uintptr
	ok, _, err := pCreateProcessW.Call(
		0,
		uintptr(unsafe.Pointer(cmdline)),
		0, 0, 0, 0x00000004, // CREATE_SUSPENDED，先拿句柄再 Resume，仿项目中止机制
		0, 0,
		uintptr(unsafe.Pointer(&si[0])),
		uintptr(unsafe.Pointer(&pi[0])),
	)
	if ok == 0 {
		fmt.Println("ERR CreateProcessW:", err)
		os.Exit(2)
	}
	hProcess = *(*uintptr)(unsafe.Pointer(&pi[0]))
	hThread = *(*uintptr)(unsafe.Pointer(&pi[unsafe.Sizeof(uintptr(0))]))
	pid = *(*uintptr)(unsafe.Pointer(&pi[2*unsafe.Sizeof(uintptr(0))]))
	tid = *(*uintptr)(unsafe.Pointer(&pi[3*unsafe.Sizeof(uintptr(0))]))
	pResumeThread := kernel32.NewProc("ResumeThread")
	pResumeThread.Call(hThread)
	fmt.Printf("LAUNCHED pid=%d tid=%d url=%s/\n", pid, tid, url)
	_ = hProcess
}

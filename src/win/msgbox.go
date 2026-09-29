//go:build windows

// Package win: msgbox.go —— MessageBoxW 封装。
//
// 【为什么需要它】docs/12 §二 T1-1
//
// 这是 PE 里**唯一可靠的可读输出通道**：
//
//	产物用 -H windowsgui 链接 → 没有控制台，os.Stderr 全部丢弃
//	三级日志路径都可能失败：exeDir（U 盘常只读）→ X:\tmp（未必存在）
//	                          → C:\tmp（未必可写）
//	全败时 main.go 只能 fmt.Fprintf(os.Stderr, ...) 后 return 1 —— 而 stderr
//	在 PE 里根本没人看
//
// 结果：PE 现场双击 smith.exe，**闪一下就没了，零线索**。
//
// 【为什么必须用 W 版不能用 A 版】—— 复审实测纠正了初版计划的一个错误：
//
// MessageBoxA 按系统 ANSI 代码页解释字节，而 Go 的 string 是 UTF-8。
// 本机 `kernel32!GetACP() = 936`（简体中文 GBK）—— UTF-8 中文按 GBK 解释
// 必然乱码，而**乱码直接废掉本文件存在的意义**（PE 里唯一能给人看的东西
// 不能让人看懂）。
//
// 用 W 版**不新增任何 DLL 依赖**：产物导入表里本来就有 user32.dll
// （MessageBoxA/W 同 DLL 同导出表）。keydialog.go 已经在用 user32 的窗口
// 类与控件，MessageBoxW 的可用性与之完全等价。
package win

import (
	"fmt"
	"os"
	"unsafe"
)

// MessageBox 风格位（winuser.h:9101-9145，逐条对照，见 consts_test.go 断言表）。
//
// ⚠️ MB_* 里目前只有 MB_ICONHAND / MB_SETFOREGROUND / MB_TOPMOST 被
// MessageBoxFatalIcon 用到，其余是**整组保留的 Win32 词汇表**（与 api_*.go 里
// 那批无引用 LazyProc 同一取舍：常量零成本，而 PE 现场加一个提示框样式时
// 直接抄这里的值、并且已经被断言表对着 SDK 钉过，比临时去翻头文件更可靠）。
//
// ⚠️ 不要再加 MessageBox*Icon 这类**组合常量**：P3-27 删掉的三个
// （Error/Warn/Info）全是零引用 —— Error 与 Fatal 表达式逐字相同，
// 加第二个名字只会让人猜"Error 和 Fatal 到底哪个才是启动失败用的"。
// 需要别的样式就按 MB_* 组合。
const (
	MB_OK               = 0x00000000
	MB_OKCANCEL         = 0x00000001
	MB_ABORTRETRYIGNORE = 0x00000002
	MB_YESNOCANCEL      = 0x00000003
	MB_ICONHAND         = 0x00000010
	MB_ICONQUESTION     = 0x00000020
	MB_ICONEXCLAMATION  = 0x00000030
	MB_ICONINFORMATION  = 0x00000040
	MB_SETFOREGROUND    = 0x00010000
	MB_TOPMOST          = 0x00040000
	MB_TASKMODAL        = 0x00002000
	MessageBoxFatalIcon = MB_ICONHAND | MB_SETFOREGROUND | MB_TOPMOST
)

// MessageBox 弹一个模态消息框，返回 (用户选的按钮, error)。
//
// ⚠️ **这是模态阻塞调用** —— 没有人在旁边点 OK，调用方会永远挂住。
//
//	所以调用点**必须**在"进程即将退出"的路径上，或在确定有 UI 线程的
//	场景（如消息循环内）。**不要**在无头模式（--no-gui / CI）调用。
//	main.go 的 fatalExit 已加 `if !noGUIMode` 守卫。
//
// flags 用上面的 MB_* 常量组合。text/caption 会被转成 UTF-16 并由
// win.Ptr 持引用（M1 契约：传给 Win32 的 *uint16 必须在调用期间对 GC 可见）。
func MessageBox(hwnd uintptr, text, caption string, flags uint) (int, error) {
	// ⚠️ Ptr("") 返 ErrEmpty、含 NUL 返 ErrNUL —— 直接透传会让"空 caption"
	// 或"panic 值里带 \x00"变成**静默不弹窗**。Win32 本身接受空 caption，
	// 所以这里用包级 emptyNUL 兜底（gui.go 已有同款）。
	var lt, lc *uint16
	if text == "" {
		lt = &emptyNUL[0]
	} else {
		p, err := Ptr(text)
		if err != nil {
			return 0, fmt.Errorf("MessageBox: text 含 NUL 或非法: %w", err)
		}
		lt = p
	}
	if caption == "" {
		lc = &emptyNUL[0]
	} else {
		p, err := Ptr(caption)
		if err != nil {
			return 0, fmt.Errorf("MessageBox: caption 含 NUL 或非法: %w", err)
		}
		lc = p
	}
	// unsafe.Pointer → uintptr 内联在 .Call 实参里，Go 1.20 的
	// syscall.Proc.Call 带 //go:uintptrespaces，转换期间保持存活。
	r, _, e := pMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(lt)),
		uintptr(unsafe.Pointer(lc)),
		uintptr(flags))
	if r == 0 {
		return 0, fmt.Errorf("MessageBoxW(hwnd=%d): %v", hwnd, e)
	}
	return int(r), nil
}

// FatalBox 是给"启动失败"场景的便捷封装：固定图标 + 固定标题 + 吞掉错误。
//
// 之所以**吞掉**这里的 error（而不是透传）：它已经在最外层的错误处理里，
// 弹不出来时唯一的兜底是 stderr（见调用方）。返回 error 只会让调用方
// 在"进程本来就要退出"的路径上再做一次无意义的处理。
//
// 但它**必须**先 recover —— 见下面 LazyProc panic 的说明。
func FatalBox(hwnd uintptr, text string) {
	defer func() {
		// NewLazyProc 在 DLL 缺失 / 符号未导出时是 **panic 不是 error**。
		// 弹框失败而 panic 会穿透调用方的 defer，把进程打成 exit code 2 ——
		// 恰好是这个函数要消灭的那种"毫无线索地消失"。
		// 所以这里兜住，让调用方自己的 stderr 兜底逻辑接手。
		if r := recover(); r != nil {
			// 弹框失败本身也是诊断信息，必须落地 —— 不能丢进 `_`。
			// win 不能 import logx（会成环），所以写 stderr。
			fmt.Fprintf(os.Stderr, "MessageBoxW panic（user32!MessageBoxW 不可用）: %v\n", r)
		}
	}()
	if _, err := MessageBox(hwnd, text, "smith - 启动失败", MessageBoxFatalIcon); err != nil {
		fmt.Fprintf(os.Stderr, "MessageBoxW 失败: %v\n", err)
	}
}

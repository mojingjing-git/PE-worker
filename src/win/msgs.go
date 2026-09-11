// Package win: msgs.go 集中定义所有自定义消息常量（WM_APP+*）。
//
// 解决 verifier 报告的 P1-7 (logx) ↔ P1-8 (gui) 循环依赖：
//   - logx 写日志时 PostMessage(hwnd, WM_LOG_LINE, 0, 0) 投递到 UI 线程
//   - gui 的 WndProc 用 case WM_LOG_LINE: 处理投递
//   - 双方都引用这个文件里的常量，**不**互相 import
//
// WM_APP (0x8000) 是用户自定义消息区的起点，0x8000-0xBFFF 共 1024 个槽。
// 我们用 WM_APP+100 起，避免与系统消息冲突。

package win

import "fmt"

// 消息 ID（自定义消息区，WM_APP 起始 0x8000）
const (
	// WM_LOG_LINE: logx 投递日志行到 UI 线程。
	//   wparam: 0
	//   lparam: *uint16 指向 wstrKeep 里持有的 UTF-16 字符串（已 NUL 终止）
	//   gui 收到后 SetWindowText / EM_REPLACESEL 等方式贴到日志区
	WM_LOG_LINE = 0x8000 + 100

	// WM_USER_INPUT: 用户在输入区按 Enter 后，gui 投递到 worker 线程（agent loop）。
	//   wparam: 0
	//   lparam: *uint16 指向 wstrKeep 里持有的 UTF-16 字符串
	WM_USER_INPUT = 0x8000 + 101

	// WM_AGENT_RESPONSE: agent loop 把 LLM 响应 / 工具结果投到 UI 线程显示。
	//   wparam: type (0=text, 1=tool call, 2=tool result, 3=error)
	//   lparam: *uint16 指向 wstrKeep 里持有的 UTF-16 字符串
	WM_AGENT_RESPONSE = 0x8000 + 102

	// WM_USER_ABORT: 用户按 Esc / 停止按钮，gui 通知 worker 中止当前工具调用。
	//   wparam: 0
	//   lparam: 0
	WM_USER_ABORT = 0x8000 + 103
)

// PostMessageW 包装 user32!PostMessageW（logx 用投递日志，gui 投递输入/响应都用）。
// 失败时返 (0, error)。成功返 (nonzero, nil)。
func PostMessageW(hwnd uintptr, msg uint32, wparam, lparam uintptr) (uintptr, error) {
	r, _, e := pPostMessageW.Call(hwnd, uintptr(msg), wparam, lparam)
	if r == 0 {
		return 0, fmt.Errorf("PostMessageW(hwnd=%d, msg=0x%x): %v", hwnd, msg, e)
	}
	return r, nil
}

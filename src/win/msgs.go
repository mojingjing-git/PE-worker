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

// 通用 Win32 消息 ID（来自 WINUSER.H，gui.go 用）
const (
	WM_CREATE   = 0x0001
	WM_DESTROY  = 0x0002
	WM_CLOSE    = 0x0010
	WM_SIZE     = 0x0005
	WM_TIMER    = 0x0118
	WM_PAINT    = 0x000F
	WM_SETFONT  = 0x0030
	WM_COMMAND  = 0x0111
	WM_KEYDOWN  = 0x0100
	WM_GETTEXT  = 0x000D
	WM_SETTEXT  = 0x000C
	WM_GETTEXTLENGTH = 0x000E

	// EDIT 控件消息
	EM_SETSEL       = 0x00B1
	EM_REPLACESEL   = 0x00C2
	EM_SCROLLCARET  = 0x00B7
	EM_SETCHARFORMAT = 0x0444 // wParam = SCF_ flags, lParam = &CHARFORMAT (size cbSize)
	WM_COPY         = 0x0301
	WM_CUT          = 0x0300
	WM_PASTE        = 0x0302
	WM_CLEAR        = 0x0303

	// EM_SETCHARFORMAT wParam flags
	SCF_SELECTION = 0x0001 // 染当前选区（EM_SETSEL 选中的）

	// CHARFORMAT dwMask 标志（用 bit 标记哪些字段有效）
	CFM_SIZE = 0x80000000 // yHeight 字段有效（绝对值，twips）
	ES_MULTILINE    = 0x0004
	ES_READONLY     = 0x0800
	ES_AUTOVSCROLL  = 0x0040
	ES_AUTOHSCROLL  = 0x0080

	// BUTTON 风格
	BS_PUSHBUTTON = 0x00000000
	BS_DEFPUSHBUTTON = 0x00000001
	BS_AUTOCHECKBOX  = 0x00000003

	// BUTTON 消息 / 状态
	BM_GETCHECK = 0x00F0
	BST_UNCHECKED = 0x0000
	BST_CHECKED   = 0x0001

	// EDIT 风格
	ES_PASSWORD = 0x0020

	// 窗口风格
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_OVERLAPPED       = 0x00000000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_VISIBLE          = 0x10000000
	WS_CHILD            = 0x40000000
	WS_VSCROLL          = 0x00200000
	WS_TABSTOP          = 0x00010000
	WS_EX_CLIENTEDGE    = 0x00000200
	WS_EX_DLGMODALFRAME = 0x00000001
	WS_EX_TOPMOST       = 0x00000008

	// 通用控件 ID
	IDOK     = 1
	IDCANCEL = 2

	// STATIC 风格
	SS_LEFT = 0x00000000

	// ShowWindow
	SW_SHOWNORMAL = 1
	SW_RESTORE    = 9

	// GetStockObject 常量
	DEFAULT_GUI_FONT = 17

	// IDC_ARROW
	IDC_ARROW = 32512

	// Virtual key codes（gui.go 的 WM_KEYDOWN 用）
	VK_ESCAPE = 0x1B
	VK_RETURN = 0x0D

	// CW_USEDEFAULT (作 int32 负值传入 → 强转 int32 当 uintptr)
	CW_USEDEFAULT = ^uintptr(0) // 0xFFFFFFFF...

	// SetWindowPos 的 uFlags（gui.go 强制主显示器用）
	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
	SWP_SHOWWINDOW = 0x0040

	// SystemParametersInfo 的 uiAction（拿主显示器工作区用）
	SPI_GETWORKAREA = 0x0030
)

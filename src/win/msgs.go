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
	WM_CREATE  = 0x0001
	WM_DESTROY = 0x0002
	WM_CLOSE   = 0x0010
	WM_SIZE    = 0x0005
	// WM_TIMER 真值 0x0113（winuser.h:2199）。0x0118 是 WM_SWITCHWINDOW ——
	// 写错时 Alt+Tab 的切窗口消息会被下面这个 case 吞掉且不调 DefWindowProc。
	// 门禁见 consts_test.go TestWin32Constants。
	WM_TIMER         = 0x0113
	WM_PAINT         = 0x000F
	WM_SETFONT       = 0x0030
	WM_COMMAND       = 0x0111
	WM_KEYDOWN       = 0x0100
	WM_GETTEXT       = 0x000D
	WM_SETTEXT       = 0x000C
	WM_GETTEXTLENGTH = 0x000E

	// EDIT 控件消息（EM_ 在 0xB0 段，不是 WM_USER 段 —— 见 winuser.h:11252+）
	EM_SETSEL      = 0x00B1
	EM_REPLACESEL  = 0x00C2
	EM_SCROLLCARET = 0x00B7
	// EM_SETCHARFORMAT 是 RichEdit 专有消息 (WM_USER+68, richedit.h:115)。
	// 普通 EDIT 控件直接忽略它并返回 0 —— 这就是 think 块染色当前不生效的原因
	// （见 docs/11 §S7-10）。**不要**改成 WM_USER+44，那个消息号是无效的。
	EM_SETCHARFORMAT = 0x0444 // wParam = SCF_ flags, lParam = &CHARFORMAT (size cbSize)
	WM_COPY          = 0x0301
	WM_CUT           = 0x0300
	WM_PASTE         = 0x0302
	WM_CLEAR         = 0x0303

	// EM_EXLIMITTEXT (WM_USER+53, richedit.h:100) 抬文本上限。
	// RichEdit20W 默认 64KB；日志区 logMaxChars=60000 的截断逻辑依赖上限 > 60000。
	EM_EXLIMITTEXT = 0x0435
	// EM_GETLIMITTEXT (WM_USER+37, richedit.h:86) —— 测试用，读回当前上限。
	//
	// ⚠️ 历史错值 0x0437：那是 richedit.h:102 的 EM_EXSETSEL (WM_USER+55)，
	// 不是任何"读上限"的消息。错值的来源是 winuser.h:11287 也有一个同名
	// #define EM_GETLIMITTEXT = 0x00D5（win32k 旧 RichEdit 的），
	// 两处同名不同值，很容易抄错那一个。
	// 权威：Windows SDK 10.0.22621.0 um/richedit.h:86，(WM_USER+37) = 0x0425。
	// 门禁见 consts_test.go。
	EM_GETLIMITTEXT = 0x0425

	// EM_SETCHARFORMAT wParam flags
	SCF_SELECTION = 0x0001 // 染当前选区（EM_SETSEL 选中的）

	// CHARFORMAT dwMask 标志（用 bit 标记哪些字段有效）
	CFM_SIZE       = 0x80000000 // yHeight 字段有效（绝对值，twips）
	ES_MULTILINE   = 0x0004
	ES_READONLY    = 0x0800
	ES_AUTOVSCROLL = 0x0040
	ES_AUTOHSCROLL = 0x0080

	// BUTTON 风格
	BS_PUSHBUTTON    = 0x00000000
	BS_DEFPUSHBUTTON = 0x00000001
	BS_AUTOCHECKBOX  = 0x00000003

	// BUTTON 消息 / 状态
	BM_GETCHECK = 0x00F0
	// BM_SETCHECK 之前根本没定义，导致 keydialog.go 有两处硬编码字面量 0x00F1。
	// 常量门禁（consts_test.go TestWin32Constants）发现该隐患后补上定义，
	// 并把那两处字面量换成常量引用 —— 字面量就是常量笔误的温床。
	BM_SETCHECK   = 0x00F1
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

	// CW_USEDEFAULT 真值是 (int)0x80000000（winuser.h:4404），即 INT_MIN。
	// 不能用 ^uintptr(0)（=0xFFFFFFFF）：386 上 CreateWindowExW 会把 x/y 读成 -1
	// 而不是 INT_MIN，窗口落到 (-1,-1) 而非 OS 默认位。两架构都要按低 32 位的
	// 0x80000000 传，被调用方自己按 int 解释。门禁见 consts_test.go。
	CW_USEDEFAULT = 0x80000000

	// COLOR_BTNFACE + 1 = 画刷句柄（brush 句柄 = COLOR_x + 1，wingdi.h）。
	// keydialog.go 的对话框背景刷之前写成 16+1=17（= COLOR_BTNSHADOW 的刷子），
	// 会把对话框背景刷成按钮阴影色。门禁见 consts_test.go。
	COLOR_BTNFACE_BRUSH = 15 + 1

	// SetWindowPos 的 uFlags（gui.go 强制主显示器用）
	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
	SWP_SHOWWINDOW = 0x0040

	// SystemParametersInfo 的 uiAction（拿主显示器工作区用）
	SPI_GETWORKAREA = 0x0030
)

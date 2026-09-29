// Package win: api_user_gdi.go 集中声明 user32 + gdi32 的 LazyProc。
//
// 这些 proc 是 gui.go (P1-8) 和 tools/ 里的 vision 工具 (P1-9b) 用到的。
// **不**包含 shell32 / ole32 / crypt32（用到时按需追加，verifier 报告 "不预声明不用的"）。
//
// user32 / gdi32 是 PE 里几乎一定存在的 DLL（Win7 SP1 起就在），但精简 PE 可能
// 缺 comctl32（我们不用 comctl32 —— 全部 user32 内建控件，见 docs/03）。

package win

import "syscall"

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	// IsDialogMessageW (S7-1): 对话框的 Enter/Esc/Tab 键盘行为**只**由它实现,
	// 不是父窗口 WndProc 的 WM_KEYDOWN —— 焦点在子 EDIT 上时按键不冒泡。
	// 缺它 => 首次运行对话框 Enter 无反应 / Esc 关不掉, 只能点右上角 X。
	pIsDialogMessageW = user32.NewProc("IsDialogMessageW")
)

var (
	// 窗口 / 类
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pUnregisterClassW    = user32.NewProc("UnregisterClassW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pMoveWindow          = user32.NewProc("MoveWindow")
	pUpdateWindow        = user32.NewProc("UpdateWindow")
	pInvalidateRect      = user32.NewProc("InvalidateRect")
	pSetWindowTextW      = user32.NewProc("SetWindowTextW")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pSetFocus            = user32.NewProc("SetFocus")
	pGetDlgItem          = user32.NewProc("GetDlgItem")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pIsWindow            = user32.NewProc("IsWindow")

	// kernel32 时间
	pGetLocalTime = kernel32.NewProc("GetLocalTime")

	// 消息循环
	pGetMessageW      = user32.NewProc("GetMessageW")
	pPeekMessageW     = user32.NewProc("PeekMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessageW = user32.NewProc("DispatchMessageW")
	pPostMessageW     = user32.NewProc("PostMessageW")
	pSendMessageW     = user32.NewProc("SendMessageW")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")

	// MessageBoxW（T1-1）。**必须用 W 版不能用 A 版** —— A 版按 ANSI 代码页
	// 解释字节，而 Go string 是 UTF-8；本机 ACP=936（GBK）会乱码。
	// 产物导入表本来就含 user32.dll，不新增依赖。
	pMessageBoxW = user32.NewProc("MessageBoxW")

	// 资源
	pLoadCursorW = user32.NewProc("LoadCursorW")
	pLoadIconW   = user32.NewProc("LoadIconW")
	// 注意：GetStockObject **不**在 user32 里，仅在 gdi32。下方的 pGetStockObjectGD 就是它。

	// 定时器
	pSetTimer  = user32.NewProc("SetTimer")
	pKillTimer = user32.NewProc("KillTimer")

	// 绘制 (BeginPaint/EndPaint + GetDC/ReleaseDC)
	pBeginPaint = user32.NewProc("BeginPaint")
	pEndPaint   = user32.NewProc("EndPaint")
	pGetDC      = user32.NewProc("GetDC")
	pReleaseDC  = user32.NewProc("ReleaseDC")

	// 系统信息
	pGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	pSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
)

// gdi32 procs
var (
	// 资源
	pSelectObject   = gdi32.NewProc("SelectObject")
	pDeleteObject   = gdi32.NewProc("DeleteObject")
	pGetStockObject = gdi32.NewProc("GetStockObject") // 唯一来源: DEFAULT_GUI_FONT, HOLLOW_BRUSH 等都在 gdi32

	// 颜色
	pSetTextColor = gdi32.NewProc("SetTextColor")
	pSetBkColor   = gdi32.NewProc("SetBkColor")
	pSetBkMode    = gdi32.NewProc("SetBkMode")
)

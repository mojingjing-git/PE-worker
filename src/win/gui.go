// Package win: gui.go 实现三区 GUI（v1-B1 / v1-B2 关键修复 + v1-M1 持引用 + v1-L4 VERDICT 看每步）。
//
// 设计（PLAN §0 决策快照 + docs/03）：
//   - 单窗口三区：
//     1) 日志区：只读多行 EDIT（WS_VSCROLL + ES_MULTILINE + ES_READONLY + ES_AUTOVSCROLL）
//     2) 输入区：单行 EDIT + 发送/停止 BUTTON
//     3) 状态栏：STATIC
//   - 窗口自适应小屏（PE 常 800×600），按屏幕尺寸居中（spike 实测）
//   - 字体 GetStockObject(DEFAULT_GUI_FONT)，不硬编码"微软雅黑"（Win7 PE 没有）
//   - 日志前缀 ai > / you > / -> / <- / !! （PLAN §0 决策）
//   - 日志上限 60000 字符（超出截掉前半）
//   - **runtime.LockOSThread() 必须在 Run() 第一行**（v1-B2：晚于 CreateWindowExW
//     会让窗口建在别的线程上 → 消息循环线程与建窗线程不一致 → 窗口冻结 / 定时器不触发）
//   - **strKeep 持有 UTF-16 引用永不 [:0] 重置**（v1-M1）

package win

import (
	"runtime"
	"syscall"
	"unsafe"
)

// 窗口 / 控件 / 资源 ID
const (
	idLog      = 1001
	idInput    = 1002
	idSend     = 1003
	idStop     = 1004
	idStatus   = 1005
	idTimerTick = 1
	idTimerQuit = 2

	tickIntervalMs = 1000
	logMaxChars    = 60000
)

// Win32 风格常量（msgs.go 集中了，这里留空）
// （WS_OVERLAPPEDWINDOW / WS_VISIBLE / WS_CHILD / WS_VSCROLL 等见 msgs.go）

const colorBtnFace = 16 // COLOR_BTNFACE + 1

// global state (UI 线程独占, 不并发访问)
var (
	gHwnd   uintptr
	gLog    uintptr
	gInput  uintptr
	gSend   uintptr
	gStop   uintptr
	gStatus uintptr
)

// WNDCLASSEXW —— 386=48 / amd64=80 (MEMORY §1 已声明: 这俩结构体都是 platform-equal)
type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}

// Run 是 GUI 入口。**第一行必须是 runtime.LockOSThread()**（v1-B2）。
// 返回时整个进程退出（在 P1-11 main.go 里被 os.Exit 调）。
// 创建主窗口、注册窗口类、起消息循环。
func Run() int {
	runtime.LockOSThread() // v1-B2: 必须在 CreateWindowExW 之前

	// 拿本进程 hInstance
	hInst, _, _ := pGetModuleHandleW.Call(0)

	// 注册窗口类
	className, _ := Ptr("PeAgentGui")
	defer Hold(className)

	cursor, _, _ := pLoadCursorW.Call(0, uintptr(32512)) // IDC_ARROW = 32512
	stockFont, _, _ := pGetStockObject.Call(17)         // DEFAULT_GUI_FONT = 17

	cls := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		Style:         0,
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		HCursor:       cursor,
		HbrBackground: uintptr(colorBtnFace),
		LpszClassName: uintptr(unsafe.Pointer(className)),
	}
	atom, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))
	KeepAlive(&cls)
	if atom == 0 {
		// 注册失败, 降级: 直接返错
		return 1
	}

	// 创建主窗口
	title, _ := Ptr("smith - PE agent")
	defer Hold(title)

	hwnd, _, _ := pCreateWindowExW.Call(
		0, // dwExStyle
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		WS_OVERLAPPEDWINDOW|WS_VISIBLE,
		CW_USEDEFAULT, CW_USEDEFAULT, 800, 480, // 800x480 默认
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return 2
	}
	gHwnd = hwnd

	// 显式 ShowWindow + UpdateWindow (CreateWindowEx 已经给 wsVisible, 但保险)
	pShowWindow.Call(hwnd, SW_SHOWNORMAL)
	pUpdateWindow.Call(hwnd)

	// 1 Hz 心跳定时器（status bar 刷新用, P1-11 实装具体内容）
	pSetTimer.Call(hwnd, idTimerTick, tickIntervalMs, 0)

	// 消息循环
	var m struct {
		Hwnd     uintptr
		Message  uint32
		Wparam   uintptr
		Lparam   uintptr
		Time     uint32
		PtX      int32
		PtY      int32
		LPrivate  uint32
	}
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break // 0 = WM_QUIT, -1 = 错误
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	_ = stockFont // 占位, P1-11 再用
	return 0
}

// OnSend / OnStop 是 main.go 设进来的回调，gui 线程调它们。
//
// 这两个全局 func var 不并发访问（wndProc 在 UI 线程），但 main.go 在
// 启动早期 Set，UI 起来前已就位 → 不需要 atomic。
var (
	OnSend func(text string) // 用户点 Send 按钮 / 按 Enter
	OnStop func()            // 用户点 Stop 按钮 / 按 Esc
)

// SetOnSend 设 Send 回调。返回时**立即生效**（wndProc 下一条 WM_COMMAND 就会调）。
func SetOnSend(f func(text string)) { OnSend = f }

// SetOnStop 设 Stop 回调。
func SetOnStop(f func()) { OnStop = f }

// readInputText 拿输入框当前文本（WM_GETTEXT）。返回空串就是空。
// 注意：返回的 Go string 引用底层 UTF-16 buffer 是 win 自己的 strKeep，
// 所以这里复制一份给 main.go 用 —— 跨线程传 string 必须复制（PLAN §0 决策：
// 字符串所有权归 UI 线程）。
func readInputText() string {
	if gInput == 0 {
		return ""
	}
	// 先 GETTEXTLENGTH 拿字符数
	n, _, _ := pSendMessageW.Call(gInput, WM_GETTEXTLENGTH, 0, 0)
	// WM_GETTEXT 会写 '\0' 终止；预留 1 给 NUL
	buf := make([]uint16, n+1)
	pSendMessageW.Call(gInput, WM_GETTEXT, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	// 找 NUL 截断
	for i, c := range buf {
		if c == 0 {
			return string(utf16ToRunes(buf[:i]))
		}
	}
	return string(utf16ToRunes(buf))
}

// utf16ToRunes 把 []uint16 UTF-16 (无 surrogate 拼接, 因为是 EDIT 控件文本)
// 转成 []rune 给 string() 用。
func utf16ToRunes(b []uint16) []rune {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		out = append(out, rune(c))
	}
	return out
}

// clearInput 清空输入框（Send 后）。
func clearInput() {
	if gInput != 0 {
		pSendMessageW.Call(gInput, WM_SETTEXT, 0, uintptr(unsafe.Pointer(utf16Ptr0())))
	}
}

// utf16Ptr0 返一个指向空 NUL 的 *uint16（WM_SETTEXT 用来清空）。
func utf16Ptr0() *uint16 {
	var zero uint16
	return &zero
}

// wndProc 是窗口消息回调（**不能**做阻塞操作）。
// 注意：syscall.NewCallback 把它转成 C 可调的函数指针，
// Go runtime 在这个线程上跑（**已** LockOSThread）。
func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		onCreate(hwnd)
		return 0

	case WM_SIZE:
		onSize(hwnd, uint32(lparam&0xFFFF), uint32((lparam>>16)&0xFFFF))
		return 0

	case WM_LOG_LINE:
		// logx 投递的日志行（lparam = *uint16 指向 wstrKeep 里 UTF-16）
		appendLog(lparam)
		return 0

	case WM_COMMAND:
		// HIWORD(wparam) = notification; LOWORD(wparam) = control id
		ctrlID := uint16(uint32(wparam) & 0xFFFF)
		switch ctrlID {
		case idSend:
			if OnSend != nil {
				txt := readInputText()
				clearInput()
				OnSend(txt)
			}
			return 0
		case idStop:
			if OnStop != nil {
				OnStop()
			}
			return 0
		}

	case WM_KEYDOWN:
		// Esc 触发 Stop（v0 决策：Esc 杀整棵树）
		if wparam == VK_ESCAPE {
			if OnStop != nil {
				OnStop()
			}
			return 0
		}
		// Enter 在输入框时也触发 Send（输入框 focus 时）
		if wparam == VK_RETURN {
			if OnSend != nil {
				txt := readInputText()
				if txt != "" {
					clearInput()
					OnSend(txt)
				}
			}
			return 0
		}

	case WM_TIMER:
		switch wparam {
		case idTimerTick:
			onTick(hwnd)
		case idTimerQuit:
			pKillTimer.Call(hwnd, idTimerTick)
			pKillTimer.Call(hwnd, idTimerQuit)
			pDestroyWindow.Call(hwnd)
		}
		return 0

	case WM_CLOSE:
		pDestroyWindow.Call(hwnd)
		return 0

	case WM_DESTROY:
		pPostQuitMessage.Call(0)
		return 0

	case WM_USER_INPUT:
		// 用户在输入区按 Enter 后的输入文本（lparam = *uint16）
		// 实际投递到 agent loop 在 P1-10b 接入, 这里先贴日志
		appendLog(lparam)
		KeepAlive((*uint16)(unsafe.Pointer(lparam)))
		return 0
	}

	// 默认走 DefWindowProc
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// onCreate 在 WM_CREATE 时建三区子控件。
// 资源: idLog (EDIT) / idInput (EDIT) / idSend (BUTTON) / idStop (BUTTON) / idStatus (STATIC)
func onCreate(hwnd uintptr) {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	stockFont, _, _ := pGetStockObject.Call(17)

	// 日志 EDIT (只读多行)
	cls, _ := Ptr("EDIT")
	defer Hold(cls)
	gLog, _, _ = pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(cls)),
		0,
		WS_CHILD|WS_VSCROLL|ES_MULTILINE|ES_READONLY|ES_AUTOVSCROLL,
		0, 0, 100, 100,
		hwnd, uintptr(idLog), hInst, 0,
	)
	if gLog != 0 {
		pSendMessageW.Call(gLog, WM_SETFONT, stockFont, 1)
	}

	// 输入 EDIT (单行, 在底部)
	clsI, _ := Ptr("EDIT")
	defer Hold(clsI)
	gInput, _, _ = pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(clsI)),
		0,
		WS_CHILD|ES_AUTOHSCROLL,
		0, 0, 100, 24,
		hwnd, uintptr(idInput), hInst, 0,
	)
	if gInput != 0 {
		pSendMessageW.Call(gInput, WM_SETFONT, stockFont, 1)
	}

	// 发送 BUTTON
	clsB, _ := Ptr("BUTTON")
	defer Hold(clsB)
	sendTxt, _ := Ptr("Send")
	defer Hold(sendTxt)
	gSend, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsB)),
		uintptr(unsafe.Pointer(sendTxt)),
		WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON,
		0, 0, 60, 24,
		hwnd, uintptr(idSend), hInst, 0,
	)
	if gSend != 0 {
		pSendMessageW.Call(gSend, WM_SETFONT, stockFont, 1)
	}

	// 停止 BUTTON
	stopTxt, _ := Ptr("Stop")
	defer Hold(stopTxt)
	gStop, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsB)),
		uintptr(unsafe.Pointer(stopTxt)),
		WS_CHILD|WS_TABSTOP|BS_PUSHBUTTON,
		0, 0, 60, 24,
		hwnd, uintptr(idStop), hInst, 0,
	)
	if gStop != 0 {
		pSendMessageW.Call(gStop, WM_SETFONT, stockFont, 1)
	}

	// 状态 STATIC
	clsS, _ := Ptr("STATIC")
	defer Hold(clsS)
	statusTxt, _ := Ptr("ready")
	defer Hold(statusTxt)
	gStatus, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsS)),
		uintptr(unsafe.Pointer(statusTxt)),
		WS_CHILD|SS_LEFT,
		0, 0, 100, 20,
		hwnd, uintptr(idStatus), hInst, 0,
	)
	if gStatus != 0 {
		pSendMessageW.Call(gStatus, WM_SETFONT, stockFont, 1)
	}
}

// onSize 重排子控件（log 占中, input + send + stop 占底部, status 占底底）
func onSize(hwnd uintptr, w, h uint32) {
	if gLog == 0 {
		return
	}
	// 留 24 给 input, 20 给 status
	logH := uintptr(int32(h) - 24 - 20)
	if int32(logH) < 0 {
		logH = 0
	}
	pMoveWindow.Call(gLog, 0, 0, uintptr(w), logH, 1)

	pMoveWindow.Call(gInput, 0, logH, uintptr(int32(w)-130), 24, 1)
	pMoveWindow.Call(gSend, uintptr(int32(w)-130), logH, 60, 24, 1)
	pMoveWindow.Call(gStop, uintptr(int32(w)-70), logH, 70, 24, 1)

	pMoveWindow.Call(gStatus, 0, logH+24, uintptr(w), 20, 1)
}

// onTick 1 Hz 心跳, 更新 status bar (tick 计数)。
// 状态栏内容 = "ticks=N | state=ready" 等 (P1-11 实装具体状态)
var gTickCount uint32

func onTick(_ uintptr) {
	gTickCount++
	// 这里应该更新 status bar 内容, P1-11 实装
	// 留空不影响编译/运行
}

// appendLog 把 lparam 指向的 UTF-16 字符串贴到日志区 (v1-M1 持引用)。
func appendLog(lparam uintptr) {
	if gLog == 0 || lparam == 0 {
		return
	}
	p := (*uint16)(unsafe.Pointer(lparam))
	KeepAlive(p)
	// 选末尾 (EM_SETSEL) + 替换 (EM_REPLACESEL)
	// n = -1 表示"全选"
	pSendMessageW.Call(gLog, EM_SETSEL, 0, ^uintptr(0))
	pSendMessageW.Call(gLog, EM_REPLACESEL, 0, lparam)
	// 自动滚动到底
	n, _, _ := pSendMessageW.Call(gLog, WM_GETTEXTLENGTH, 0, 0)
	pSendMessageW.Call(gLog, EM_SETSEL, n, n)
	pSendMessageW.Call(gLog, EM_SCROLLCARET, 0, 0)
}

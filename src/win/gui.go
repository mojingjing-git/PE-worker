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
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
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
	idLogCopy  = 1006
	idLogClear = 1007
	idLogSave  = 1008
	idTimerTick = 1
	idTimerQuit = 2

	tickIntervalMs = 1000
	logMaxChars    = 60000
	logTruncateKeep = 30000 // 超过 logMaxChars 时保留后 30000 字符
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
	gLogCopy  uintptr
	gLogClear uintptr
	gLogSave  uintptr
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

	// 主窗口就绪回调（main.go 在此设 logx.SetHWND，把日志投递到本窗口的 WM_LOG_LINE）。
	// 必须在消息循环启动前调，否则 logx 拿不到 hwnd → 日志走 stderr 兜底 → GUI 看不到任何输出。
	if OnMainWindowCreated != nil {
		OnMainWindowCreated(hwnd)
	}

	// 把窗口强制放到**主显示器**的工作区左上角。
	// 不做这一步的话，在装了多个虚拟显示适配器（MuMu / Todesk / GameViewer /
	// Meta VR 等）的开发机上，OS 可能把窗口分配到一个没有真屏的虚拟适配器
	// → 渲染到不存在的 surface → 看起来"白屏"。
	// SPI_GETWORKAREA 拿主显示器的可用工作区（去掉任务栏），(0,0) 相对工作区原点
	// 即"任务栏上面那一行的左上角"，最稳的位置。
	//
	// 第二个修：显式 ShowWindow(SW_RESTORE)。前面 CreateWindowExW 用
	// WS_VISIBLE + CW_USEDEFAULT + CW_USEDEFAULT 时，Win32 老 bug 会让窗口
	// 默认处于**最小化**状态（坐标跑到 -32000, -32000 的 taskbar 角落），
	// SW_SHOWNORMAL 解不掉，需要 SW_RESTORE（=9）显式 un-minimize。
	forceOnPrimaryMonitor(hwnd)
	pShowWindow.Call(hwnd, SW_RESTORE)

	// 显式 UpdateWindow（CreateWindowEx 已经给 wsVisible, 但保险）
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

// OnMainWindowCreated 主窗口句柄就绪回调。win.Run() 在 CreateWindowExW 之后、消息
// 循环启动前调一次。main.go 用它把 hwnd 绑给 logx.SetHWND，使后续日志走
// PostMessage(WM_LOG_LINE) 到本窗口（兜底 fallback 是写文件，GUI 看不到）。
var OnMainWindowCreated func(hwnd uintptr)

// SetOnSend 设 Send 回调。返回时**立即生效**（wndProc 下一条 WM_COMMAND 就会调）。
func SetOnSend(f func(text string)) { OnSend = f }

// SetOnStop 设 Stop 回调。
func SetOnStop(f func()) { OnStop = f }

// SetOnMainWindowCreated 设主窗口就绪回调。
func SetOnMainWindowCreated(f func(hwnd uintptr)) { OnMainWindowCreated = f }

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

// forceOnPrimaryMonitor 把窗口强制放到主显示器工作区左上角。
//
// 背景：开发机装了多个虚拟显示适配器（MuMu / Todesk / GameViewer / Meta VR
// 等），它们没接真屏但还占显示位。CW_USEDEFAULT 让 OS 自己选显示器 → 经常
// 选到虚拟适配器 → GDI 渲染到不存在的 surface → "白屏"。
//
// 用 SPI_GETWORKAREA 拿主显示器的工作区（去掉任务栏），(0,0) 即工作区原点。
// SetWindowPos 的 SWP_NOSIZE 保留窗口尺寸，SWP_NOZORDER 不抢 z-order。
//
// 失败时静默 fallback：原 CW_USEDEFAULT 位置（多显示器环境下也不一定白屏，
// 真 PE 里没有这些虚拟适配器，强行 fallback 反而稳）。
func forceOnPrimaryMonitor(hwnd uintptr) {
	var workArea struct{ Left, Top, Right, Bottom int32 }
	r, _, _ := pSystemParametersInfoW.Call(
		SPI_GETWORKAREA,
		0,
		uintptr(unsafe.Pointer(&workArea)),
		0,
	)
	if r == 0 {
		return // SPI 失败 → 保留 CW_USEDEFAULT 默认位置
	}
	pSetWindowPos.Call(
		hwnd, 0,
		uintptr(int32(workArea.Left)), uintptr(int32(workArea.Top)),
		0, 0, // 0,0 + SWP_NOSIZE = 保留尺寸
		SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE,
	)
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
		// logx 投递的日志行（lparam = *uint16 指向 wstrKeep 里 UTF-16；wparam = level）
		appendLog(wparam, lparam)
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
		case idLogCopy:
			// 全选 + 复制到剪贴板 + 取消选择
			pSendMessageW.Call(gLog, EM_SETSEL, 0, ^uintptr(0))
			pSendMessageW.Call(gLog, WM_COPY, 0, 0)
			pSendMessageW.Call(gLog, EM_SETSEL, ^uintptr(0), ^uintptr(0))
			return 0
		case idLogClear:
			// 清空日志（用空串 WM_SETTEXT；utf16Ptr0 是 NUL 指针）
			pSendMessageW.Call(gLog, WM_SETTEXT, 0, uintptr(unsafe.Pointer(utf16Ptr0())))
			return 0
		case idLogSave:
			saveLogToFile()
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
		appendLog(0, lparam) // wparam=0 (Info 标签) for legacy WM_USER_INPUT
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
		WS_CHILD|WS_VISIBLE|WS_VSCROLL|ES_MULTILINE|ES_READONLY|ES_AUTOVSCROLL,
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
		WS_CHILD|WS_VISIBLE|ES_AUTOHSCROLL,
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
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
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
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
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
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		0, 0, 100, 20,
		hwnd, uintptr(idStatus), hInst, 0,
	)
	if gStatus != 0 {
		pSendMessageW.Call(gStatus, WM_SETFONT, stockFont, 1)
	}

	// 日志工具按钮（Copy / Clear / Save）—— 状态栏右侧
	clsB2 := clsB // BUTTON class 已存在
	_ = clsB2
	copyTxt, _ := Ptr("Copy")
	defer Hold(copyTxt)
	gLogCopy, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsB)),
		uintptr(unsafe.Pointer(copyTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		0, 0, 60, 22,
		hwnd, uintptr(idLogCopy), hInst, 0,
	)
	if gLogCopy != 0 {
		pSendMessageW.Call(gLogCopy, WM_SETFONT, stockFont, 1)
	}
	clearTxt, _ := Ptr("Clear")
	defer Hold(clearTxt)
	gLogClear, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsB)),
		uintptr(unsafe.Pointer(clearTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		0, 0, 60, 22,
		hwnd, uintptr(idLogClear), hInst, 0,
	)
	if gLogClear != 0 {
		pSendMessageW.Call(gLogClear, WM_SETFONT, stockFont, 1)
	}
	saveTxt, _ := Ptr("Save")
	defer Hold(saveTxt)
	gLogSave, _, _ = pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(clsB)),
		uintptr(unsafe.Pointer(saveTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		0, 0, 60, 22,
		hwnd, uintptr(idLogSave), hInst, 0,
	)
	if gLogSave != 0 {
		pSendMessageW.Call(gLogSave, WM_SETFONT, stockFont, 1)
	}
}

// onSize 重排子控件（log 占中, input + send + stop 占底部, status + 3 个 log 按钮占底底）
func onSize(hwnd uintptr, w, h uint32) {
	if gLog == 0 {
		return
	}
	// 留 24 给 input, 24 给 status+3 按钮（多 4 像素让按钮好看）
	logH := uintptr(int32(h) - 24 - 24)
	if int32(logH) < 0 {
		logH = 0
	}
	pMoveWindow.Call(gLog, 0, 0, uintptr(w), logH, 1)

	pMoveWindow.Call(gInput, 0, logH, uintptr(int32(w)-130), 24, 1)
	pMoveWindow.Call(gSend, uintptr(int32(w)-130), logH, 60, 24, 1)
	pMoveWindow.Call(gStop, uintptr(int32(w)-70), logH, 70, 24, 1)

	// 状态栏：左侧 status text 占 (w - 180)，右侧 3 个按钮各 60
	const btnW = 60
	const btnGap = 0
	const btnsTotalW = btnW*3 + btnGap*2 // 180
	pMoveWindow.Call(gStatus, 0, logH+24, uintptr(int32(w)-btnsTotalW), 22, 1)
	pMoveWindow.Call(gLogCopy, uintptr(int32(w)-btnsTotalW), logH+24, btnW, 22, 1)
	pMoveWindow.Call(gLogClear, uintptr(int32(w)-btnsTotalW+btnW+btnGap), logH+24, btnW, 22, 1)
	pMoveWindow.Call(gLogSave, uintptr(int32(w)-btnsTotalW+2*(btnW+btnGap)), logH+24, btnW, 22, 1)
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
//
// wparam = log level (0=Debug / 1=Info / 2=Warn / 3=Error)；暂未染色（prepend 时间戳
// 已够定位，颜色留给后续 EM_SETCHARFORMAT commit）。
//
// 行为：
//   1. 检查 buffer 长度，超 logMaxChars 就删前段保留后 logTruncateKeep（B 修复 M-3）
//   2. prepend `[HH:MM:SS] ` 时间戳（D：方便定位日志时间）
//   3. 找 think 段（<think>...</think>），单独 EM_SETCHARFORMAT 改 yHeight 让其"小一号"
//   4. EM_SETSEL(-1,-1) + EM_REPLACESEL 追加 + EM_SCROLLCARET 滚动
func appendLog(wparam, lparam uintptr) {
	if gLog == 0 || lparam == 0 {
		return
	}
	p := (*uint16)(unsafe.Pointer(lparam))
	KeepAlive(p)

	// 1) B 修复：logMaxChars 截断（M-3 实现）
	truncateLogIfNeeded()

	// 2) D 修复：prepend 时间戳 + level 标签
	prefix := formatLogPrefix(int32(wparam))

	// 3) 拼 prefix + line 一次投（避免 2 次 REPLACESEL 触发 2 次重绘）
	prefixBuf, _ := syscall.UTF16FromString(prefix)
	// lineBuf: 原 line 的 UTF-16（需要算长度）
	lineLen := 0
	for i := 0; ; i++ {
		c := *(*uint16)(unsafe.Pointer(lparam + uintptr(i)*2))
		if c == 0 {
			lineLen = i
			break
		}
	}
	lineBuf := make([]uint16, lineLen)
	for i := 0; i < lineLen; i++ {
		lineBuf[i] = *(*uint16)(unsafe.Pointer(lparam + uintptr(i)*2))
	}
	combined := make([]uint16, 0, len(prefixBuf)+len(lineBuf))
	// prefixBuf 来自 syscall.UTF16FromString，含尾部 NUL。EM_REPLACESEL 是 NUL-terminated
	// 字符串写入（NUL 出现就截断），所以 combined 末尾不能有 NUL 干扰 lineBuf 进入 EDIT。
	// 方案：剥 prefixBuf 尾 NUL（lineBuf 本身不含 NUL，appendLog 入口用 c==0 检测提前 break）。
	if len(prefixBuf) > 0 {
		combined = append(combined, prefixBuf[:len(prefixBuf)-1]...)
	}
	combined = append(combined, lineBuf...)

	// 移到末尾 + 追加
	pSendMessageW.Call(gLog, EM_SETSEL, ^uintptr(0), ^uintptr(0))
	if len(combined) > 0 {
		pSendMessageW.Call(gLog, EM_REPLACESEL, 0, uintptr(unsafe.Pointer(&combined[0])))
	}
	pSendMessageW.Call(gLog, EM_SCROLLCARET, 0, 0)

	// 4) think 段染色（yHeight 缩小 = 小一号）
	// prefixLen = 实际写入 EDIT 的 prefix 字符数 = len(prefixBuf)-1（剥了尾 NUL）
	if lineLen > 0 {
		styleThinkBlocks(lineBuf, len(prefixBuf)-1)
	}
}

// thinkSpan 描述一个 think 段在 lineBuf 里的起止位置（uint16 单元数）。
type thinkSpan struct{ Start, End int }

// findThinkBlocks 扫描 lineBuf 找所有 <think>...</think> 段（嵌套不支持）。
// lineBuf 是 UTF-16 LE 的 []uint16；tag 是 7/8 个 ASCII 字符（< 占 1 uint16 单元）。
// 返回的 Start/End 是 lineBuf 的 uint16 单元索引，End = </think> 之后位置（开区间）。
func findThinkBlocks(lineBuf []uint16) []thinkSpan {
	var spans []thinkSpan
	n := len(lineBuf)
	const tagStartLen = 7 // len("<think>")
	const tagEndLen = 8   // len("</think>")
	for i := 0; i < n; {
		// 找 <think>
		s := -1
		for j := i; j+tagStartLen <= n; j++ {
			if lineBuf[j] == '<' && lineBuf[j+1] == 't' && lineBuf[j+2] == 'h' &&
				lineBuf[j+3] == 'i' && lineBuf[j+4] == 'n' && lineBuf[j+5] == 'k' &&
				lineBuf[j+6] == '>' {
				s = j
				break
			}
		}
		if s < 0 {
			break
		}
		// 找 </think>
		e := -1
		for j := s + tagStartLen; j+tagEndLen <= n; j++ {
			if lineBuf[j] == '<' && lineBuf[j+1] == '/' && lineBuf[j+2] == 't' &&
				lineBuf[j+3] == 'h' && lineBuf[j+4] == 'i' && lineBuf[j+5] == 'n' &&
				lineBuf[j+6] == 'k' && lineBuf[j+7] == '>' {
				e = j + tagEndLen
				break
			}
		}
		if e < 0 {
			// 没结尾就染到末尾（DeepSeek 偶尔截断）
			e = n
		}
		spans = append(spans, thinkSpan{Start: s, End: e})
		i = e
	}
	return spans
}

// newCharFormatSize 构造 CHARFORMATW byte buffer（92 字节），只设 cbSize/dwMask/yHeight。
//
// yHeight 单位 twips：1pt = 20 twips。EDIT 默认 ~9pt = 180；think 段用 140 = 7pt 显小。
// CHARFORMATW 是 NT-based EDIT（user32）期望的 layout（Wine dlls/user32/edit.c 严格
// 比较 cbSize == sizeof(CHARFORMATW) == 92，60 会被拒）。字段偏移：cbSize=0, dwMask=4,
// yHeight=12 (cbSize+dwMask+DwEffects 之后)。其他字段默认 0 (dwEffects/yOffset/crTextColor/
// bCharSet/bPitchAndFamily/szFaceName[64])。用 byte buffer + binary.LittleEndian 写，避开
// Go struct padding 跨平台大小不一致问题。
func newCharFormatSize(yHeight int32) []byte {
	const cfSize = 92 // CHARFORMATW 实际 layout size（user32 EDIT 接受值）
	buf := make([]byte, cfSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(cfSize)) // cbSize
	binary.LittleEndian.PutUint32(buf[4:8], uint32(CFM_SIZE)) // dwMask = CFM_SIZE
	// yHeight 在 offset 12（cbSize+dwMask+DwEffects 之后）
	binary.LittleEndian.PutUint32(buf[12:16], uint32(yHeight))
	return buf
}

// styleThinkBlocks 把 lineBuf 里所有 think 段在 EDIT buffer 里单独缩小字号。
// prefixLen 是投到 EDIT 之前 prefix 占的 UTF-16 单元数（用于换算到绝对位置）。
// 染色后取消选择 + 移到末尾。
func styleThinkBlocks(lineBuf []uint16, prefixLen int) {
	spans := findThinkBlocks(lineBuf)
	if len(spans) == 0 {
		return
	}
	cf := newCharFormatSize(140) // 7pt 字符高度
	for _, sp := range spans {
		start := uintptr(prefixLen + sp.Start)
		end := uintptr(prefixLen + sp.End)
		pSendMessageW.Call(gLog, EM_SETSEL, start, end)
		pSendMessageW.Call(gLog, EM_SETCHARFORMAT, SCF_SELECTION, uintptr(unsafe.Pointer(&cf[0])))
	}
	// 取消选择，移到末尾（避免滚动时高亮干扰）
	pSendMessageW.Call(gLog, EM_SETSEL, ^uintptr(0), ^uintptr(0))
}

// truncateLogIfNeeded 检查日志 buffer 长度，超 logMaxChars 就删前段保留后 logTruncateKeep。
// EM_SETSEL + WM_CLEAR 删选中内容；删完光标会乱，appendLog 后续的 EM_SETSEL(-1,-1) 会重置。
func truncateLogIfNeeded() {
	n, _, _ := pSendMessageW.Call(gLog, WM_GETTEXTLENGTH, 0, 0)
	if uint32(n) <= logMaxChars {
		return
	}
	cut := uint32(n) - logTruncateKeep
	pSendMessageW.Call(gLog, EM_SETSEL, 0, uintptr(cut))
	pSendMessageW.Call(gLog, WM_CLEAR, 0, 0)
}

// formatLogPrefix 拼 `[HH:MM:SS] [L] ` 前缀（L = D/I/W/E）。
// 用 GetLocalTime（kernel32）拿本地时间。
func formatLogPrefix(level int32) string {
	var st struct {
		Year         uint16
		Month        uint16
		Dow          uint16
		Day          uint16
		Hour         uint16
		Minute       uint16
		Second       uint16
		Milliseconds uint16
	}
	pGetLocalTime.Call(uintptr(unsafe.Pointer(&st)))
	var label byte
	switch level {
	case 0:
		label = 'D'
	case 1:
		label = 'I'
	case 2:
		label = 'W'
	case 3:
		label = 'E'
	default:
		label = '?'
	}
	return fmt.Sprintf("[%02d:%02d:%02d] [%c] ", st.Hour, st.Minute, st.Second, label)
}

// saveLogToFile 把日志 buffer 全部 dump 到 exeDir/smith.session.log。
// 中文路径 OK（os.WriteFile 走 UTF-8）。GUI 在 UI 线程跑（不阻塞）。
// 状态栏显示结果（win 包不 import logx，单向依赖 logx→win；状态栏是 UI 自反馈）。
func saveLogToFile() {
	n, _, _ := pSendMessageW.Call(gLog, WM_GETTEXTLENGTH, 0, 0)
	if n == 0 {
		setStatusText("log empty, nothing to save")
		return
	}
	buf := make([]uint16, n+1)
	pSendMessageW.Call(gLog, WM_GETTEXT, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	// 找 NUL 截断
	length := 0
	for i, c := range buf {
		if c == 0 {
			length = i
			break
		}
		length = i + 1
	}
	s := string(utf16ToRunes(buf[:length]))
	exe, _ := os.Executable()
	path := filepath.Join(filepath.Dir(exe), "smith.session.log")
	if err := os.WriteFile(path, []byte(s), 0644); err != nil {
		setStatusText(fmt.Sprintf("save failed: %v", err))
		return
	}
	setStatusText(fmt.Sprintf("saved %d chars to %s", length, filepath.Base(path)))
}

// setStatusText 设置状态栏文字。状态栏没创建时静默跳过。
func setStatusText(s string) {
	if gStatus == 0 {
		return
	}
	buf, _ := syscall.UTF16FromString(s)
	if len(buf) > 0 {
		pSendMessageW.Call(gStatus, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&buf[0])))
	}
}

//go:build windows

// spike/gui -- P0-3
//
// 目标：验证「纯 Go（CGO_ENABLED=0）+ syscall 调 Win32」能不能在目标 PE 里
// 把一个真正的窗口画出来。
//
// 用到的关键手段：
//   - syscall.NewCallback 把 Go 函数变成可以交给 Win32 的 WNDPROC
//     （Go 1.20 确实导出它，见 src/syscall/syscall_windows.go:180）
//   - runtime.LockOSThread() 把消息循环钉在一个 OS 线程上
//   - 只用 user32 内建控件：EDIT / BUTTON / STATIC（不用 comctl32）
//
// 这个 spike **不加 -H windowsgui**，故意保留控制台，
// 这样窗口建不起来时还能从 stdout 看到到底哪一步失败。
// 输出全 ASCII。
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")

	pRegisterClassExW  = user32.NewProc("RegisterClassExW")
	pCreateWindowExW   = user32.NewProc("CreateWindowExW")
	pDestroyWindow     = user32.NewProc("DestroyWindow")
	pDefWindowProcW    = user32.NewProc("DefWindowProcW")
	pShowWindow        = user32.NewProc("ShowWindow")
	pUpdateWindow      = user32.NewProc("UpdateWindow")
	pGetMessageW       = user32.NewProc("GetMessageW")
	pTranslateMessage  = user32.NewProc("TranslateMessage")
	pDispatchMessageW  = user32.NewProc("DispatchMessageW")
	pPostQuitMessage   = user32.NewProc("PostQuitMessage")
	pMoveWindow        = user32.NewProc("MoveWindow")
	pSendMessageW      = user32.NewProc("SendMessageW")
	pSetWindowTextW    = user32.NewProc("SetWindowTextW")
	pSetTimer          = user32.NewProc("SetTimer")
	pKillTimer         = user32.NewProc("KillTimer")
	pGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	pGetDesktopWindow  = user32.NewProc("GetDesktopWindow")
	pGetStockObject    = gdi32.NewProc("GetStockObject")
	pGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	pGetClientRect     = user32.NewProc("GetClientRect")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsVScroll          = 0x00200000
	wsBorder           = 0x00800000
	wsTabStop          = 0x00010000
	esMultiline        = 0x0004
	esReadonly         = 0x0800
	esAutoVScroll      = 0x0040
	esAutoHScroll      = 0x0080
	wsExClientEdge     = 0x00000200
	bsPushButton       = 0x00000000
	ssLeft             = 0x00000000
	ssCenterImage      = 0x00000200
	swShow             = 5
	defaultGuiFont     = 17
	wmCreate           = 0x0001
	wmDestroy          = 0x0002
	wmSize             = 0x0005
	wmSetFont          = 0x0030
	wmTimer            = 0x0113
	wmClose            = 0x0010
	wmAppendLog        = 0x8000 + 1
	emSetSel           = 0x00B1
	emReplaceSel       = 0x00C2
	emScrollCaret      = 0x0115
	emSetLimitText     = 0x00C5
	idLog              = 1001
	idInput            = 1002
	idSend             = 1003
	idStop             = 1004
	idStatus           = 1005
	idTimerTick        = 1
	idTimerQuit        = 2
)

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

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }
type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

var (
	gHwnd, gLog, gInput, gSend, gStop, gStatus uintptr
	gTicks                                     int
)

// strKeep 持有所有交给 Win32 的 UTF-16 缓冲，防止它们在我们还在用的时候被 GC 回收。
//
// 为什么必需（二轮审计 C1；本轮复核确认真实存在、且文档谎称已修）：
// syscall.UTF16PtrFromString 返回的 *uint16 一旦被转成 uintptr，Go 的 GC
// **就再也看不到它**（GC 不追踪 uintptr）。从 wcs() 返回到 .Call() 真正陷入内核
// 之间，只要发生一次 GC，这块内存就可能被回收并复用 —— Win32 拿到的是野指针。
// 32 位 PE 地址空间小、堆复用快，命中概率远高于 64 位，而 32 位正是主战场。
//
// 历史教训：docs/07 的 B1 条目声称"加包级 strKeep []unsafe.Pointer 持有引用"，
// 但 strKeep 在文件里从未存在，3 个调用点（mustCreate / RegisterClassExW /
// CreateWindowExW）全部裸奔。这里补上。
//
// 为什么不设上限（第四轮独立审计 M1）：
// 早期版本"到顶就 strKeep = strKeep[:0]"看似控制内存，实为定时炸弹 ——
// 一旦某个在飞 Win32 Call 持有被踢出去的 *uint16，重置 → append 之间的
// GC 窗口里 Win32 就会读到野指针。GUI 表现会像"PE 不支持 GUI"这种最难排查
// 的故障。删掉上限后：strKeep 增长到一定规模后 append 会重用 backing array，
// 实际峰值 < 100 个元素（GUI 启动期），长期持有几 KB 引用 + 几百 KB 字符串
// 对一个 GUI 程序完全可接受。
var strKeep []*uint16

func wcs(s string) uintptr {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	strKeep = append(strKeep, p)
	return uintptr(unsafe.Pointer(p))
}

// WndProc —— 整个 spike 里唯一的 callback，绝不能放进循环（回调数量有上限）
func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmCreate:
		gHwnd = hwnd
		gLog = mustCreate(wsExClientEdge, "EDIT", "",
			wsChild|wsVisible|wsVScroll|esMultiline|esReadonly|esAutoVScroll|ssLeft,
			idLog)
		gInput = mustCreate(wsExClientEdge, "EDIT", "",
			wsChild|wsVisible|esAutoHScroll, idInput)
		gSend = mustCreate(0, "BUTTON", "Send",
			wsChild|wsVisible|wsTabStop|bsPushButton, idSend)
		gStop = mustCreate(0, "BUTTON", "Stop (Esc)",
			wsChild|wsVisible|wsTabStop|bsPushButton, idStop)
		gStatus = mustCreate(0, "STATIC", "status: created",
			wsChild|wsVisible|ssLeft|ssCenterImage, idStatus)

		// 字体：取系统默认 GUI 字体，别硬编码字体名（PE 里没有雅黑）
		hf, _, _ := pGetStockObject.Call(defaultGuiFont)
		for _, h := range []uintptr{gLog, gInput, gSend, gStop, gStatus} {
			if h != 0 {
				pSendMessageW.Call(h, wmSetFont, hf, 1)
			}
		}
		if gLog != 0 {
			pSendMessageW.Call(gLog, emSetLimitText, 60000, 0)
		}

		// 布局必须用「客户区」尺寸，不能用 CreateWindowExW 传进来的外框尺寸 ——
		// 外框含标题栏和边框，比客户区大，直接拿它算会把状态栏推到可见区外。
		// WM_CREATE 时窗口的客户区已经确定了，所以这里问得到正确值。
		cw, ch := clientSize(hwnd)
		layout(cw, ch)
		appendLog("gui spike: window created, controls laid out.")
		appendLog("if you can read this and the layout wraps, V1 GUI is feasible in PE.")
		pSetTimer.Call(hwnd, idTimerTick, 1000, 0)
		// ⚠️ 这里**不能**顺手起 idTimerQuit。
		// 原来 WM_CREATE 无条件设了 1000ms，而 main() 只在 *secs > 0 时才去改它
		// （二轮审计 C2）—— 结果 `-secs 0`（帮助文本写着 "0 = stay open"）实际
		// 1 秒后自己关了，跟参数语义完全相反。
		// 现在退出定时器只在 main() 里按 -secs 创建，这里不碰。
		return 0

	case wmSize:
		layout(int(lparam&0xFFFF), int((lparam>>16)&0xFFFF))
		return 0

	case wmAppendLog:
		appendLog("tick")
		return 0

	case wmTimer:
		if wparam == idTimerTick {
			gTicks++
			appendLog(fmt.Sprintf("tick %d  (timer works, message loop alive)", gTicks))
		} else if wparam == idTimerQuit {
			fmt.Printf("auto-close timer fired after %d ticks\n", gTicks)
			pKillTimer.Call(hwnd, idTimerTick)
			pKillTimer.Call(hwnd, idTimerQuit)
			pDestroyWindow.Call(hwnd)
		}
		return 0

	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0

	case wmDestroy:
		fmt.Println("WM_DESTROY -> PostQuitMessage")
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func mustCreate(exStyle uintptr, class, text string, style uintptr, id uintptr) uintptr {
	h, _, _ := pCreateWindowExW.Call(exStyle, wcs(class), wcs(text), style,
		0, 0, 10, 10, gHwnd, id, 0, 0)
	return h
}

// clientSize 返回客户区（不含标题栏/边框）的宽高。失败时回退到 640x480，
// 保证 layout() 拿到的是正数，不会算出负宽度。
func clientSize(hwnd uintptr) (int, int) {
	var rc rect
	r, _, _ := pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if r == 0 {
		return 640, 480
	}
	return int(rc.Right - rc.Left), int(rc.Bottom - rc.Top)
}

func layout(cx, cy int) {
	const pad, btnw, rowh, statush = 8, 76, 26, 22
	if cx < 320 {
		cx = 320
	}
	if cy < 240 {
		cy = 240
	}
	logh := cy - pad*3 - rowh - statush
	if logh < 60 {
		logh = 60
	}
	sendx := cx - pad - btnw*2 - pad
	inw := sendx - pad*2
	if inw < 60 {
		inw = 60
	}
	move(gLog, pad, pad, cx-pad*2, logh)
	move(gInput, pad, pad+logh+pad, inw, rowh)
	move(gSend, sendx, pad+logh+pad, btnw, rowh)
	move(gStop, sendx+btnw+pad, pad+logh+pad, btnw, rowh)
	move(gStatus, pad, cy-statush-2, cx-pad*2, statush)
}

func move(h uintptr, x, y, w, hh int) {
	if h != 0 {
		pMoveWindow.Call(h, uintptr(x), uintptr(y), uintptr(w), uintptr(hh), 1)
	}
}

func appendLog(s string) {
	if gLog == 0 {
		return
	}
	p, _ := syscall.UTF16PtrFromString(s + "\r\n")
	if p == nil {
		return
	}
	txt := uintptr(unsafe.Pointer(p))
	// 光标移到末尾再插入
	n, _, _ := pSendMessageW.Call(gLog, 0x000E, 0, 0) // WM_GETTEXTLENGTH
	pSendMessageW.Call(gLog, emSetSel, n, n)
	pSendMessageW.Call(gLog, emReplaceSel, 1, txt)
	pSendMessageW.Call(gLog, emScrollCaret, 0, 0)
	// p 在这里已经"死"了（编译器只看到 txt 这个 uintptr），必须显式续命，
	// 否则从 txt 求值到 emReplaceSel 返回之间 GC 可以回收它。同 wcs() 的注释。
	runtime.KeepAlive(p)
}

func main() {
	// ⚠️ 必须是 main 的第一件事。
	// 创建窗口的 OS 线程必须和跑 GetMessage 的是同一个线程，否则窗口消息会被投递到
	// 线程 A 的消息队列、而线程 B 在 GetMessage 上永远等不到 —— 表现就是窗口完全冻结、
	// 定时器不触发、-secs 自动关闭失效。放在 CreateWindowExW 之后加锁是错的：
	// 那时候 goroutine 可能已经迁移到别的 OS 线程了。
	runtime.LockOSThread()

	secs := flag.Int("secs", 8, "auto-close after N seconds (0 = stay open)")
	flag.Parse()

	fmt.Println("========================================================")
	fmt.Println(" pe-spike-gui  (P0-3 pure-Go Win32 window)")
	fmt.Println("========================================================")
	fmt.Printf("Go %s  %s/%s  compiler=%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.Compiler)

	// --- 预检：PE 里有没有可用的桌面 ---
	desk, _, _ := pGetDesktopWindow.Call()
	smCX, _, _ := pGetSystemMetrics.Call(0) // SM_CXSCREEN
	smCY, _, _ := pGetSystemMetrics.Call(1) // SM_CYSCREEN
	fmt.Printf("preflight: GetDesktopWindow=%d  screen=%dx%d\n", desk, smCX, smCY)

	hInst, _, _ := pGetModuleHandleW.Call(0)
	fmt.Printf("preflight: hInstance=%d\n", hInst)

	// --- 注册窗口类 ---
	cls := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		Style:         0,
		LpfnWndProc:   syscall.NewCallback(wndProc), // ← 唯一一次创建 callback
		HInstance:     hInst,
		HCursor:       mustLoadCursor(),
		HbrBackground: 16, // COLOR_BTNFACE + 1
		LpszClassName: wcs("PeSpikeGui"),
	}
	atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))
	// cls 之后再也不被引用，编译器可以判定它已死亡 —— 但 Win32 刚刚才用它。
	// cls.LpfnWndProc / cls.HCursor / cls.LpszClassName 全都是 uintptr，GC 看不见。
	runtime.KeepAlive(&cls)
	fmt.Printf("RegisterClassExW atom=%d err=%v\n", atom, err)
	if atom == 0 {
		fmt.Println("FATAL: could not register window class -> GUI not feasible here")
		os.Exit(3)
	}

	// --- 建窗口（小屏自动收缩） ---
	cw, ch := 780, 520
	if int(smCX) < cw+40 {
		cw = int(smCX) - 40
	}
	if int(smCY) < ch+60 {
		ch = int(smCY) - 60
	}
	if cw < 320 {
		cw = 320
	}
	if ch < 240 {
		ch = 240
	}
	// 居中（二轮审计 C3）：原来硬编码 (10,10)。PE 常见 800x600，
	// 左上角贴边会把状态栏和右下角的按钮挤到很别扭的位置。
	wx := (int(smCX) - cw) / 2
	wy := (int(smCY) - ch) / 2
	if wx < 0 {
		wx = 0
	}
	if wy < 0 {
		wy = 0
	}
	hwnd, _, err := pCreateWindowExW.Call(0, wcs("PeSpikeGui"), wcs("pe-spike-gui"),
		wsOverlappedWindow, uintptr(wx), uintptr(wy), uintptr(cw), uintptr(ch), 0, 0, hInst, 0)
	fmt.Printf("CreateWindowExW hwnd=%d err=%v\n", hwnd, err)
	if hwnd == 0 {
		fmt.Println("FATAL: CreateWindowExW failed -> GUI not feasible here")
		os.Exit(4)
	}

	// --- 显示 ---
	r, _, _ := pShowWindow.Call(hwnd, swShow)
	fmt.Printf("ShowWindow ret=%d (0 = was previously hidden, still OK)\n", r)
	pUpdateWindow.Call(hwnd)
	fmt.Printf("window should be visible now; auto-close in %d s\n", *secs)

	// --- 消息循环：必须钉在同一个 OS 线程上 ---
	// main() 第一行已经 LockOSThread 过，这里再调一次是幂等的，只为把
	// "建窗线程 == 消息循环线程" 这条契约写在现场（二轮审计确认 B2 已修好）。
	runtime.LockOSThread()

	// 退出定时器在这里创建，而不是 WM_CREATE —— 这样 `-secs 0` 才真的是"保持打开"。
	if *secs > 0 {
		pSetTimer.Call(hwnd, idTimerQuit, uintptr(*secs*1000), 0)
	} else {
		fmt.Println("auto-close disabled (-secs 0): window stays open until closed by hand")
	}

	var m msgT
	for {
		got, _, gerr := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(got) < 0 {
			// ⚠️ -1 是"出错"，不是 WM_QUIT。原来用 `<= 0` 一起 break，
			// 把消息队列坏掉伪装成正常退出（二轮审计 C8）。
			fmt.Printf("FATAL: GetMessageW returned -1 (%v)\n", gerr)
			os.Exit(5)
		}
		if got == 0 {
			break // WM_QUIT
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	fmt.Printf("message loop exited after %d ticks\n", gTicks)
	fmt.Println("RESULT: gui spike completed. If you saw a window with a log pane, P0-3 PASSES.")
	os.Exit(0)
}

// IDC_ARROW 是资源 id 32512，MAKEINTRESOURCE(32512) 就是直接把 32512 当指针传
func mustLoadCursor() uintptr {
	h, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	return h
}

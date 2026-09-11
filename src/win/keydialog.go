// Package win — keydialog.go
//
// 首次运行 API Key 输入对话框（顶层 modal 窗口，不依赖主窗口）。
//
// 行为：
//   - 用户输入 key + 勾选/不勾选"保存到磁盘" + 点 OK
//   - 不勾选：key 只在内存里，进程退出就丢
//   - 勾选：调用方负责写到 smith.key（win 包不直接做 IO，调用方传回调）
//
// 设计原则（PLAN §0.6 A5 + v1 第四轮）：
//   - 顶层窗口（无 parent），不需要等主窗口
//   - 子消息循环做 modal 行为
//   - 失败时 err 透传，不吞
package win

import (
	"syscall"
	"unsafe"
)

const (
	idKeyEdit   = 1201
	idKeySave   = 1202
	idKeyOKBtn  = 1203
	idKeyCancel = 1204
)

// WM_QUIT 是消息循环终止信号。
const WM_QUIT = 0x0012

// PM_REMOVE for PeekMessage.
const PM_REMOVE = 0x0001

// keydialog 状态（package-level global，sub loop 期间读写）。
// 每次 PromptAPIKey 入口 reset。
var (
	keyDialogClassReg    bool
	keyDialogClassName16 *uint16
	keyDialogResultKey   string
	keyDialogResultSave  bool
	keyDialogResultOK    bool
)

// 标准 Win32 控件 class 名（lazy alloc 一次）
var (
	stringClass *uint16
	editClass   *uint16
	buttonClass *uint16
)

func init() {
	stringClass = mustClass("STATIC")
	editClass = mustClass("EDIT")
	buttonClass = mustClass("BUTTON")
}

func mustClass(name string) *uint16 {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		panic("win keydialog: " + err.Error())
	}
	return p
}

type keyDialogErr struct{ msg string }

func (e *keyDialogErr) Error() string { return "win keydialog: " + e.msg }

var errKeyDialogClass = &keyDialogErr{"register window class failed"}

// RegisterKeyDialogClass 幂等注册 dialog 用的 window class。
func RegisterKeyDialogClass() error {
	if keyDialogClassReg {
		return nil
	}
	hInst, _, _ := pGetModuleHandleW.Call(0)
	name, err := syscall.UTF16PtrFromString("SmithKeyDialog")
	if err != nil {
		return err
	}
	keyDialogClassName16 = name

	// WNDCLASSEXW
	var cls struct {
		CbSize        uint32
		Style         uint32
		WndProc       uintptr
		CbClsExtra    int32
		CbWndExtra    int32
		HInstance     uintptr
		HIcon         uintptr
		HCursor       uintptr
		HbrBackground uintptr
		MenuName      *uint16
		ClassName     *uint16
		IconSm        uintptr
	}
	cls.CbSize = uint32(unsafe.Sizeof(cls))
	cls.WndProc = syscall.NewCallback(keyDialogWndProc)
	cls.HInstance = hInst
	cls.HCursor = loadCursor(0, IDC_ARROW)
	cls.HbrBackground = 16 + 1 // COLOR_BTNFACE+1
	cls.ClassName = name

	atom, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))
	KeepAlive(&cls)
	if atom == 0 {
		return errKeyDialogClass
	}
	keyDialogClassReg = true
	return nil
}

// loadCursor 加载系统 cursor
func loadCursor(hInstance, id uintptr) uintptr {
	r, _, _ := user32.NewProc("LoadCursorW").Call(hInstance, id)
	return r
}

// keyDialogWndProc dialog 消息处理
func keyDialogWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		onKeyDialogCreate(hwnd)
		return 0
	case WM_COMMAND:
		ctrlID := uint16(uint32(wparam) & 0xFFFF)
		switch ctrlID {
		case idKeyOKBtn:
			keyDialogCollectResult(hwnd)
			pDestroyWindow.Call(hwnd)
			return 0
		case idKeyCancel:
			keyDialogResultOK = false
			pDestroyWindow.Call(hwnd)
			return 0
		}
	case WM_KEYDOWN:
		switch wparam {
		case VK_ESCAPE:
			keyDialogResultOK = false
			pDestroyWindow.Call(hwnd)
			return 0
		case VK_RETURN:
			keyDialogCollectResult(hwnd)
			pDestroyWindow.Call(hwnd)
			return 0
		}
	case WM_CLOSE:
		keyDialogResultOK = false
		pDestroyWindow.Call(hwnd)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// keyDialogCollectResult 读 edit + checkbox 内容写进 globals。
func keyDialogCollectResult(hwnd uintptr) {
	const bufSize = 1024
	buf := make([]uint16, bufSize)
	editHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyEdit)
	pSendMessageW.Call(editHwnd, WM_GETTEXT, bufSize, uintptr(unsafe.Pointer(&buf[0])))
	length := 0
	for i, c := range buf {
		if c == 0 {
			length = i
			break
		}
		length = i + 1
	}
	keyDialogResultKey = string(utf16ToRunes(buf[:length]))

	cbHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeySave)
	checked, _, _ := pSendMessageW.Call(cbHwnd, BM_GETCHECK, 0, 0)
	keyDialogResultSave = checked == BST_CHECKED

	keyDialogResultOK = true
}

// onKeyDialogCreate 建控件。
func onKeyDialogCreate(hwnd uintptr) {
	hInst, _, _ := pGetModuleHandleW.Call(0)

	// 1) label
	lblTxt, _ := Ptr("请输入 LLM API Key（首次运行）:")
	defer Hold(lblTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(lblTxt)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 12, 420, 20,
		hwnd, 0, hInst, 0,
	)

	// 2) password edit
	editTxt, _ := Ptr("")
	defer Hold(editTxt)
	pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(editTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|ES_PASSWORD|ES_AUTOHSCROLL,
		12, 40, 420, 26,
		hwnd, uintptr(idKeyEdit), hInst, 0,
	)

	// 3) checkbox
	cbTxt, _ := Ptr("保存到磁盘（明文写到 smith.key；U 盘给别人用请取消勾选）")
	defer Hold(cbTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(cbTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX,
		12, 78, 436, 22,
		hwnd, uintptr(idKeySave), hInst, 0,
	)

	// 4) OK button
	okTxt, _ := Ptr("确定")
	defer Hold(okTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(okTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON,
		220, 130, 96, 30,
		hwnd, uintptr(idKeyOKBtn), hInst, 0,
	)

	// 5) Cancel button
	cnTxt, _ := Ptr("取消")
	defer Hold(cnTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(cnTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		324, 130, 96, 30,
		hwnd, uintptr(idKeyCancel), hInst, 0,
	)

	// 焦点到 edit
	editHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyEdit)
	pSetFocus.Call(editHwnd)
}

// PromptAPIKey 弹首次运行 key 输入对话框，阻塞到用户 OK / Cancel。
//
// 参数：
//   - existingKey: 预填到密码框（如果 cfg 已经有 key，方便用户编辑）
//
// 返回：
//   - key:  用户输入的 key
//   - save: 是否要保存到磁盘（调用方负责落盘）
//   - ok:   true = 用户按 OK，false = 用户按 Cancel/Esc/关窗
//   - err:  致命错误
func PromptAPIKey(existingKey string) (key string, save bool, ok bool, err error) {
	if err := RegisterKeyDialogClass(); err != nil {
		return "", false, false, err
	}
	keyDialogResultKey = ""
	keyDialogResultSave = false
	keyDialogResultOK = false

	hInst, _, _ := pGetModuleHandleW.Call(0)

	// 屏幕居中
	sw, _, _ := pGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := pGetSystemMetrics.Call(1) // SM_CYSCREEN
	const dlgW, dlgH = 460, 200
	x := (int32(sw) - dlgW) / 2
	y := (int32(sh) - dlgH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	title, _ := Ptr("smith - API Key 设置（首次运行）")
	defer Hold(title)

	dlgHwnd, _, _ := pCreateWindowExW.Call(
		WS_EX_DLGMODALFRAME|WS_EX_TOPMOST,
		uintptr(unsafe.Pointer(keyDialogClassName16)),
		uintptr(unsafe.Pointer(title)),
		WS_CAPTION|WS_SYSMENU|WS_VISIBLE,
		uintptr(x), uintptr(y), dlgW, dlgH,
		0, 0, hInst, 0,
	)
	if dlgHwnd == 0 {
		return "", false, false, &keyDialogErr{"CreateWindowEx failed"}
	}

	// 强制 un-minimize（CW_USEDEFAULT 老坑）
	pShowWindow.Call(dlgHwnd, SW_RESTORE)

	// 预填 existing key
	if existingKey != "" {
		editHwnd, _, _ := pGetDlgItem.Call(dlgHwnd, idKeyEdit)
		fillBuf, _ := syscall.UTF16FromString(existingKey)
		if len(fillBuf) > 0 {
			pSendMessageW.Call(editHwnd, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&fillBuf[0])))
		}
	}

	// Sub message loop
	var msg struct {
		Hwnd     uintptr
		Message  uint32
		Wparam   uintptr
		Lparam   uintptr
		Time     uint32
		PtX      int32
		PtY      int32
		LPrivate uint32
	}
	for {
		ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		// 收到 dialog 自己被 destroy 就不发了
		if msg.Hwnd == dlgHwnd && msg.Message == WM_DESTROY {
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	// 清理：吃掉可能从 sub loop 漏出的 WM_QUIT（避免外层 win.Run 立刻退）
	var peek struct {
		Hwnd     uintptr
		Message  uint32
		Wparam   uintptr
		Lparam   uintptr
		Time     uint32
		PtX      int32
		PtY      int32
		LPrivate uint32
	}
	for {
		ret, _, _ := pPeekMessageW.Call(uintptr(unsafe.Pointer(&peek)), 0, WM_QUIT, WM_QUIT, PM_REMOVE)
		if ret == 0 {
			break
		}
	}

	return keyDialogResultKey, keyDialogResultSave, keyDialogResultOK, nil
}

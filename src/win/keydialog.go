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
	idKeyBaseURL = 1210
	idKeyModel   = 1211
	// 协议预设 radio（用户可手填 base/model 覆盖）
	idKeyRadioOpenAI    = 1221 // OpenAI Chat Completions 协议：POST {base}/chat/completions, Bearer auth
	idKeyRadioAnthropic = 1222 // Anthropic Messages 协议：POST {base}/v1/messages, x-api-key auth
)

// providerDefaultBase 返回 provider 默认的 base URL（用户可改）。
func providerDefaultBase(p string) string {
	switch p {
	case "anthropic":
		return "https://api.anthropic.com"
	default:
		return "https://api.openai.com/v1"
	}
}

// providerDefaultModel 返回 provider 默认的 model（用户可改）。
func providerDefaultModel(p string) string {
	switch p {
	case "anthropic":
		return "claude-3-5-sonnet-20241022"
	default:
		return "gpt-4"
	}
}

// WM_QUIT 是消息循环终止信号。
const WM_QUIT = 0x0012

// PM_REMOVE for PeekMessage.
const PM_REMOVE = 0x0001

// BS_AUTORADIOBUTTON + 第一组 radio 用 WS_GROUP 标记
const (
	BS_AUTORADIOBUTTON = 0x0009
	BS_GROUPBOX        = 0x0007
	WS_GROUP           = 0x00020000
)

// keydialog 状态（package-level global，sub loop 期间读写）。
// 每次 PromptAPIKey 入口 reset。
var (
	keyDialogClassReg    bool
	keyDialogClassName16 *uint16
	keyDialogResultKey   string
	keyDialogResultSave  bool
	keyDialogResultOK    bool
	keyDialogResultProvider string // "openai" / "anthropic"
	keyDialogResultBaseURL  string
	keyDialogResultModel    string

	// radio 切换 → 改 base URL / model 用
	keyDialogHwnd        uintptr
	keyDialogBaseURLHwnd uintptr
	keyDialogModelHwnd    uintptr
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
		// HIWORD(wparam) = notification (0 = ??); LOWORD(wparam) = control id
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
		case idKeyRadioOpenAI, idKeyRadioAnthropic:
			// radio 切换 → 自动填 base URL + model
			var prov string
			switch ctrlID {
			case idKeyRadioOpenAI:
				prov = "openai"
			case idKeyRadioAnthropic:
				prov = "anthropic"
			}
			if keyDialogBaseURLHwnd != 0 {
				fillBuf, _ := syscall.UTF16FromString(providerDefaultBase(prov))
				if len(fillBuf) > 0 {
					pSendMessageW.Call(keyDialogBaseURLHwnd, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&fillBuf[0])))
				}
			}
			if keyDialogModelHwnd != 0 {
				fillBuf, _ := syscall.UTF16FromString(providerDefaultModel(prov))
				if len(fillBuf) > 0 {
					pSendMessageW.Call(keyDialogModelHwnd, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&fillBuf[0])))
				}
			}
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

// keyDialogCollectResult 读 edit + checkbox + radios 写进 globals。
func keyDialogCollectResult(hwnd uintptr) {
	const bufSize = 1024
	buf := make([]uint16, bufSize)

	// Key
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

	// Save to disk
	cbHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeySave)
	checked, _, _ := pSendMessageW.Call(cbHwnd, BM_GETCHECK, 0, 0)
	keyDialogResultSave = checked == BST_CHECKED

	// Base URL
	bufBase := make([]uint16, bufSize)
	baseHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyBaseURL)
	pSendMessageW.Call(baseHwnd, WM_GETTEXT, bufSize, uintptr(unsafe.Pointer(&bufBase[0])))
	lengthBase := 0
	for i, c := range bufBase {
		if c == 0 {
			lengthBase = i
			break
		}
		lengthBase = i + 1
	}
	keyDialogResultBaseURL = string(utf16ToRunes(bufBase[:lengthBase]))

	// Model
	bufModel := make([]uint16, bufSize)
	modelHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyModel)
	pSendMessageW.Call(modelHwnd, WM_GETTEXT, bufSize, uintptr(unsafe.Pointer(&bufModel[0])))
	lengthModel := 0
	for i, c := range bufModel {
		if c == 0 {
			lengthModel = i
			break
		}
		lengthModel = i + 1
	}
	keyDialogResultModel = string(utf16ToRunes(bufModel[:lengthModel]))

	// Provider (看哪个 radio 选中)
	keyDialogResultProvider = "openai" // 默认
	openAIHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyRadioOpenAI)
	openAIChecked, _, _ := pSendMessageW.Call(openAIHwnd, BM_GETCHECK, 0, 0)
	if openAIChecked == BST_CHECKED {
		keyDialogResultProvider = "openai"
	} else {
		antHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyRadioAnthropic)
		antChecked, _, _ := pSendMessageW.Call(antHwnd, BM_GETCHECK, 0, 0)
		if antChecked == BST_CHECKED {
			keyDialogResultProvider = "anthropic"
		}
	}

	keyDialogResultOK = true
}

// onKeyDialogCreate 建控件。
// 布局 (520x250):
//   y=10:  label "协议预设:" + 2 radio (一行: OpenAI 格式 / Anthropic 格式)
//   y=44:  label "Base URL:" + edit  (用户可手填覆盖)
//   y=72:  label "Model:" + edit    (用户可手填覆盖)
//   y=100: hint "OpenAI 格式: POST {base}/chat/completions, Bearer auth;  Anthropic 格式: POST {base}/v1/messages, x-api-key auth"
//   y=122: label "API Key:" + password edit
//   y=158: checkbox "保存到磁盘"
//   y=200: OK / Cancel buttons
func onKeyDialogCreate(hwnd uintptr) {
	hInst, _, _ := pGetModuleHandleW.Call(0)

	// 1) 协议预设 label
	lblProv, _ := Ptr("协议预设:")
	defer Hold(lblProv)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(lblProv)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 14, 90, 18,
		hwnd, 0, hInst, 0,
	)

	// 1a) 2 radios（OpenAI 格式 / Anthropic 格式）
	mkRadio := func(text string, id int, x, y, w, h uintptr, checked bool) uintptr {
		s, _ := Ptr(text)
		defer Hold(s)
		styles := uint32(WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_AUTORADIOBUTTON)
		if checked {
			styles |= WS_GROUP // 第一个 radio 加 WS_GROUP 分组
		}
		hCtrl, _, _ := pCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(buttonClass)),
			uintptr(unsafe.Pointer(s)),
			uintptr(styles),
			x, y, w, h,
			hwnd, uintptr(id), hInst, 0,
		)
		if checked {
			pSendMessageW.Call(hCtrl, 0x00F1, 1, 0) // BM_SETCHECK = 0x00F1, BST_CHECKED = 1
		}
		return hCtrl
	}
	mkRadio("OpenAI 格式", idKeyRadioOpenAI, 110, 12, 110, 22, true)
	mkRadio("Anthropic 格式", idKeyRadioAnthropic, 230, 12, 130, 22, false)

	// 2) Base URL label
	lblURL, _ := Ptr("Base URL:")
	defer Hold(lblURL)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(lblURL)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 46, 90, 18,
		hwnd, 0, hInst, 0,
	)

	// 2a) Base URL edit
	urlTxt, _ := Ptr(providerDefaultBase("openai"))
	defer Hold(urlTxt)
	urlHwnd, _, _ := pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(urlTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|ES_AUTOHSCROLL,
		110, 44, 396, 24,
		hwnd, uintptr(idKeyBaseURL), hInst, 0,
	)
	keyDialogBaseURLHwnd = urlHwnd

	// 2b) Model label
	lblModel, _ := Ptr("Model:")
	defer Hold(lblModel)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(lblModel)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 74, 90, 18,
		hwnd, 0, hInst, 0,
	)

	// 2c) Model edit
	modelTxt, _ := Ptr(providerDefaultModel("openai"))
	defer Hold(modelTxt)
	modelHwnd, _, _ := pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(modelTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|ES_AUTOHSCROLL,
		110, 72, 396, 24,
		hwnd, uintptr(idKeyModel), hInst, 0,
	)
	keyDialogModelHwnd = modelHwnd

	// 2d) 小字提示
	hint1, _ := Ptr("OpenAI 格式: POST {base}/chat/completions (Bearer auth)    Anthropic 格式: POST {base}/v1/messages (x-api-key auth)")
	defer Hold(hint1)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(hint1)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 100, 496, 18,
		hwnd, 0, hInst, 0,
	)

	// 3) API Key label
	lblKey, _ := Ptr("API Key:")
	defer Hold(lblKey)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(stringClass)),
		uintptr(unsafe.Pointer(lblKey)),
		WS_CHILD|WS_VISIBLE|SS_LEFT,
		12, 126, 90, 18,
		hwnd, 0, hInst, 0,
	)

	// 3a) password edit
	editTxt, _ := Ptr("")
	defer Hold(editTxt)
	pCreateWindowExW.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(editTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|ES_PASSWORD|ES_AUTOHSCROLL,
		110, 124, 396, 26,
		hwnd, uintptr(idKeyEdit), hInst, 0,
	)

	// 4) Save checkbox
	cbTxt, _ := Ptr("保存到磁盘（写 smith.ini + smith.key；U 盘给别人用请取消勾选）")
	defer Hold(cbTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(cbTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX,
		12, 164, 496, 22,
		hwnd, uintptr(idKeySave), hInst, 0,
	)

	// 5) OK button
	okTxt, _ := Ptr("确定")
	defer Hold(okTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(okTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON,
		288, 200, 96, 30,
		hwnd, uintptr(idKeyOKBtn), hInst, 0,
	)

	// 6) Cancel button
	cnTxt, _ := Ptr("取消")
	defer Hold(cnTxt)
	pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(cnTxt)),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		394, 200, 96, 30,
		hwnd, uintptr(idKeyCancel), hInst, 0,
	)

	// 焦点到 key edit
	editHwnd, _, _ := pGetDlgItem.Call(hwnd, idKeyEdit)
	pSetFocus.Call(editHwnd)
}

// PromptAPIKey 弹首次运行 key 输入对话框，阻塞到用户 OK / Cancel。
//
// 参数：
//   - existingKey:   预填到密码框（如果 cfg 已经有 key，方便用户编辑）
//   - existingProv:  预选协议预设 radio（"openai" / "anthropic"）
//   - existingURL:   预填 base URL
//   - existingModel: 预填 model 名
//
// 返回：
//   - key:      用户输入的 key
//   - provider: 选择的协议预设（"openai" / "anthropic"）
//   - baseURL:  用户输入或自动填的 base URL
//   - model:    用户输入或自动填的 model 名
//   - save:     是否要保存到磁盘（调用方负责落盘）
//   - ok:       true = 用户按 OK，false = 用户按 Cancel/Esc/关窗
//   - err:      致命错误
func PromptAPIKey(existingKey, existingProv, existingURL, existingModel string) (key, provider, baseURL, model string, save bool, ok bool, err error) {
	if err := RegisterKeyDialogClass(); err != nil {
		return "", "", "", "", false, false, err
	}
	keyDialogResultKey = ""
	keyDialogResultSave = false
	keyDialogResultOK = false
	keyDialogResultProvider = ""
	keyDialogResultBaseURL = ""
	keyDialogResultModel = ""
	keyDialogHwnd = 0
	keyDialogBaseURLHwnd = 0
	keyDialogModelHwnd = 0

	hInst, _, _ := pGetModuleHandleW.Call(0)

	// 屏幕居中
	sw, _, _ := pGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := pGetSystemMetrics.Call(1) // SM_CYSCREEN
	const dlgW, dlgH = 520, 250
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
	keyDialogHwnd = dlgHwnd
	if dlgHwnd == 0 {
		return "", "", "", "", false, false, &keyDialogErr{"CreateWindowEx failed"}
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

	// 预选 provider radio（先 radio 后填 URL/model — radio change 会自动写）
	// 老 ini 的 "deepseek" 映射到 "openai"（同协议，只是 base URL 不同）
	preProv := existingProv
	if preProv == "" || preProv == "deepseek" {
		preProv = "openai"
	}
	var provID int
	switch preProv {
	case "anthropic":
		provID = idKeyRadioAnthropic
	default:
		provID = idKeyRadioOpenAI
	}
	provHwnd, _, _ := pGetDlgItem.Call(dlgHwnd, uintptr(provID))
	if provHwnd != 0 {
		pSendMessageW.Call(provHwnd, 0x00F1, 1, 0) // BM_SETCHECK
	}

	// 预填 base URL（existingURL 优先，否则 provider default）
	preURL := existingURL
	if preURL == "" {
		preURL = providerDefaultBase(preProv)
	}
	if keyDialogBaseURLHwnd != 0 {
		fillURL, _ := syscall.UTF16FromString(preURL)
		if len(fillURL) > 0 {
			pSendMessageW.Call(keyDialogBaseURLHwnd, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&fillURL[0])))
		}
	}

	// 预填 model（existingModel 优先，否则 provider default）
	preModel := existingModel
	if preModel == "" {
		preModel = providerDefaultModel(preProv)
	}
	if keyDialogModelHwnd != 0 {
		fillModel, _ := syscall.UTF16FromString(preModel)
		if len(fillModel) > 0 {
			pSendMessageW.Call(keyDialogModelHwnd, WM_SETTEXT, 0, uintptr(unsafe.Pointer(&fillModel[0])))
		}
	}

	// Sub message loop
	//
	// 重要：DestroyWindow **send** WM_DESTROY 给 WndProc 时是**直接 send**（绕过 message
	// queue），MSDN: "sends a WM_DESTROY message directly to the window procedure,
	// bypassing the message queue"。所以不能用 "等到 WM_DESTROY" 作为退出条件——
	// 等不到，会死锁在 GetMessage。
	//
	// 正确做法：每次 GetMessage 前后用 IsWindow(hwnd) 检查——窗口已销毁就 break。
	// 配合 DestroyWindow 同步清理子窗口 + 我们自己的 pDestroyWindow(dlgHwnd) 调用
	// 流程，dialog destroy 后下一轮循环时 IsWindow == 0，sub loop 立即退出。
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
		// 退出条件 1: dialog 窗口已不存在（DestroyWindow 完成后）
		isWin, _, _ := pIsWindow.Call(dlgHwnd)
		if isWin == 0 {
			break
		}
		ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		// 退出条件 2: 收到 WM_QUIT（极端情况，比如外层 win.Run 同时跑了——不会
		// 在 PromptAPIKey 调用期间发生，但保险）
		if msg.Message == WM_QUIT {
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

	return keyDialogResultKey, keyDialogResultProvider, keyDialogResultBaseURL, keyDialogResultModel, keyDialogResultSave, keyDialogResultOK, nil
}

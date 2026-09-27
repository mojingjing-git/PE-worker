//go:build windows

// Package win: consts_test.go —— Win32 常量正确性门禁。
//
// 为什么需要这个文件（docs/11 §S6-4）：
//
//	本项目已经栽过一次 —— WM_TIMER 被写成 0x0118（真值 0x0113，0x0118 是
//	WM_SWITCHWINDOW）。它没有让程序崩溃，只是让 case 永远不触发，于是
//	"1Hz 状态栏心跳"一写就不工作。这类错误编译能过、vet 报不出来、
//	现有测试也覆盖不到，只能靠人眼 —— 人眼不可靠。
//
//	所以把每个常量钉死在这里。任何一个被改错，`go test ./src/win/...` 立刻红。
//
// 权威来源（Windows SDK 10.0.22621.0）：
//
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\winuser.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\richedit.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\wingdi.h
//
// EM_ 系列的值已在本机桌面实测复核（RichEdit20W 控件 + 定点验证，
// 设 yHeight=0/100/200 三次读回全部一致），不是照抄头文件。
package win

import "testing"

// TestWin32Constants 把本包定义的 Win32 常量与 Windows SDK 头文件核对。
//
// 表驱动，每行给出：常量名、实际值、权威值、来源。
// 改错任何一个常量都会在这里失败 —— 这是本项目防"静默失效"的第一道闸。
//
// 覆盖范围：src/win/ 下**全部** Win32 常量（msgs.go + keydialog.go 的 const 块）。
// 新增常量时必须在这里补一行，否则就是门禁漏网 —— 审计正是靠"全包 const 定义
// 与本表机械 diff"找出过 7 个漏网常量（其中 WS_OVERLAPPEDWINDOW 是 gui.go
// 唯一用到的窗口样式，写错只会改窗口外观、不报错，最典型的静默失效）。
func TestWin32Constants(t *testing.T) {
	cases := []struct {
		name string
		got  uint64
		want uint64
		src  string
	}{
		// ---- 基础窗口消息（winuser.h:1979-2006, 2173, 2197, 2199, 2467-2470）----
		{"WM_CREATE", WM_CREATE, 0x0001, "winuser.h:1979"},
		{"WM_DESTROY", WM_DESTROY, 0x0002, "winuser.h:1980"},
		{"WM_SIZE", WM_SIZE, 0x0005, "winuser.h:1982"},
		{"WM_SETTEXT", WM_SETTEXT, 0x000C, "winuser.h:1996"},
		{"WM_GETTEXT", WM_GETTEXT, 0x000D, "winuser.h:1997"},
		{"WM_GETTEXTLENGTH", WM_GETTEXTLENGTH, 0x000E, "winuser.h:1998"},
		{"WM_PAINT", WM_PAINT, 0x000F, "winuser.h:1999"},
		{"WM_CLOSE", WM_CLOSE, 0x0010, "winuser.h:2000"},
		{"WM_QUIT", WM_QUIT, 0x0012, "winuser.h:2006"},
		{"WM_SETFONT", WM_SETFONT, 0x0030, "winuser.h:2057"},
		{"WM_KEYDOWN", WM_KEYDOWN, 0x0100, "winuser.h:2173"},
		{"WM_COMMAND", WM_COMMAND, 0x0111, "winuser.h:2197"},

		// ⚠️ 本门禁的正主：历史上被写成 0x0118（= WM_SWITCHWINDOW）
		{"WM_TIMER", WM_TIMER, 0x0113, "winuser.h:2199"},

		{"WM_CUT", WM_CUT, 0x0300, "winuser.h:2467"},
		{"WM_COPY", WM_COPY, 0x0301, "winuser.h:2468"},
		{"WM_PASTE", WM_PASTE, 0x0302, "winuser.h:2469"},
		{"WM_CLEAR", WM_CLEAR, 0x0303, "winuser.h:2470"},

		// ---- 消息循环（keydialog.go:54-57）----
		{"WM_QUIT(dialog)", WM_QUIT, 0x0012, "winuser.h:2006"},
		{"PM_REMOVE", PM_REMOVE, 0x0001, "winuser.h:3534"},

		// ---- keydialog 的 radio / groupbox 样式（keydialog.go:60-64）----
		// 漏网过的 7 个常量之一。BS_AUTORADIOBUTTON=0x09 是第 9 号按钮类，
		// 与 BS_AUTOCHECKBOX=0x03 只差一位 —— 写错不崩，只是 radio 变 checkbox。
		{"BS_AUTORADIOBUTTON", BS_AUTORADIOBUTTON, 0x00000009, "winuser.h:11334"},
		{"BS_GROUPBOX", BS_GROUPBOX, 0x00000007, "winuser.h:11332"},
		{"WS_GROUP", WS_GROUP, 0x00020000, "winuser.h:2805"},

		// ---- EDIT / RichEdit 消息 ----
		// EM_ 在 0xB0 段（winuser.h:11252+），不是 WM_USER 段。
		{"EM_SETSEL", EM_SETSEL, 0x00B1, "winuser.h:11253"},
		{"EM_REPLACESEL", EM_REPLACESEL, 0x00C2, "winuser.h:11268"},
		// ⚠️ 另一个历史上的错误来源：spike/gui 写的是 0x0115（= WM_VSCROLL），
		// 产品代码的 0x00B7 才是对的（winuser.h:11259）。**别去"对齐 spike"改错。**
		{"EM_SCROLLCARET", EM_SCROLLCARET, 0x00B7, "winuser.h:11259"},
		// EM_SETCHARFORMAT = WM_USER+68（richedit.h:115）。本机桌面实测：
		// 0x0444 有效（yHeight 定点验证 0/100/200 三次读回一致），
		// WM_USER+44 (0x042C) 无效。普通 EDIT 控件会忽略此消息。
		{"EM_SETCHARFORMAT", EM_SETCHARFORMAT, 0x0444, "richedit.h:115 (WM_USER+68)"},

		// ---- CHARFORMAT 相关（richedit.h:976, wingdi.h）----
		{"SCF_SELECTION", SCF_SELECTION, 0x0001, "richedit.h:976"},
		{"CFM_SIZE", CFM_SIZE, 0x80000000, "CHARFORMATW.dwMask"},

		// ---- EDIT 样式（winuser.h:11190-11198）----
		{"ES_MULTILINE", ES_MULTILINE, 0x0004, "winuser.h:11190"},
		{"ES_PASSWORD", ES_PASSWORD, 0x0020, "winuser.h:11193"},
		{"ES_AUTOVSCROLL", ES_AUTOVSCROLL, 0x0040, "winuser.h:11194"},
		{"ES_AUTOHSCROLL", ES_AUTOHSCROLL, 0x0080, "winuser.h:11195"},
		{"ES_READONLY", ES_READONLY, 0x0800, "winuser.h:11198"},

		// ---- 窗口样式（winuser.h:2789-2845）----
		// WS_OVERLAPPEDWINDOW 是组合宏，头文件里由 5 项 | 组成：
		//   WS_CAPTION 0x00C00000 | WS_SYSMENU 0x00080000 | WS_THICKFRAME
		//   0x00040000 | WS_MINIMIZEBOX 0x00020000 | WS_MAXIMIZEBOX 0x00010000
		//   = 0x00CF0000
		// 漏网过的 7 个常量之一，而且是 gui.go 唯一用到的窗口样式 ——
		// 写错不崩、不报错，只改窗口外观，属于最难查的那一类。
		{"WS_OVERLAPPEDWINDOW", WS_OVERLAPPEDWINDOW, 0x00CF0000, "winuser.h:2820 (combo)"},
		{"WS_OVERLAPPED", WS_OVERLAPPED, 0x00000000, "winuser.h:2789"},
		{"WS_CHILD", WS_CHILD, 0x40000000, "winuser.h:2791"},
		{"WS_VISIBLE", WS_VISIBLE, 0x10000000, "winuser.h:2793"},
		{"WS_CAPTION", WS_CAPTION, 0x00C00000, "winuser.h:2798"},
		{"WS_VSCROLL", WS_VSCROLL, 0x00200000, "winuser.h:2801"},
		{"WS_SYSMENU", WS_SYSMENU, 0x00080000, "winuser.h:2803"},
		{"WS_TABSTOP", WS_TABSTOP, 0x00010000, "winuser.h:2806"},
		{"WS_EX_DLGMODALFRAME", WS_EX_DLGMODALFRAME, 0x00000001, "winuser.h:2836"},
		{"WS_EX_TOPMOST", WS_EX_TOPMOST, 0x00000008, "winuser.h:2838"},
		{"WS_EX_CLIENTEDGE", WS_EX_CLIENTEDGE, 0x00000200, "winuser.h:2845"},

		// ---- 按钮（winuser.h:11325-11392）----
		{"BS_PUSHBUTTON", BS_PUSHBUTTON, 0x00000000, "winuser.h:11325"},
		{"BS_DEFPUSHBUTTON", BS_DEFPUSHBUTTON, 0x00000001, "winuser.h:11326"},
		{"BS_AUTOCHECKBOX", BS_AUTOCHECKBOX, 0x00000003, "winuser.h:11328"},
		{"BM_GETCHECK", BM_GETCHECK, 0x00F0, "winuser.h:11376"},
		{"BM_SETCHECK", BM_SETCHECK, 0x00F1, "winuser.h:11377"},
		{"BST_UNCHECKED", BST_UNCHECKED, 0x0000, "winuser.h:11391"},
		{"BST_CHECKED", BST_CHECKED, 0x0001, "winuser.h:11392"},
		{"SS_LEFT", SS_LEFT, 0x00000000, "winuser.h:11401"},

		// ---- ShowWindow / SetWindowPos / SPI（winuser.h:394, 404, 4868-4874, 12424）----
		{"SW_SHOWNORMAL", SW_SHOWNORMAL, 1, "winuser.h:394"},
		{"SW_RESTORE", SW_RESTORE, 9, "winuser.h:404"},
		{"SWP_NOSIZE", SWP_NOSIZE, 0x0001, "winuser.h:4868"},
		{"SWP_NOMOVE", SWP_NOMOVE, 0x0002, "winuser.h:4869"},
		{"SWP_NOZORDER", SWP_NOZORDER, 0x0004, "winuser.h:4870"},
		{"SWP_NOACTIVATE", SWP_NOACTIVATE, 0x0010, "winuser.h:4872"},
		{"SWP_SHOWWINDOW", SWP_SHOWWINDOW, 0x0040, "winuser.h:4874"},
		{"SPI_GETWORKAREA", SPI_GETWORKAREA, 0x0030, "winuser.h:12424"},

		// ---- 杂项 ----
		{"IDOK", IDOK, 1, "dialog IDs"},
		{"IDCANCEL", IDCANCEL, 2, "dialog IDs"},
		{"VK_RETURN", VK_RETURN, 0x0D, "winuser.h:486"},
		{"VK_ESCAPE", VK_ESCAPE, 0x1B, "winuser.h:508"},
		{"IDC_ARROW", IDC_ARROW, 32512, "winuser.h:10638"},
		{"DEFAULT_GUI_FONT", DEFAULT_GUI_FONT, 17, "wingdi.h:1929"},

		// ⚠️ 历史上写成 ^uintptr(0) = 0xFFFFFFFF。真值是 (int)0x80000000
		// (winuser.h:4404)，386 上 ^uintptr(0) 会被读成 -1 而非 INT_MIN。
		{"CW_USEDEFAULT", CW_USEDEFAULT, 0x80000000, "winuser.h:4404"},

		// ⚠️ 历史上 keydialog.go 硬编码 16+1=17（= COLOR_BTNSHADOW 的刷子）。
		// brush 句柄 = COLOR_x + 1，COLOR_BTNFACE=15，所以是 16。
		// 注意 COLOR_BTNFACE 定义在 WinUser.h 而不是 wingdi.h（审计订正）。
		{"COLOR_BTNFACE_BRUSH", COLOR_BTNFACE_BRUSH, 16, "WinUser.h:9592 COLOR_BTNFACE+1"},
	}

	// 覆盖度自检：常量数对不上时提醒补断言。
	// 门禁最大的风险不是某条值写错，而是"新增了常量却忘了加断言"——
	// 那道缝会一直敞着，且没有任何东西会提醒你。
	//
	// 维护约定：src/win/ 新增或删除 Win32 常量时，同步更新这个数字。
	// 当前覆盖 68 条（msgs.go + keydialog.go 的全部 Win32 常量）。
	const expectedCount = 68
	if len(cases) != expectedCount {
		t.Errorf("常量门禁覆盖了 %d 条，期望 %d 条。"+
			"若确实新增/删除了常量，请同步改 expectedCount；若没改，说明有新常量漏加断言",
			len(cases), expectedCount)
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = 0x%X, want 0x%X  (权威: %s)",
				c.name, c.got, c.want, c.src)
		}
	}
}

// TestWM_TIMERNotSwitchWindow 单独钉死 WM_TIMER 那个坑。
//
// 0x0118 是 WM_SWITCHWINDOW。如果 WM_TIMER 再次被写成 0x0118，
// Alt+Tab 切窗口会走进 case WM_TIMER 分支且不调 DefWindowProc。
func TestWM_TIMERNotSwitchWindow(t *testing.T) {
	if WM_TIMER == 0x0118 {
		t.Fatalf("WM_TIMER 被写成 0x0118 —— 那是 WM_SWITCHWINDOW，不是 WM_TIMER（真值 0x0113）")
	}
	if WM_TIMER != 0x0113 {
		t.Fatalf("WM_TIMER = 0x%X, want 0x0113", WM_TIMER)
	}
}

// TestEMSetCharFormatIsRichEditOnly 记录 EM_SETCHARFORMAT 的真实行为约束。
//
// 这不是常量断言而是**行为契约**：0x0444 是 RichEdit 2.0 的消息，
// 普通 EDIT 控件会忽略它（返回 0）且不产生任何副作用。
// 当前 gui.go 的日志区是普通 EDIT，所以 think 块染色是 no-op
// （docs/11 §S7-10）。本测试把这个事实固定下来 ——
// 等 S7-10 把日志区换成 RichEdit20W 时，这个测试提醒同步更新注释。
func TestEMSetCharFormatIsRichEditOnly(t *testing.T) {
	if EM_SETCHARFORMAT != 0x0444 {
		t.Fatalf("EM_SETCHARFORMAT = 0x%X, want 0x0444 (WM_USER+68, richedit.h:115)。"+
			"注意 WM_USER+44 (0x042C) 是无效消息号", EM_SETCHARFORMAT)
	}
	// 记录当前宿主控件：gui.go 用 "EDIT"（普通控件），该消息对它无效。
	// 一旦改成 "RichEdit20W"，请同步更新本注释与 docs/11 §S7-10。
	t.Log("EM_SETCHARFORMAT=0x0444 是 RichEdit 专有；普通 EDIT 控件忽略它。" +
		"gui.go 当前日志区 class = EDIT，think 染色因此是 no-op（见 docs/11 §S7-10）")
}

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
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\winnt.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\winbase.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\TlHelp32.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\um\WinNls.h
//	C:\Program Files (x86)\Windows Kits\10\Include\10.0.22621.0\shared\winerror.h
//
// EM_SETCHARFORMAT 的值已在本机桌面实测复核（RichEdit20W 控件 + 定点验证，
// 设 yHeight=0/100/200 三次读回全部一致），不是照抄头文件。
//
// 门禁是**两条腿**，缺一条就是自证：
//   - 本文件 TestWin32Constants：断言表里的**值**对不对 SDK
//   - consts_scan_test.go TestWin32ConstsNoUnasserted：包内的常量**有没有**进表
//
// 第三类（项目自定义编号，如 4 个 WM_APP 消息号）在 win32ConstCases 之外，
// 由本文件底部的 TestCustomMessageIDs 单独钉 —— 见那里的注释。
package win

import "testing"

// win32ConstCases 是 C1 门禁的**值**表：表里每一行都是一个 Win32 常量的
// 权威值与出处。改错任何一个常量都会在 TestWin32Constants 里失败。
//
// 提升为包级变量（原来是 TestWin32Constants 的局部变量）是为了让
// consts_scan_test.go 的 assertedConstNames() 能读它 —— 覆盖度由
// TestWin32ConstsNoUnasserted 的**真 diff** 负责，不再由行数自检负责。
//
// ⚠️ 维护约定：src/win/ 新增 Win32 常量时，**同一个 commit** 里补一行。
type win32ConstCase struct {
	name string
	got  uint64
	want uint64
	src  string
}

var win32ConstCases = []win32ConstCase{
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
	// ⚠️ 这里原来还有一行 {"WM_QUIT(dialog)", ...}，是上方 WM_QUIT 的重复断言
	// （同一个符号、同一个值、同一个出处），只因为想标注"keydialog 也用它"。
	// 重复行会占掉 expectedCount 的名额，而新门禁的 stale 检查还会把
	// "WM_QUIT(dialog)" 当成一个不存在的符号报红。标注信息改为注释。
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

	// =====================================================================
	// 以下 28 条是 docs/13 Task B1 的 AST 真 diff 门禁（consts_scan_test.go）
	// 扫出来的"已定义但从未被断言"的常量。旧门禁只有 expectedCount=68 的
	// 行数自检，从不与包内实际 const 声明比对，所以这 28 条一直漏着。
	// 每条的值与出处已在本机 SDK 10.0.22621.0 逐条 grep 复核。
	// =====================================================================

	// ---- msgs.go: RichEdit 文本上限（richedit.h）----
	// ⚠️ 这里有**两个**值得记的坑：
	//   EM_EXLIMITTEXT  正确 = richedit.h:100 (WM_USER+53) = 0x0435
	//   EM_GETLIMITTEXT 正确 = richedit.h:86  (WM_USER+37) = 0x0425
	// 0x0437 = richedit.h:102 的 EM_EXSETSEL (WM_USER+55)，**不是**任何 get 消息。
	// 陷阱来源：winuser.h:11287 也有一个 #define EM_GETLIMITTEXT = 0x00D5
	//（那是 win32k 旧 RichEdit 的，与 richedit.h 同名不同值，很容易抄错那一个）。
	// 权威：Windows SDK 10.0.22621.0 um/richedit.h。
	{"EM_EXLIMITTEXT", EM_EXLIMITTEXT, 0x0435, "richedit.h:100 (WM_USER+53)"},
	{"EM_GETLIMITTEXT", EM_GETLIMITTEXT, 0x0425, "richedit.h:86 (WM_USER+37)"},

	// ---- job.go: Job Object 限制（winnt.h）----
	{"jobObjectLimitKillOnJobClose", jobObjectLimitKillOnJobClose, 0x00002000, "winnt.h:13228 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE"},

	// ---- proc.go: 进程访问权限 / 快照 / 创建标志 ----
	{"th32csSnapProcess", th32csSnapProcess, 0x00000002, "TlHelp32.h:54 TH32CS_SNAPPROCESS"},
	{"errorNoMoreFiles", errorNoMoreFiles, 18, "winerror.h:392 ERROR_NO_MORE_FILES"},
	{"processTerminate", processTerminate, 0x0001, "winnt.h:12200 PROCESS_TERMINATE"},
	{"processQueryLimited", processQueryLimited, 0x1000, "winnt.h:12212 PROCESS_QUERY_LIMITED_INFORMATION"},
	{"createSuspended", createSuspended, 0x00000004, "winbase.h:652 CREATE_SUSPENDED"},
	{"createBreakawayFromJob", createBreakawayFromJob, 0x01000000, "winbase.h:679 CREATE_BREAKAWAY_FROM_JOB"},
	{"createNoWindow", createNoWindow, 0x08000000, "winbase.h:682 CREATE_NO_WINDOW"},

	// ---- jobexec.go: STARTUPINFO / 句柄标志 / 等待超时（winbase.h, winuser.h）----
	{"startfUseStdHandles", startfUseStdHandles, 0x00000100, "winbase.h:3113 STARTF_USESTDHANDLES"},
	{"startfUseShowWindow", startfUseShowWindow, 0x00000001, "winbase.h:3105 STARTF_USESHOWWINDOW"},
	{"waitInfinite", waitInfinite, 0xFFFFFFFF, "winbase.h:822 INFINITE"},
	{"handleFlagInherit", handleFlagInherit, 0x00000001, "winbase.h:2068 HANDLE_FLAG_INHERIT"},
	{"swHide", swHide, 0, "winuser.h:393 SW_HIDE"},

	// ---- msgbox.go: MessageBox 样式（winuser.h:9101-9145）----
	{"MB_OK", MB_OK, 0x00000000, "winuser.h:9101"},
	{"MB_OKCANCEL", MB_OKCANCEL, 0x00000001, "winuser.h:9102"},
	{"MB_ABORTRETRYIGNORE", MB_ABORTRETRYIGNORE, 0x00000002, "winuser.h:9103"},
	{"MB_YESNOCANCEL", MB_YESNOCANCEL, 0x00000003, "winuser.h:9104"},
	{"MB_ICONHAND", MB_ICONHAND, 0x00000010, "winuser.h:9112"},
	{"MB_ICONQUESTION", MB_ICONQUESTION, 0x00000020, "winuser.h:9113"},
	{"MB_ICONEXCLAMATION", MB_ICONEXCLAMATION, 0x00000030, "winuser.h:9114"},
	// MB_ICONINFORMATION 在 SDK 里是 #define ... MB_ICONASTERISK（别名，不是字面量），
	// 值取 MB_ICONASTERISK —— 头文件别名跟着走，两个值相同。
	{"MB_ICONINFORMATION", MB_ICONINFORMATION, 0x00000040, "winuser.h:9123 (= MB_ICONASTERISK @9115)"},
	{"MB_TASKMODAL", MB_TASKMODAL, 0x00002000, "winuser.h:9135"},
	{"MB_SETFOREGROUND", MB_SETFOREGROUND, 0x00010000, "winuser.h:9141"},
	{"MB_TOPMOST", MB_TOPMOST, 0x00040000, "winuser.h:9145"},

	// ---- oem.go: 代码页（WinNls.h，不是 winnt.h）----
	{"cpOEMCP", cpOEMCP, 1, "WinNls.h:391 CP_OEMCP"},
	{"cpUTF8", cpUTF8, 65001, "WinNls.h:397 CP_UTF8"},
}

// assertedConstNames 返断言表里出现过的**全部**符号，供 consts_scan_test.go 的
// stale 检查用。
//
// ⚠️ 必须取**第一列**（c.name）—— 表结构是 {name, got, want, src}，第二列
// c.got 是 uint64 的**值**，不是符号名，取错列会得到一堆十进制数字。
//
// 表里不允许出现 `WM_QUIT(dialog)` 这类"符号名(备注)"的标注行：它会被当成
// 一个独立符号名，随后被 stale 检查报红。重复断言同一个符号请直接删掉重复行。
func assertedConstNames() map[string]bool {
	m := map[string]bool{}
	for _, c := range win32ConstCases {
		m[c.name] = true
	}
	return m
}

// TestWin32Constants 把本包定义的 Win32 常量与 Windows SDK 头文件核对。
//
// 表驱动，每行给出：常量名、实际值、权威值、来源。
// 改错任何一个常量都会在这里失败 —— 这是本项目防"静默失效"的第一道闸。
//
// 覆盖范围：`src/win/` 下全部 Win32 字面量常量（AST 扫描，见 consts_scan_test.go）；
// 非 Win32 的项目内部常量在 constsExempt 里显式豁免。
// 新增常量时必须在这里补一行，否则 TestWin32ConstsNoUnasserted 会红 ——
// 审计正是靠"全包 const 定义与本表机械 diff"找出过 7 个漏网常量
// （其中 WS_OVERLAPPEDWINDOW 是 gui.go 唯一用到的窗口样式，写错只会改
// 窗口外观、不报错，最典型的静默失效）。
func TestWin32Constants(t *testing.T) {
	for _, c := range win32ConstCases {
		if c.got != c.want {
			t.Errorf("%s = 0x%X, want 0x%X  (权威: %s)",
				c.name, c.got, c.want, c.src)
		}
	}
}

// TestCustomMessageIDs 钉死 4 个项目自定义 WM_APP 消息号（msgs.go:16-37）。
//
// 为什么它们**不在**上面那张 win32ConstCases 表里：那张表每一行的 src 都是
// 某个头文件里的 #define，读者据此能自己去核对；而这 4 个是**本项目自己约定的
// 编号**，任何 SDK 头文件里都查不到。混进那张表会让"对照 SDK 头文件"这个承诺
// 失真 —— 那正是本轮整改反复在修的"注释声称 A、实际是 B"。所以单开本测试，
// 并且必须在这里钉住它们，而不是靠 constsExempt 里的豁免蒙过去。
//
// ⚠️ 这 4 个曾经是"永久无断言区"（docs/13 复审 F3）：msgs.go 把它们写成
// `0x8000 + 100` 这类组合表达式，而当时的扫描器只收 *ast.BasicLit，于是
// 它们既不在扫描器的 defined 集合里、又没有一行值断言（src/test/e2e_test.go
// 里的 `var _ = win.WM_LOG_LINE` 是引用，不是断言）。
// 失败时序：有人把 WM_USER_INPUT 改成 `0x8000 + 102`（与 WM_AGENT_RESPONSE
// 撞号）→ go test 全绿 → gui.go 把用户按 Enter 投递进 response 分支 →
// worker 从不收输入、UI 不显示回复 → PE 上表现为"回车没反应"，无日志无崩溃。
//
// 三类断言缺一不可：
//
//	① 值断言    —— 0x8064~0x8067
//	② 互异性    —— 4 个两两不相等（撞号是上面那个失败时序的直接成因）
//	③ WM_APP 区间 —— 全部落在系统保留给 application 消息的 0x8000~0xBFFF；
//	                 往下越界会与 0x0001~0x7FFF 的系统消息撞号，往上越界会与
//	                 0xC000 起的 RegisterWindowMessage 私有注册区撞号
//
// 新增第 5 个自定义消息时，请把下面表里加一行 —— consts_scan_test.go 会因为
// 它不在 constsExempt 里而报红，但那份红只是"要你表态"，真正的值/互异性
// 断言在本文件。
func TestCustomMessageIDs(t *testing.T) {
	const (
		wmAppLo = 0x8000 // winuser.h 的 WM_APP，系统保留给 application 消息的区间下界
		wmAppHi = 0xBFFF // 同一区间的上界；0xC000 起是 RegisterWindowMessage 的私有注册区
	)
	cases := []struct {
		name string
		got  uint32
		want uint32 // WM_APP+ 偏移
	}{
		{"WM_LOG_LINE", WM_LOG_LINE, wmAppLo + 100},
		{"WM_USER_INPUT", WM_USER_INPUT, wmAppLo + 101},
		{"WM_AGENT_RESPONSE", WM_AGENT_RESPONSE, wmAppLo + 102},
		{"WM_USER_ABORT", WM_USER_ABORT, wmAppLo + 103},
	}

	// ② 互异性：撞号是静默失效的常见成因，必须单独断言。
	seen := map[uint32]string{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = 0x%X, want 0x%X (WM_APP+%d)", c.name, c.got, c.want, c.want-wmAppLo)
		}
		if c.got < wmAppLo || c.got > wmAppHi {
			t.Errorf("%s = 0x%X 落在 WM_APP 消息区 [0x%X, 0x%X] 之外："+
				"往下会与系统消息、往上会与 RegisterWindowMessage 注册区撞号"+
				"（gui.go / logx 都靠这个号投递）",
				c.name, c.got, wmAppLo, wmAppHi)
		}
		if other, dup := seen[c.got]; dup {
			t.Errorf("%s 与 %s 撞号（都是 0x%X）：gui.go 的 switch 会走错分支 —— "+
				"投递/处理对不上时表现为'回车没反应'，无日志无崩溃", c.name, other, c.got)
		}
		seen[c.got] = c.name
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

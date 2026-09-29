//go:build windows

// Package win: consts_scan_test.go —— C1 门禁的**真 diff** 部分。
//
// 为什么有这个文件（docs/13 Task B1）：
//
//	改之前的 consts_test.go 里有一行 `const expectedCount = 68`，它只比对
//	**表内行数**与硬编码的 68，**从不与包内实际的 const 声明做比对**。
//	机械比对的结果是：win/ 定义了 100+ 条字面量常量，门禁只断言 67 个符号，
//	而 `go test ./src/win/...` 是**绿的**。
//
//	也就是说 AGENTS.md §3 承诺的"新增常量漏加断言直接红"**是假的**。
//	`spike/richedit`、`spike/hello`、`src/win/msgs.go` 的 EM_GETLIMITTEXT
//	各有一个错常量潜伏了很久，根因都是同一个洞。
//
// 现在的门禁是两条腿：
//
//	TestWin32Constants  （consts_test.go）—— 断言表里的**值**对不对 SDK
//	TestWin32ConstsNoUnasserted（本文件）—— 包内的常量**有没有**进断言表
//
// 少了第二条腿，第一条就是"自证"：表里写什么就比什么，表外的东西永远没人看。
package win

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// constsExempt 是**非 Win32 SDK**的包内常量，列出理由以免被当成漏网。
// 新增常量若不在这张表里、也不在 TestWin32Constants 的断言表里 → 门禁红。
//
// ⚠️ 在这张表里 **≠ 没有断言**，只等于"不进 SDK 值表"这一件事。
// 豁免与断言是两件独立的事，各自都不充分：
//
//	豁免（本表）    —— 告诉扫描器"这个常量不需要对照 SDK 头文件"，并写下理由
//	值断言（consts_test.go）—— 真的把值/关系钉死，改错就红
//
// 4 个 WM_APP 消息号就是"两件都有"的情形：它们是本项目自己约定的编号，
// 任何 SDK 头文件里都查不到，所以在本表豁免；同时值断言 + 互异性 +
// WM_APP 区间断言在 consts_test.go 的 TestCustomMessageIDs。
// 早期版本这里只有豁免、又没有值断言，扫描器又因只收 *ast.BasicLit 而
// 看不见它们，于是形成"既无值断言、门禁也永远看不见"的永久无断言区 ——
// 撞号时全绿，PE 上表现为"回车没反应"，无日志无崩溃。
var constsExempt = map[string]string{
	// 项目自定义的 WM_APP 消息（msgs.go:16-37）：本项目约定的消息号，
	// 不是 SDK #define。值断言见 consts_test.go TestCustomMessageIDs。
	"WM_LOG_LINE":       "项目自定义 WM_APP 消息（值断言见 TestCustomMessageIDs）",
	"WM_USER_INPUT":     "项目自定义 WM_APP 消息（值断言见 TestCustomMessageIDs）",
	"WM_AGENT_RESPONSE": "项目自定义 WM_APP 消息（值断言见 TestCustomMessageIDs）",
	"WM_USER_ABORT":     "项目自定义 WM_APP 消息（值断言见 TestCustomMessageIDs）",
	// GUI 控件 ID 与布局参数（gui.go / keydialog.go），纯项目内部编号
	"idInput": "控件 ID", "idLog": "控件 ID", "idLogClear": "控件 ID", "idLogCopy": "控件 ID",
	"idLogSave": "控件 ID", "idSend": "控件 ID", "idStatus": "控件 ID", "idStop": "控件 ID",
	"idTimerQuit": "控件 ID", "idTimerTick": "控件 ID",
	"idKeyBaseURL": "控件 ID", "idKeyCancel": "控件 ID", "idKeyEdit": "控件 ID",
	"idKeyModel": "控件 ID", "idKeyOKBtn": "控件 ID", "idKeySave": "控件 ID",
	"idKeyRadioAnthropic": "控件 ID", "idKeyRadioOpenAI": "控件 ID",
	"logMaxChars": "布局参数", "logTruncateKeep": "布局参数", "tickIntervalMs": "布局参数",
	// Job 布局事实（job.go），由 job_test.go 的 TestJobExtLimitInfoSizes 单独钉，
	// 不是 Win32 常量而是 Go 侧的字节数
	"jobExtLimitInfoSizeX86": "布局事实，见 job_test.go", "jobExtLimitInfoSizeX64": "布局事实，见 job_test.go",
	"jobLimitFlagsOffset": "布局事实，见 job_test.go",
	// Go 侧哨兵（proc.go），不是 Win32 API 返回值
	"errnoNone": "Go 侧哨兵",
	// 组合表达式常量：msgs.go:163 `COLOR_BTNFACE_BRUSH = 15 + 1`。
	// 它的值已在 win32ConstCases 里断言（16），豁免只是说明"不是照抄某个
	// #define"；msgbox.go 的 MessageBoxFatalIcon 是同一类。
	"COLOR_BTNFACE_BRUSH": "组合表达式(15+1)，不是照抄 #define；值见 win32ConstCases",
	"MessageBoxFatalIcon": "组合表达式(MB_* 按位或)，各项值已在 win32ConstCases 断言",
}

// scanWin32Consts 扫本包非测试文件里所有**包级**常量，返回「名字 → file.go = 右值原文」。
//
// 口径（改动这个口径会让漏网常量重新溜过去，务必谨慎）：
//   - 只看顶层 f.Decls 里的 GenDecl，函数内的局部 const 不算
//     （局部 const 是实现细节，不是"需要跨文件核对的 SDK 值"）
//   - **不按右值形态过滤**：`0x8000 + 100`（组合表达式）、`15 + 1`、
//     `MB_ICONHAND | MB_SETFOREGROUND`（引用别的常量）一律纳入。
//     早期版本只收 *ast.BasicLit，于是 4 个项目自定义 WM_APP 消息号落在
//     扫描器视野之外、又因在 constsExempt 里而免检，两头落空 ——
//     详见 consts_test.go TestCustomMessageIDs 的注释。
//   - 排除 _test.go：测试文件里的局部常量副本（job_test.go / proc_test.go 各自
//     重声明 PROCESS_QUERY_INFORMATION）不属于产品代码
//
// 结论：本包的每个包级常量都必须"要么进 win32ConstCases、要么进 constsExempt"，
// 没有第四种去处。
func scanWin32Consts(t *testing.T) map[string]string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						// 名字数多于右值数，**光看 AST 分不出是哪一种**：
						//   const ( a = 1;   b ) → b 隐式重复上一行
						//   const ( a, b = f() )  → b 是 f() 的第 2 个返回值
						// 要分清得上 go/types 做类型检查，代价与收益不成比例，所以只如实说
						// "没有独立右值"、不猜是哪一种。当前 win/ 里两种写法都没出现（也没有
						// iota 链）；哪天真出现了，下面这行标签要跟着改，别让它变成新的误导。
						out[n.Name] = name + " = （无独立右值）"
						continue
					}
					v := vs.Values[i]
					if bl, ok := v.(*ast.BasicLit); ok {
						out[n.Name] = name + " = " + bl.Value
						continue
					}
					lo := fset.Position(v.Pos()).Offset
					hi := fset.Position(v.End()).Offset
					out[n.Name] = name + " = " + strings.TrimSpace(string(src[lo:hi]))
				}
			}
		}
	}
	return out
}

// TestWin32ConstsNoUnasserted 是 C1 门禁真正的主闸：
// 「包内定义了字面量常量，但既不在断言表、也不在豁免表」→ 红。
func TestWin32ConstsNoUnasserted(t *testing.T) {
	defined := scanWin32Consts(t)
	// 断言表里出现过的符号
	asserted := assertedConstNames()
	var missing []string
	for name := range defined {
		if asserted[name] || constsExempt[name] != "" {
			continue
		}
		missing = append(missing, name+" ("+defined[name]+")")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("以下常量已定义但既未在 TestWin32Constants 断言、也不在 constsExempt 豁免表：\n  %s\n"+
			"  Win32 SDK 值请对照头文件加进 consts_test.go 的 win32ConstCases；"+
			"非 Win32 的项目内部常量请在 consts_test.go 补上值断言，"+
			"并在 constsExempt 里写明理由（豁免只表示'不进 SDK 值表'，不等于没有断言）。",
			strings.Join(missing, "\n  "))
	}
	// 反向：断言表里有、包内却没有对应定义的符号也要报。
	// 豁免表里的符号跳过 —— 例如 COLOR_BTNFACE_BRUSH 是组合表达式，
	// 若哪天它被从包内删掉，豁免会盖住这条 stale 报告，这是豁免的已知代价
	//（豁免表每条都写了理由，理由失效时要连同删掉这一行）。
	var stale []string
	for name := range asserted {
		if defined[name] == "" && constsExempt[name] == "" {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("断言表里的符号在包内已不存在: %v", stale)
	}
}

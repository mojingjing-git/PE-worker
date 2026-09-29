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

// constsExempt 是**非 Win32**的包内常量，列出理由以免被当成漏网。
// 新增常量若不在这张表里、也不在 TestWin32Constants 的断言表里 → 门禁红。
var constsExempt = map[string]string{
	// 项目自定义的 WM_APP 消息（msgs.go:16-37），不是 Win32 常量
	"WM_LOG_LINE": "项目自定义 WM_APP 消息", "WM_USER_INPUT": "项目自定义 WM_APP 消息",
	"WM_AGENT_RESPONSE": "项目自定义 WM_APP 消息", "WM_USER_ABORT": "项目自定义 WM_APP 消息",
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
	// 组合表达式常量：msgs.go:156 `COLOR_BTNFACE_BRUSH = 15 + 1`。
	// 扫描器只收 *ast.BasicLit（见下），组合表达式**不在 defined 里**，
	// 若不豁免，反向 stale 检查会每次都报它 —— 门禁第一天就红。
	"COLOR_BTNFACE_BRUSH": "组合表达式(15+1)，扫描器只收字面量",
}

// scanWin32Consts 扫本包非测试文件里所有「名字 = 字面量」形式的**包级**常量。
//
// 口径（改动这个口径会让漏网常量重新溜过去，务必谨慎）：
//   - 只看顶层 f.Decls 里的 GenDecl，函数内的局部 const 不算
//     （局部 const 是实现细节，不是"需要跨文件核对的 SDK 值"）
//   - 只收 *ast.BasicLit：组合表达式（`15 + 1`、`A | B`）**不**纳入机械比对，
//     因为它们不是照抄某个 #define 就能判对错的
//   - 排除 _test.go：测试文件里的局部常量副本（job_test.go / proc_test.go 各自
//     重声明 PROCESS_QUERY_INFORMATION）不属于产品代码
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
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
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
						continue
					}
					bl, ok := vs.Values[i].(*ast.BasicLit)
					if !ok {
						continue // 非字面量（组合表达式）不纳入机械比对
					}
					out[n.Name] = name + ":" + bl.Value
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
			"  Win32 值请对照 SDK 头文件加进 consts_test.go 的 cases；"+
			"非 Win32 的项目内部常量请加进 constsExempt 并写明理由。",
			strings.Join(missing, "\n  "))
	}
	// 反向：断言表里有、包内却没有对应定义的符号也要报。
	// 只报「在断言表、但既不是字面量常量、也不在豁免表」的符号 ——
	// COLOR_BTNFACE_BRUSH 是 `15 + 1`（组合表达式，扫描器不收），
	// 它已进 constsExempt 所以被豁免掉，不会天天报红。
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

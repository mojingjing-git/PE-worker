// src/tools/read_hardening_test.go: docs/11 S5 工具正确性批的回归测试。
//
// 覆盖 4 条:
//   - S5-1 quoteArg 的 \" 转义不是 cmd 转义 → ls/cat/grep/find 可注入执行任意命令
//   - S5-1 附带 带空格路径 100% 失败
//   - S5-2 find 的 pattern 不是通配符 → "*.txt" 静默返 0 行
//   - S5-3 selftest 描述承诺"注册数 == N"但实现不查
//   - S5-5 只读工具也弹 confirm, 文案还是 exec 的
package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 注入串统一带这个标记: 只要它出现在工具输出里, 就说明第二条命令真跑了。
const injectMark = "PWNED_TOOLS_"

var injectPayloads = []string{
	`a" & echo ` + injectMark + ` & "b`,
	`a" | echo ` + injectMark + ` & "b`,
	`a" & echo ` + injectMark,
	`" & echo ` + injectMark + ` & "`,
	`a" & call ` + injectMark + ` & "b`,
	`a" && echo ` + injectMark + ` && "b`,
}

// mkFile 造一个文件, 返回路径。失败直接 t.Fatal。
func mkFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spaceDir 在 t.TempDir() 下造一个**带空格**的目录, 并放一个文件。
// 老 quoteArg 会把它包成 ""C:\...\Program Files\..."", 必然失败。
func spaceDir(t *testing.T) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Program Files Test")
	return dir, mkFile(t, filepath.Join(dir, "spaced.txt"), "spaced-content\n")
}

// ---------------------------------------------------------------- S5-1 注入

func TestLs_NoInjection(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a.txt"), "a")
	t0, ok := Get("ls")
	if !ok {
		t.Fatal("ls 未注册")
	}
	for _, payload := range injectPayloads {
		r, _ := t0.Run(&Context{}, payload)
		if strings.Contains(r.Text, injectMark) {
			t.Errorf("ls(%q) 发生命令注入, 输出: %q", payload, r.Text)
		}
		// 同一个 payload 指向真实目录也必须无害。
		r2, _ := t0.Run(&Context{}, filepath.Join(dir, payload))
		if strings.Contains(r2.Text, injectMark) {
			t.Errorf("ls(%q) 发生命令注入, 输出: %q", filepath.Join(dir, payload), r2.Text)
		}
	}
}

func TestCat_NoInjection(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a.txt"), "a")
	t0, _ := Get("cat")
	for _, payload := range injectPayloads {
		r, _ := t0.Run(&Context{}, payload)
		if strings.Contains(r.Text, injectMark) {
			t.Errorf("cat(%q) 发生命令注入, 输出: %q", payload, r.Text)
		}
	}
}

func TestGrep_NoInjection(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a.txt"), "hello world\n")
	t0, _ := Get("grep")
	// pattern 和 path 两侧都试。
	for _, payload := range injectPayloads {
		r, _ := t0.Run(&Context{}, payload+" "+dir)
		if strings.Contains(r.Text, injectMark) {
			t.Errorf("grep(pattern=%q) 发生命令注入, 输出: %q", payload, r.Text)
		}
		r2, _ := t0.Run(&Context{}, "hello "+filepath.Join(dir, payload))
		if strings.Contains(r2.Text, injectMark) {
			t.Errorf("grep(path=%q) 发生命令注入, 输出: %q", filepath.Join(dir, payload), r2.Text)
		}
	}
}

func TestFind_NoInjection(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a.txt"), "x")
	t0, _ := Get("find")
	for _, payload := range injectPayloads {
		r, _ := t0.Run(&Context{}, payload+" "+dir)
		if strings.Contains(r.Text, injectMark) {
			t.Errorf("find(pattern=%q) 发生命令注入, 输出: %q", payload, r.Text)
		}
		r2, _ := t0.Run(&Context{}, "a.txt "+filepath.Join(dir, payload))
		if strings.Contains(r2.Text, injectMark) {
			t.Errorf("find(path=%q) 发生命令注入, 输出: %q", filepath.Join(dir, payload), r2.Text)
		}
	}
}

// 合法的怪文件名（Windows 允许 & 但不允许 "）必须原样列出, 不能被当命令分隔符。
func TestLs_InjectionPayloadAsRealName(t *testing.T) {
	dir := t.TempDir()
	weird := `a & echo ` + injectMark + ` & b.txt`
	mkFile(t, filepath.Join(dir, weird), "x")
	mkFile(t, filepath.Join(dir, "plain.txt"), "x")
	t0, _ := Get("ls")
	r, err := t0.Run(&Context{}, dir)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(r.Text, weird) {
		t.Errorf("ls 应原样列出含 & 的怪文件名, 实际: %q", r.Text)
	}
	// 输出只有文件名, 没有第二个命令的输出 —— 怪文件名本身不含换行, 断言行数 == 2。
	if n := strings.Count(strings.TrimRight(r.Text, "\n"), "\n") + 1; n != 2 {
		t.Errorf("ls 输出应只有 2 行(文件名含注入串但未被执行), 实际 %d 行: %q", n, r.Text)
	}
}

// ---------------------------------------------------------------- S5-1 空格路径

func TestLs_SpacePath(t *testing.T) {
	dir, _ := spaceDir(t)
	t0, _ := Get("ls")
	r, err := t0.Run(&Context{}, dir)
	if err != nil {
		t.Fatalf("ls 带空格路径应成功: %v", err)
	}
	if !strings.Contains(r.Text, "spaced.txt") {
		t.Errorf("ls 带空格路径应列出 spaced.txt, 实际: %q", r.Text)
	}
}

func TestCat_SpacePath(t *testing.T) {
	_, file := spaceDir(t)
	t0, _ := Get("cat")
	r, err := t0.Run(&Context{}, file)
	if err != nil {
		t.Fatalf("cat 带空格路径应成功: %v", err)
	}
	if !strings.Contains(r.Text, "spaced-content") {
		t.Errorf("cat 带空格路径应读到内容, 实际: %q", r.Text)
	}
}

func TestGrep_SpacePath(t *testing.T) {
	_, file := spaceDir(t)
	t0, _ := Get("grep")
	// path 写成整体加引号的形式（LLM 常见写法）。
	r, err := t0.Run(&Context{}, "spaced-content \""+filepath.Dir(file)+"\"")
	if err != nil {
		t.Fatalf("grep 带空格路径应成功: %v", err)
	}
	if !strings.Contains(r.Text, "spaced-content") {
		t.Errorf("grep 带空格路径应找到内容, 实际: %q", r.Text)
	}
}

func TestFind_SpacePath(t *testing.T) {
	dir, _ := spaceDir(t)
	t0, _ := Get("find")
	r, err := t0.Run(&Context{}, "*.txt \""+dir+"\"")
	if err != nil {
		t.Fatalf("find 带空格路径应成功: %v", err)
	}
	if !strings.Contains(r.Text, "spaced.txt") {
		t.Errorf("find 带空格路径应找到 spaced.txt, 实际: %q", r.Text)
	}
}

// resolvePath: Cwd 生效 + 绝对路径不被拼坏。
func TestResolvePath_Cwd(t *testing.T) {
	dir := t.TempDir()
	ctx := &Context{Cwd: dir}
	if got := resolvePath(ctx, "sub"); got != filepath.Join(dir, "sub") {
		t.Errorf("相对路径应拼到 Cwd, 实际: %q", got)
	}
	abs := `C:\Windows`
	if got := resolvePath(ctx, abs); got != abs {
		t.Errorf("绝对路径应原样返回, 实际: %q", got)
	}
	if got := resolvePath(nil, "x"); got != "x" {
		t.Errorf("ctx 为 nil 时应原样返回, 实际: %q", got)
	}
}

func TestLs_Cwd(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "in_cwd.txt"), "x")
	t0, _ := Get("ls")
	// args 空 = CWD; 相对路径也按 ctx.Cwd 解析（老的 cmd.Dir 时代同样成立）。
	r, err := t0.Run(&Context{Cwd: dir}, "")
	if err != nil {
		t.Fatalf("ls 空 args 应列 Cwd: %v", err)
	}
	if !strings.Contains(r.Text, "in_cwd.txt") {
		t.Errorf("ls 空 args 应按 ctx.Cwd 解析并列出 in_cwd.txt, 实际: %q", r.Text)
	}
	r2, err2 := t0.Run(&Context{Cwd: dir}, ".")
	if err2 != nil {
		t.Fatalf("ls .: %v", err2)
	}
	if !strings.Contains(r2.Text, "in_cwd.txt") {
		t.Errorf("ls . 应相对 ctx.Cwd 解析并列出 in_cwd.txt, 实际: %q", r2.Text)
	}
}

// ---------------------------------------------------------------- S5-2 find 通配

func TestFind_WildcardStar(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a.txt"), "x")
	mkFile(t, filepath.Join(dir, "b.log"), "x")
	mkFile(t, filepath.Join(dir, "c.txt"), "x")
	t0, _ := Get("find")
	r, err := t0.Run(&Context{}, "*.txt "+dir)
	if err != nil {
		t.Fatalf("find *.txt: %v", err)
	}
	if !strings.Contains(r.Text, "a.txt") || !strings.Contains(r.Text, "c.txt") {
		t.Errorf("find *.txt 应匹配到 a.txt + c.txt, 实际: %q", r.Text)
	}
	if strings.Contains(r.Text, "b.log") {
		t.Errorf("find *.txt 不应匹配 b.log, 实际: %q", r.Text)
	}
}

func TestFind_WildcardQuestion(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "a1.txt"), "x")
	mkFile(t, filepath.Join(dir, "a12.txt"), "x")
	t0, _ := Get("find")
	r, err := t0.Run(&Context{}, "a?.txt "+dir)
	if err != nil {
		t.Fatalf("find a?.txt: %v", err)
	}
	if !strings.Contains(r.Text, "a1.txt") {
		t.Errorf("find a?.txt 应匹配 a1.txt, 实际: %q", r.Text)
	}
	if strings.Contains(r.Text, "a12.txt") {
		t.Errorf("find a?.txt 不应匹配 a12.txt, 实际: %q", r.Text)
	}
}

// 旧行为: pattern 无通配符时按子串找（find note 期望能找到 note.txt）。
func TestFind_SubstringNoWildcard(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "note.txt"), "x")
	mkFile(t, filepath.Join(dir, "zzz.bin"), "x")
	t0, _ := Get("find")
	r, err := t0.Run(&Context{}, "note "+dir)
	if err != nil {
		t.Fatalf("find note: %v", err)
	}
	if !strings.Contains(r.Text, "note.txt") {
		t.Errorf("find note 应找到 note.txt, 实际: %q", r.Text)
	}
	if strings.Contains(r.Text, "zzz.bin") {
		t.Errorf("find note 不应匹配 zzz.bin, 实际: %q", r.Text)
	}
}

// ---------------------------------------------------------------- S5-3 selftest 计数

// fakeProbeTool 只为把注册数临时顶到 15。
type fakeProbeTool struct{}

func (fakeProbeTool) Name() string        { return "__probe__" }
func (fakeProbeTool) Description() string { return "临时注册, 用于验证 selftest 计数" }
func (fakeProbeTool) Risk() RiskLevel     { return RiskRead }
func (fakeProbeTool) Run(*Context, string) (Result, error) {
	return Result{Text: "probe"}, nil
}

func TestSelftest_ReportsRegisteredCount(t *testing.T) {
	t0, ok := Get("selftest")
	if !ok {
		t.Fatal("selftest 未注册")
	}
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("selftest.Run: %v", err)
	}
	if strings.HasPrefix(r.Text, "FAIL:") {
		t.Fatalf("工具数正确时 selftest 不应报 FAIL, 实际: %q", r.Text)
	}
	if !strings.Contains(r.Text, "OK:") {
		t.Errorf("工具数正确时 selftest 应报 OK, 实际: %q", r.Text)
	}
	if !strings.Contains(r.Text, strconv.Itoa(expectRegisteredTools)) {
		t.Errorf("selftest 输出应含期望工具数 %d, 实际: %q", expectRegisteredTools, r.Text)
	}
	if !strings.Contains(t0.Description(), strconv.Itoa(expectRegisteredTools)) {
		t.Errorf("selftest 描述应承诺注册数 == %d, 实际: %q", expectRegisteredTools, t0.Description())
	}
}

func TestSelftest_FailsOnExtraTool(t *testing.T) {
	Register(fakeProbeTool{})
	t.Cleanup(func() {
		mu.Lock()
		delete(all, fakeProbeTool{}.Name())
		mu.Unlock()
	})
	t0, _ := Get("selftest")
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("selftest.Run: %v", err)
	}
	if !strings.HasPrefix(r.Text, "FAIL:") {
		t.Errorf("多注册一个工具应报 FAIL 开头, 实际: %q", r.Text)
	}
	if !strings.Contains(r.Text, strconv.Itoa(expectRegisteredTools+1)) {
		t.Errorf("FAIL 输出应含实际注册数 %d, 实际: %q", expectRegisteredTools+1, r.Text)
	}
}

func TestSelftest_FailsOnMissingTool(t *testing.T) {
	mu.Lock()
	saved, ok := all["exec"]
	delete(all, "exec")
	mu.Unlock()
	if !ok {
		t.Fatal("exec 未注册, 测试前置失效")
	}
	t.Cleanup(func() {
		mu.Lock()
		all["exec"] = saved
		mu.Unlock()
	})

	t0, _ := Get("selftest")
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("selftest.Run: %v", err)
	}
	if !strings.HasPrefix(r.Text, "FAIL:") {
		t.Errorf("少注册一个工具应报 FAIL 开头, 实际: %q", r.Text)
	}
	if !strings.Contains(r.Text, strconv.Itoa(expectRegisteredTools-1)) {
		t.Errorf("FAIL 输出应含实际注册数 %d, 实际: %q", expectRegisteredTools-1, r.Text)
	}
}

// ---------------------------------------------------------------- S5-5 只读不弹 confirm

// 只读工具 Risk=Read, 不该弹 confirm —— 老实现走 execTool.Run 会弹
// `exec: "dir . /B /A"`, 用户以为在看目录实际在跑命令。
func TestReadTools_NoConfirm(t *testing.T) {
	dir, file := spaceDir(t)
	boom := func(tool string) func(string) bool {
		return func(p string) bool {
			t.Errorf("%s 不应弹 confirm, 实际弹了: %q", tool, p)
			return true
		}
	}
	ls, _ := Get("ls")
	if _, err := ls.Run(&Context{Confirm: boom("ls")}, dir); err != nil {
		t.Fatalf("ls: %v", err)
	}
	cat, _ := Get("cat")
	if _, err := cat.Run(&Context{Confirm: boom("cat")}, file); err != nil {
		t.Fatalf("cat: %v", err)
	}
	grep, _ := Get("grep")
	if _, err := grep.Run(&Context{Confirm: boom("grep")}, "spaced-content "+dir); err != nil {
		t.Fatalf("grep: %v", err)
	}
	find, _ := Get("find")
	if _, err := find.Run(&Context{Confirm: boom("find")}, "*.txt "+dir); err != nil {
		t.Fatalf("find: %v", err)
	}
}

// ---------------------------------------------------------------- 杂项

func TestLs_NotFound(t *testing.T) {
	t0, _ := Get("ls")
	if _, err := t0.Run(&Context{}, filepath.Join(t.TempDir(), "nope-not-here")); err == nil {
		t.Error("ls 不存在的目录应返 err")
	}
}

func TestCat_NotFound(t *testing.T) {
	t0, _ := Get("cat")
	if _, err := t0.Run(&Context{}, filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("cat 不存在的文件应返 err")
	}
}

func TestGrep_Empty(t *testing.T) {
	t0, _ := Get("grep")
	if _, err := t0.Run(&Context{}, "  "); err == nil {
		t.Error("grep 空 args 应返 err")
	}
}

func TestFind_Empty(t *testing.T) {
	t0, _ := Get("find")
	if _, err := t0.Run(&Context{}, "  "); err == nil {
		t.Error("find 空 args 应返 err")
	}
}

// grep 非法正则应退回字面量匹配, 而不是整轮失败。
func TestGrep_InvalidRegexFallsBackToLiteral(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "x.txt"), "has [bracket] inside\n")
	t0, _ := Get("grep")
	r, err := t0.Run(&Context{}, "[bracket "+dir)
	if err != nil {
		t.Fatalf("非法正则不应让 grep 失败: %v", err)
	}
	if !strings.Contains(r.Text, "[bracket]") {
		t.Errorf("非法正则应按字面量匹配到 [bracket], 实际: %q", r.Text)
	}
}

// grep 输出带行号（对应 findstr /N）。
func TestGrep_LineNumbers(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "x.txt"), "foo\nbar\nfoo again\n")
	t0, _ := Get("grep")
	r, err := t0.Run(&Context{}, "foo "+dir)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(r.Text, ":1:") || !strings.Contains(r.Text, ":3:") {
		t.Errorf("grep 输出应含行号 1 和 3, 实际: %q", r.Text)
	}
}

// grep 默认忽略大小写（对应 findstr /I）。
func TestGrep_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "x.txt"), "Hello World\n")
	t0, _ := Get("grep")
	r, err := t0.Run(&Context{}, "hello "+dir)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(r.Text, "Hello World") {
		t.Errorf("grep 应忽略大小写匹配到 Hello World, 实际: %q", r.Text)
	}
}

// cat 读 UTF-8 中文内容不能被当成 GBK 转花。
func TestCat_UTF8NotMangled(t *testing.T) {
	dir := t.TempDir()
	path := mkFile(t, filepath.Join(dir, "u8.txt"), "中文内容 hello\n")
	t0, _ := Get("cat")
	r, err := t0.Run(&Context{}, path)
	if err != nil {
		t.Fatalf("cat: %v", err)
	}
	if !strings.Contains(r.Text, "中文内容") {
		t.Errorf("cat UTF-8 内容应原样返回, 实际: %q", r.Text)
	}
}

func TestLimitOutput_Truncates(t *testing.T) {
	big := strings.Repeat("A", 600*1024)
	got := limitOutput(big)
	if !strings.Contains(got, "[truncated ") {
		t.Fatalf("超长输出应带截断标记, 实际尾部: %q", got[len(got)-80:])
	}
	short := limitOutput("ok")
	if short != "ok" {
		t.Errorf("短输出不应改动, 实际: %q", short)
	}
}

// matchName: 通配 + 子串 + 带分隔符 pattern。
func TestMatchName(t *testing.T) {
	cases := []struct {
		pattern, base, full, root string
		want                      bool
	}{
		{"*.txt", "a.txt", `C:\d\a.txt`, `C:\d`, true},
		{"*.txt", "a.log", `C:\d\a.log`, `C:\d`, false},
		{"a?.txt", "a1.txt", `C:\d\a1.txt`, `C:\d`, true},
		{"a?.txt", "a12.txt", `C:\d\a12.txt`, `C:\d`, false},
		{"note", "note.txt", `C:\d\note.txt`, `C:\d`, true},
		{"note", "NOTE.TXT", `C:\d\NOTE.TXT`, `C:\d`, true},
		{"note", "zzz.bin", `C:\d\zzz.bin`, `C:\d`, false},
		{`sub\*.txt`, "a.txt", `C:\d\sub\a.txt`, `C:\d`, true},
		{`sub\*.txt`, "a.txt", `C:\d\other\a.txt`, `C:\d`, false},
	}
	for _, c := range cases {
		if got := matchName(c.pattern, c.base, c.full, c.root); got != c.want {
			t.Errorf("matchName(%q, %q) = %v, 期望 %v", c.pattern, c.base, got, c.want)
		}
	}
}

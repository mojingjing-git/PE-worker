// Package test — smoke_bin_test.go
//
// 跑 dist/smith.exe --no-gui 验证整条 boot 链路：早期文件日志、cfg 加载、
// worker 起动、LLM 缺配置时的 graceful 报错、可正常退出。
//
// 跑法：先 build.cmd（已生成 dist/smith.exe），再 go test。
// 缺 dist/smith.exe 时 SKIP（不 FAIL，避免 CI 没先 build 的问题）。
package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSmoke_SmithBinary 跑 dist/smith.exe --no-gui，验证：
//  1. 产物不陈旧（比最新源码新）—— 见 assertBinaryFresh
//  2. 进程起得来（不闪退）
//  3. 日志文件落盘
//  4. exit code 0
//  5. 日志里能看到 boot 序列
func TestSmoke_SmithBinary(t *testing.T) {
	src := findSmithBinary(t)
	if src == "" {
		t.Skip("dist/smith.exe not found; run build.cmd first")
	}
	// 陈旧产物会让后面所有断言失去意义：它们验的不是当前代码。
	// 这里用 Fatalf（不是 Skip）—— 缺产物可以 Skip，产物过期是必须修的错误。
	assertBinaryFresh(t, src)

	// 把 smith.exe 复制到 temp dir 跑，logx 写日志到 exe 同目录
	// 这样测试结束清理不污染 dist/
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "smith.exe")
	if err := copyFile(src, bin); err != nil {
		t.Fatalf("copy smith.exe: %v", err)
	}

	cmd := exec.Command(bin, "--no-gui")
	cmd.Dir = tmpDir
	var stderr strings.Builder
	cmd.Stderr = &stderr

	// --no-gui 模式 worker 处理一条输入后 cancel，应该 5s 内退
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("smith.exe --no-gui failed: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Errorf("smith.exe --no-gui 超时 15s; stderr=%s", stderr.String())
		return
	}

	// 验证日志文件存在且有内容
	logPath := filepath.Join(tmpDir, "smith.log")
	st, err := os.Stat(logPath)
	if err != nil {
		t.Errorf("no smith.log: %v", err)
		return
	}
	if st.Size() < 50 {
		t.Errorf("smith.log too small: %d bytes", st.Size())
	}
	content, _ := os.ReadFile(logPath)
	if !strings.Contains(string(content), "boot start") {
		t.Errorf("smith.log missing boot start marker:\n%s", content)
	}

	// 【T4-5】原来断言 `strings.Contains(content, "llm")` —— 而空配置路径
	// **恰好**打 `!! llm: 缺配置`，等于被一条错误日志满足，这条断言没有
	// 验任何东西。
	//
	// 而"断言 `你 > ver` / `ver turn=`"（一度想改成的方向）**也不对**：
	// smoke 在空 temp 目录跑，没有 smith.ini → llmClient == nil → runWorker
	// 在 `if llm == nil { continue }` 直接跳过，**loop.Run 根本不会被调用**。
	// 且全仓 grep `你 >` 0 命中 —— 那个标记根本不存在。
	//
	// 所以断言"如实反映当前无 LLM 的降级路径"三条，合起来证明：
	// worker 起了 → loop 建好了 → 输入被消费并走完 no-gui 退出路径。
	for _, marker := range []string{
		"boot start",        // [2] 早期日志开了
		"agent loop ready",  // worker 起动且 NewLoop 成功
		"no-gui smoke done", // 完整走完退出路径
	} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("smith.log 缺少 %q：\n%s", marker, content)
		}
	}
	// 反向：确认走的是"LLM 未配置"降级而不是别的分支
	if !strings.Contains(string(content), "LLM 未配置") {
		t.Errorf("smith.log 缺少 LLM 未配置标记（应确认输入被 worker 消费后走了降级分支）:\n%s", content)
	}
	// ⚠️ 不要断言 "tools=14" —— T3 已把它变成 17，且随工具增减漂移。
}

// copyFile 复制文件（简单实现，PE-agent 测试用）
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0755)
}

// findSmithBinary 在 dist/ 根找 386 版本（与 win7 PE 主力一致）。
func findSmithBinary(t *testing.T) string {
	// 项目根
	root, err := projectRoot()
	if err != nil {
		t.Logf("project root not found: %v", err)
		return ""
	}
	candidates := []string{
		filepath.Join(root, "dist", "smith.exe"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// assertBinaryFresh 断言产物不比任何源文件旧。
//
// 为什么必须有（docs/11 §S6-2）：
//
//	findSmithBinary 原来只做 os.Stat 存在性判断。而 dist/ 里的 exe 曾经
//	是别的工具用裸 `go build` 建的（PE 子系统=3 CONSOLE、没剥符号、
//	比正确产物大 47%），还比 src/win/oem.go 旧 3 个 commit。
//	这个测试对着它跑完，打印 PASS —— 一盏与真实交付物零相关的绿灯。
//
//	一个"跑交付物"的测试如果不检查交付物本身新鲜，等于没测。
func assertBinaryFresh(t *testing.T, bin string) {
	t.Helper()

	binInfo, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat %s: %v", bin, err)
	}
	binTime := binInfo.ModTime()

	root, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}

	// 参与新鲜度判定的输入：源码 + 构建脚本 + 嵌入资源 + 测试自身用到的配置模板。
	// 漏掉任何一类都会让"改了东西但没触发重建"静默通过。
	roots := []string{
		filepath.Join(root, "src"),
		filepath.Join(root, "assets"),
	}
	files := []string{
		filepath.Join(root, "build.cmd"),
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "smith.ini.example"),
	}
	for _, r := range roots {
		// _test.go 必须排除：`go build .\src` 根本不编译测试文件，
		// 改一个测试文件不会让产物变陈旧。把它算进来会导致
		// "只改了测试就直接跑 go test"必红 —— 那是噪音，不是闸门。
		if werr := filepath.Walk(r, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				// 目录读不到就跳过这一项，但要让用户看见（L5：不吞错）。
				// 静默截断会让比对集变小 —— 也就是漏判。
				t.Logf("walk %s: %v", p, err)
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil
			}
			if filepath.Ext(p) == ".go" || filepath.Ext(p) == ".pem" {
				files = append(files, p)
			}
			return nil
		}); werr != nil {
			t.Fatalf("walk %s: %v", r, werr)
		}
	}

	var newest os.FileInfo
	var newestPath string
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest, newestPath = info, f
		}
	}
	if newest == nil {
		t.Fatalf("找不到任何源文件来比对新鲜度（root=%s）", root)
	}

	if binTime.Before(newest.ModTime()) {
		rel, _ := filepath.Rel(root, newestPath)
		t.Fatalf("产物已过期：%s 建于 %s，但 %s 建于 %s。\n"+
			"     跑 build.cmd 重建后再测 —— 否则这个测试验的不是当前代码。",
			filepath.Base(bin), binTime.Format("2006-01-02 15:04:05"),
			rel, newest.ModTime().Format("2006-01-02 15:04:05"))
	}
	t.Logf("产物新鲜：%s（%s）>= 最新输入 %s（%s）",
		filepath.Base(bin), binTime.Format("01-02 15:04:05"),
		newestPath, newest.ModTime().Format("01-02 15:04:05"))
}

// projectRoot 找 go.mod 所在目录。
func projectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// 当前可能在 .../src/test，往上找 go.mod
	dir := wd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

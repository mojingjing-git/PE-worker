// Package test — smoke_bin_test.go
//
// 跑 dist/owl.exe --no-gui 验证整条 boot 链路：早期文件日志、cfg 加载、
// worker 起动、LLM 缺配置时的 graceful 报错、可正常退出。
//
// 跑法：先 build.cmd（已生成 dist/owl.exe），再 go test。
// 缺 dist/owl.exe 时 SKIP（不 FAIL，避免 CI 没先 build 的问题）。
package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSmoke_OWLBinary 跑 dist/owl.exe --no-gui，验证：
//   1. 进程起得来（不闪退）
//   2. 日志文件落盘
//   3. exit code 0
//   4. 日志里能看到 boot 序列
func TestSmoke_OWLBinary(t *testing.T) {
	src := findOWLBinary(t)
	if src == "" {
		t.Skip("dist/owl.exe not found; run build.cmd first")
	}

	// 把 owl.exe 复制到 temp dir 跑，logx 写日志到 exe 同目录
	// 这样测试结束清理不污染 dist/
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "owl.exe")
	if err := copyFile(src, bin); err != nil {
		t.Fatalf("copy owl.exe: %v", err)
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
			t.Errorf("owl.exe --no-gui failed: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Errorf("owl.exe --no-gui 超时 15s; stderr=%s", stderr.String())
		return
	}

	// 验证日志文件存在且有内容
	logPath := filepath.Join(tmpDir, "owl.log")
	st, err := os.Stat(logPath)
	if err != nil {
		t.Errorf("no owl.log: %v", err)
		return
	}
	if st.Size() < 50 {
		t.Errorf("owl.log too small: %d bytes", st.Size())
	}
	content, _ := os.ReadFile(logPath)
	if !strings.Contains(string(content), "boot start") {
		t.Errorf("owl.log missing boot start marker:\n%s", content)
	}
	if !strings.Contains(string(content), "llm") {
		t.Errorf("owl.log missing llm status line:\n%s", content)
	}
}

// copyFile 复制文件（简单实现，PE-agent 测试用）
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0755)
}

// findOWLBinary 在 dist/ 根找 386 版本（与 win7 PE 主力一致）。
func findOWLBinary(t *testing.T) string {
	// 项目根
	root, err := projectRoot()
	if err != nil {
		t.Logf("project root not found: %v", err)
		return ""
	}
	candidates := []string{
		filepath.Join(root, "dist", "owl.exe"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
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

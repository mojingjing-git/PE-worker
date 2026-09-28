// tools/edit_guard_test.go —— S0-1 回归门禁：edit 的空 old 守卫。
//
// 为什么这是全套工具里最该有测试的一条：
//
//	它是唯一的"破坏用户数据 + 系统报告成功"路径。
//	old 为空时 strings.Count(content, "") == len+1（≠0，绕过"找不到"检查），
//	而 strings.Replace(content, "", new, -1) 会在**每个 rune 之间**插入 new。
//	审计实测：abc → XaXbXcX，工具回 "OK 替换 4 处"。
//	触发条件极低：模型只要发 `path\n\nnewtext`（中间行空）就会中招。
//	PE 里被改的可能是分区配置或磁盘信息。
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEdit_EmptyOld_MustNotDestroyFile 是 S0-1 的核心门禁：
// 传空 old 必须返 error，且文件内容**逐字节不变**。
func TestEdit_EmptyOld_MustNotDestroyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "critical.ini")
	original := "[disk]\npartition=0\nboot=1\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// args 三行：path / <空 old> / new —— 模型误发这种参数就会触发原 bug
	_, err := RunByName(&Context{}, "edit", path+"\n\nX")
	if err == nil {
		t.Fatalf("edit 空 old 必须返 error，却成功了 —— 文件可能已被打碎")
	}
	if !strings.Contains(err.Error(), "old") {
		t.Errorf("错误信息应说明是 old 的问题，实际: %v", err)
	}

	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("回读失败: %v", rerr)
	}
	if string(after) != original {
		t.Fatalf("文件被改动了！\n before=%q\n after =%q", original, string(after))
	}
}

// TestEdit_EmptyOld_ViaToolRun 再从 Tool.Run 走一遍，确认注册表分发路径
// 也拦得住（而不只是 RunByName 这一层）。
func TestEdit_EmptyOld_ViaToolRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tl, ok := Get("edit")
	if !ok {
		t.Fatal("edit 工具未注册")
	}
	if _, err := tl.Run(&Context{}, path+"\n\nX"); err == nil {
		t.Fatal("edit 空 old 竟然成功了")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "abc" {
		t.Errorf("文件被破坏：abc -> %q", string(got))
	}
}

// TestEdit_NormalStillWorks 确认守卫没有误伤正常路径。
func TestEdit_NormalStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := RunByName(&Context{}, "edit", path+"\nworld\nPE")
	if err != nil {
		t.Fatalf("正常 edit 失败: %v", err)
	}
	if !strings.Contains(res.Text, "OK") {
		t.Errorf("应回 OK，实际: %q", res.Text)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello PE" {
		t.Errorf("替换结果不对: %q", string(got))
	}
}

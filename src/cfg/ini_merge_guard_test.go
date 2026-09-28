// cfg/ini_merge_guard_test.go —— S0-3 回归门禁：Save 合并模式不能把 ini 写坏。
//
// 为什么这条是 CRITICAL：
//
//	ini 是用户手写的（smith.ini.example / PE 上用记事本改），
//	"最后一行没有换行"是极常见状态。原实现在这种输入下会把 [llm] 段头
//	粘到上一行末尾：
//
//	  in : "[agent]\nconfirm = 1"           ← 无末尾换行
//	  out: "[agent]\nconfirm = 1[llm]\n..."  ← 写坏
//	  Load err: invalid bool: "1[llm]"
//
//	后果不是"这次保存不好看"，而是**下次启动 Load 直接失败 → main.go
//	return 1 → GUI 永远起不来**。一台用来救砖的 PE 机器被自己的配置文件
//	锁死，而写坏那次用户完全看不到（-H windowsgui 无输出，日志里只有一行
//	"ver ini updated"）。审计实测复现。
package cfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveMerge_NoTrailingNewline 是 S0-3 的核心门禁。
func TestSaveMerge_NoTrailingNewline(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"单行无换行", "[agent]\nconfirm = 1"},
		{"多行无换行", "[agent]\nconfirm = 1\n\n[ui]\nfontsize = 14"},
		{"带空格的行", "[agent]\nwhitelist = dir, ver"},
		{"只有段头", "[agent]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "smith.ini")
			if err := os.WriteFile(path, []byte(tc.in), 0644); err != nil {
				t.Fatalf("seed ini: %v", err)
			}

			c, err := Load(path)
			if err != nil {
				t.Fatalf("Load 原文件就失败: %v", err)
			}
			// 模拟首次运行对话框勾选"保存到磁盘"
			c.LLM.Base = "https://api.example.com/v1"
			c.LLM.Model = "test-model"
			if err := Save(path, c, false); err != nil {
				t.Fatalf("Save: %v", err)
			}

			// 关键：写坏后的文件必须仍能被 Load 回来
			c2, err := Load(path)
			if err != nil {
				t.Fatalf("Save 写坏了 ini，Load 失败: %v\n内容:\n%s",
					err, mustRead(t, path))
			}
			if c2.LLM.Base != "https://api.example.com/v1" {
				t.Errorf("[llm] base 没写进去: %q", c2.LLM.Base)
			}
			// 原有段不能被破坏
			if c2.Agent.Confirm != c.Agent.Confirm {
				t.Errorf("[agent] confirm 被改坏: %v -> %v", c.Agent.Confirm, c2.Agent.Confirm)
			}
		})
	}
}

// TestSaveMerge_PreservesOtherSections 确认合并模式真的保留其它段
// （merge=true 走的是同一条 mergeLLMSection 路径）。
func TestSaveMerge_PreservesOtherSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smith.ini")
	in := "[agent]\nconfirm = 0\nmaxturns = 7\n\n[ui]\nfontsize = 18"
	if err := os.WriteFile(path, []byte(in), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c.LLM.Base = "https://x.example/v1"
	c.LLM.Model = "m"
	if err := Save(path, c, true); err != nil {
		t.Fatalf("Save merge: %v", err)
	}
	body := mustRead(t, path)
	for _, want := range []string{"[agent]", "[ui]", "confirm = 0", "maxturns = 7", "fontsize = 18", "[llm]"} {
		if !strings.Contains(body, want) {
			t.Errorf("合并后丢失了 %q:\n%s", want, body)
		}
	}
	c2, err := Load(path)
	if err != nil {
		t.Fatalf("合并后 Load 失败: %v", err)
	}
	if c2.Agent.MaxTurns != 7 {
		t.Errorf("maxturns 被改坏: %v", c2.Agent.MaxTurns)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLs_Dir(t *testing.T) {
	dir := t.TempDir()
	// 创 2 个文件
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	t0, _ := Get("ls")
	ctx := &Context{Confirm: func(string) bool { return true }}
	r, err := t0.Run(ctx, dir)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(r.Text, "a.txt") || !strings.Contains(r.Text, "b.txt") {
		t.Errorf("ls 应列两个文件, 实际: %s", r.Text)
	}
}

func TestCat_Real(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t0, _ := Get("cat")
	ctx := &Context{Confirm: func(string) bool { return true }}
	r, err := t0.Run(ctx, path)
	if err != nil {
		t.Fatalf("cat: %v", err)
	}
	if !strings.Contains(r.Text, "hello") {
		t.Errorf("cat 应读到 hello, 实际: %s", r.Text)
	}
}

func TestGrep_FindsInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	os.WriteFile(path, []byte("hello world\nfoo bar\nhello again\n"), 0644)
	t0, _ := Get("grep")
	ctx := &Context{Confirm: func(string) bool { return true }}
	r, err := t0.Run(ctx, "hello "+path)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(r.Text, "hello") {
		t.Errorf("grep 应找到 hello, 实际: %s", r.Text)
	}
}

func TestFind_FindsByName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "needle.txt"), []byte("x"), 0644)
	t0, _ := Get("find")
	ctx := &Context{Confirm: func(string) bool { return true }}
	r, err := t0.Run(ctx, "needle.txt "+dir)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !strings.Contains(r.Text, "needle.txt") {
		t.Errorf("find 应找到 needle.txt, 实际: %s", r.Text)
	}
}

func TestWrite_Declined(t *testing.T) {
	t0, _ := Get("write")
	ctx := &Context{Confirm: func(string) bool { return false }}
	_, err := t0.Run(ctx, "Z:\\nope.txt\ncontent")
	if err != nil {
		t.Fatalf("declined 应不返 err: %v", err)
	}
}

func TestWrite_Real(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.txt")
	t0, _ := Get("write")
	ctx := &Context{Confirm: func(string) bool { return true }}
	_, err := t0.Run(ctx, path+"\nhello from write\n")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello from write\n" {
		t.Errorf("写文件内容 = %q", got)
	}
}

func TestEdit_Replace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.txt")
	os.WriteFile(path, []byte("foo bar baz\n"), 0644)
	t0, _ := Get("edit")
	ctx := &Context{Confirm: func(string) bool { return true }}
	_, err := t0.Run(ctx, path+"\nbar\nBAR")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "foo BAR baz\n" {
		t.Errorf("edit 后 = %q", got)
	}
}

func TestEdit_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.txt")
	os.WriteFile(path, []byte("abc\n"), 0644)
	t0, _ := Get("edit")
	ctx := &Context{Confirm: func(string) bool { return true }}
	_, err := t0.Run(ctx, path+"\nNOTHERE\nREPLACED")
	if err == nil {
		t.Fatal("找不到 old 应返 err")
	}
}

func TestAppend_Real(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("line1\n"), 0644)
	t0, _ := Get("append")
	ctx := &Context{Confirm: func(string) bool { return true }}
	_, err := t0.Run(ctx, path+"\nline2\n")
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "line1\nline2\n" {
		t.Errorf("append 后 = %q", got)
	}
}

func TestPS_Smoke(t *testing.T) {
	t0, _ := Get("ps")
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if !strings.Contains(r.Text, "PID") {
		t.Errorf("ps 输出应含表头 PID, 实际: %q", r.Text)
	}
}

func TestPS_Filter(t *testing.T) {
	t0, _ := Get("ps")
	// filter 匹配本进程 (go test 进程)
	r, err := t0.Run(nil, "go")
	if err != nil {
		t.Fatalf("ps filter: %v", err)
	}
	if !strings.Contains(r.Text, "PID") {
		t.Errorf("ps filter 输出应含表头, 实际: %q", r.Text)
	}
}

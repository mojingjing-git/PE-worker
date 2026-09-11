package tools

import (
	"strings"
	"testing"
)

func TestRegister_All(t *testing.T) {
	tools := All()
	if len(tools) == 0 {
		t.Fatal("All() 应至少有 4 个工具 (exec/run_script/help/selftest)")
	}
	names := map[string]bool{}
	for _, t2 := range tools {
		if names[t2.Name()] {
			t.Errorf("重复 name: %s", t2.Name())
		}
		names[t2.Name()] = true
		if t2.Description() == "" {
			t.Errorf("%s 描述为空", t2.Name())
		}
	}
	for _, want := range []string{"exec", "run_script", "help", "selftest"} {
		if !names[want] {
			t.Errorf("缺 %s 工具", want)
		}
	}
}

func TestHelp_Run(t *testing.T) {
	t0, ok := Get("help")
	if !ok {
		t.Fatal("help 未注册")
	}
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("help.Run: %v", err)
	}
	if !strings.Contains(r.Text, "exec") || !strings.Contains(r.Text, "run_script") {
		t.Errorf("help 输出应含 exec + run_script, 实际: %s", r.Text)
	}
}

func TestSelftest_Run(t *testing.T) {
	t0, ok := Get("selftest")
	if !ok {
		t.Fatal("selftest 未注册")
	}
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("selftest.Run: %v", err)
	}
	if !strings.Contains(r.Text, "OK:") {
		t.Errorf("selftest 应报 OK, 实际: %s", r.Text)
	}
}

func TestExec_Empty(t *testing.T) {
	t0, _ := Get("exec")
	_, err := t0.Run(nil, "")
	if err == nil {
		t.Fatal("空命令应返 err")
	}
}

func TestExec_Declined(t *testing.T) {
	t0, _ := Get("exec")
	ctx := &Context{
		Confirm: func(string) bool { return false },
	}
	r, err := t0.Run(ctx, "ver")
	if err != nil {
		t.Fatalf("declined 应不返 err, 实际: %v", err)
	}
	if !strings.Contains(r.Text, "declined") {
		t.Errorf("期望 'declined' in result, 实际: %s", r.Text)
	}
}

func TestExec_RealVer(t *testing.T) {
	t0, _ := Get("exec")
	ctx := &Context{
		Confirm: func(string) bool { return true },
	}
	r, err := t0.Run(ctx, "ver")
	if err != nil {
		t.Fatalf("ver 应成功: %v", err)
	}
	if !strings.Contains(r.Text, "Microsoft") {
		t.Errorf("ver 输出应含 Microsoft, 实际: %q", r.Text)
	}
}

func TestRunScript_Empty(t *testing.T) {
	t0, _ := Get("run_script")
	_, err := t0.Run(nil, "")
	if err == nil {
		t.Fatal("空脚本应返 err")
	}
}

func TestRunScript_RejectsNonASCII(t *testing.T) {
	t0, _ := Get("run_script")
	ctx := &Context{Confirm: func(string) bool { return true }}
	_, err := t0.Run(ctx, "echo 中文")
	if err == nil {
		t.Fatal("非 ASCII 脚本应被拒")
	}
}

func TestRunScript_Declined(t *testing.T) {
	t0, _ := Get("run_script")
	ctx := &Context{Confirm: func(string) bool { return false }}
	r, err := t0.Run(ctx, "echo hello")
	if err != nil {
		t.Fatalf("declined 应不返 err: %v", err)
	}
	if !strings.Contains(r.Text, "declined") {
		t.Errorf("期望 declined, 实际: %s", r.Text)
	}
}

func TestRunScript_Real(t *testing.T) {
	t0, _ := Get("run_script")
	ctx := &Context{Confirm: func(string) bool { return true }}
	r, err := t0.Run(ctx, "@echo off\necho hello_from_bat\n")
	if err != nil {
		t.Fatalf("run_script 应成功: %v", err)
	}
	if !strings.Contains(r.Text, "hello_from_bat") {
		t.Errorf("run_script 输出应含 hello_from_bat, 实际: %q", r.Text)
	}
}

func TestContainsToken(t *testing.T) {
	if !containsToken([]string{"EXEC", "read"}, "exec") {
		t.Error("containsToken 不区分大小写")
	}
	if containsToken([]string{"exec"}, "read") {
		t.Error("containsToken 应不匹配")
	}
}

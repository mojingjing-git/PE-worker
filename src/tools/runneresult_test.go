package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// classifyRunOutcome —— exec / run_script 共用结果分类阶梯
// ---------------------------------------------------------------------------

// errDecode / errRun 是两个固定的哨兵 err，用来验 errors.Is 透传（L5 不吞错）。
var (
	errDecode = errors.New("decode boom")
	errRun    = errors.New("exit status 1")
)

func TestClassifyRunOutcome(t *testing.T) {
	cases := []struct {
		name       string
		outStr     string
		decErr     error
		runErr     error
		timedOut   bool
		canceled   bool
		timeoutSec int

		wantText string
		wantErr  string // "" = 期望 err 为 nil
		// wantIsDecErr: 返回的 err 链里应能找到 errDecode（L5 %w 透传）
		wantIsDecErr bool
	}{
		{
			name: "正常", outStr: "hello", timeoutSec: 60,
			wantText: "hello", wantErr: "",
		},
		{
			name: "runErr_带有效输出", outStr: "partial", runErr: errRun, timeoutSec: 60,
			wantText: "partial", wantErr: "exec: exit status 1",
		},
		{
			name: "runErr_空输出", outStr: "", runErr: errRun, timeoutSec: 60,
			wantText: "", wantErr: "exec: exit status 1",
		},
		{
			name: "decErr", outStr: "raw", decErr: errDecode, timeoutSec: 60,
			wantText: "raw", wantErr: "exec: decode output: decode boom",
			wantIsDecErr: true,
		},
		{
			name: "runErr_加_decErr", outStr: "raw", decErr: errDecode, runErr: errRun, timeoutSec: 60,
			wantText: "raw", wantErr: "exec: exit status 1 (decode output: decode boom)",
			wantIsDecErr: true,
		},
		{
			name: "超时_输出包进提示行", outStr: "half", timedOut: true, timeoutSec: 60,
			wantText: "[exec timeout 60s] partial: half", wantErr: "exec: timeout after 60s",
		},
		{
			name: "超时_加_decErr_不包提示行", outStr: "half", decErr: errDecode, timedOut: true, timeoutSec: 60,
			wantText: "half", wantErr: "exec: timeout after 60s (decode output: decode boom)",
			wantIsDecErr: true,
		},
		{
			name: "取消", outStr: "half", canceled: true, timeoutSec: 60,
			wantText: "half", wantErr: "exec: canceled by user (Esc/Stop)",
		},
		{
			// classifyAbort 不会同时返两个 true，但签名允许；这里钉死优先级：
			// canceled && !timedOut 才走取消分支，两个都 true 时按"超时"报。
			name: "取消与超时同置位_按超时报", outStr: "half", timedOut: true, canceled: true, timeoutSec: 60,
			wantText: "[exec timeout 60s] partial: half", wantErr: "exec: timeout after 60s",
		},
		{
			// runErr 不能盖掉 canceled：用户按了 Stop 就是"取消"，
			// 哪怕 kill 让子进程返回了非零退出码。
			name: "取消_优先于_runErr", outStr: "half", runErr: errRun, canceled: true, timeoutSec: 60,
			wantText: "half", wantErr: "exec: canceled by user (Esc/Stop)",
		},
		{
			// 超时同理：60s 到了就是超时，不该被 runErr 降级成 "exit status 1"。
			name: "超时_优先于_runErr", outStr: "half", runErr: errRun, timedOut: true, timeoutSec: 60,
			wantText: "[exec timeout 60s] partial: half", wantErr: "exec: timeout after 60s",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyRunOutcome("exec", tc.outStr, tc.decErr, tc.runErr,
				tc.timedOut, tc.canceled, tc.timeoutSec)

			if got.Text != tc.wantText {
				t.Errorf("Result.Text =\n  %q\nwant\n  %q", got.Text, tc.wantText)
			}
			if got.AttachImage != "" {
				t.Errorf("AttachImage 应为空，实际 %q", got.AttachImage)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatalf("err = nil, want %q", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Errorf("err.Error() = %q, want %q", err.Error(), tc.wantErr)
				}
			}
			if tc.wantIsDecErr && !errors.Is(err, errDecode) {
				t.Errorf("errors.Is(err, errDecode) = false, want true（L5：decErr 必须被包进错误链）")
			}
		})
	}
}

// TestClassifyRunOutcome_ToolNameParameterized 钉死"工具名参数化"：
// 同一个阶梯只靠 toolName 区分前缀，run_script 的消息不能漏成 "exec:"。
func TestClassifyRunOutcome_ToolNameParameterized(t *testing.T) {
	for _, tc := range []struct {
		tool     string
		timeout  int
		timedOut bool
		canceled bool
		wantErr  string
		wantText string
	}{
		{tool: "exec", timeout: 60, timedOut: true,
			wantText: "[exec timeout 60s] partial: o", wantErr: "exec: timeout after 60s"},
		{tool: "run_script", timeout: 120, timedOut: true,
			wantText: "[run_script timeout 120s] partial: o", wantErr: "run_script: timeout after 120s"},
		{tool: "run_script", timeout: 120, canceled: true,
			wantText: "o", wantErr: "run_script: canceled by user (Esc/Stop)"},
		{tool: "run_script", timeout: 120, wantErr: "run_script: exit status 3", wantText: "o"},
	} {
		// 只在第 4 例放 runErr
		var runErr error
		if tc.wantErr == "run_script: exit status 3" {
			runErr = errors.New("exit status 3")
		}
		got, err := classifyRunOutcome(tc.tool, "o", nil, runErr, tc.timedOut, tc.canceled, tc.timeout)
		if err == nil || err.Error() != tc.wantErr {
			t.Errorf("[%s] err = %v, want %q", tc.tool, err, tc.wantErr)
		}
		if got.Text != tc.wantText {
			t.Errorf("[%s] Text = %q, want %q", tc.tool, got.Text, tc.wantText)
		}
	}
}

// TestSynthRunErr 验退出码 / Wait 错 / Kill 错三者的合成优先级。
func TestSynthRunErr(t *testing.T) {
	killBoom := errors.New("kill boom")
	cases := []struct {
		name     string
		exitCode uint32
		waitErr  error
		killErr  error
		want     string // "" = nil
	}{
		{name: "全零", want: ""},
		{name: "非零退出码", exitCode: 1, want: "exit status 1"},
		{name: "waitErr_优先于退出码", exitCode: 5, waitErr: errors.New("wait boom"), want: "wait boom"},
		{name: "killErr_仅在无 runErr 时接管", killErr: killBoom, want: "kill boom"},
		{name: "退出码_优先于 killErr", exitCode: 7, killErr: killBoom, want: "exit status 7"},
		{name: "waitErr_优先于 killErr", waitErr: errors.New("wait boom"), killErr: killBoom, want: "wait boom"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := synthRunErr(tc.exitCode, tc.waitErr, tc.killErr)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// run_script 迁移到 win.StartJobCmd 后的两个行为保证
//
// ⚠️ 为什么这些用例住在 runneresult_test.go 而不在 tools_test.go：
// 本批的可改文件边界是 run_script.go / exec.go / runneresult.go /
// runneresult_test.go 四个，tools_test.go 不在其中。它们验的正是
// classifyRunOutcome 两侧调用点的接线（而不是分类函数本身），放一起更好读。
// ---------------------------------------------------------------------------

// runBatFiles 列出 temp 目录里当前所有 run_script 生成的 .bat。
// 用来断言"临时 .bat 用完即删"（PE 跑在内存盘上，不能堆）。
func runBatFiles(t *testing.T) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	ents, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "peagent_run_") && strings.HasSuffix(e.Name(), ".bat") {
			m[e.Name()] = true
		}
	}
	return m
}

func assertNoNewBat(t *testing.T, before map[string]bool) {
	t.Helper()
	after := runBatFiles(t)
	for name := range after {
		if !before[name] {
			t.Errorf("临时 .bat 残留未删: %s", name)
		}
	}
}

// spinBatScript 造一个"先打 head、中间纯 cmd.exe 空转、再打 tail"的批。
//
// 用 `for /L ... do @rem` 而不是 ping 是刻意的：它不产生任何子进程，
// 避开 src/win/jobexec_test.go 的 countPings()（它数全系统 ping 并要求归零，
// 跨包并行跑时会撞车）；且只用 cmd.exe 内建命令，不依赖本机有 go 工具链。
//
// n=1000000 实测约 0.5s（~0.46us/次）；n=3000000 约 1.4s。
func spinBatScript(n int) string {
	return fmt.Sprintf("@echo off\necho head_marker\nfor /L %%%%i in (1,1,%d) do @rem\necho tail_marker\n", n)
}

// TestRunScript_Trap1_BatOutlivesProcess 验陷阱 1 的核心保证：
// 临时 .bat 必须活到子进程读完为止。
//
// 迁移前 cmd.Run() 等进程死透，defer os.Remove(path) 在其后执行，天然安全。
// 迁移到 win.StartJobCmd 后，"删除"如果挂在 Wait 之前的任何位置，cmd.exe
// 逐行读批时会在中途拿到 EOF —— 脚本尾部（tail_marker）被静默截断，
// 退出码还可能是 0，看起来完全像"脚本本来就这么短"。
//
// 所以本用例断言两件事：尾标记在输出里、.bat 用完即删。
func TestRunScript_Trap1_BatOutlivesProcess(t *testing.T) {
	t0, ok := Get("run_script")
	if !ok {
		t.Fatal("run_script 未注册")
	}
	before := runBatFiles(t)
	ctx := &Context{Confirm: func(string) bool { return true }}

	r, err := t0.Run(ctx, spinBatScript(1000000))
	if err != nil {
		t.Fatalf("run_script 应成功: %v (output=%q)", err, r.Text)
	}
	if !strings.Contains(r.Text, "head_marker") {
		t.Errorf("输出缺 head_marker: %q", r.Text)
	}
	if !strings.Contains(r.Text, "tail_marker") {
		t.Errorf("输出缺 tail_marker —— 脚本尾部被截断（临时 .bat 提前被删）: %q", r.Text)
	}
	assertNoNewBat(t, before)
}

// TestRunScript_Trap1_CancelPathAlsoCleansBat 验取消路径同样在 Wait 之后删 .bat。
//
// 取消是唯一一条"在进程还没读完时就把它弄死"的路径：kill watcher 调
// jc.Kill()（只发终止，不等死透），脚本必然停在空转中途。若删除发生在
// Wait 之前，cmd.exe 就会在"被杀"和"读到 EOF"之间出现竞态。
// 断言：报了"取消"（而不是超时/裸 signal 错）+ .bat 已删。
func TestRunScript_Trap1_CancelPathAlsoCleansBat(t *testing.T) {
	t0, _ := Get("run_script")
	before := runBatFiles(t)

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &Context{Ctx: parent, Confirm: func(string) bool { return true }}

	// 在空转（约 1.4s）中途取消。
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	r, err := t0.Run(ctx, spinBatScript(3000000))
	if err == nil {
		t.Fatalf("被取消的 run_script 不该返回 nil err (output=%q)", r.Text)
	}
	if !strings.Contains(err.Error(), "canceled by user") {
		t.Errorf("err = %v, want 含 %q", err, "canceled by user")
	}
	if !strings.Contains(r.Text, "head_marker") {
		t.Errorf("取消前已产出的输出应保留: %q", r.Text)
	}
	if strings.Contains(r.Text, "tail_marker") {
		t.Errorf("脚本被杀前不该跑到 tail_marker: %q", r.Text)
	}
	assertNoNewBat(t, before)
}

// TestRunScript_Trap2_StartJobCmdFailure 验陷阱 2：StartJobCmd 失败时
// jc == nil，绝不能注册 defer jc.Close()。
//
// 制造失败的办法：给一个不存在的 Cwd —— CreateProcessW 带无效
// lpCurrentDirectory 会直接失败，StartJobCmd 返 (nil, err)。
// 若把 defer 写在 err 判断之前，jc.Close() 会 nil 解引用 j.mu → panic
// （Go 的方法调用在 nil 指针上取 receiver 字段即 panic，不是返回错误）。
//
// 本用例同时验证该分支把临时 .bat 删掉了。
func TestRunScript_Trap2_StartJobCmdFailure(t *testing.T) {
	t0, _ := Get("run_script")
	before := runBatFiles(t)

	ctx := &Context{
		Confirm: func(string) bool { return true },
		Cwd:     filepath.Join(os.TempDir(), "peagent_no_such_dir_D2"),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StartJobCmd 失败路径 panic（多半是 defer jc.Close() 撞上 nil）: %v", r)
		}
	}()

	_, err := t0.Run(ctx, "echo hi")
	if err == nil {
		t.Fatal("不存在的 Cwd 应让启动失败")
	}
	if !strings.Contains(err.Error(), "run_script") {
		t.Errorf("err = %v, want 前缀 run_script", err)
	}
	assertNoNewBat(t, before)
}

// TestRunScript_DrainsStderrAfterP3_20 验 stderr 管道真的能收到内容。
//
// P3-20 之前 win/jobexec.go 把 stderr **读端**当成写端关了，
// jc.stderrRd 恒为 0，TakeStderrPipe() 返 nil —— 而 exec 那边的
// `_, _ = io.Copy(cw, nil)` 把这个静默吞了，测试照样全绿。
// run_script 迁移后走同一条路径，这里钉死"stderr 不是黑的"。
//
// 用 `1>&2` 把 echo 重定向到 stderr；再 `2>&1` 混进 stdout 一起验限流写。
func TestRunScript_DrainsStderrAfterP3_20(t *testing.T) {
	t0, _ := Get("run_script")
	before := runBatFiles(t)
	ctx := &Context{Confirm: func(string) bool { return true }}

	r, err := t0.Run(ctx, "@echo off\necho out_line\necho err_line 1>&2\n")
	if err != nil {
		t.Fatalf("run_script 应成功: %v (output=%q)", err, r.Text)
	}
	if !strings.Contains(r.Text, "out_line") {
		t.Errorf("输出缺 stdout 行: %q", r.Text)
	}
	if !strings.Contains(r.Text, "err_line") {
		t.Errorf("输出缺 stderr 行 —— stderr 管道没排空（P3-20 回归）: %q", r.Text)
	}
	assertNoNewBat(t, before)
}

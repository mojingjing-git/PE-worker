//go:build windows

// Package win: jobexec_test.go —— T2 的门禁测试。
//
// 【本批最重要的一条】TestJobCmd_KillsGrandchildren
//
//	它验的是"Job Object 真能杀掉孙进程"，而不是"能杀直接子进程"。
//	后者 os/exec 的 CommandContext 就已经做到了 —— 那不是本批的价值。
//	本批的价值在于：cmd /c start ping -t 1.1.1.1 会 spawn 一个**独立**的
//	ping.exe（不在 cmd.exe 的 job 里，除非我们显式把 cmd 绑进 job 并让
//	job 的 KILL_ON_JOB_CLOSE 生效）。
//	在 PE 里这条链断了的后果是：Esc 杀不掉 diskpart / dism，
//	用户以为停了，它们继续持裸盘句柄。
package win

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// readAll 读干一个管道。**nil 文件当 error 报出来** —— Take*Pipe 返回 nil
// 意味着句柄管理出了 bug（被取走过 / Close 过 / P3-20 那种读端被误关），
// 绝不能当成"没输出"静默吞掉。
func readAll(f *os.File) (string, error) {
	if f == nil {
		return "", os.ErrInvalid
	}
	b, err := io.ReadAll(f)
	return string(b), err
}

// drainBoth 并发读两个管道直到 EOF。**必须并发** —— 管道缓冲约 64KB，
// 写满后子进程阻塞、永不退出。漏了这条本测试会超时而不是失败。
//
// 串行读（先 stdout 后 stderr）同样会死锁：任一管道写满 → 子进程阻塞 →
// 不退出 → stdout 永远不 EOF → 卡在第一根管道上。
//
// ⚠️ **stderr 的 error 必须断言**（P3-20）。历史 bug：StartJobCmd 把 stderr
// 的**读端**当写端关了 → jc.stderrRd 恒为 0 → TakeStderrPipe() 恒返 nil →
// `io.ReadAll(nil)` 立刻返回 os.ErrInvalid，而旧写法 `_` 把它丢掉 →
// stderr 恒空，测试照样绿。**丢 error 就是把 bug 藏起来。**
//
// 返回 (stdout, stderr) 分开：合在一起断言会让"stderr 丢了但 stdout 对"
// 这类回归从测试缝里溜过去。
func drainBoth(t *testing.T, jc *JobCmd) (stdout, stderr string) {
	t.Helper()
	type drainResult struct {
		so, se string
		soErr  error
		seErr  error
	}
	done := make(chan drainResult, 1)
	go func() {
		var (
			r  drainResult
			wg sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.so, r.soErr = readAll(jc.TakeStdoutPipe())
		}()
		go func() {
			defer wg.Done()
			r.se, r.seErr = readAll(jc.TakeStderrPipe())
		}()
		wg.Wait()
		done <- r
	}()
	select {
	case r := <-done:
		var err error
		switch {
		case r.soErr != nil:
			err = fmt.Errorf("ReadAll(stdout): %w", r.soErr)
		case r.seErr != nil:
			err = fmt.Errorf("ReadAll(stderr): %w", r.seErr)
		}
		if err != nil {
			t.Fatalf("排空管道失败: %v —— TakeStdoutPipe()/TakeStderrPipe() 返回了 nil 或坏句柄"+
				"（jobexec.go 父进程关写端时若误关读端，stderrRd 恒为 0）", err)
		}
		return r.so, r.se
	case <-time.After(30 * time.Second):
		t.Fatalf("排空管道超时 30s —— 子进程可能因管道写满而阻塞（排空逻辑有 bug）")
		return "", ""
	}
}

// TestJobCmd_BasicRun 冒烟：能启动、能拿到输出、能拿到退出码。
//
// ⚠️ 被测命令**必须同时往 stdout 和 stderr 写**（P3-20）。只写 stdout 时，
// "stderr 读端被误关"这个 bug 在本用例里完全不可见 —— stdout 走的是另一根管道。
func TestJobCmd_BasicRun(t *testing.T) {
	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", "echo JOBEXEC_SMOKE_OK & echo JOBEXEC_SMOKE_ERR 1>&2"},
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	defer jc.Close()

	so, se := drainBoth(t, jc)
	code, err := jc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(so, "JOBEXEC_SMOKE_OK") {
		t.Errorf("stdout 里没有预期标记，实际:\n%s", so)
	}
	if !strings.Contains(se, "JOBEXEC_SMOKE_ERR") {
		t.Errorf("stderr 里没有预期标记（读端被误关？），实际:\n%q", se)
	}
}

// TestJobCmd_NonZeroExit 非零退出码要能如实取到（exec 工具依赖它报错）。
func TestJobCmd_NonZeroExit(t *testing.T) {
	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", "echo PARTIAL_OUT & exit /b 7"},
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	defer jc.Close()

	so, se := drainBoth(t, jc)
	code, _ := jc.Wait()
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	// 部分输出必须还在（exec 的 "partial output" 机制依赖它）
	if !strings.Contains(so, "PARTIAL_OUT") {
		t.Errorf("丢了部分输出:\n%s", so)
	}
	if strings.TrimSpace(se) != "" {
		t.Errorf("stderr 本用例不应有内容，实际:\n%q", se)
	}
}

// TestJobCmd_KillsGrandchildren 是本批的**核心门禁**。
//
// 场景：cmd /c start ping -t 127.0.0.1 会 spawn 一个**独立进程**的
// ping.exe。os/exec 的 CommandContext 杀不掉它（只能杀 cmd.exe），
// 但 Job Object + KILL_ON_JOB_CLOSE 能杀掉整棵树。
func TestJobCmd_KillsGrandchildren(t *testing.T) {
	jc, err := StartJobCmd(StartJobSpec{
		ExePath:   "cmd.exe",
		Args:      []string{"cmd.exe", "/c", "start /b ping -t 127.0.0.1"},
		Hidden:    true,
		Breakaway: true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	defer jc.Close()

	// 给它一点时间 spawn 出 ping
	time.Sleep(1200 * time.Millisecond)

	root := jc.RootPID()
	before := countPings()
	t.Logf("root pid=%d  Kill 前系统里 ping 进程数=%d", root, before)

	if before == 0 {
		t.Skip("本机没有 ping.exe（精简环境），无法验证孙进程杀树")
	}

	// 杀整棵树
	if err := jc.Kill(); err != nil {
		t.Logf("Kill 返回 error（可能 TerminateJobObject 已杀掉全部，走降级链）: %v", err)
	}

	// 轮询等 ping 消失
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countPings() == 0 {
			t.Log("孙进程已被清空 —— Job 杀树生效")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Kill 之后 5s 仍有 %d 个 ping 进程存活 —— "+
		"Job 杀树没生效（os/exec 也做不到这件事，所以这是本批的核心价值）", countPings())
}

// countPings 数当前系统里的 ping 进程数。
func countPings() int {
	procs, err := SnapshotProcesses()
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range procs {
		if strings.EqualFold(p.Name, "ping.exe") {
			n++
		}
	}
	return n
}

// TestJobCmd_LargeOutput_NoDeadlock 钉死"排空 goroutine 缺失"这个坑。
//
// 计划里明确警告过：管道缓冲约 64KB，而本项目已知有 14.7MB 输出的场景。
// 没有并发排空 → 子进程写满管道阻塞 → Wait 永远等不到。
// 这条测试用 ~2MB 输出（足够超过任何管道缓冲，又不至于跑太久）。
func TestJobCmd_LargeOutput_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过（要跑 ~2MB 输出）")
	}
	// 每行 ~400 字节 × 5000 行 ≈ 2MB
	filler := strings.Repeat("0123456789", 40)
	// 末尾也往 stderr 写一点：只有 stdout 有输出时，"stderr 读端被误关"
	// 在本用例里不可见（io.Copy 的 error 同样必须断言，见下）。
	cmd := fmt.Sprintf("for /L %%i in (1,1,5000) do @echo %s & echo LARGE_STDERR 1>&2", filler)

	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", cmd},
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	defer jc.Close()

	// 注意：这里必须**先并发排空再 Wait**，不能 Wait 完再读
	// ⚠️ **两个管道必须各自一个 goroutine 并发排空**。串行读（先 stdout
	// 后 stderr）是错的：任一管道写满 → 子进程阻塞 → 不退出 → stdout 永远
	// 不 EOF → 串行读永远卡在第一根管道上，45s 超时。旧实现正是串行的，
	// 只因为被测命令恰好只写 stdout 才侥幸没暴露。
	type copyResult struct {
		nOut, nErr int64
		outErr     error
		errErr     error
	}
	done := make(chan copyResult, 1)
	go func() {
		var (
			r  copyResult
			wg sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.nOut, r.outErr = io.Copy(io.Discard, jc.TakeStdoutPipe())
		}()
		go func() {
			defer wg.Done()
			// ⚠️ 旧写法 `_, _ =` 吞掉了 error —— TakeStderrPipe() 返 nil 时
			// io.Copy 立刻返回 os.ErrInvalid 被丢掉，本测试照样绿。
			r.nErr, r.errErr = io.Copy(io.Discard, jc.TakeStderrPipe())
		}()
		wg.Wait()
		done <- r
	}()

	waitDone := make(chan struct{})
	go func() {
		_, _ = jc.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		select {
		case r := <-done:
			var err error
			switch {
			case r.outErr != nil:
				err = fmt.Errorf("io.Copy(stdout): %w", r.outErr)
			case r.errErr != nil:
				err = fmt.Errorf("io.Copy(stderr): %w", r.errErr)
			}
			if err != nil {
				t.Fatalf("排空管道失败: %v —— TakeStdoutPipe()/TakeStderrPipe() 返回了 nil 或坏句柄"+
					"（jobexec.go 父进程关写端时若误关读端，stderrRd 恒为 0）", err)
			}
			t.Logf("~2MB 输出跑完，stdout 读到 %d 字节 / stderr 读到 %d 字节"+
				"（管道排空正常，无死锁）", r.nOut, r.nErr)
		case <-time.After(10 * time.Second):
			t.Fatal("进程退出了但管道没排空 —— 尾部输出会丢")
		}
	case <-time.After(45 * time.Second):
		t.Fatal("45s 还没退出 —— 管道写满导致子进程阻塞（排空 goroutine 缺失/失效）")
	}
}

// TestJobCmd_LargeStderr_NoDeadlock 钉死"父进程没关 stderr 写端"这个坑（P3-20）。
//
// 症状：父进程一直握着 seWr → 子进程 stderr 写满 64KB 管道后永久阻塞 →
// jc.Wait() 阻塞 → 靠 exec.go 的 kill watcher 硬吃满 execTimeoutSec(60s)
// 后报一个**误导性的 timeout**。
//
// 写 ~80KB（刚过 64KB 管道缓冲）到 stderr，断言 Wait 能及时返回。
func TestJobCmd_LargeStderr_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过（要写 ~80KB stderr）")
	}
	// 每行 400 字节 × 200 行 ≈ 80KB
	filler := strings.Repeat("0123456789", 40)
	cmd := fmt.Sprintf("for /L %%i in (1,1,200) do @echo %s 1>&2", filler)

	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", cmd},
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	defer jc.Close()

	// 必须并发排空（stdout 也要读，否则 stdout 的写端没人管）
	var nErr int64
	drained := make(chan error, 1)
	go func() {
		go func() { _, _ = io.Copy(io.Discard, jc.TakeStdoutPipe()) }()
		n, err := io.Copy(io.Discard, jc.TakeStderrPipe())
		nErr = n
		drained <- err
	}()

	waitDone := make(chan struct{})
	go func() {
		_, _ = jc.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(20 * time.Second):
		t.Fatal("20s 还没退出 —— stderr 超过 64KB 却没退出，说明父进程没关 stderr 写端" +
			"（子进程写满管道后永久阻塞，exec 那边表现为假超时）")
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("io.Copy(stderr): %v —— TakeStderrPipe() 返回了 nil 或坏句柄"+
				"（jobexec.go 父进程关写端时若误关读端，stderrRd 恒为 0）", err)
		}
		if nErr < 64*1024 {
			t.Errorf("stderr 只读到 %d 字节，期望 ≥ 64KB（管道没排空）", nErr)
		}
		t.Logf("~80KB stderr 跑完，读到 %d 字节，无死锁", nErr)
	case <-time.After(10 * time.Second):
		t.Fatal("进程退出了但 stderr 管道没排空 —— 尾部输出会丢")
	}
}

// TestJobCmd_HandlesClosed 关掉所有句柄，且不留引用让 GC 提前回收。
func TestJobCmd_HandlesClosed(t *testing.T) {
	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", "echo x"},
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("StartJobCmd: %v", err)
	}
	drainBoth(t, jc)
	_, _ = jc.Wait()

	if err := jc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 重复 Close 必须幂等
	if err := jc.Close(); err != nil {
		t.Errorf("重复 Close 返回错误（应幂等）: %v", err)
	}
	if jc.hJob != 0 || jc.hProc != 0 || jc.stdoutRd != 0 || jc.stderrRd != 0 {
		t.Errorf("Close 后句柄未清零: hJob=%d hProc=%d out=%d err=%d",
			jc.hJob, jc.hProc, jc.stdoutRd, jc.stderrRd)
	}
}

// TestJobCmd_AssignFails_FallsBack 是 docs/12 §七 Q3 的**硬前置条件**：
// 降级链必须在本机可测（Win11 支持嵌套 job，测不出真实触发条件，所以用注入）。
//
// ⚠️ 缺这条测试 T2 不许合入。设计要求：
//  1. 从 **exec 工具入口** 端到端（不是直接调内部函数）—— 否则被替换的
//     var 可能压根没被生产路径调用，重构时改回 pXxx.Call 也照样绿
//  2. **行为断言**（进程确实被杀）优先于日志断言
//  3. 逐失败点注入，不只覆盖 Assign —— 尤其 SetKillOnJobClose 静默失效
//  4. 留一条"Win7 实测 Assign"的待办，不得因单测通过而勾掉
func TestJobCmd_AssignFails_FallsBack(t *testing.T) {
	// 备份并注入"Assign 总是失败"
	orig := procAssignProcessToJobObject
	procAssignProcessToJobObject = func(hJob, hProcess uintptr) error {
		return ErrAssignProcessToJobObject
	}
	defer func() { procAssignProcessToJobObject = orig }()

	var logs []string
	origLog := KillLogFunc
	KillLogFunc = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	defer func() { KillLogFunc = origLog }()

	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", "echo SHOULD_NOT_RUN"},
		Hidden:  true,
	})
	if err == nil {
		jc.Close()
		t.Fatal("Assign 被注入为失败，StartJobCmd 却返回成功 —— 注入点没接进生产路径")
	}
	if !strings.Contains(err.Error(), "AssignProcessToJobObject") {
		t.Errorf("错误信息应指明是 Assign 失败，实际: %v", err)
	}
	t.Logf("注入生效，StartJobCmd 正确返回错误: %v", err)
}

// TestJobCmd_CreateJobObjectFails 逐失败点注入（第 3 条防绕过条款）。
func TestJobCmd_CreateJobObjectFails(t *testing.T) {
	orig := procCreateJobObject
	procCreateJobObject = func() (uintptr, error) { return 0, fmt.Errorf("injected") }
	defer func() { procCreateJobObject = orig }()

	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe", Args: []string{"cmd.exe", "/c", "echo x"}, Hidden: true,
	})
	if err == nil {
		jc.Close()
		t.Fatal("CreateJobObject 被注入为失败，StartJobCmd 却成功")
	}
	if !strings.Contains(err.Error(), "CreateJobObject") {
		t.Errorf("错误信息应指明 CreateJobObject，实际: %v", err)
	}
}

// TestJobCmd_SetKillOnJobCloseFails 注入最容易漏的一级。
func TestJobCmd_SetKillOnJobCloseFails(t *testing.T) {
	orig := procSetKillOnJobClose
	procSetKillOnJobClose = func(hJob uintptr) error { return fmt.Errorf("injected") }
	defer func() { procSetKillOnJobClose = orig }()

	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe", Args: []string{"cmd.exe", "/c", "echo x"}, Hidden: true,
	})
	if err == nil {
		jc.Close()
		t.Fatal("SetKillOnJobClose 被注入为失败，StartJobCmd 却成功")
	}
	if !strings.Contains(err.Error(), "SetKillOnJobClose") {
		t.Errorf("错误信息应指明 SetKillOnJobClose，实际: %v", err)
	}
}

// TestJobCmd_NoHoleInPath 路径含空格必须能工作（args 数组化的意义）。
func TestJobCmd_NoHoleInPath(t *testing.T) {
	dir := t.TempDir() // Windows temp 下通常含空格
	jc, err := StartJobCmd(StartJobSpec{
		ExePath: "cmd.exe",
		Args:    []string{"cmd.exe", "/c", "echo CWD_WORKS"},
		Cwd:     dir,
		Hidden:  true,
	})
	if err != nil {
		t.Fatalf("含空格的 Cwd 启动失败: %v", err)
	}
	defer jc.Close()
	so, _ := drainBoth(t, jc)
	if _, err := jc.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(so, "CWD_WORKS") {
		t.Errorf("输出不对:\n%s", so)
	}
}

// keep os import used if the file's helpers change
var _ = os.Getpid
var _ = runtime.NumCPU

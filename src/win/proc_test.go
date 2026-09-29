package win

import (
	"os"
	"strings"
	"testing"
	"unsafe"
)

// TestSnapshotProcs_Smoke 真调 SnapshotProcesses 拿当前系统进程列表。
// 至少应包含当前 Go test 进程自己。
func TestSnapshotProcs_Smoke(t *testing.T) {
	procs, err := snapshotProcs()
	if err != nil {
		t.Fatalf("snapshotProcs: %v", err)
	}
	pid, _, _ := pGetCurrentProcessId.Call()
	if _, ok := procs[uint32(pid)]; !ok {
		t.Fatalf("snapshot 找不到自己 pid=%d", pid)
	}
	t.Logf("snapshot 找到 %d 个进程, 含 self pid=%d", len(procs), pid)
}

// TestQueryInheritedFromPid_Self 查自己进程的父 PID。
// 测试进程通常在 cmd/powershell 下启动，PPID 应是 cmd/powershell 的 pid。
func TestQueryInheritedFromPid_Self(t *testing.T) {
	pid, _, _ := pGetCurrentProcessId.Call()
	const PROCESS_QUERY_INFORMATION = 0x0400
	hProcess, _, e := pOpenProcess.Call(PROCESS_QUERY_INFORMATION, 0, uintptr(pid))
	if hProcess == 0 {
		t.Skipf("OpenProcess 失败 (err=%v) -- 需要 admin", e)
	}
	defer pCloseHandle.Call(hProcess)
	ppid, err := QueryInheritedFromPid(hProcess)
	if err != nil {
		t.Fatalf("QueryInheritedFromPid: %v", err)
	}
	t.Logf("self pid=%d 父 PID=%d", pid, ppid)
}

// TestOpenProcessCloseHandle 验证 OpenProcess + CloseHandle 这一对能正常往返。
//
// 修 B4：原实现体内零断言 —— 只有 t.Skipf + t.Logf，OpenProcess 失败就静默
// skip、成功就只打一行日志，等于什么都验。这里改成走产品封装的
// OpenProcess/CloseHandle，并给出真断言。
//
// ⚠️ 覆盖边界（B4 变异实测，勿高估本用例）：把 CloseHandle 整个改成 no-op 之后
// **本用例仍然绿**。它证明的是「OpenProcess 遵守 L1 的 (T, error) 契约并返回
// 真句柄，CloseHandle 之后再开同一 pid 仍然正常」，**不**证明"进程句柄总数不增长"
// —— 那需要 GetProcessHandleCount，得动 api_kernel.go + C1 门禁，
// 超出本任务只改测试文件的边界。
func TestOpenProcessCloseHandle(t *testing.T) {
	pid := uint32(os.Getpid())

	h, err := OpenProcess(pid)
	if err != nil {
		// 不再静默 skip：对自己进程做 PROCESS_TERMINATE|PROCESS_QUERY_LIMITED
		// 的 OpenProcess 在任何 Windows 版本上都该成功（含 PE 精简镜像）。
		// 失败说明平台/权限真的有问题，必须红。
		t.Fatalf("OpenProcess(self pid=%d): %v", pid, err)
	}
	if h == 0 {
		t.Fatalf("OpenProcess(self pid=%d) 返回 err=nil 但句柄为 0 —— 违反了 L1 的 (T, error) 契约", pid)
	}
	CloseHandle(h)

	// CloseHandle 之后再开一次同一 pid 必须仍然成功（重复关闭 / 吞错会在这里暴露）。
	h2, err := OpenProcess(pid)
	if err != nil || h2 == 0 {
		t.Fatalf("CloseHandle 后重新 OpenProcess(self pid=%d) 失败: h=%v err=%v", pid, h2, err)
	}
	CloseHandle(h2)
	t.Logf("OpenProcess + CloseHandle x2 OK，最后一次 h=%d", h2)
}

// TestTerminateProcess_NotCrash 验证 TerminateProcess 调非法 handle 返 error 而不 panic。
func TestTerminateProcess_NotCrash(t *testing.T) {
	err := TerminateProcess(uintptr(0xDEADBEEF), 1)
	if err == nil {
		t.Fatal("TerminateProcess(0xDEADBEEF) 应该返 error 但返了 nil")
	}
	t.Logf("TerminateProcess 非法 handle 正确返 err: %v", err)
}

// TestKillTreeSelfContained_PidNotInSnapshot 验证 root pid 不在快照里时入口就 abort。
//
// 这是 proc.go 的 "pid %d no longer present" 分支。原 TestKillTreeSelfContained_InvalidPid
// 实际只覆盖到了这里（详见下面 M2(a) 用例的说明），这里把它补成一条**精确**门禁：
// 必须命中这一条消息，不能是 "PID reused"（那是另一个分支，见下）。
func TestKillTreeSelfContained_PidNotInSnapshot(t *testing.T) {
	killed, errs := KillTreeSelfContained(0xFFFFFFFF, "definitely-not-a-real-image.exe")
	if killed != 0 {
		t.Fatalf("root 不在快照里必须整轮 abort，实际却杀了 %d 个进程", killed)
	}
	if len(errs) != 1 {
		t.Fatalf("errs 期望恰好 1 条（入口 abort），实际 %d 条: %q", len(errs), errs)
	}
	if !strings.Contains(errs[0], "no longer present") {
		t.Fatalf("errs[0] = %q，期望含 'no longer present'（入口 abort 分支）", errs[0])
	}
	t.Logf("KillTreeSelfContained(0xFFFFFFFF) killed=%d errs=%v", killed, errs)
}

// TestKillTreeSelfContained_M2aRootNameMismatch 是 **M2(a) 的真门禁**：
// root PID 存在但映像名与 expectName 不符时，必须整轮中止、**一个都不杀**。
//
// ⚠️ 这里必须用真实 PID（os.Getpid()）。历史版本传 0xFFFFFFFF，
// 那个 PID 不在快照里，在 proc.go 的 "not present" 分支就 abort 了，
// **永远到不了 M2(a) 的名字校验** —— 门禁是假的。这正是 M2(a) 在 kill 工具里
// 失效了这么久都没被发现的直接原因（失效本身已在 P3-21 修掉，门禁的洞留到 B4 才补）。
//
// 断言 `errs` 含 "PID reused" 本身就是分支判据：入口的 "no longer present"
// 消息里没有这几个字，所以这条断言不可能被错误分支满足。
func TestKillTreeSelfContained_M2aRootNameMismatch(t *testing.T) {
	// 前置守卫：self 必须真在快照里，否则下面会退回 "no longer present" 分支，
	// 用例又退化成假覆盖。这里真出问题时宁可 skip 也不要给一个假绿。
	self := uint32(os.Getpid())
	procs, err := snapshotProcs()
	if err != nil {
		t.Fatalf("snapshotProcs: %v", err)
	}
	if _, ok := procs[self]; !ok {
		t.Skipf("self pid=%d 不在快照里，无法验证 M2(a) 的名字校验", self)
	}

	// ⚠️ 这里必须用真实 PID（os.Getpid()）。历史版本传 0xFFFFFFFF，
	// 那个 PID 不在快照里，在 proc.go 的 "not present" 分支就 abort 了，
	// **永远到不了 M2(a) 的名字校验** —— 门禁是假的。
	killed, errs := KillTreeSelfContained(self, "definitely-not-this-image.exe")
	if killed != 0 {
		t.Fatalf("M2(a) 失效：名字不符却杀了 %d 个进程", killed)
	}
	joined := strings.Join(errs, "; ")
	if !strings.Contains(joined, "PID reused") {
		t.Fatalf("期望 errs 含 'PID reused'（M2(a) 整轮中止），实际: %q", joined)
	}
	// 反向判据：不能是入口那个 "not present" 分支（那正是本用例的历史假绿）。
	if strings.Contains(joined, "no longer present") {
		t.Fatalf("M2(a) 用例退化了：命中的是入口 'no longer present' 分支而非名字校验，实际: %q", joined)
	}
	// 消息里应带上真实的映像名，便于排障时确认比对的是哪个名字。
	if !strings.Contains(joined, procs[self].Name) {
		t.Fatalf("errs 未带出真实映像名 %q，实际: %q", procs[self].Name, joined)
	}
	t.Logf("M2(a) 真门禁命中：self pid=%d 映像名=%q，killed=%d errs=%v",
		self, procs[self].Name, killed, errs)
}

// TestTreeOf_Empty 测试空 map 上 treeOf 不死循环。
func TestTreeOf_Empty(t *testing.T) {
	all := map[uint32]ProcessInfo{}
	order := treeOf(all, 1234)
	if len(order) != 1 || order[0] != 1234 {
		t.Errorf("空 map 上 treeOf 应返 [1234], 实际 %v", order)
	}
}

// TestTreeOf_Basic 测试两层父子。
func TestTreeOf_Basic(t *testing.T) {
	all := map[uint32]ProcessInfo{
		100: {Name: "root", PPID: 0},
		200: {Name: "child1", PPID: 100},
		300: {Name: "child2", PPID: 100},
		400: {Name: "grandchild", PPID: 200},
	}
	order := treeOf(all, 100)
	wantLen := 4
	if len(order) != wantLen {
		t.Errorf("treeOf 长度 = %d, 期望 %d (order=%v)", len(order), wantLen, order)
	}
	// 第一个必须是 root
	if order[0] != 100 {
		t.Errorf("order[0] 应为 100, 实际 %d", order[0])
	}
}

// TestSizeProcessEntry32_PlatformDoc 文档化 processEntry32 尺寸。
func TestSizeProcessEntry32_PlatformDoc(t *testing.T) {
	sz := unsafe.Sizeof(processEntry32{})
	ptrSize := unsafe.Sizeof(uintptr(0))
	t.Logf("processEntry32 sizeof = %d 字节 (指针 %d 字节, %d-bit 平台, sp 预期 386=556)",
		sz, ptrSize, ptrSize*8)
	// 386 上应是 556（T4-9 补的真断言就在下面几行，不靠这条注释背书）
	// amd64 上：12 字段 4+8 对齐 + ExeFile[260]uint16 (520) = 实际可能 592 或不同
	// 这里**不**断言 amd64 尺寸，让 sp -diag 在真 PE 上看。
	// 【T4-9】原来这个函数**只有 t.Logf，零断言** —— 名字带 Size、注释写着
	// "386 上应是 556"，却什么都没验。而 processEntry32 正是 M2 双层防护
	// 依赖的结构体，§S1 铁律（手写 Win32 结构体含 64 位成员 → 386 对齐风险）
	// 正是被尺寸错位坑出来的。补真断言。
	if ptrSize == 4 && sz != 556 {
		t.Fatalf("processEntry32 在 386 上 sizeof = %d，期望 556 —— "+
			"M2 的进程快照依赖这个尺寸，错位会让枚举结果全错", sz)
	}
}

func TestSizeProcessBasicInformation_PlatformDoc(t *testing.T) {
	sz := unsafe.Sizeof(processBasicInformation{})
	t.Logf("processBasicInformation sizeof = %d 字节 (预期 386=24, amd64=48)", sz)
	if sz != 24 && sz != 48 {
		t.Errorf("processBasicInformation sizeof=%d, 预期 24 (386) 或 48 (amd64)", sz)
	}
}

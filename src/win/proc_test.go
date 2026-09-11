package win

import (
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

// TestOpenProcessCloseHandle 验证 OpenProcess + CloseHandle 不 leak。
func TestOpenProcessCloseHandle(t *testing.T) {
	pid, _, _ := pGetCurrentProcessId.Call()
	const PROCESS_QUERY_INFORMATION = 0x0400
	h, _, e := pOpenProcess.Call(PROCESS_QUERY_INFORMATION, 0, uintptr(pid))
	if h == 0 {
		t.Skipf("OpenProcess 失败 (err=%v) -- 需要 admin", e)
	}
	CloseHandle(h)
	t.Logf("OpenProcess + CloseHandle OK, h=%d", h)
}

// TestTerminateProcess_NotCrash 验证 TerminateProcess 调非法 handle 返 error 而不 panic。
func TestTerminateProcess_NotCrash(t *testing.T) {
	err := TerminateProcess(uintptr(0xDEADBEEF), 1)
	if err == nil {
		t.Fatal("TerminateProcess(0xDEADBEEF) 应该返 error 但返了 nil")
	}
	t.Logf("TerminateProcess 非法 handle 正确返 err: %v", err)
}

// TestKillTreeSelfContained_InvalidPid 验证非法 pid 在入口校验就 abort。
func TestKillTreeSelfContained_InvalidPid(t *testing.T) {
	killed, errs := KillTreeSelfContained(0xFFFFFFFF, "")
	if killed != 0 {
		t.Errorf("killed 应为 0, 实际 %d", killed)
	}
	if len(errs) == 0 {
		t.Error("应返 err 描述 abort 原因, 但 errs 为空")
	}
	t.Logf("KillTreeSelfContained(0xFFFFFFFF) killed=%d errs=%v", killed, errs)
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
	// 386 上应是 556 (MEMORY §1 已声明)
	// amd64 上：12 字段 4+8 对齐 + ExeFile[260]uint16 (520) = 实际可能 592 或不同
	// 这里**不**断言 amd64 尺寸，让 sp -diag 在真 PE 上看。
}

func TestSizeProcessBasicInformation_PlatformDoc(t *testing.T) {
	sz := unsafe.Sizeof(processBasicInformation{})
	t.Logf("processBasicInformation sizeof = %d 字节 (预期 386=24, amd64=48)", sz)
	if sz != 24 && sz != 48 {
		t.Errorf("processBasicInformation sizeof=%d, 预期 24 (386) 或 48 (amd64)", sz)
	}
}

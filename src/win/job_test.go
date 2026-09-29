package win

import (
	"runtime"
	"testing"
	"unsafe"
)

// TestBuildJobExtLimitInfo_Sizes 是 P1-5 的核心回归门禁（verifier 报告 "必跑 -diag 断言"）：
// 386 上 buildJobExtLimitInfo 必须返 112 字节，amd64 上必须返 144 字节。
// LimitFlags 必须写在偏移 16 的位置（两个 LARGE_INTEGER 之后）。
//
// 任何未来对 buildJobExtLimitInfo 的改动如果让这个 test 失败，说明破坏
// 了 386 字节缓冲契约，会让 32 位 PE 上的 KILL_ON_JOB_CLOSE 静默失效。
func TestBuildJobExtLimitInfo_Sizes(t *testing.T) {
	buf := buildJobExtLimitInfo(false)
	wantSize := jobExtLimitInfoSizeX64
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantSize = jobExtLimitInfoSizeX86
	}
	if len(buf) != wantSize {
		t.Fatalf("buildJobExtLimitInfo 长度 = %d, 期望 %d (架构 %d-bit, 必跑 -diag 契约)",
			len(buf), wantSize, unsafe.Sizeof(uintptr(0))*8)
	}
	t.Logf("buildJobExtLimitInfo: %d 字节 (架构 %d-bit, 期望 %d)",
		len(buf), unsafe.Sizeof(uintptr(0))*8, wantSize)
}

func TestBuildJobExtLimitInfo_LimitFlagsAt16(t *testing.T) {
	// LimitFlags 在偏移 16 字节。killOnJobClose=true 时该位置必须写入 0x00002000。
	buf := buildJobExtLimitInfo(true)
	if len(buf) < 20 {
		t.Fatalf("buf 太短: %d 字节", len(buf))
	}
	// 用 4 字节 little-endian 读 LimitFlags
	flags := uint32(buf[16]) | uint32(buf[17])<<8 | uint32(buf[18])<<16 | uint32(buf[19])<<24
	want := uint32(jobObjectLimitKillOnJobClose)
	if flags != want {
		t.Fatalf("LimitFlags @ offset 16 = 0x%x, 期望 0x%x", flags, want)
	}
	t.Logf("LimitFlags @ offset 16 = 0x%x (架构 %d-bit, 偏移正确)",
		flags, unsafe.Sizeof(uintptr(0))*8)
}

func TestBuildJobExtLimitInfo_NoKill(t *testing.T) {
	// killOnJobClose=false 时 LimitFlags 必须为 0
	buf := buildJobExtLimitInfo(false)
	flags := uint32(buf[16]) | uint32(buf[17])<<8 | uint32(buf[18])<<16 | uint32(buf[19])<<24
	if flags != 0 {
		t.Fatalf("killOnJobClose=false 时 LimitFlags = 0x%x, 期望 0", flags)
	}
}

func TestCreateJobObject_Smoke(t *testing.T) {
	h, err := CreateJobObject()
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	if h == 0 {
		t.Fatal("CreateJobObject 返 0")
	}
	defer pCloseHandle.Call(h)
	t.Logf("CreateJobObject: hJob=%d", h)
}

func TestSetKillOnJobClose_RealCall(t *testing.T) {
	// 真实调 SetInformationJobObject，验证 386=112 / amd64=144 字节缓冲真能用
	h, err := CreateJobObject()
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	defer pCloseHandle.Call(h)

	if err := SetKillOnJobClose(h); err != nil {
		t.Fatalf("SetKillOnJobClose (架构 %d-bit): %v", unsafe.Sizeof(uintptr(0))*8, err)
	}
	t.Logf("SetKillOnJobClose 成功 (架构 %d-bit, cbSize=%d)",
		unsafe.Sizeof(uintptr(0))*8, jobExtLimitInfoSizeX64)
}

func TestIsProcessInJob_Smoke(t *testing.T) {
	// 断言 1：必须真的调用 IsProcessInJob，且**不崩**。
	// 旧版这个测试因为"会崩"而 t.Skip + t.Logf，等于零断言给地雷盖章。
	// 现在的实现是诚实降级（恒返 false, nil），所以可以真调。
	pid, _, _ := pGetCurrentProcessId.Call()
	const PROCESS_QUERY_INFORMATION = 0x0400
	hProcess, _, e := pOpenProcess.Call(PROCESS_QUERY_INFORMATION, 0, uintptr(pid))
	if hProcess == 0 {
		t.Skipf("OpenProcess(PROCESS_QUERY_INFORMATION) 失败 (err=%v) —— 无法自进程探测，跳过", e)
	}
	defer pCloseHandle.Call(hProcess)

	inJob, err := IsProcessInJob(hProcess)
	if err != nil {
		t.Fatalf("IsProcessInJob(hProcess=%#x) 返回 err=%v，期望 nil（诚实降级不应报错）", hProcess, err)
	}
	// 断言 2：返回值语义 —— 降级实现必须恒 false。
	// 若这里变成 true，说明有人把探测接回来了，而探测在 Win11 实测会猝死进程。
	if inJob {
		t.Fatalf("IsProcessInJob 返回 true，但本实现是诚实降级，恒返 false —— " +
			"说明有人重新接回了 kernel32!IsProcessInJob（实测 0xc0000005 猝死）")
	}
	t.Logf("IsProcessInJob(hProcess=%#x) = (%v, %v) —— 未崩溃，符合诚实降级契约", hProcess, inJob, err)
}

func TestIsProcessInJob_NeverPanics(t *testing.T) {
	// 断言：伪句柄（-1）和 0 这两个"历史上喂进去就猝死"的入参，
	// 现在都必须安静返回 (false, nil)，不崩、不报错。
	//
	// 实测背景（探针，386 + amd64 双架构）：kernel32!IsProcessInJob 只要 hJob
	// 通过句柄校验就会 AV；这里三个入参都不会走到 kernel32（实现不探测），
	// 但本测试把"不会猝死"钉成回归门禁，防止将来有人改回探测。
	pseudo, _, _ := pGetCurrentProcess.Call()
	cases := []struct {
		name     string
		hProcess uintptr
	}{
		{"GetCurrentProcess() pseudo", pseudo},
		{"NULL handle", 0},
	}
	for _, c := range cases {
		inJob, err := IsProcessInJob(c.hProcess)
		if err != nil {
			t.Errorf("IsProcessInJob(%s = %#x) 返回 err=%v，期望 nil", c.name, c.hProcess, err)
		}
		if inJob {
			t.Errorf("IsProcessInJob(%s = %#x) 返回 true，期望 false（诚实降级）", c.name, c.hProcess)
		}
		t.Logf("IsProcessInJob(%s = %#x) = (%v, nil) 未崩溃", c.name, c.hProcess, inJob)
	}
}

func TestJobSizeContract_PlatformDoc(t *testing.T) {
	// 文档化当前平台的 size，让 spike -diag 输出可比
	ptrSize := unsafe.Sizeof(uintptr(0))
	t.Logf("指针大小=%d 字节 (%d-bit 平台)", ptrSize, ptrSize*8)
	t.Logf("jobExtLimitInfoSizeX86 = %d (386/amd64 测 MSVC)", jobExtLimitInfoSizeX86)
	t.Logf("jobExtLimitInfoSizeX64 = %d (amd64 测 MSVC)", jobExtLimitInfoSizeX64)
	t.Logf("jobLimitFlagsOffset = %d (两种架构一致)", jobLimitFlagsOffset)
	t.Logf("当前 Go runtime: %s, GOARCH=windows/%s", runtime.Version(), runtime.GOARCH)

	// 【T4-3】原来这个函数**只有 t.Logf，零断言** —— 而 §S1 铁律
	//（含 64 位成员的手写 Win32 结构体有 386 对齐风险）恰恰是本项目最贵的一课
	//（jobExtLimitInfo 的 108 vs 112 错位曾让 KILL_ON_JOB_CLOSE 静默失效）。
	// 同族的 TestBuildJobExtLimitInfo_Sizes 才是真门禁，这里补齐同款断言。
	if jobExtLimitInfoSizeX86 != 112 {
		t.Errorf("jobExtLimitInfoSizeX86 = %d，期望 112（MSVC x86，8 字节对齐 LARGE_INTEGER）",
			jobExtLimitInfoSizeX86)
	}
	if jobExtLimitInfoSizeX64 != 144 {
		t.Errorf("jobExtLimitInfoSizeX64 = %d，期望 144（MSVC x64）", jobExtLimitInfoSizeX64)
	}
	if jobLimitFlagsOffset != 16 {
		t.Errorf("jobLimitFlagsOffset = %d，期望 16（两个 LARGE_INTEGER 之后，两架构同值）",
			jobLimitFlagsOffset)
	}
}

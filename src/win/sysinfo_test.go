package win

import "testing"

// 8 个 smoke test，每个调一次 Win32 API 并验证返值非零/非空。
// v1-L1 核心契约：所有 API 调用必须返 (T, error)，失败时调用方能立刻看到。
// 这些 test 同时验证"声明的 proc 名拼写对、struct 布局对、Length 字段设对"。

func TestMemoryStatus_Smoke(t *testing.T) {
	m, err := MemoryStatus()
	if err != nil {
		t.Fatalf("MemoryStatus: %v", err)
	}
	if m.Length == 0 {
		t.Fatal("MemoryStatus.Length = 0 after success (cbSize 没设?)")
	}
	if m.TotalPhys == 0 {
		t.Fatal("MemoryStatus.TotalPhys = 0 (64-bit 读不出来?)")
	}
	t.Logf("TotalPhys=%d MB, AvailPhys=%d MB, Load=%d%%, AvailPageFile=%d MB, AvailVirtual=%d MB",
		m.TotalPhys/1024/1024, m.AvailPhys/1024/1024, m.MemoryLoad,
		m.AvailPageFile/1024/1024, m.AvailVirtual/1024/1024)
}

func TestOSVersion_Smoke(t *testing.T) {
	v, err := OSVersion()
	if err != nil {
		t.Fatalf("OSVersion: %v", err)
	}
	if v.MajorVersion == 0 {
		t.Fatal("OSVersion.MajorVersion = 0 (RtlGetVersion 没读到?)")
	}
	t.Logf("OS %d.%d build %d (PlatformId=%d)",
		v.MajorVersion, v.MinorVersion, v.BuildNumber, v.PlatformId)
}

func TestNativeSystemInfo_Smoke(t *testing.T) {
	s, err := NativeSystemInfo()
	if err != nil {
		t.Fatalf("NativeSystemInfo: %v", err)
	}
	if s.PageSize == 0 {
		t.Fatal("SystemInfo.PageSize = 0 (void 函数没填 struct?)")
	}
	t.Logf("PageSize=%d, Procs=%d, Arch=%d", s.PageSize, s.NumberOfProcessors, s.ProcessorArchitecture)
}

func TestTickCount_Smoke(t *testing.T) {
	t1 := TickCount()
	t2 := TickCount()
	if t2 < t1 {
		t.Fatalf("TickCount went backwards: %d -> %d", t1, t2)
	}
	t.Logf("TickCount=%d ms (delta=%d)", t2, t2-t1)
}

func TestLogicalDrives_Smoke(t *testing.T) {
	d, err := LogicalDrives()
	if err != nil {
		t.Fatalf("LogicalDrives: %v", err)
	}
	if d == 0 {
		t.Fatal("LogicalDrives = 0 (没找到任何盘符?)")
	}
	t.Logf("LogicalDrives=0x%x (bits set = A: Z:)", d)
}

func TestDriveType_Smoke(t *testing.T) {
	dt, err := DriveType("C:\\")
	if err != nil {
		t.Fatalf("DriveType: %v", err)
	}
	if dt == 0 {
		t.Fatal("DriveType(C:\\) = 0 (DRIVE_UNKNOWN -- 盘符不存在?)")
	}
	t.Logf("C:\\ drive type = %d (3=FIXED)", dt)
}

func TestComputerName_Smoke(t *testing.T) {
	n, err := ComputerName()
	if err != nil {
		t.Fatalf("ComputerName: %v", err)
	}
	if n == "" {
		t.Fatal("ComputerName empty")
	}
	t.Logf("ComputerName=%q", n)
}

func TestUserName_Smoke(t *testing.T) {
	u, err := UserName()
	if err != nil {
		t.Fatalf("UserName: %v", err)
	}
	if u == "" {
		t.Fatal("UserName empty")
	}
	t.Logf("UserName=%q", u)
}

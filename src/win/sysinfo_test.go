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
	t1, err := TickCount()
	if err != nil {
		t.Fatalf("TickCount: %v", err)
	}
	t2, err := TickCount()
	if err != nil {
		t.Fatalf("TickCount: %v", err)
	}
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

// 下面两个是 diskinfo 工具新增的底层 API（T3 接线）。

func TestDiskFreeSpace_Smoke(t *testing.T) {
	d, err := DiskFreeSpace("C:\\")
	if err != nil {
		t.Fatalf("DiskFreeSpace(C:\\): %v", err)
	}
	if d.Total == 0 {
		t.Fatal("DiskFreeSpace(C:\\).Total = 0（GetDiskFreeSpaceExW 没读到？）")
	}
	if d.FreeAvail > d.Total {
		t.Fatalf("FreeAvail %d > Total %d", d.FreeAvail, d.Total)
	}
	t.Logf("Total=%d GB, FreeAvail=%d GB, FreeTotal=%d GB",
		d.Total/1024/1024/1024, d.FreeAvail/1024/1024/1024, d.FreeTotal/1024/1024/1024)
}

func TestDiskFreeSpace_EmptyPath(t *testing.T) {
	// Ptr("") 返 ErrEmpty —— 必须透传成 err，不能 panic、不能返零值 DiskSpace。
	if _, err := DiskFreeSpace(""); err == nil {
		t.Fatal("DiskFreeSpace(\"\") 应返 err")
	}
}

func TestVolumeLabel_Smoke(t *testing.T) {
	label, err := VolumeLabel("C:\\")
	if err != nil {
		t.Fatalf("VolumeLabel(C:\\): %v", err)
	}
	// label 可以是 ""（未格式化分区 / RAW 卷）—— 那不是错误。
	t.Logf("VolumeLabel(C:\\)=%q", label)
}

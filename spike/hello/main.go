//go:build windows

// spike/hello -- P0-1 + P0-6
//
// 验证两件事：
//   P0-1  Go 1.20 编出来的 exe 能不能在目标 PE 里跑起来
//   P0-6  32 位 Go 在低内存 PE 里的分配表现
//
// 重要：输出全部用 ASCII。PE 控制台是 OEM 代码页，中文标签会乱码，
// 诊断工具的输出一旦乱码就完全失去意义（对应审核 A2 那条坑）。
// 源码本身的注释用中文没问题 —— Go 源文件永远是 UTF-8，不存在 MSVC 那种代码页问题。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	pGetLogicalDrives     = kernel32.NewProc("GetLogicalDrives")
	pGetDriveTypeW        = kernel32.NewProc("GetDriveTypeW")
	pGetComputerNameW     = kernel32.NewProc("GetComputerNameW")
	pGetNativeSystemInfo  = kernel32.NewProc("GetNativeSystemInfo")
	pGetTickCount         = kernel32.NewProc("GetTickCount")
	pRtlGetVersion        = ntdll.NewProc("RtlGetVersion")
	pGetUserNameW         = advapi32.NewProc("GetUserNameW")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// RTL_OSVERSIONINFOW（不含 EX 那一段）。
// 用 ntdll!RtlGetVersion 而不是 GetVersionEx —— 后者在 Win8.1+ 无 manifest 时
// 会返回假版本号（对应 docs/02 的 API 红线）。
type rtlOSVersionInfoW struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformId        uint32
	CSDVersion        [128]uint16
}

type systemInfo struct {
	ProcessorArchitecture     uint16
	Reserved                  uint16
	PageSize                  uint32
	MinimumApplicationAddress uintptr
	MaximumApplicationAddress uintptr
	ActiveProcessorMask       uintptr
	NumberOfProcessors        uint32
	ProcessorType             uint32
	AllocationGranularity     uint32
	ProcessorLevel            uint16
	ProcessorRevision         uint16
}

// mem 返回当前系统内存状态。API 失败时返回 (zero, error)，
// 调用方必须检查 error —— 否则全零结构体会被 allocTest 误读为
// "地址空间已耗尽"，报告 "0 MB fell to 0 MB" 完全误导
// （第四轮独立审计 L1 修法）。
func mem() (memoryStatusEx, error) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, err := pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return m, fmt.Errorf("GlobalMemoryStatusEx: %v", err)
	}
	return m, nil
}

// 立刻把内存压力打出来，不缓存
func memLine(tag string) {
	m, err := mem()
	if err != nil {
		fmt.Printf("  [%-8s] <mem api failed: %v>\n", tag, err)
		return
	}
	fmt.Printf("  [%-8s] phys total=%.0f MB avail=%.0f MB  load=%d%%   virt avail=%.0f MB\n",
		tag,
		float64(m.TotalPhys)/1024/1024,
		float64(m.AvailPhys)/1024/1024,
		m.MemoryLoad,
		float64(m.AvailVirtual)/1024/1024)
}

func osVersion() (uint32, uint32, uint32, string) {
	var vi rtlOSVersionInfoW
	vi.OSVersionInfoSize = uint32(unsafe.Sizeof(vi))
	r, _, err := pRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi)))
	if r != 0 {
		return 0, 0, 0, fmt.Sprintf("RtlGetVersion failed: %v", err)
	}
	sp := syscall.UTF16ToString(vi.CSDVersion[:])
	return vi.MajorVersion, vi.MinorVersion, vi.BuildNumber, sp
}

func computerName() string {
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	r, _, err := pGetComputerNameW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return fmt.Sprintf("<err %v>", err)
	}
	return syscall.UTF16ToString(buf[:n])
}

func userName() string {
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	r, _, err := pGetUserNameW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return fmt.Sprintf("<err %v>", err)
	}
	return syscall.UTF16ToString(buf[:n])
}

var driveTypeName = map[uintptr]string{
	0: "UNKNOWN", 1: "NO_ROOT", 2: "REMOVABLE", 3: "FIXED",
	4: "REMOTE", 5: "CDROM", 6: "RAMDISK",
}

func drives() []string {
	mask, _, _ := pGetLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		p, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		dt, _, _ := pGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
		out = append(out, fmt.Sprintf("%s %s", root, driveTypeName[dt]))
	}
	return out
}

// 写一个文件再读回来 —— PE 里 X: 是内存盘，必须验证可写
func fileTest(dir, label string) {
	if dir == "" {
		fmt.Printf("  [%s] skipped (empty dir)\n", label)
		return
	}
	p := filepath.Join(dir, "pe_spike_hello.txt")
	payload := []byte("pe-spike-hello " + time.Now().Format(time.RFC3339) + "\r\n")
	if err := os.WriteFile(p, payload, 0644); err != nil {
		fmt.Printf("  [%s] WRITE FAILED %s: %v\n", label, p, err)
		return
	}
	got, err := os.ReadFile(p)
	if err != nil {
		fmt.Printf("  [%s] READ BACK FAILED %s: %v\n", label, p, err)
		return
	}
	same := string(got) == string(payload)
	fmt.Printf("  [%s] write+read %s  (%d bytes)  %s\n", label, p, len(got),
		map[bool]string{true: "OK", false: "MISMATCH"}[same])
}

// tryAlloc 只是最后一道网：接住**一般性 panic**。
//
// ⚠️ 实测教训（三轮代码审计 C2 的修法在这里被推翻了）：
// 一开始以为用 recover 就能兜住"地址空间耗尽"。**不行**。Go 的分配器在拿不到
// 地址空间时调的是 runtime.throw("out of memory") —— 那是不可恢复的致命错误，
// 不是 panic，recover 完全接不住，进程照样带 goroutine dump 退出。
// （实测：hello_386.exe -alloc 3000 依旧打了一整屏栈。）
// 所以真正的手段是下面的**分配前余量检查**；这个函数只用来兜住别的 panic 类型。
func tryAlloc(n int) (b []byte, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			b, ok = nil, false
		}
	}()
	return make([]byte, n), true
}

// P0-6：持续分配并持有，看 32 位进程能撑到多少。
//
// 核心机制是"每轮分配前先看地址空间余量，快见底就主动停手并报告" ——
// 因为 OOM 是不可恢复的 throw，只能在它发生之前刹车。
func allocTest(mb int) {
	fmt.Printf("\n-- allocation test: holding %d MB in 1 MB chunks --\n", mb)
	memLine("before")

	// 32 位下 Go 的堆是向系统**按 arena 预留**地址空间的（一次 64MB on 386），
	// 所以余量必须留得比一个 arena 大，否则刹车了还是可能在预留时 throw。
	const safetyMarginMB = 224

	keep := make([][]byte, 0, 256)
	held := 0
	stopped := ""
	for i := 0; i < mb; i++ {
		// [audit] 原来只读 AvailVirtual —— 但下面真碰页会把虚拟地址提交成物理页，
		// 撞的是**提交上限**（commit limit），不是虚拟地址空间上限。
		// 在"小物理内存 + 小页面文件"的 PE 上，commit 上限可能比虚拟地址空间先耗尽，
		// AvailVirtual 还宽裕时进程就被 OS 直接 STATUS_COMMITMENT_LIMIT 杀掉，
		// 然后整条探针的结论 —— "32 位下能持有多少内存" —— 拿不到任何数据。
		// 两个都要看，谁先到谁叫停。
		m, err := mem()
		if err != nil {
			// 第四轮独立审计 L1：API 失败时不要继续 —— 全零结构体会被下面
			// 误读为"地址空间跌到 0 MB"，污染整条探针的结论。
			fmt.Printf("  GlobalMemoryStatusEx failed: %v\n", err)
			fmt.Println("  cannot probe memory; aborting allocTest to avoid bogus \"0 MB left\" report")
			return
		}
		availVirtMB := float64(m.AvailVirtual) / 1024 / 1024
		availCommitMB := float64(m.AvailPageFile) / 1024 / 1024
		if availVirtMB < safetyMarginMB {
			stopped = fmt.Sprintf(
				"stopped early: available virtual address space fell to %.0f MB (< %d MB safety margin)",
				availVirtMB, safetyMarginMB)
			break
		}
		if availCommitMB < safetyMarginMB {
			stopped = fmt.Sprintf(
				"stopped early: commit limit only %.0f MB left (< %d MB) — small pagefile, would be killed by OS before OOM",
				availCommitMB, safetyMarginMB)
			break
		}
		b, ok := tryAlloc(1 << 20)
		if !ok {
			stopped = "stopped early: allocation returned a failure"
			break
		}
		// 真碰一下，强制提交物理页（不然 Go 只预留地址空间、不算真的用上了）
		for j := 0; j < len(b); j += 4096 {
			b[j] = byte(i)
		}
		keep = append(keep, b)
		held++
		if held%64 == 0 {
			memLine(fmt.Sprintf("+%dMB", held))
		}
	}

	memLine("after")
	runtime.GC()
	memLine("gc")
	fmt.Printf("  held %d MB of %d requested\n", held, mb)
	if stopped != "" {
		fmt.Printf("  %s\n", stopped)
		fmt.Println("  NOTE: this is the PROBE's own stop point, not the process ceiling.")
		fmt.Println("        The agent itself only needs tens of MB, so this is not a risk.")
	}
	fmt.Printf("  runtime.MemStats HeapSys = %.0f MB\n", float64(readHeapSys())/1024/1024)
	// 用 KeepAlive 而不是 `_ = keep`：后者在编译器眼里是死代码，
	// keep 可能在循环结束前就被判定为不可达，测出来的数字就没意义了。
	runtime.KeepAlive(keep)
}

func readHeapSys() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapSys
}

func main() {
	allocMB := flag.Int("alloc", 0, "allocate and hold N MB to probe 32-bit address space")
	flag.Parse()

	fmt.Println("========================================================")
	fmt.Println(" pe-spike-hello  (P0-1 runs / P0-6 memory)")
	fmt.Println("========================================================")

	fmt.Printf("Go runtime     : %s\n", runtime.Version())
	fmt.Printf("GOOS/GOARCH    : %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("NumCPU         : %d   GOMAXPROCS=%d\n", runtime.NumCPU(), runtime.GOMAXPROCS(0))
	fmt.Printf("pointer size   : %d-bit\n", 32<<(^uintptr(0)>>63))

	maj, min, build, sp := osVersion()
	fmt.Printf("OS via RtlGetVersion : %d.%d build %d  sp=%q\n", maj, min, build, sp)

	var si systemInfo
	pGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&si)))
	const (
		archIntel = 0
		archARM   = 12
		archIA64  = 6
		archAMD64 = 9
	)
	archName := map[uint16]string{archIntel: "x86", archARM: "ARM", archIA64: "IA64", archAMD64: "x64"}[si.ProcessorArchitecture]
	fmt.Printf("native arch          : %s (%d)  procs=%d  pagesize=%d\n",
		archName, si.ProcessorArchitecture, si.NumberOfProcessors, si.PageSize)
	fmt.Printf("VA range             : 0x%08X .. 0x%08X\n",
		si.MinimumApplicationAddress, si.MaximumApplicationAddress)

	fmt.Printf("computer name  : %s\n", computerName())
	fmt.Printf("user name      : %s\n", userName())
	fmt.Printf("cwd            : %s\n", mustCwd())
	fmt.Printf("exe path       : %s\n", mustExe())
	fmt.Printf("TEMP           : %s\n", os.TempDir())

	tick, _, _ := pGetTickCount.Call()
	fmt.Printf("uptime (GetTickCount) : %d s  (%.2f h)\n", tick/1000, float64(tick)/3600000)

	fmt.Printf("\nlogical drives :\n")
	for _, d := range drives() {
		fmt.Printf("  %s\n", d)
	}

	fmt.Printf("\nmemory:\n")
	memLine("now")

	fmt.Printf("\nfile write/read test:\n")
	fileTest(filepath.Dir(mustExe()), "exedir")
	fileTest(os.TempDir(), "temp")

	if *allocMB > 0 {
		allocTest(*allocMB)
	}

	fmt.Println("\nRESULT: hello completed without crashing.")
	os.Exit(0)
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "<err " + err.Error() + ">"
	}
	return d
}

func mustExe() string {
	e, err := os.Executable()
	if err != nil {
		return "<err " + err.Error() + ">"
	}
	return strings.TrimSpace(e)
}

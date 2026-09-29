// Package win: sysinfo.go 实现系统信息查询。
//
// v1-L1 硬规则（PLAN §0.9 §7）：所有"小结构体 + 必填 cbSize"API 调，
// 必须返 (T, error)。调用方见到 err 立即 return + 打 WARN，绝不能让
// 零值结构体继续走探针逻辑 —— 零值会被读为"地址空间 0 MB"污染整条
// 探针结论（spike/hello/main.go:79-89 的 v1-L1 修法复刻）。
//
// 显式声明：本文件**不**使用 GetTickCount64（MEMORY §10：Win7 PE 缺，
// 运行时炸）。要 64-bit tick 用 GetTickCount 配合 uint32 wraparound。
// 也不使用 GetVersionExA（MEMORY §10：Win8.1+ 无 manifest 返假值），
// 改用 RtlGetVersion。

package win

import (
	"errors"
	"fmt"
	"unsafe"
)

// MemoryStatusEx 是 GlobalMemoryStatusEx 的返回值。
// 字段顺序与 Win32 文档完全一致。**尺寸两架构都是 64 字节**
// （2×uint32 + 7×uint64；MSVC x86 也把 DWORDLONG 对齐到 8）。
// ⚠️ 历史注释曾误写 72 —— 照它"修正"会让 GlobalMemoryStatusEx 返
// ERROR_INVALID_PARAMETER（实测 cbLength=64 成功、36 失败）。
// 教训：72 从来不是量出来的，是心算滑的 —— 旧注释自己写的理由
// （"无混合大小，Go 自然对齐 8"）加起来就是 64，与它自己的结论自相矛盾；
// 又因为没有任何 unsafe.Sizeof 断言钉住这个数，编译器也不会报错，
// 于是它以"很权威的口吻"活了下来，还跟 tools/sysinfo.go 的 64 和
// docs/11 S7-9 的判词互相打架。**注释会骗人，只有断言不会。**
// 门禁见 sysinfo_test.go TestMemoryStatusExSize。
type MemoryStatusEx struct {
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

// OSVersionInfoW 是 RtlGetVersion 的返回值（MEMORY §10: 不用 GetVersionExA）。
// CSDVersion 是 UTF-16 定长缓冲（128 元素），GO 自动按 8 对齐 386/amd64 都是 276 字节
// —— 与 MEMORY §1 声明的 RTL_OSVERSIONINFOW(276) 匹配。
type OSVersionInfoW struct {
	OsVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformId        uint32
	CSDVersion        [128]uint16
}

// SystemInfo 是 GetNativeSystemInfo 的返回值。
// 不使用 GetSystemInfo（WOW64 会重定向）—— GetNativeSystemInfo 始终返回真实信息。
type SystemInfo struct {
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

// 显式错误（v1-L1 契约：调用方用 errors.Is 判）
var (
	ErrMemoryStatus  = errors.New("win: GlobalMemoryStatusEx failed")
	ErrRtlGetVersion = errors.New("win: RtlGetVersion failed")
	// ErrSysInfo 已删（P3-27）：GetNativeSystemInfo 是 **void 函数、永不失败**
	//（见 NativeSystemInfo 注释），所以根本不存在需要 sentinel 的失败路径。
	ErrLogicalDrives = errors.New("win: GetLogicalDrives failed")
	ErrDriveType     = errors.New("win: GetDriveTypeW failed")
	ErrComputerName  = errors.New("win: GetComputerNameW failed")
	ErrUserName      = errors.New("win: GetUserNameW failed")
	ErrDiskFreeSpace = errors.New("win: GetDiskFreeSpaceExW failed")
	ErrVolumeLabel   = errors.New("win: GetVolumeInformationW failed")
)

// DiskSpace 是 GetDiskFreeSpaceExW 的三个 out 参数。
//
// ⚠️ 纯 Go 结构体，**不是** Win32 的布局 —— 三个 uint64 之间无 padding，
// 386 / amd64 都是 24 字节，所以不走 S1（手工字节缓冲 + 显式偏移）那条路。
type DiskSpace struct {
	// FreeAvail = lpFreeBytesAvailableToCaller：调用者**实际**能用的空间。
	// 与 FreeTotal 的差值就是配额/系统保留（Win7 PE 的 RAM disk 常见）。
	FreeAvail uint64
	// Total = lpTotalNumberOfBytes：盘总容量。
	Total uint64
	// FreeTotal = lpTotalNumberOfFreeBytes：盘上剩余总量（不含配额扣减）。
	FreeTotal uint64
}

// DiskFreeSpace 调 GetDiskFreeSpaceExW 拿 rootPath（如 "C:\"）的容量信息。
// 失败时返 (DiskSpace{}, err) —— 调用方必须判 err，**不要**用零值当"0 字节盘"。
//
// 常见失败：空光驱（无介质时 ERROR_NOT_READY）、已断开的网络盘。
// 这类"盘存在但问不到容量"是**正常现场**，不是代码 bug，由调用方决定怎么呈现。
func DiskFreeSpace(rootPath string) (DiskSpace, error) {
	lp, err := Ptr(rootPath)
	if err != nil {
		return DiskSpace{}, fmt.Errorf("%w: %v", ErrDiskFreeSpace, err)
	}
	var avail, total, free uint64
	r, _, e := pGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(lp)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)),
	)
	// M1：lp 由 Ptr() 存进 wstrKeep（永久保活），但三个 out 参数是**栈上局部变量**，
	// 转成 uintptr 后 GC 看不见 —— 必须显式 KeepAlive 到 Call 之后。
	KeepAlive(lp)
	KeepAlive(&avail)
	KeepAlive(&total)
	KeepAlive(&free)
	if r == 0 {
		return DiskSpace{}, fmt.Errorf("%w: %s: %v", ErrDiskFreeSpace, rootPath, e)
	}
	return DiskSpace{FreeAvail: avail, Total: total, FreeTotal: free}, nil
}

// VolumeLabel 调 GetVolumeInformationW 拿 rootPath 的卷标（如 "Windows" / "系统盘"）。
//
// **无卷标不是错误**：未格式化的分区 / RAW 卷返回成功但首字节就是 NUL，
// 此时返 ("", nil) —— 调用方该显示 "(无卷标)"，而不是报错。
// 真失败（ERROR_PATH_NOT_FOUND / 无权限）才返 err。
func VolumeLabel(rootPath string) (string, error) {
	lp, err := Ptr(rootPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrVolumeLabel, err)
	}
	// 260 = MAX_PATH（卷标最大长度），+1 留 NUL 终止位。
	var buf [261]uint16
	var serial, maxComp, fsFlags uint32 // 三个 out 参数用不到，但 API 签名要求非空
	r, _, e := pGetVolumeInformationW.Call(
		uintptr(unsafe.Pointer(lp)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&serial)),
		uintptr(unsafe.Pointer(&maxComp)),
		uintptr(unsafe.Pointer(&fsFlags)),
		0, // lpFileSystemNameBuffer：不需要文件系统名，NULL 合法
		0, // nFileSystemNameSize：同上
	)
	KeepAlive(lp)
	KeepAlive(&buf)
	if r == 0 {
		return "", fmt.Errorf("%w: %s: %v", ErrVolumeLabel, rootPath, e)
	}
	// 成功但首字节就是 NUL（未格式化分区 / RAW 卷）时，UTF16ZToString 返 ""，
	// 与本函数"无卷标不是错误"的契约一致（见上方注释）。
	return UTF16ZToString(buf[:]), nil
}

// MemoryStatus 调 GlobalMemoryStatusEx 拿当前系统内存状态。
// 内部设 Length = sizeof(MemoryStatusEx)，这是 API 强制要求。
// 失败时返 (MemoryStatusEx{}, err) —— 调用方必须判 err，绝不能用零值
// 继续算"地址空间 0 MB"（v1-L1 核心契约）。
func MemoryStatus() (MemoryStatusEx, error) {
	var m MemoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, e := pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return MemoryStatusEx{}, fmt.Errorf("%w: %v", ErrMemoryStatus, e)
	}
	return m, nil
}

// OSVersion 调 RtlGetVersion 拿 OS 版本（MEMORY §10: 不用 GetVersionExA）。
// 失败时返 (OSVersionInfoW{}, err)。NTSTATUS 非 0 = 失败（极罕见，ntdll 几乎不会）。
func OSVersion() (OSVersionInfoW, error) {
	var v OSVersionInfoW
	v.OsVersionInfoSize = uint32(unsafe.Sizeof(v))
	r, _, _ := pRtlGetVersion.Call(uintptr(unsafe.Pointer(&v)))
	if r != 0 { // NTSTATUS 0 = STATUS_SUCCESS
		return OSVersionInfoW{}, fmt.Errorf("%w: 0x%x", ErrRtlGetVersion, r)
	}
	return v, nil
}

// NativeSystemInfo 调 GetNativeSystemInfo（不是 GetSystemInfo，不被 WOW64 重定向）。
//
// **GetNativeSystemInfo 是 void 函数，永不失败**。它通过传出参数填充 SystemInfo。
// r 实际是 last-error 但**没**语义意义（Win32 文档说"返回 void"）。这里直接
// 忽略 r，始终返 nil err。保留 (T, error) 签名以遵循 v1-L1 契约。
func NativeSystemInfo() (SystemInfo, error) {
	var s SystemInfo
	_, _, _ = pGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&s)))
	return s, nil
}

// TickCount 调 GetTickCount 拿 32-bit 毫秒 tick（49.7 天 wraparound）。
//
// L1 契约（T4-11）：GetTickCount 本身没有失败路径（kernel 内部实现），
// 但**所有 API 统一返 (T, error)** —— 这里永远返 nil err，只是为了
// 让调用方不必为"这个函数特判一下签名"破例。
func TickCount() (uint32, error) {
	r, _, _ := pGetTickCount.Call()
	return uint32(r), nil
}

// LogicalDrives 调 GetLogicalDrives，bit mask 形式返回存在的盘符。
// bit 0 = A:, bit 1 = B:, ..., bit 25 = Z:。失败时返 (0, err)。
func LogicalDrives() (uint32, error) {
	r, _, e := pGetLogicalDrives.Call()
	if r == 0 {
		return 0, fmt.Errorf("%w: %v", ErrLogicalDrives, e)
	}
	return uint32(r), nil
}

// DriveType 调 GetDriveTypeW 拿 rootPath 的盘类型。
// DRIVE_UNKNOWN=0 / DRIVE_NO_ROOT_DIR=1 / DRIVE_REMOVABLE=2 / DRIVE_FIXED=3 /
// DRIVE_REMOTE=4 / DRIVE_CDROM=5 / DRIVE_RAMDISK=6。
// 返 (uint32, error) —— 错误来自 Ptr() 失败（rootPath 为空 / 含 NUL）。
// Win32 本身不返错误（rootPath 不存在返 DRIVE_NO_ROOT_DIR=1），所以 err 仅来自本层防御。
func DriveType(rootPath string) (uint32, error) {
	lp, err := Ptr(rootPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrDriveType, err)
	}
	r, _, _ := pGetDriveTypeW.Call(uintptr(unsafe.Pointer(lp)))
	return uint32(r), nil
}

// ComputerName 调 GetComputerNameW 拿机器名。失败时返 ("", err)。
func ComputerName() (string, error) {
	var buf [256]uint16
	n := uint32(len(buf))
	r, _, e := pGetComputerNameW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 {
		return "", fmt.Errorf("%w: %v", ErrComputerName, e)
	}
	// n 是 API 回填的字符数，但缓冲区不足时 MSDN 允许它超过 len(buf)，
	// 所以不拿它去切 buf，改由 UTF16ZToString 自己扫 NUL 定界（P3-32）。
	// n 仍必须传（in/out 参数，API 签名要求）。
	return UTF16ZToString(buf[:]), nil
}

// UserName 调 GetUserNameW 拿当前用户名。失败时返 ("", err)。
func UserName() (string, error) {
	var buf [256]uint16
	n := uint32(len(buf))
	r, _, e := pGetUserNameW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 {
		return "", fmt.Errorf("%w: %v", ErrUserName, e)
	}
	// 同 ComputerName：n 只作为 in/out 参数存在，不用于切 buf（P3-32）。
	return UTF16ZToString(buf[:]), nil
}

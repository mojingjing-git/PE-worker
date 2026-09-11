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
	"syscall"
	"unsafe"
)

// MemoryStatusEx 是 GlobalMemoryStatusEx 的返回值。
// 字段顺序与 Win32 文档完全一致（含 padding 由 Go 编译器按平台 ABI 自动处理）。
// 386 / amd64 两边尺寸都是 72（uint64 字段 + uint32 字段，无混合大小，Go 自然对齐 8）。
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
	ErrMemoryStatus   = errors.New("win: GlobalMemoryStatusEx failed")
	ErrRtlGetVersion  = errors.New("win: RtlGetVersion failed")
	ErrSysInfo        = errors.New("win: GetNativeSystemInfo failed")
	ErrLogicalDrives  = errors.New("win: GetLogicalDrives failed")
	ErrDriveType      = errors.New("win: GetDriveTypeW failed")
	ErrComputerName   = errors.New("win: GetComputerNameW failed")
	ErrUserName       = errors.New("win: GetUserNameW failed")
)

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
// 不返 error：GetTickCount 永远成功（kernel 内部实现，无失败路径）。
func TickCount() uint32 {
	r, _, _ := pGetTickCount.Call()
	return uint32(r)
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
	return syscall.UTF16ToString(buf[:n]), nil
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
	return syscall.UTF16ToString(buf[:n]), nil
}

// tools/sysinfo.go: diskinfo / sysinfo / kill 三个 sys 类工具。
//
// PLAN §3 的验收标准写着"模型自动调 diskinfo"，而 diskinfo **从来没有存在过** ——
// 底层 8 个函数（win/sysinfo.go）写好了、测过，却一个都没暴露成工具。
// 本文件只做工具层包装：解析 args → 调 win/* → 渲染 LLM-friendly 纯文本。
//
// ── 依赖关系（务必先读）──────────────────────────────────────────────
//
// kill 工具**故意不实现**。它的正确实现必须复用 T2 批次在
// win/proc.go 里做的 M2 双层 PID 复用防护（root 名字不符整轮中止 /
// 非 root 节点查 InheritedFromUniqueProcessId），而 KillTreeSelfContained
// 当前签名是 (root uint32, expectName string) (int, []string) —— **没有 error**，
// T2 正在把它改成 (int, error)。本批次**不碰那个签名、不调它**，避免和 T2 冲突；
// Run 只返回一个明确的"未接入"错误。工具仍然注册，是为了让工具数稳定
// （17），T5 文档对齐时不用再改数字。
//
// ── 契约 ────────────────────────────────────────────────────────────
//
// L1：所有 win/* 调用都返 (T, error)，本文件**一处不漏**地判 err。
// L5：错误用 fmt.Errorf("ctx: %w", err) 透传，不用 errors.New 重新造。
// C1：DRIVE_* / PROCESSOR_ARCHITECTURE_* 常量对照 Win32 SDK 头文件
//
//	（winbase.h / winnt.h）逐条核对，值在 tools/sysinfo_test.go 里有断言。
//	它们**故意放在 tools 包而不是 win 包** —— win/ 的每个 Win32 常量都要
//	在 win/consts_test.go 登记（expectedCount 门禁），本批次不改那个文件。
package tools

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"peagent/src/win"
)

// ── Win32 常量（对照 SDK 头文件，非凭记忆）─────────────────────────────
//
// winbase.h: #define DRIVE_UNKNOWN 0 / DRIVE_NO_ROOT_DIR 1 /
//
//	DRIVE_REMOVABLE 2 / DRIVE_FIXED 3 / DRIVE_REMOTE 4 /
//	DRIVE_CDROM 5 / DRIVE_RAMDISK 6
const (
	driveUnknown    uint32 = 0
	driveNoRootDir  uint32 = 1
	driveRemovable  uint32 = 2
	driveFixed      uint32 = 3
	driveRemote     uint32 = 4
	driveCDROM      uint32 = 5
	driveRAMDisk    uint32 = 6
	driveTypeMaxLen uint32 = 7 // 覆盖度自检用，见 sysinfo_test.go
)

// winnt.h: #define PROCESSOR_ARCHITECTURE_INTEL 0 / ARM 5 / IA64 6 /
//
//	AMD64 9 / ARM64 12
const (
	archIntel uint16 = 0
	archARM   uint16 = 5
	archIA64  uint16 = 6
	archAMD64 uint16 = 9
	archARM64 uint16 = 12
)

// errKillNotWired 是 kill 工具当前**唯一**的返回值。
// 用 sentinel 而不是 fmt.Errorf 是为了让测试能 errors.Is 判，
// 也让调用方（agent loop）能识别"这是未接线，不是运行失败"。
var errKillNotWired = errors.New("kill: 待 win.KillTreeSelfContained 签名改造完成（T2 批次）后接入")

// ── diskinfo ──────────────────────────────────────────────────────────

type diskinfoTool struct{}

func (diskinfoTool) Name() string { return "diskinfo" }
func (diskinfoTool) Description() string {
	return `查磁盘容量。args 可选: <盘符或路径>（如 "C:\" / "C:" / "C:\Windows"）; 省略则列所有逻辑盘。` +
		`输出每盘: 盘符 / 卷标 / 驱动类型 / 总容量 / 剩余容量 / 剩余百分比。空光驱或断开的网络盘容量为 ?（不是错误）。`
}
func (diskinfoTool) Risk() RiskLevel { return RiskRead }

func (diskinfoTool) Run(_ *Context, args string) (Result, error) {
	target := parseDriveArg(args)
	if target != "" {
		return driveReport(target)
	}
	mask, err := win.LogicalDrives()
	if err != nil {
		return Result{}, fmt.Errorf("diskinfo: %w", err)
	}
	drives := make([]string, 0, 8)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 { // bit 0 = A:
			drives = append(drives, string(rune('A'+i))+":\\")
		}
	}
	if len(drives) == 0 {
		// L1：GetLogicalDrives 返非 0 但一个 bit 都没有 = 数据异常，**不**返空结果。
		return Result{}, fmt.Errorf("diskinfo: GetLogicalDrives 掩码 0x%x 里一个盘符都没有", mask)
	}
	return driveReport(drives...)
}

// parseDriveArg 从 args 里取出盘符。返回 "" 表示"没给，列所有盘"。
//
// 模型爱加引号（`diskinfo "C:\"`）也会给子路径（`diskinfo C:\Windows`），
// 所以：去引号 → 取盘符部分 → 规范化成 "X:\"。
// 注意用**字节**切而不是转 rune：`C:\路径` 里的中文是 UTF-8 多字节。
func parseDriveArg(args string) string {
	s := strings.TrimSpace(args)
	s = strings.Trim(s, `"`)
	if s == "" {
		return ""
	}
	// 形如 "C:" / "C:\" / "C:/..." / "C"（裸盘符）都能收。
	if len(s) >= 2 && s[1] == ':' {
		c := s[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			return string([]byte{c, ':', '\\'})
		}
	}
	// 裸盘符 "C" / "c"（模型偶尔这么写）。
	if len(s) == 1 {
		c := s[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			return string([]byte{c, ':', '\\'})
		}
	}
	// 给的是 UNC 或别的路径形态（\\server\share）：原样交给 Win32，它自己会报。
	return s
}

// driveReport 渲染一批盘。**单个盘失败不让整轮失败** —— 空光驱 / 断网盘
// 是 PE 现场的常态，整轮报错等于"一个光驱坏了所以什么都看不到"。
// 失败信息**如实写进输出**（L5 的精神：进 LLM 眼前的文本不能比真相好看）。
func driveReport(drives ...string) (Result, error) {
	var sb strings.Builder
	sb.WriteString("盘符  卷标  类型  总容量  剩余容量  剩余百分比\n")
	var failed []string
	for _, d := range drives {
		line, err := driveLine(d)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d, err))
			sb.WriteString(fmt.Sprintf("%s  ?  ?  查询失败: %v\n", d, err))
			continue
		}
		sb.WriteString(line)
	}
	if len(failed) > 0 {
		sb.WriteString(fmt.Sprintf("（%d/%d 个盘查询失败: %s）\n",
			len(failed), len(drives), strings.Join(failed, "; ")))
	}
	return Result{Text: sb.String()}, nil
}

// driveLine 渲染单个盘的一行。
func driveLine(root string) (string, error) {
	label, err := win.VolumeLabel(root)
	if err != nil {
		// 卷标查不到**不影响**容量输出（未授权访问目录等），降级成 "(无)"。
		label = "(无)"
	}
	dt, err := win.DriveType(root)
	if err != nil {
		return "", fmt.Errorf("DriveType: %w", err)
	}
	totalStr, freeStr, pctStr := "?", "?", "?"
	if dt != driveNoRootDir {
		ds, err := win.DiskFreeSpace(root)
		if err != nil {
			// 空光驱 / 断开的网络盘：如实留 "?"，不算整轮失败。
			totalStr, freeStr, pctStr = "?", "?", "?"
		} else {
			totalStr = humanBytes(ds.Total)
			freeStr = humanBytes(ds.FreeAvail)
			if ds.Total > 0 {
				// 用整数百分比算，避免 386 上 float 精度带来的 "0.0%"。
				pct := ds.FreeAvail * 100 / ds.Total
				pctStr = fmt.Sprintf("%d%%", pct)
			}
		}
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s\n", root, label, driveTypeName(dt), totalStr, freeStr, pctStr), nil
}

// driveTypeName 把 GetDriveTypeW 的数字翻成中文（给 LLM 看，中文更省 token 也更好懂）。
func driveTypeName(dt uint32) string {
	switch dt {
	case driveFixed:
		return "固定盘"
	case driveRemovable:
		return "可移动"
	case driveRemote:
		return "网络盘"
	case driveCDROM:
		return "光驱"
	case driveRAMDisk:
		return "内存盘"
	case driveNoRootDir:
		return "无盘符(空槽)"
	case driveUnknown:
		return "未知"
	}
	return fmt.Sprintf("未知(%d)", dt)
}

// ── sysinfo ───────────────────────────────────────────────────────────

type sysinfoTool struct{}

func (sysinfoTool) Name() string { return "sysinfo" }
func (sysinfoTool) Description() string {
	return "查本机概况：OS 版本 / 机型（架构·CPU 数·页大小）/ 内存（总量·可用·负载）/ 计算机名 / 用户名 / 已运行秒数。args 忽略。"
}
func (sysinfoTool) Risk() RiskLevel { return RiskRead }

func (sysinfoTool) Run(_ *Context, _ string) (Result, error) {
	var sb strings.Builder

	// OS 版本 —— RtlGetVersion，**不用 GetVersionExA**（Win8.1+ 无 manifest 返假值）。
	v, err := win.OSVersion()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	csd := strings.TrimSpace(utf16String(v.CSDVersion[:]))
	if csd == "" {
		csd = "无补丁信息"
	}
	sb.WriteString(fmt.Sprintf("操作系统: %d.%d build %d（%s，PlatformId=%d）\n",
		v.MajorVersion, v.MinorVersion, v.BuildNumber, csd, v.PlatformId))

	// 机型 —— GetNativeSystemInfo（不被 WOW64 重定向，所以 386 进程在 x64 上
	// 也能报出真机架构）。它是 void 函数，永不失败，但签名按 L1 统一 (T, error)。
	si, err := win.NativeSystemInfo()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	sb.WriteString(fmt.Sprintf("机型: %s，%d 个逻辑处理器，页大小 %d 字节，分配粒度 %d 字节\n",
		archName(si.ProcessorArchitecture), si.NumberOfProcessors, si.PageSize, si.AllocationGranularity))

	// 内存 —— GlobalMemoryStatusEx。
	//
	// ⚠️ MemoryStatusEx 的成员类型**不许动**（有人说该把 uint64 换成 uintptr
	// 来"修" 386 对齐，那是错的）：SDK sysinfoapi.h 里是 DWORDLONG 不是 SIZE_T，
	// MSVC x86 也把它对齐到 8，所以 386/amd64 都是 64 字节。实测 cbLength=64
	// 成功、36 返 ERROR_INVALID_PARAMETER。这条同时是本工具的冒烟门禁。
	mem, err := win.MemoryStatus()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	sb.WriteString(fmt.Sprintf("内存: 总量 %s / 可用 %s / 负载 %d%%\n",
		humanBytes(mem.TotalPhys), humanBytes(mem.AvailPhys), mem.MemoryLoad))
	sb.WriteString(fmt.Sprintf("内存(提交): 页面文件 总量 %s / 可用 %s\n",
		humanBytes(mem.TotalPageFile), humanBytes(mem.AvailPageFile)))

	name, err := win.ComputerName()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	sb.WriteString("计算机名: " + name + "\n")

	user, err := win.UserName()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	sb.WriteString("用户名: " + user + "\n")

	// 已运行秒数 —— GetTickCount（**不是** GetTickCount64，Win7 PE 缺）。
	// uint32 毫秒 49.7 天 wraparound；PE 会话远短于 49.7 天，直接用。
	tick, err := win.TickCount()
	if err != nil {
		return Result{}, fmt.Errorf("sysinfo: %w", err)
	}
	secs := tick / 1000
	sb.WriteString(fmt.Sprintf("已运行: %d 秒（%d 小时 %d 分）\n", secs, secs/3600, (secs%3600)/60))

	return Result{Text: sb.String()}, nil
}

func archName(a uint16) string {
	switch a {
	case archIntel:
		return "x86 (32 位)"
	case archAMD64:
		return "x64 (64 位)"
	case archARM:
		return "ARM (32 位)"
	case archARM64:
		return "ARM64 (64 位)"
	case archIA64:
		return "IA64 (Itanium)"
	}
	return fmt.Sprintf("架构 %d", a)
}

// ── kill ──────────────────────────────────────────────────────────────

type killTool struct{}

func (killTool) Name() string { return "kill" }
func (killTool) Description() string {
	return "按 PID 杀进程树（连同子进程）。args = \"<pid>\" 或 \"<pid> <期望进程名>\"。注意: 本批次**尚未接入**（依赖 T2 批次的 M2 进程名复核），调用会直接返回未接入错误。"
}
func (killTool) Risk() RiskLevel { return RiskDangerous }

// Run 当前**故意不实现** —— 见文件头"依赖关系"段。
//
// 为什么不先接一个能跑的版本：kill 的全部价值在于 M2 双层 PID 复用防护。
// 少了它，"杀 1234" 可能杀掉 PID 已被复用的**无辜进程** —— 在 PE 现场
// 那等于数据事故。宁可诚实报"未接入"，也不要一个看起来能用但会杀错进程的 kill。
func (killTool) Run(_ *Context, _ string) (Result, error) {
	return Result{}, errKillNotWired
}

// ── 共用小工具 ────────────────────────────────────────────────────────

// humanBytes 把字节数渲染成人类可读的十进制单位（1 KB = 1024 B）。
// 0 字节 → "0 B"（不是 "0.0 B"），避免输出里出现一堆无意义小数。
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// utf16String 把定长 UTF-16 数组（含尾部 NUL 填充）转成 Go 字符串。
func utf16String(a []uint16) string {
	n := 0
	for n < len(a) && a[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(a[:n])
}

func init() {
	Register(diskinfoTool{})
	Register(sysinfoTool{})
	Register(killTool{})
}

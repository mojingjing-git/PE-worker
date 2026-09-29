// tools/sysinfo.go: diskinfo / sysinfo / kill 三个 sys 类工具。
//
// PLAN §3 的验收标准写着"模型自动调 diskinfo"，而 diskinfo **从来没有存在过** ——
// 底层 8 个函数（win/sysinfo.go）写好了、测过，却一个都没暴露成工具。
// 本文件只做工具层包装：解析 args → 调 win/* → 渲染 LLM-friendly 纯文本。
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
	"strconv"
	"strings"

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
	// CSDVersion 是 RTL_OSVERSIONINFOW 里的定长 [128]uint16 字段（含尾部 NUL
	// 填充），用 win 侧的 UTF16ZToString 统一转（P3-32：原先本文件有一份
	// 私有的 utf16String 副本，与 win/sysinfo.go 三处重复，已删）。
	// v 是 win.OSVersion() **按值返回**的局部变量，转完即丢，不跨调用持有。
	csd := strings.TrimSpace(win.UTF16ZToString(v.CSDVersion[:]))
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
	return `按 PID 杀整棵进程树（连同所有子孙进程）。args = "<pid>" 或 "<pid> <期望进程名>"。` +
		`强烈建议带上期望进程名：会校验该 PID 当前确实是这个程序，PID 被系统复用给别人时拒绝误杀。` +
		`高危操作，执行前请自行向用户确认。`
}
func (killTool) Risk() RiskLevel { return RiskDangerous }

// Run 调 win.KillTreeSelfContained 杀整棵树，M2 双层 PID 复用防护见 proc.go。
func (killTool) Run(_ *Context, args string) (Result, error) {
	s := strings.TrimSpace(args)
	if s == "" {
		return Result{}, errors.New("kill: 需要 PID，形如 `kill 1234` 或 `kill 1234 cmd.exe`")
	}
	fields := strings.Fields(s)
	pid64, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return Result{}, fmt.Errorf("kill: PID %q 不是合法数字: %w", fields[0], err)
	}
	pid := uint32(pid64)
	if pid == 0 {
		return Result{}, errors.New("kill: PID 0 无效")
	}

	// M2 第 (a) 层：root 名字校验。KillTreeSelfContained 的三处名字校验
	//（proc.go:222/241/255）全部带 `if expectName != ""` 前置条件，
	// 传空串会让**整层防护短路**。因此这里必须给出一个非空的 expectName。
	//
	// 优先级：**用户传入的期望名 > 快照里查到的当前名**。
	//   - 用户传了名 → 校验的是"这个 PID 现在是不是用户以为的那个进程"，
	//     这才是 M2(a) 的本意：拦住"PID 已被复用给另一个程序"。
	//   - 用户没传名 → 退回快照名，校验退化为"两次快照之间 PID 没被换成同名的
	//     另一个进程"。这层较弱（同名复用拦不住），但比完全没有强，
	//     且不引入"替用户编一个期望值"的假安全。
	//
	// 残余竞态（如实记录）：本进程可能在本快照与 KillTree 自己的首次快照之间
	// 退出、PID 被复用。这是 M2 设计的既有取舍，不为此再加一次快照。
	expectName := ""
	if len(fields) > 1 {
		expectName = fields[1]
	}
	if expectName == "" {
		snap, err := win.SnapshotProcesses()
		if err != nil {
			return Result{}, fmt.Errorf("kill: 取进程快照失败: %w", err)
		}
		info, ok := snap[pid]
		if !ok {
			return Result{Text: fmt.Sprintf("未找到 pid=%d 的进程（可能已退出）", pid)}, nil
		}
		expectName = info.Name
	}

	// M2 第 (b) 层（非 root 节点）由 KillTreeSelfContained 内部用
	// InheritedFromUniqueProcessId 校验，见 proc.go。
	killed, errs := win.KillTreeSelfContained(pid, expectName)
	if killed == 0 {
		// killed==0 时**不能**说"已终止" —— 典型两种：被 M2(a) 以 PID 复用
		// 为由拒杀，或 root 中途消失。如实说"未杀任何进程"并把 errs 原样
		// 带给模型（errs 里就有 "PID reused, abort" 这类判定依据）。
		if len(errs) == 0 {
			return Result{Text: fmt.Sprintf(
				"未杀到任何进程：pid=%d 可能已退出，或它不是根进程（若是子进程，"+
					"请连它的父进程一起 kill）", pid)}, nil
		}
		return Result{Text: fmt.Sprintf(
			"未杀任何进程：pid=%d 被安全策略拒绝或中途失败，未终止任何进程（%d 项失败：%v）",
			pid, len(errs), errs)}, nil
	}
	msg := fmt.Sprintf("已终止 pid=%d 的进程树，共 %d 个进程", pid, killed)
	if len(errs) > 0 {
		msg += fmt.Sprintf("（%d 项失败：%v）", len(errs), errs)
	}
	return Result{Text: msg}, nil
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

func init() {
	Register(diskinfoTool{})
	Register(sysinfoTool{})
	Register(killTool{})
}

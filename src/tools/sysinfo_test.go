// src/tools/sysinfo_test.go —— diskinfo / sysinfo / kill 三个 sys 类工具的测试。
//
// 关键门禁：
//   - TestSysinfo_NoError 在 **386 和 amd64 下都必须过** —— 它顺带证明
//     win.MemoryStatusEx 的成员类型（uint64，不是 uintptr）现状是对的：
//     386 上 cbLength 必须是 64，传 36 会返 ERROR_INVALID_PARAMETER。
//   - TestKill_NotYetWired 锁住"kill 已注册但未接入"这个中间态：
//     T2 接线之后**要**改这条测试（改成真杀一个自己起的进程），但在那之前
//     它不能被"静默改成返回 nil"蒙过去。
package tools

import (
	"strings"
	"testing"
)

// ---------- diskinfo ----------

// 不传参 = 列所有盘。至少要含 C:（PE 上的系统盘；万一在别的盘也至少有内容）。
func TestDiskinfo_NoArgs_ListsAllDrives(t *testing.T) {
	t0, ok := Get("diskinfo")
	if !ok {
		t.Fatal("diskinfo 未注册")
	}
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("diskinfo 空参数应成功, 实际 err: %v", err)
	}
	if !strings.Contains(r.Text, "C:") {
		t.Errorf("diskinfo 空参数输出应含 C:, 实际: %s", r.Text)
	}
	// 表头 + 至少一行盘
	if n := strings.Count(r.Text, "\n"); n < 2 {
		t.Errorf("diskinfo 输出行数 = %d, 期望 >= 2（表头+盘）: %s", n, r.Text)
	}
}

// 传单个盘符 → 只列该盘。两种写法都要认（模型常省略反斜杠）。
func TestDiskinfo_SingleDrive(t *testing.T) {
	t0, _ := Get("diskinfo")
	for _, arg := range []string{`C:\`, `C:`, `"C:\"`, `c:\`} {
		r, err := t0.Run(nil, arg)
		if err != nil {
			t.Fatalf("diskinfo %q: %v", arg, err)
		}
		if !strings.Contains(r.Text, "C:\\") {
			t.Errorf("diskinfo %q 应输出 C:\\ 这一行, 实际: %s", arg, r.Text)
		}
		// 只有表头 + 一行 → 不能顺手把别的盘也列出来
		if n := strings.Count(r.Text, "\n"); n != 2 {
			t.Errorf("diskinfo %q 行数 = %d, 期望 2（表头+1 盘）: %s", arg, n, r.Text)
		}
	}
}

// 解析逻辑单测：模型会加引号、给子路径、给裸盘符、小写盘符。
func TestDiskinfo_ParseDriveArg(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"   ":         "",
		`C:\`:         `C:\`,
		`C:`:          `C:\`,
		`c:\Windows`:  `C:\`,
		`"C:\"`:       `C:\`,
		`D`:           `D:\`,
		`\\srv\share`: `\\srv\share`, // UNC 原样交给 Win32
		`不是盘符`:        `不是盘符`,
	}
	for in, want := range cases {
		if got := parseDriveArg(in); got != want {
			t.Errorf("parseDriveArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- sysinfo ----------

// 双架构都必须返 nil err。任何一步 Win32 失败都会在这里红。
func TestSysinfo_NoError(t *testing.T) {
	t0, ok := Get("sysinfo")
	if !ok {
		t.Fatal("sysinfo 未注册")
	}
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("sysinfo 应成功: %v", err)
	}
	if r.Text == "" {
		t.Fatal("sysinfo 输出为空")
	}
	t.Logf("sysinfo 输出:\n%s", r.Text)
}

// 输出里必须有 OS 版本 + 内存 + 计算机名 + 用户名 + 已运行。
func TestSysinfo_ContainsOSVersion(t *testing.T) {
	t0, _ := Get("sysinfo")
	r, err := t0.Run(nil, "")
	if err != nil {
		t.Fatalf("sysinfo: %v", err)
	}
	for _, want := range []string{
		"操作系统:", "机型:", "内存:", "计算机名:", "用户名:", "已运行:",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("sysinfo 输出缺 %q:\n%s", want, r.Text)
		}
	}
	// 版本号形如 "10.0 build NNNN"（RtlGetVersion 拿到的是真值，不带 manifest 依赖）
	if !strings.Contains(r.Text, "build") {
		t.Errorf("sysinfo 输出应含 build 号: %s", r.Text)
	}
}

// ---------- kill ----------

// TestKill_RealBehavior 验证 kill 工具的**真实行为**（T2 收尾后已接线）。
//
// 原来这里是 TestKill_NotYetWired，锁的是"已注册未接入"的中间态；
// T2 把 KillTreeSelfContained 接上后它就必然红。改成验真正该验的东西：
// 参数校验走 L1 返 error、非法 PID 不崩、不误杀。
func TestKill_RealBehavior(t *testing.T) {
	// 空参数
	if _, err := RunByName(&Context{}, "kill", ""); err == nil {
		t.Error("kill 空参数应返 error")
	}
	// 非数字
	if _, err := RunByName(&Context{}, "kill", "not-a-pid"); err == nil {
		t.Error("kill 非数字 PID 应返 error")
	}
	// PID 0 无效
	if _, err := RunByName(&Context{}, "kill", "0"); err == nil {
		t.Error("kill PID 0 应返 error")
	}
	// 一个几乎必然不存在的 PID：不应 panic，
	// 应返回如实描述（进程可能已退出，不是错误）
	res, err := RunByName(&Context{}, "kill", "4294967294")
	if err != nil {
		t.Logf("不存在的 PID 返回 error，属可接受: %v", err)
	} else if res.Text == "" {
		t.Error("kill 应返回非空的说明性文本")
	}
}

// ---------- 元信息自检 ----------

func TestSelftest_CountIs17(t *testing.T) {
	if expectRegisteredTools != 17 {
		t.Fatalf("expectRegisteredTools = %d, want 17（新增工具时三处清单要一起改）", expectRegisteredTools)
	}
	if len(allToolNames) != expectRegisteredTools {
		t.Fatalf("allToolNames 有 %d 个, expectRegisteredTools = %d", len(allToolNames), expectRegisteredTools)
	}
	if got := len(All()); got != expectRegisteredTools {
		t.Fatalf("实际注册 %d 个工具, 期望 %d", got, expectRegisteredTools)
	}
	for _, name := range []string{"diskinfo", "sysinfo", "kill"} {
		if _, ok := Get(name); !ok {
			t.Errorf("%s 未注册", name)
		}
	}
}

// ---------- C1：Win32 常量对照 SDK 头文件 ----------

// 凭记忆写 Win32 常量是本项目踩过的坑（WM_TIMER 曾写成 0x0118）。
// 这里的断言把 driveTypeName 的映射表锁死。
func TestDriveTypeName_MatchesSDK(t *testing.T) {
	cases := map[uint32]string{
		driveFixed:     "固定盘",
		driveRemovable: "可移动",
		driveRemote:    "网络盘",
		driveCDROM:     "光驱",
		driveRAMDisk:   "内存盘",
		driveNoRootDir: "无盘符(空槽)",
		driveUnknown:   "未知",
	}
	for dt, want := range cases {
		if got := driveTypeName(dt); got != want {
			t.Errorf("driveTypeName(%d) = %q, want %q", dt, got, want)
		}
	}
	if driveTypeMaxLen != 7 {
		t.Errorf("driveTypeMaxLen = %d, want 7（新增 DRIVE_* 要同步）", driveTypeMaxLen)
	}
	if got := driveTypeName(99); !strings.Contains(got, "99") {
		t.Errorf("未知类型应把原值打出来（不能静默变'未知'）: %q", got)
	}
	if got := archName(archAMD64); !strings.Contains(got, "x64") {
		t.Errorf("archName(AMD64) = %q", got)
	}
	if got := archName(archIntel); !strings.Contains(got, "x86") {
		t.Errorf("archName(INTEL) = %q", got)
	}
}

// ---------- humanBytes ----------

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		0:             "0 B",
		512:           "512 B",
		1024:          "1.0 KiB",
		1024 * 1024:   "1.0 MiB",
		1 << 30:       "1.0 GiB",
		5 * (1 << 30): "5.0 GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

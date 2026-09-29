// src/tools/sysinfo_test.go —— diskinfo / sysinfo / kill 三个 sys 类工具的测试。
//
// 关键门禁：
//   - TestSysinfo_NoError 在 **386 和 amd64 下都必须过** —— 它顺带证明
//     win.MemoryStatusEx 的成员类型（uint64，不是 uintptr）现状是对的：
//     386 上 cbLength 必须是 64，传 36 会返 ERROR_INVALID_PARAMETER。
//   - TestKill_M2aNameCheck 钉 M2(a)：用户传入的期望进程名与实际不符时，
//     kill 必须拒绝杀并如实报 "PID reused"，且**被测进程真的还活着**。
//     被测对象是**一次性子进程**，不是测试进程自身 —— 旧实现（expectName
//     传空）会真的把它杀掉，用 os.Getpid() 会让红态下的测试进程自杀。
//   - TestKill_SnapshotFallbackName 覆盖"不传进程名"的默认路径：Run 自己取
//     快照兜底，M2(a) 仍然生效（没被短路成空 expectName）。
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"peagent/src/win"
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

// TestHelperSleeper 不是真正的测试：它是给 kill 测试当被测对象的**一次性替身进程**。
// 只有带着 SMITH_TEST_SLEEPER=1 被重新 exec 起来时才会真的睡；正常跑测试时立刻返回。
func TestHelperSleeper(t *testing.T) {
	if os.Getenv("SMITH_TEST_SLEEPER") != "1" {
		t.Skip("仅供 kill 测试以子进程方式调用")
	}
	time.Sleep(60 * time.Second)
}

// startSleeperCmd 起一个会睡很久的子进程当 kill 的被测对象，返回它的 cmdline。
//
// 为什么用**测试二进制自己**而不是 cmd.exe /c ping：
//   - 不产生孙进程。cmd.exe /c ping 会把 ping 变成孙进程；M2(a) 的用例
//     **故意不杀**，清理时只能杀掉 cmd.exe，ping 就成了活 60 秒的孤儿，
//     而 src/win 的 TestJobCmd_KillsGrandchildren 用 countPings() 数**全系统**
//     的 ping 并要求归零 —— go test 并行跑包时会被这批孤儿打挂。
//   - 进程名是 <pkg>.test.exe，不叫 ping，天然不与 countPings() 抢。
//
// 为什么用**一次性子进程**而不是 os.Getpid()：M2(a) 没接上时（旧实现）kill 会
// 真的把它杀掉，用自身 PID 会让测试进程自杀、红态下整个包崩掉。
//
// 起不来是**真问题**（测试二进制自己必然能起来），不是环境缺条件，所以 t.Fatal
// 而不是 t.Skip —— Skip 会让 M2(a) 的门禁在 CI 上假绿。
func startSleeperCmd(t *testing.T) (cmd *exec.Cmd, pid int) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=TestHelperSleeper", "-test.timeout=90s")
	child.Env = append(os.Environ(), "SMITH_TEST_SLEEPER=1")
	if err := child.Start(); err != nil {
		t.Fatalf("起不了子进程: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child, child.Process.Pid
}

// assertProcessAlive 用生产同款的进程快照确认 pid 还在。
// kill 的效果就是"从快照里消失"，所以这是最贴近语义的存在性判据。
func assertProcessAlive(t *testing.T, pid int, why string) {
	t.Helper()
	snap, err := win.SnapshotProcesses()
	if err != nil {
		t.Fatalf("%s: 取快照失败: %v", why, err)
	}
	if _, ok := snap[uint32(pid)]; !ok {
		t.Fatalf("%s: pid=%d 已不在进程快照里 —— 进程被杀了", why, pid)
	}
}

// TestKill_M2aNameCheck 钉 M2(a)：用户传入的进程名与实际不符时，
// kill 必须**拒绝杀**并如实报告，绝不能把 PID 复用风险敞着。
func TestKill_M2aNameCheck(t *testing.T) {
	_, pid := startSleeperCmd(t)

	// 故意传一个与真实映像名不符的期望名
	res, err := killTool{}.Run(&Context{}, fmt.Sprintf("%d definitely-not-the-sleeper.exe", pid))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Text, "PID reused") {
		t.Fatalf("期望报告 PID 复用拒绝（实际: %q）—— M2(a) 未生效：expectName 没传给 KillTreeSelfContained", res.Text)
	}
	// 光有 "PID reused" 字样不够 —— 必须确认**真的没杀**。这条才是本测试的核心：
	// 文本对了但进程死了，等于防护是假的。
	assertProcessAlive(t, pid, "M2(a) 拒杀后")
	// 拒杀时不能说"已终止"（否则 0 个进程也报"已终止 ... 共 0 个进程"）
	if strings.Contains(res.Text, "已终止") {
		t.Errorf("M2(a) 拒杀时输出不应含\"已终止\"（实际: %q）", res.Text)
	}
}

// TestKill_SnapshotFallbackName 覆盖"用户没传进程名"的默认路径：
// Run 自己取快照、用快照里的当前名当 expectName，M2(a) 仍**生效**（不是被短路）。
func TestKill_SnapshotFallbackName(t *testing.T) {
	_, pid := startSleeperCmd(t)

	// 只给 PID，不给进程名 → 走快照兜底分支
	res, err := killTool{}.Run(&Context{}, fmt.Sprintf("%d", pid))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 快照里明明有这个子进程，走过兜底分支就不能再报"未找到"
	if strings.Contains(res.Text, "未找到 pid") {
		t.Fatalf("快照兜底分支误报未找到（实际: %q）", res.Text)
	}
	// expectName 来自快照 = 真实当前名 → M2(a) 校验**通过**。
	// 这同时证明 M2(a) 没被短路：若 expectName 传空，防护被跳过；
	// 若快照取错了名字，M2(a) 会在这里以 "PID reused" 拒杀。
	if strings.Contains(res.Text, "PID reused") {
		t.Fatalf("快照兜底取到的名字不该触发 M2(a) 拒杀（实际: %q）", res.Text)
	}
	if !strings.Contains(res.Text, "已终止") {
		t.Fatalf("期望真的杀掉子进程（实际: %q）", res.Text)
	}
	// 这次是真的要杀它，确认它确实消失了
	snap, err := win.SnapshotProcesses()
	if err != nil {
		t.Fatalf("取快照失败: %v", err)
	}
	if _, ok := snap[uint32(pid)]; ok {
		t.Errorf("pid=%d 应已被杀，仍在快照里", pid)
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

// Package tools: registry.go 注册表 + 工具接口。
//
// 实际注册的 17 个工具（按 docs/02 §3 的分层）：
//   - read  (4): ls / cat / grep / find      ← S5 起为纯 Go 实现，不经 cmd.exe
//   - write (3): write / edit / append
//   - exec  (2): exec / run_script
//   - net   (2): http_get / https_get
//   - meta  (2): help / selftest
//   - sys   (4): ps / diskinfo / sysinfo / kill
//
// ✅ kill **已接线**，走 win.KillTreeSelfContained 的完整 M2 双层 PID 复用防护：
// root 名字校验 + 子节点 InheritedFromUniqueProcessId 校验。带期望进程名调用
// `kill <pid> <name>` 时，PID 被系统复用给别人会**拒绝误杀**并如实报告。
//
// ⚠️ docs/02 §3 原计划里还有 screenshot / hash / netinfo / download ——
// **至今未实现**。上表是当前代码的真实状态，不要照抄 PLAN 里的清单。
// PLAN §3 的验收标准写了"模型自动调 diskinfo"，diskinfo 到 P3-17 才补上
// （底层 win/sysinfo.go 的 8 个函数更早就有，只是没暴露成工具 —— 见 docs/11 §一）。
//
// v1-L1 硬规则：所有工具 Run() 返 (Result, error)。
// docs/02 §7 6 条约定：白名单是软护栏 + confirm 拦危险 + exec 校验首 token +
//
//	run_script/download 不经白名单。
package tools

import (
	"fmt"
	"sort"
	"sync"
)

// RiskLevel 工具风险等级（决定 confirm 交互 + 日志前缀）
type RiskLevel int

const (
	RiskRead      RiskLevel = iota // 只读
	RiskWrite                      // 写文件
	RiskExec                       // 执行命令
	RiskDangerous                  // 写 .bat / 调外部服务
)

func (r RiskLevel) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskExec:
		return "exec"
	case RiskDangerous:
		return "dangerous"
	}
	return "unknown"
}

// Result 是工具执行的输出。
//
// Text 必填（agent loop 会把 Text 贴给 LLM）。
// AttachImage 是图片附件路径（MEMORY §9 多模态回传通道）—— 仅 screenshot 用。
//
// ⚠️ AttachImage 目前**没有任何写入方**（P3-27 复核：全项目只有这一处出现，
// screenshot 工具未实现，见 README 特性表）。**保留**是因为它是 Tool 接口
// 输出契约的一部分：先实现视觉工具再回头改 Result 的形状，等于让 agent loop
// 的多模态分支和工具实现两处同时动。删字段 = 预留通道，不叫死代码。
type Result struct {
	Text        string
	AttachImage string // "" = 无图；当前恒为 ""，见上方说明
}

// Tool 是所有工具的接口。
type Tool interface {
	Name() string
	Description() string
	Risk() RiskLevel
	// Run 执行工具。args 是工具的 string 参数（tool-specific schema）。
	// 返回的 Result.Text 必须**纯文本**（LLM-friendly），不要 ANSI/控制字符。
	Run(ctx *Context, args string) (Result, error)
}

// Context 是工具执行的上下文。**定义在 context.go**（S1-1 从此处移过去，
// 因为它现在承载取消信号的透传职责，和 timeoutContext 是同一件事）。
// 字段：Ctx / Confirm / Cwd / Config。

// Config 是工具需要的 smith.ini 字段子集（独立于 cfg.Config 以避免循环 import）。
type Config struct {
	Whitelist []string
	Confirm   bool
}

// 注册表（全局, 一次性初始化）
var (
	mu  sync.RWMutex
	all = map[string]Tool{}
)

// Register 注册一个工具。name 重复会覆盖（启动期应无重复）。
func Register(t Tool) {
	mu.Lock()
	defer mu.Unlock()
	all[t.Name()] = t
}

// Get 按名取一个工具。
func Get(name string) (Tool, bool) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := all[name]
	return t, ok
}

// All 列出所有工具（按 name 排序）。用于 help 工具。
func All() []Tool {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// RunByName 按名执行工具（agent loop 用）。
func RunByName(ctx *Context, name, args string) (Result, error) {
	t, ok := Get(name)
	if !ok {
		return Result{}, fmt.Errorf("tools: unknown tool %q", name)
	}
	return t.Run(ctx, args)
}

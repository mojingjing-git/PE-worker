// Package tools: registry.go 注册表 + 工具接口。
//
// 14 工具按 docs/02 §3 分层：
//   - read   (5): ls / cat / grep / find / screenshot / read
//   - write  (3): write / edit / append
//   - exec   (2): exec / run_script
//   - net    (2): http_get / https_get
//   - meta   (2): help / selftest
//   - sys    (1): ps (process list)
//
// v1-L1 硬规则：所有工具 Run() 返 (Result, error)。
// docs/02 §7 6 条约定：白名单是软护栏 + confirm 拦危险 + exec 校验首 token +
//   run_script/download 不经白名单。
package tools

import (
	"fmt"
	"sort"
	"sync"
)

// RiskLevel 工具风险等级（决定 confirm 交互 + 日志前缀）
type RiskLevel int

const (
	RiskRead       RiskLevel = iota // 只读
	RiskWrite                        // 写文件
	RiskExec                         // 执行命令
	RiskDangerous                    // 写 .bat / 调外部服务
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
type Result struct {
	Text        string
	AttachImage string // "" = 无图
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

// Context 是工具执行的上下文（持有 Config / Confirm 回调 / 工作目录等）。
type Context struct {
	// Confirm 让工具要求用户确认。true = 继续, false = 拒绝。
	// 不需要确认的工具**不**调。
	Confirm func(prompt string) bool
	// Cwd 是工具执行的工作目录（默认 smith.exe 同目录）。
	Cwd string
	// Config 是 smith.ini 加载的配置。
	Config *Config
}

// Config 是工具需要的 smith.ini 字段子集（独立于 cfg.Config 以避免循环 import）。
type Config struct {
	Whitelist []string
	Confirm   bool
}

// 注册表（全局, 一次性初始化）
var (
	mu   sync.RWMutex
	all  = map[string]Tool{}
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

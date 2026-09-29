// tools/context.go: 工具执行上下文 —— 取消信号 + 超时 的统一入口。
//
// 【S1-1 修复的根因】
//
// 原来这里只有 timeoutContext(sec)，它写死 `context.Background()` 作为 parent。
// 于是 tools.Context（Confirm/Cwd/Config 三个字段）**根本没有 ctx**，
// 工具自建的 ctx 与 GUI 的 runCtx 是两条平行线 —— 按 Esc / Stop 时，
// loop.Run 的 ctx 被取消了，但工具层的 ctx 完全无感知。
//
// 审计实测后果：
//
//	T1 用户点 Stop → runCancel() → runCtx.Done 关闭
//	T2 worker 正跑 execTool.Run，cctx 来自 context.Background()
//	T2 cmd.CombinedOutput() 阻塞，**对它无感知**
//	T1 GUI 全程无反应 → 用户判定"卡死"
//	T2 最长 60s 后返回 → 本轮才结束
//	若此刻关窗口，cmd.exe 变孤儿进程继续运行
//
// 现在 parent 由调用方（loop.Run）透传下来，取消信号真正到达工具层。
package tools

import (
	"context"
	"time"
)

// Context 是工具执行上下文。
//
// ⚠️ Ctx 字段由 loop.Run 在**每一轮**构造时透传本轮的 runCtx，
// 不要缓存到轮外复用（那会让上一轮的取消信号泄漏到下一轮）。
type Context struct {
	// Ctx 是本轮 agent loop 的取消信号（来自 GUI 的 Esc/Stop）。
	// 为 nil 时工具退化为"只有超时、没有用户取消"的老行为。
	Ctx context.Context

	// Confirm 让工具要求用户确认。true = 继续, false = 拒绝。
	// 不需要确认的工具**不**调。
	Confirm func(prompt string) bool
	// Cwd 是工具执行的工作目录（默认 smith.exe 同目录）。
	Cwd string
	// Config 是 smith.ini 加载的配置。
	Config *Config
}

// parent 返回可用的 parent context（Ctx 为 nil 时给 Background，保证非 nil）。
func (c *Context) parent() context.Context {
	if c != nil && c.Ctx != nil {
		return c.Ctx
	}
	return context.Background()
}

// timeoutContext 返"本轮 ctx + 超时"组合出的执行 ctx + cancel 函数。
//
// parent 取自 Context.Ctx（用户取消信号），再叠加工具自己的超时上限。
// 两个信号是"或"关系：用户按 Stop 立即生效，或超时触发 —— 谁先到听谁的。
//
// 所有 exec-style 工具共用。sec <= 0 表示只要用户超时、不要工具级超时。
func (c *Context) timeoutContext(sec int) (context.Context, context.CancelFunc) {
	parent := c.parent()
	if sec <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(sec)*time.Second)
}

// deadlineExceeded 报"是否因为超时/取消而中止"，用于区分错误原因：
// 超时 → 报 timeout；用户取消 → 报 canceled；其他 → 都不是。
//
// 注意不要只判 context.DeadlineExceeded：用户按 Stop 时是 Canceled，
// 而两者对用户是完全不同的反馈（"这命令太慢" vs "你按了停止"）。
func classifyAbort(cctx context.Context) (timedOut bool, canceled bool) {
	err := cctx.Err()
	if err == nil {
		return false, false
	}
	if err == context.DeadlineExceeded {
		return true, false
	}
	return false, true // context.Canceled
}

// Package agent — loop.go
//
// agent loop：核心编排。
//
// 流程：
//
//  1. 拿用户输入 → 追加到 history
//  2. 发 history + tools 给 LLM
//  3. 把 assistant 消息追加到 history
//  4. 如果 LLM 返回 tool_calls：
//     a. 每个 tool 调一次，按序追加 tool result 到 history
//     b. 削历史（clipHistory + ClipImages）
//     c. 回到 2
//  5. 如果 LLM 返回纯文本：返回给用户
//  6. maxTurns 上限防止死循环
//  7. ctx 取消立即返（每轮前 check）
//
// 日志约定（PLAN §8 + L4 硬规则）：
//
//	you > ...     用户输入
//	ai  > ...     assistant 文本
//	->  name ...  调工具（带 args）
//	<-  result    工具结果
//	!!  err       工具失败（不掩盖，PLAN L5）
//	**  ver       VERDICT 总结（每轮一次）
//
// 所有 API 返 (T, error) — L1 硬规则。
// 错误一律透传 — L5 硬规则（不吞）。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"peagent/src/logx"
	"peagent/src/tools"
)

// Loop 是一次会话的编排器。
// 并发安全：不在内部加锁；一个 Loop 实例**单 goroutine 用**。
// UI 线程或后台 worker 各持一份。
type Loop struct {
	llm      Client
	toolCtx  *tools.Context
	MaxTurns int
	history  []Message
	system   string
	tools    []ToolDef
}

// NewLoop 构造 loop。toolCtx 可为 nil（工具调用 confirm 总是 true）。
func NewLoop(llm Client, toolCtx *tools.Context, maxTurns int, systemPrompt string) *Loop {
	if maxTurns <= 0 {
		maxTurns = 10
	}
	tools := tools.All()
	defs := make([]ToolDef, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
		})
	}
	l := &Loop{
		llm:      llm,
		toolCtx:  toolCtx,
		MaxTurns: maxTurns,
		system:   systemPrompt,
		tools:    defs,
	}
	if systemPrompt != "" {
		l.history = []Message{{Role: RoleSystem, Content: systemPrompt}}
	}
	return l
}

// History 返回当前 history 的快照（只读）。测试/调试用。
func (l *Loop) History() []Message {
	out := make([]Message, len(l.history))
	copy(out, l.history)
	return out
}

// Reset 清空 history（保留 system prompt）。换话题时调。
func (l *Loop) Reset() {
	if l.system != "" {
		l.history = []Message{{Role: RoleSystem, Content: l.system}}
	} else {
		l.history = nil
	}
}

// ToolNames 返回已注册工具的名字列表（启动时打日志用，确认工具确实挂上了）。
func (l *Loop) ToolNames() []string {
	out := make([]string, 0, len(l.tools))
	for _, t := range l.tools {
		out = append(out, t.Name)
	}
	return out
}

// Run 跑一轮对话直到 LLM 返回纯文本。
//
// 错误一律透传（L5）：
//   - ctx 取消 → 返 ctx.Err()
//   - LLM 错误 → 透传
//   - 工具错误 → 不中断 loop，**作为 tool result 回灌**让模型决定下一步
//     （这是 PLAN §0.6 A7 的设计："工具失败要让模型看到，模型可能重试或换工具"）
//
// finalText 是 LLM 最后返回的纯文本。
// 任何一轮超 maxTurns → 返 ErrMaxTurns。
func (l *Loop) Run(ctx context.Context, userInput string) (finalText string, err error) {
	if userInput == "" {
		return "", errors.New("agent: empty user input")
	}
	// 1. 追加 user 输入
	_ = logx.Info("you > %s", truncate(userInput, 200))
	l.history = append(l.history, Message{Role: RoleUser, Content: userInput})

	for turn := 0; turn < l.MaxTurns; turn++ {
		// 2. ctx check
		if err := ctx.Err(); err != nil {
			return "", err
		}

		// 3. 削历史
		l.history = ClipHistory(l.history)
		ClipImages(l.history)

		// 4. 调 LLM
		resp, err := l.llm.Chat(ctx, Request{
			Messages: l.history,
			Tools:    l.tools,
		})
		if err != nil {
			_ = logx.Error("!! llm: %v", err)
			return "", fmt.Errorf("agent: turn %d llm: %w", turn+1, err)
		}

		// 5. 追加 assistant 消息
		am := Message{
			Role:      RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		}
		l.history = append(l.history, am)
		if resp.Text != "" {
			_ = logx.Info("ai  > %s", truncate(resp.Text, 300))
		}

		// 6. 没有 tool_calls → 完成
		if len(resp.ToolCalls) == 0 {
			_ = logxVerdict(turn+1, "end_turn", resp.StopReason, len(l.history))
			return resp.Text, nil
		}

		// 7. 有 tool_calls → 逐个执行；结果作为 tool 消息回灌
		for _, tc := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			args := extractInputArg(tc.Arguments)
			_ = logx.Info("-> %s %s", tc.Name, truncate(args, 200))
			// 【S1-1】每轮构造 toolCtx，把本轮 runCtx 透传进工具层。
			// 之前 toolCtx 是循环外的单例，且 tools.Context 根本没有 ctx 字段 ——
			// 按 Esc/Stop 的取消信号永远到不了工具（实测命令继续跑满 60s）。
			tc2 := &tools.Context{
				Ctx:     ctx,
				Confirm: l.toolCtx.Confirm,
				Cwd:     l.toolCtx.Cwd,
				Config:  l.toolCtx.Config,
			}
			result, runErr := tools.RunByName(tc2, tc.Name, args)
			if runErr != nil {
				// 工具失败 → 把错误文本作为 tool result 回灌；
				// loop 不中断，模型可重试或换工具。
				_ = logx.Error("!! %s: %v", tc.Name, runErr)
				// 【S3-audit】工具出错时 result.Text 常常是**有效输出**
				// （exec 在非零退出/超时时会把已产出的部分输出放进 Text）。
				// 原实现只回 "ERROR: exit status 1"，把内容整个丢了 ——
				// `echo hello & exit /b 1` 这种最常见的"有输出但退出码非 0"
				// 场景，模型看不到 hello，于是反复重试同一条命令直到 MaxTurns。
				// 违反 PLAN §0.6 A7（工具失败要让模型看到）与 L5 精神。
				msg := fmt.Sprintf("ERROR: %v", runErr)
				if result.Text != "" {
					msg += "\n--- partial output (may be truncated) ---\n" + truncateRunes(result.Text, 4000)
				}
				l.history = append(l.history, Message{
					Role:       RoleTool,
					ToolCallID: tc.ID,
					// 【S4-audit A7】参数解析失败时补一句明确的自我纠正提示。
					// 没有它，模型只看到 "ERROR: exit status 1"，根本不知道自己的
					// arguments 没被解析（PLAN §0.6 A7：让模型自我纠正）。
					Content: argsHintNote(tc.Arguments, msg),
				})
				continue
			}
			_ = logx.Info("<- %s: %s", tc.Name, truncate(result.Text, 300))
			l.history = append(l.history, Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Content:    argsHintNote(tc.Arguments, result.Text),
			})
		}
		_ = logxVerdict(turn+1, "continue", "", len(l.history))
	}
	return "", fmt.Errorf("agent: max turns (%d) exceeded", l.MaxTurns)
}

// ErrMaxTurns 是 MaxTurns 上限的预定义 error。loop 内部用 fmt.Errorf 返，
// errors.Is 判断。
var ErrMaxTurns = errors.New("agent: max turns exceeded")

// extractInputArg 从 tool call 的 arguments JSON 里取 "input" 字段。
// tools 统一收 string 参数；OpenAI 兼容协议里我们让模型包成 {"input": "..."}。
//
// 只对 input 字段做二次 unmarshal：解到 map[string]string 时，模型只要多吐
// 一个数字/布尔/嵌套字段（`{"input":"echo hi","timeout":5}` 是极常见输出）
// 就**整包**失败，走回退分支把原始 JSON 当命令/路径塞进工具 —— 模型看到的是
// "ERROR: exit status 1"，完全不知道为什么。改成 json.RawMessage 后，别的字段
// 是什么类型都无所谓。
//
// input 本身不是字符串（模型吐了数字/对象）也算解析失败 → 回退原字符串。
func extractInputArg(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return argsJSON
	}
	raw, ok := m["input"]
	if !ok {
		return argsJSON
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return argsJSON
	}
	return s
}

// logxVerdict 记本轮的 VERDICT —— L4 硬规则"每一步都要在 VERDICT 里"。
// 不是测试断言用的 Verdict（那个在 verdict.go），是 logx 里的运营日志。
func logxVerdict(turn int, kind, stopReason string, histLen int) error {
	return logx.Info("** ver turn=%d kind=%s stop=%s hist=%d",
		turn, kind, stopReason, histLen)
}

// truncateRunes 按 rune 截断（不是按字节）。
//
// 为什么需要它：loop.go 里的 truncate 是按字节切的，会把 UTF-8 多字节字符劈开，
// 结果就是 tool result / 日志里出现乱码 —— 而这些文本是要喂回给 LLM 的，
// 乱码会浪费 token 还可能让模型误解内容。中文 PE 场景下尤其明显。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + fmt.Sprintf("...[truncated %d runes]", len(rs)-max)
}

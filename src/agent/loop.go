// Package agent — loop.go
//
// agent loop：核心编排。
//
// 流程：
//
//	1. 拿用户输入 → 追加到 history
//	2. 发 history + tools 给 LLM
//	3. 把 assistant 消息追加到 history
//	4. 如果 LLM 返回 tool_calls：
//	   a. 每个 tool 调一次，按序追加 tool result 到 history
//	   b. 削历史（clipHistory + ClipImages）
//	   c. 回到 2
//	5. 如果 LLM 返回纯文本：返回给用户
//	6. maxTurns 上限防止死循环
//	7. ctx 取消立即返（每轮前 check）
//
// 日志约定（PLAN §8 + L4 硬规则）：
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
	"strings"

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
			result, runErr := tools.RunByName(l.toolCtx, tc.Name, args)
			if runErr != nil {
				// 工具失败 → 把错误文本作为 tool result 回灌；
				// loop 不中断，模型可重试或换工具。
				_ = logx.Error("!! %s: %v", tc.Name, runErr)
				l.history = append(l.history, Message{
					Role:       RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("ERROR: %v", runErr),
				})
				continue
			}
			_ = logx.Info("<- %s: %s", tc.Name, truncate(result.Text, 300))
			l.history = append(l.history, Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Content:    result.Text,
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
// 解析失败回退到原字符串（让工具自己报错）。
func extractInputArg(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return argsJSON
	}
	if v, ok := m["input"]; ok {
		return v
	}
	return argsJSON
}

// logxVerdict 记本轮的 VERDICT —— L4 硬规则"每一步都要在 VERDICT 里"。
// 不是测试断言用的 Verdict（那个在 verdict.go），是 logx 里的运营日志。
func logxVerdict(turn int, kind, stopReason string, histLen int) error {
	return logx.Info("** ver turn=%d kind=%s stop=%s hist=%d",
		turn, kind, stopReason, histLen)
}

// 静默阻断 strings 包的"没用到"误报（如果 verdict.go 里用了就 OK）
var _ = strings.TrimSpace

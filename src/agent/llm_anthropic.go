// Package agent — llm_anthropic.go
//
// Anthropic Messages API 适配层（POST {base}/v1/messages）。
//
// 关键差异（vs OpenAI 兼容）：
//   - 鉴权 header 是 x-api-key + anthropic-version: 2023-06-01（不是 Bearer）。
//   - system 字段是**顶层**字段，不在 messages 里。
//   - 消息内容是**数组**（typed blocks）而不是字符串：
//       text     → {"type":"text","text":"..."}
//       tool_use → {"type":"tool_use","id":"...","name":"...","input":{...}}
//       tool_result → {"type":"tool_result","tool_use_id":"...","content":"..."}
//   - tool 的 input_schema 字段名、tool_use_id 字段名都和 OpenAI 不同。
//
// 因此本文件是**独立分支**，不复用 openai 的 struct。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// anthropicVersion 锁死。改版本会让 wire 格式变。
const anthropicVersion = "2023-06-01"

type anthropicClient struct {
	cfg  Config
	http *http.Client
}

// ----- 请求体 -----

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string                   `json:"role"`
	Content []map[string]interface{} `json:"content"`
}
type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ----- 响应体 -----

type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicRespBlk `json:"content"`
	StopReason string             `json:"stop_reason"`
	// Error 字段在 4xx 响应里出现；成功时不存在。
	Error *anthropicError `json:"error,omitempty"`
}

type anthropicRespBlk struct {
	Type string `json:"type"`
	// text block
	Text string `json:"text,omitempty"`
	// tool_use block
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ----- 实现 Client -----

func (c *anthropicClient) Chat(ctx context.Context, req Request) (Response, error) {
	wire, err := buildAnthropicRequest(c.cfg, req)
	if err != nil {
		return Response{}, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return Response{}, fmt.Errorf("llm: anthropic: marshal request: %w", err)
	}
	httpReq, err := http.NewRequest("POST", strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	var resp anthropicResponse
	status, rawBody, err := doHTTP(ctx, c.http, httpReq, &resp)
	if err != nil {
		return Response{}, err
	}
	_ = status
	_ = rawBody
	if resp.Error != nil {
		// 4xx 已经在 doHTTP 里报过了；这里再守一道，万一上游 2xx 但带 error
		return Response{}, fmt.Errorf("llm: anthropic returned error: %s: %s", resp.Error.Type, resp.Error.Message)
	}
	return parseAnthropicResponse(resp), nil
}

// buildAnthropicRequest 把统一 Request 翻译成 Anthropic wire。
func buildAnthropicRequest(cfg Config, req Request) (anthropicRequest, error) {
	wire := anthropicRequest{
		Model:     cfg.Model,
		MaxTokens: req.MaxTokens,
		Tools:     buildAnthropicTools(req.Tools),
	}
	if wire.MaxTokens <= 0 {
		wire.MaxTokens = cfg.MaxTokens
	}

	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			// Anthropic 的 system 是顶层字段；多条 system 消息就拼起来。
			if wire.System != "" {
				wire.System += "\n\n"
			}
			wire.System += m.Content
		case RoleUser:
			blk, err := userContentToAnthropic(m)
			if err != nil {
				return wire, err
			}
			wire.Messages = append(wire.Messages, anthropicMessage{Role: "user", Content: blk})
		case RoleAssistant:
			blk, err := assistantContentToAnthropic(m)
			if err != nil {
				return wire, err
			}
			wire.Messages = append(wire.Messages, anthropicMessage{Role: "assistant", Content: blk})
		case RoleTool:
			// tool 消息是 user 消息里夹 tool_result block。
			blk := []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content},
			}
			wire.Messages = append(wire.Messages, anthropicMessage{Role: "user", Content: blk})
		default:
			return wire, fmt.Errorf("llm: anthropic: unknown role %q", m.Role)
		}
	}

	// Anthropic 要求 messages 非空（system 不算 messages）。
	if len(wire.Messages) == 0 {
		return wire, errors.New("llm: anthropic: no user/assistant messages")
	}
	// 强制：相邻同 role 要合并（不然会 400）。
	wire.Messages = mergeAdjacentRoles(wire.Messages)
	return wire, nil
}

func userContentToAnthropic(m Message) ([]map[string]interface{}, error) {
	if len(m.Images) == 0 {
		return []map[string]interface{}{{"type": "text", "text": m.Content}}, nil
	}
	// 多模态：文本 + 图片（Phase 4）
	out := []map[string]interface{}{}
	if m.Content != "" {
		out = append(out, map[string]interface{}{"type": "text", "text": m.Content})
	}
	for _, img := range m.Images {
		out = append(out, map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": img.MediaType,
				"data":       img.Data, // []byte 会被 json 编码为 base64
			},
		})
	}
	return out, nil
}

func assistantContentToAnthropic(m Message) ([]map[string]interface{}, error) {
	out := []map[string]interface{}{}
	if m.Content != "" {
		out = append(out, map[string]interface{}{"type": "text", "text": m.Content})
	}
	for _, tc := range m.ToolCalls {
		// Arguments 是 JSON 字符串；要还原成 map 给 Anthropic。
		var input map[string]interface{}
		if tc.Arguments == "" {
			input = map[string]interface{}{}
		} else if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
			return nil, fmt.Errorf("llm: anthropic: tool call %q has invalid JSON args: %w", tc.Name, err)
		}
		out = append(out, map[string]interface{}{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": input,
		})
	}
	if len(out) == 0 {
		// 兜底：纯空 assistant 是不允许的
		out = append(out, map[string]interface{}{"type": "text", "text": ""})
	}
	return out, nil
}

func buildAnthropicTools(defs []ToolDef) []anthropicTool {
	out := make([]anthropicTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, anthropicTool{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{
						"type":        "string",
						"description": "工具参数（具体格式见 description 字段，或先用 help <tool> 查询）",
					},
				},
				"required": []string{"input"},
			},
		})
	}
	return out
}

// asBlocks 把 []map 转成可序列化的 interface{}。
// 现在 anthropicMessage.Content 直接就是 []map[string]interface{}，所以
// 这层包装不再需要；保留 stub 以便将来 Content 改类型时不用到处改。
func asBlocks(in []map[string]interface{}) []map[string]interface{} { return in }

// mergeAdjacentRoles 合并相邻同 role（Anthropic 强制规则）。
func mergeAdjacentRoles(msgs []anthropicMessage) []anthropicMessage {
	if len(msgs) <= 1 {
		return msgs
	}
	out := []anthropicMessage{msgs[0]}
	for i := 1; i < len(msgs); i++ {
		last := &out[len(out)-1]
		if last.Role == msgs[i].Role {
			last.Content = append(last.Content, msgs[i].Content...)
		} else {
			out = append(out, msgs[i])
		}
	}
	return out
}

// parseAnthropicResponse 拆 content blocks 成统一 Response。
func parseAnthropicResponse(r anthropicResponse) Response {
	out := Response{StopReason: r.StopReason}
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			out.Text += b.Text
		case "tool_use":
			// 还原成 args JSON 字符串给上层。
			argsBytes, _ := json.Marshal(b.Input)
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: string(argsBytes),
			})
		}
	}
	// StopReason 归一化
	switch r.StopReason {
	case "tool_use":
		out.StopReason = "tool_calls"
	case "end_turn", "stop_sequence":
		out.StopReason = "end_turn"
	case "max_tokens":
		// 保持原样
	}
	return out
}

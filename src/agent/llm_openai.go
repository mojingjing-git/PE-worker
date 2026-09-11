// Package agent — llm_openai.go
//
// OpenAI 兼容 Chat Completions API 适配层（POST {base}/v1/chat/completions）。
//
// 同时支持 OpenAI 和 DeepSeek。两者 wire 几乎一样，但有 3 个**实测过会咬人**的差异：
//
//	(1) DeepSeek 多一个 `reasoning_content` 字段（链式思维），在 streaming 模式
//	    下还会单独推送；非流式我们也兼容：把它吸到 Response.Text 里（拼到前面）
//	    让 loop 看到完整推理。
//	(2) tool_calls.id：OpenAI 是 `call_xxx`，DeepSeek 是 `toolu_xxx` 风格（虽然
//	    同一协议），不影响 wire，但**空 tool_calls 数组 vs 缺字段**两种情况都有。
//	(3) stop_reason 枚举：OpenAI = stop|length|tool_calls|content_filter；
//	    DeepSeek = stop|tool_calls|length。统一归一化成我们自己的 4 档。
//
// 所以这里用 `openAIKind` 区分，但代码路径只有 1 个；只在收响应时按 kind 提取
// reasoning_content。
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

type openAIKind int

const (
	openAIKindOpenAI openAIKind = iota
	openAIKindDeepSeek
)

type openAIClient struct {
	cfg  Config
	http *http.Client
	kind openAIKind
}

// ----- 请求体 -----

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content,omitempty"` // pointer to allow null
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

// ----- 响应体 -----

type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Message      openAIRespMessage `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	// DeepSeek 特有：reasoning_content 在 message 字段里。openaiResponse.message
	// 抽出来定义。
}

type openAIRespMessage struct {
	Role             string           `json:"role"`
	Content          *string          `json:"content"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // DeepSeek only
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIErrorResp struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ----- 实现 Client -----

func (c *openAIClient) Chat(ctx context.Context, req Request) (Response, error) {
	wire := buildOpenAIRequest(c.cfg, req)
	body, err := json.Marshal(wire)
	if err != nil {
		return Response{}, fmt.Errorf("llm: openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequest("POST", strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+c.cfg.APIKey)

	var resp openAIResponse
	if _, _, err := doHTTP(ctx, c.http, httpReq, &resp); err != nil {
		return Response{}, err
	}
	if len(resp.Choices) == 0 {
		return Response{}, errors.New("llm: openai: response has 0 choices (truncated or upstream anomaly)")
	}
	choice := resp.Choices[0]
	return parseOpenAIResponse(choice.Message, choice.FinishReason, c.kind), nil
}

func buildOpenAIRequest(cfg Config, req Request) openAIRequest {
	wire := openAIRequest{
		Model:     cfg.Model,
		MaxTokens: req.MaxTokens,
		Tools:     buildOpenAITools(req.Tools),
	}
	if wire.MaxTokens <= 0 {
		wire.MaxTokens = cfg.MaxTokens
	}
	for _, m := range req.Messages {
		om := openAIMessage{Role: string(m.Role)}
		// Content 在多模态时是 string（Phase 4）；P1-10a 都是 string。
		if m.Content != "" {
			s := m.Content
			om.Content = &s
		}
		switch m.Role {
		case RoleSystem, RoleUser:
			// 已设 Role + Content
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				om.ToolCalls = make([]openAIToolCall, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					om.ToolCalls[i] = openAIToolCall{
						ID:    tc.ID,
						Type:  "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: tc.Name, Arguments: tc.Arguments},
					}
				}
			}
		case RoleTool:
			om.ToolCallID = m.ToolCallID
		}
		wire.Messages = append(wire.Messages, om)
	}
	return wire
}

func buildOpenAITools(defs []ToolDef) []openAITool {
	out := make([]openAITool, 0, len(defs))
	for _, d := range defs {
		t := openAITool{Type: "function"}
		t.Function.Name = d.Name
		t.Function.Description = d.Description
		t.Function.Parameters = map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{
					"type":        "string",
					"description": "工具参数（具体格式见 description 字段，或先用 help <tool> 查询）",
				},
			},
			"required": []string{"input"},
		}
		out = append(out, t)
	}
	return out
}

func parseOpenAIResponse(m openAIRespMessage, finish string, kind openAIKind) Response {
	out := Response{StopReason: finish}
	// 文本 + 推理
	if m.ReasoningContent != nil && *m.ReasoningContent != "" && kind == openAIKindDeepSeek {
		// DeepSeek 推理拼到 Text 前面，让 loop 看完整思路。
		// 用 "<think>...</think>\n" 标记，方便 Phase 4 做 UI 折叠。
		out.Text = "<think>" + *m.ReasoningContent + "</think>\n"
	}
	if m.Content != nil {
		out.Text += *m.Content
	}
	// tool_calls
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	// StopReason 归一化
	switch finish {
	case "stop":
		out.StopReason = "end_turn"
	case "tool_calls":
		out.StopReason = "tool_calls"
	case "length":
		out.StopReason = "max_tokens"
	}
	return out
}

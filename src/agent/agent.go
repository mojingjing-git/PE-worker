// Package agent 负责和 LLM 通信 + 工具循环编排。
//
// P1-10a 范围：HTTP 客户端 + provider 适配层 + 系统提示词。
// P1-10b 范围：循环（agent/loop.go + history.go + verdict.go）。
//
// 设计原则（PLAN §0.6 A11、§0.9 v1 硬规则）：
//
//	(1) **provider 适配层** — 三个 provider（Anthropic / OpenAI / DeepSeek）走不同
//	    的 wire 格式；DeepSeek 特有的 `reasoning_content` 也在这里吸收；
//	    上层 loop 只看统一的 Message / ToolCall / Response。
//
//	(2) **不假设"OpenAI 兼容 = 统一"** — DeepSeek 是 OpenAI 兼容但 tool_calls
//	    字段、空响应保护都有差异，单独分支处理。
//
//	(3) **CA bundle 走自己的** — 用 peagent/assets 嵌入的 cacert.pem 构池，
//	    绝不走系统根证书库（PE 镜像里没有新根 CA）。
//
//	(4) **所有导出 API 返 (T, error)** — L1 硬规则。
//
//	(5) **错误透传** — 4xx/5xx 把服务端 body 一并返出来，方便 PE 里排查（PE 没
//	    有浏览器能查文档；不返 body 等于让用户蒙）。
package agent

// Provider 标识 LLM 服务类型。
//
// Anthropic 走 /v1/messages；OpenAI / DeepSeek 都走 /v1/chat/completions。
// DeepSeek 虽然 "OpenAI 兼容"，但 reasoning_content、tool_calls 字段有差异，
// 所以独立成一个常量（不要做"openai compatible"通用适配器）。
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderDeepSeek  Provider = "deepseek"
)

// Role 是一轮对话的角色。统一表示，跨 provider 自适配。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型决定要调用的工具。
//
// ID 在 OpenAI 兼容协议下模型生成；在 Anthropic 协议下由我们
// （`tool_use_<idx>`）生成。
// Arguments 是**原始 JSON 字符串**（不是 map[string]any），让 provider
// 适配层决定怎么编码。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Image 是一张要发给模型的图片（Phase 4 才用，P1-10a 占位）。
type Image struct {
	// MediaType 是 MIME，如 "image/png"。
	MediaType string
	// Data 是原始字节。
	Data []byte
}

// Message 是一轮对话。
//
// 工具结果：Role=tool, ToolCallID=<对应 assistant 调用的 ID>, Content=<结果文本>。
// 工具调用：Role=assistant, Content=<可选推理/文本>, ToolCalls=[...]。
// 多模态：Role=user, Content=<文本>, Images=[...]。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Images     []Image    `json:"-"`
}

// ToolDef 是发给模型的工具描述。
//
// Description 包含 args 格式说明（保持 PLAN §0.6 系统提示词 <1000 token，
// "不写各工具用法，靠 help 工具自查"的约束）。Phase 4 可加 input_schema。
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Request 是发往 LLM 的一次请求。
type Request struct {
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int // 0 = 用 provider 默认
}

// Response 是 LLM 的一次返回。
//
// StopReason 含义跨 provider 统一：
//   - "end_turn"  / "stop"      → 正常文本结束
//   - "tool_use"  / "tool_calls" → 模型想调工具
//   - "max_tokens"              → 截断（要回灌或警告）
//   - ""                        → 解析不出来（异常路径，loop 要兜底）
type Response struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
}

// Config 是 llm.go 需要的最小配置子集（不 import cfg/，避免循环）。
type Config struct {
	Provider  Provider
	BaseURL   string // 不带尾斜杠；provider 适配层自己拼路径
	Model     string
	APIKey    string
	TimeoutS  int    // 默认 120
	MaxTokens int    // 默认 2048
}

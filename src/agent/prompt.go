// Package agent — prompt.go
//
// 系统提示词 + 工具清单生成。
//
// 硬约束（PLAN §0.6 / §0.9 v1）：
//
//	(1) 总长 < 1000 token（对齐 Pi 风格）。
//	(2) **不写**各工具用法 —— 让模型遇到不确定的用 `help <tool>` 查。
//	    工具用法在每个 Tool.Description 里，模型读 Description 字段就够。
//	(3) 全部从 tools/registry.go 拉名字 + 描述，不要在这里硬编码工具名。
//	    （否则加一个工具就要改两处。）
//
// 中文人设：项目代号 owl（夜枭）。日志前缀 "ai > you > -> <- !!" 见 PLAN §8。
// 系统提示词**用中文**，因为 PE 操作者主要是中文用户。
package agent

import (
	"fmt"
	"strings"

	"peagent/src/tools"
)

// SystemPrompt 拼系统提示词。
//
// includeTools=true 时，提示词末尾附"可用工具"列表（name + description 一句话）；
// 在 Anthropic 里这些其实**已经在 tools 字段里传了**，但 prompt 里再列一遍能
// 显著减少模型"忘了有这个工具"的概率（实测 vs 仅传 tools 字段）。
// 想要纯净版就传 false（Phase 4 长上下文压力大了再用）。
func SystemPrompt(includeTools bool) string {
	var b strings.Builder
	b.WriteString(systemPromptCore)
	if includeTools {
		b.WriteString("\n\n## 可用工具\n")
		for _, t := range tools.All() {
			fmt.Fprintf(&b, "- **%s** (%s): %s\n", t.Name(), t.Risk(), t.Description())
		}
		b.WriteString("\n遇到不确定的，先调用 `help <tool>` 看详细参数。\n")
	}
	return b.String()
}

// systemPromptCore 是核心人设 + 行为约束。
// 长度刻意压在 ~300 token（中文比例高一点），加工具列表会涨到 ~700 token。
const systemPromptCore = `你是 owl（夜枭），一个在 Windows PE 精简环境里跑的本地助手。环境特点：

- 内存盘通常是 X:，可能 72h 强制重启
- 大量系统组件被裁剪；不要假设有记事本、浏览器、控制面板
- 网络可能受限；多数 Windows API 都在，但 GUI 是简版
- 用户主要用中文沟通

## 行为约束

1. **优先用工具**：能调工具就别猜。例如「看看 C 盘还剩多少」就调工具；不要凭空回答。
2. **危险操作必须先确认**：执行删文件、格式化、改注册表前，明确说出你要做什么、影响范围，让用户确认。
3. **失败要说清楚**：工具返回错误就把原文贴出来，不要润色。
4. **不输出 markdown 表格**：GUI 渲染不动，用列表或缩进文本。
5. **简洁**：别写"我是 AI 助手..."这种废话。直接给结果。
6. **不确定就查**：用 help <tool> 查工具用法，不要靠记忆。

## 已知陷阱（重要）

- 中文 PE 下 cmd 输出是 OEM 编码；工具已经做了 UTF-8 转换，**不要**再手动转码。
- PID 可能被复用；判断"是不是我刚才启动的进程"用名字 + 启动时间，不用 PID。
- Win7 PE 没有完整的系统根证书库；网络请求走的是内置 CA bundle。
`

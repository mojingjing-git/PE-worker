// Package agent — history.go
//
// 会话历史：滑动窗口 + Phase 4 图片裁剪的占位结构。
//
// 约束（PLAN §0.5 / §0.6 / §0.9）：
//
//	(1) **滑动窗口**：超 maxHistoryMessages 条消息就丢最旧配对，
//	    system 永远保留（不丢）。
//	(2) **图片裁剪**（Phase 4 落地）：imgHistory 轮之外的 user 消息如果带图，
//	    把 Images 清空、Content 末尾加 "[截图已省略 ...]" ——
//	    只保留最近 N 轮的图片，避免 token 爆炸。
//	    P1-10b 占位：方法签名先定，等 Phase 4 截图工具来了再调。
//	(3) **奇数 user/assistant 配对保护**：削历史时如果切到一半，
//	    会出现"上一条是 assistant 但没 user/tool 应答"的非法序列 → API 报错。
//	    削时必须从**最前**开始、且保持 messages[0] 不是 assistant。
//	(4) **OOM 防御**：每条消息 Content 设上限 MaxContentBytes；超了截断 +
//	    标 "[truncated]"。不然 PE 里 72h 不重启 + 长会话 = 内存盘爆。
package agent

// MaxContentBytes 单条消息 Content 字节上限。
// 32KB ≈ 8K token（中文 1 字 ≈ 2-3 token，英文 1B ≈ 0.25 token）。
// 超了截断避免 LLM 上游拒收 + 本地内存涨。
const MaxContentBytes = 32 * 1024

// maxHistoryMessages 滑动窗口上限。
// 4 个 user+assistant 配对 = 8 轮 + 2 工具结果 = 约 10 轮对话。
// 配合 MaxTurns 一起做硬上限。
const maxHistoryMessages = 40

// imgHistory 保留最近几轮的图片（PLAN §0.6 B6 字段 imghistory 默认 2）。
// Phase 4 落地时这个值从 Config 拿；P1-10b 写死 2。
const imgHistory = 2

// ClipHistory 裁剪 m：超 maxHistoryMessages 就丢最旧配对；
// 任何消息 Content 超 MaxContentBytes 就截断。返回新 slice。
//
// 必须返新 slice（不能就地改 caller 的 slice header）—— Go 的 slice 是
// 栈上的 (ptr,len,cap) 三元组，函数参数拿到的是值拷贝。
func ClipHistory(m []Message) []Message {
	if len(m) == 0 {
		return m
	}
	// (1) 单条截断（就地改 OK，因为改的是元素不是 header）
	for i := range m {
		if len(m[i].Content) > MaxContentBytes {
			m[i].Content = m[i].Content[:MaxContentBytes] + "\n[truncated]"
		}
	}
	// (2) 滑动窗口
	if len(m) <= maxHistoryMessages {
		return m
	}
	// 找最后一个 system 的位置（system 必须保留）
	lastSystem := -1
	for i, mm := range m {
		if mm.Role == RoleSystem {
			lastSystem = i
		}
	}
	cut := len(m) - maxHistoryMessages
	// 保留 system 段（m[0..lastSystem+1]）+ 最新消息，**总长度 ≤ maxHistoryMessages**。
	// system 也算进 cap（system 也占 LLM context token）。
	// C-5 修复前：直接 m[:start] 取最旧，方向反了，模型丢所有近期上下文。
	// 修复后：保留 system + 最新 maxHistoryMessages-(system_len) 条。
	head := m[:lastSystem+1] // 整个 system 段（lastSystem=-1 时为空切片）
	systemLen := len(head)
	// tail 最多 maxHistoryMessages - systemLen 条
	maxTail := maxHistoryMessages - systemLen
	if maxTail < 0 {
		maxTail = 0
	}
	start := cut
	if start < lastSystem+1 {
		start = lastSystem + 1 // cut 落在 system 段时，从 system 之后开始
	}
	tail := m[start:]
	if len(tail) > maxTail {
		tail = tail[len(tail)-maxTail:] // 取最新 maxTail 条
	}
	out := make([]Message, 0, systemLen+len(tail))
	out = append(out, head...)
	out = append(out, tail...)
	// 配对修复：发给 LLM 的第一条非 system 消息必须是 **user**。
	//
	// 原因（两条都是审计实测复现的真 400）：
	//
	//	(a) 切在 assistant 中间 → 修复逻辑已有，处理成"跳到下一个 user/tool"。
	//	(b) 切在 **tool** 中间 → 原实现完全没处理！tool 消息的父
	//	    assistant(tool_calls) 被切掉了，OpenAI 直接报
	//	    "messages with role 'tool' must be a response to a preceding
	//	     message with 'tool_calls'"，Anthropic 报 tool_result 无对应 tool_use。
	//	    本会话之后**每次请求都 400**，用户只能重启进程。
	//	    触发门槛很低：40 条滑窗 ≈ 15 轮工具调用，14 个工具的 agent 很容易到。
	//
	// 修法：起点只允许是 user。若起点落在 tool/assistant 上，就向前回退到
	// 最近一条"带 tool_calls 的 assistant"（那才是这批 tool 的合法父节点）；
	// 再往前退到 user。
	msgStart := -1
	for i := 0; i < len(out); i++ {
		if out[i].Role == RoleSystem {
			continue
		}
		if out[i].Role == RoleUser {
			msgStart = i
			break
		}
		// 非 user 开头：向前回退找带 tool_calls 的 assistant 及其 user
		back := -1
		for j := i - 1; j >= 0; j-- {
			if out[j].Role == RoleAssistant && len(out[j].ToolCalls) > 0 {
				back = j
				break
			}
		}
		if back < 0 {
			// 找不到合法父节点：只保留 system 段，绝不能返回空 messages。
			// 返空会让上游 400（MAJOR-3）。
			return append([]Message{}, head...)
		}
		// 从 back 往前找 user 作为真正的起点
		for j := back; j >= 0; j-- {
			if out[j].Role == RoleUser {
				msgStart = j
				break
			}
		}
		if msgStart < 0 {
			return append([]Message{}, head...)
		}
		break
	}
	if msgStart < 0 {
		// 全是 system（或空）—— 保留 system，不返回空
		return append([]Message{}, head...)
	}
	// 起点落在 tail 里时，**必须把 head(system 段) 拼回去**。
	// out = head + tail，直接切 out[msgStart:] 会把 system 一起丢掉 ——
	// [system, user×N] 这种"全是 user、没有 tool"的常见历史就会触发
	// （回归门禁 TestClipHistory_KeepsSystem）。
	if msgStart >= len(head) && len(head) > 0 {
		merged := make([]Message, 0, len(head)+len(out)-msgStart)
		merged = append(merged, head...)
		merged = append(merged, out[msgStart:]...)
		return merged
	}
	return out[msgStart:]
}

// ClipImages 削图片（Phase 4 占位实现）。
//
// 当前版本：把超出 imgHistory 轮的 user 消息的 Images 清掉、Content 末尾
// 标 "[截图已省略 media=...]"。
//
// P1-10b 阶段 tools.All() 还没有 screenshot，所以这段逻辑**不会被触发**。
// 但保留方法签名和逻辑，让 Phase 4 接 screenshot 时不用改 API。
func ClipImages(m []Message) {
	// 找最近 imgHistory 轮的 user 消息下标。
	// "一轮" = 一个 user 消息 + 后续直到下一个 user 的 assistant/tool 配对。
	userIdx := []int{}
	for i, mm := range m {
		if mm.Role == RoleUser {
			userIdx = append(userIdx, i)
		}
	}
	if len(userIdx) == 0 {
		return
	}
	keepFrom := 0
	if len(userIdx) > imgHistory {
		keepFrom = userIdx[len(userIdx)-imgHistory]
	}
	// 清 [0, keepFrom) 的图；保留 [keepFrom, end) 的图
	for i := 0; i < keepFrom; i++ {
		if len(m[i].Images) > 0 {
			for _, img := range m[i].Images {
				m[i].Content += "\n[截图已省略 media=" + img.MediaType + "]"
			}
			m[i].Images = nil
		}
	}
}

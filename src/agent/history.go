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
	start := cut
	// system 段不能切；如果 cut 落在 system 段里，把 start 推到 system 之后
	if lastSystem >= 0 && start <= lastSystem {
		start = lastSystem + 1
	}
	out := make([]Message, 0, len(m)-start)
	out = append(out, m[:start]...)
	// 配对修复：如果 out[0] 现在是 assistant，削到第一个 user/tool
	if len(out) > 0 && out[0].Role == RoleAssistant {
		for i := 1; i < len(out); i++ {
			if out[i].Role == RoleUser || out[i].Role == RoleTool {
				out = append([]Message{}, out[i:]...)
				break
			}
		}
	}
	return out
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

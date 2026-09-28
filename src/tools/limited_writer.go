// tools/limited_writer.go —— S3 核心：工具输出硬上限。
//
// 【为什么必须限流】
//
// 原实现用 `cmd.CombinedOutput()`，它把 stdout+stderr 全部攒进一个 bytes.Buffer，
// 没有 LimitReader、没有临时文件、没有内存上限。而 OOM 在这个项目里是
// `runtime.throw` 而不是 `panic` —— **recover() 接不住，defer 也不执行**，
// 进程直接消失，无日志、无 dump（PLAN §0.8 硬规则第 3 条）。
//
// 审计实测：
//
//	`for /L %i in (1,1,3000000) do @echo 0123456789`
//	  → 命令还没跑完就已经吃掉 14.7 MB 缓冲 + 43 MB heap
//	`cat` 一个 3 MB 文件 → 3 MB 全部进内存
//	`dir C:\ /S` → 整个盘的目录树进内存
//
// PE 跑在内存盘上（scratch space 默认 32MB），一次 `dir /S` 就可能触发。
//
// 【设计要点】
//
//  1. 截断后**如实告诉模型**省略了多少字节 —— 不能让它以为看全了，
//     否则模型会基于不完整信息下结论（这比报错更坏）。
//  2. 截断点回退到 rune 边界，不劈开 UTF-8 多字节字符（否则乱码）。
//  3. Write 对被丢弃的字节返回 len(p)（假成功），否则 io.Copy 会当成
//     写失败而提前中止管道，导致子进程收到 SIGPIPE 类的怪行为。
package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxToolOutputBytes 是单个工具返回文本的硬上限（512 KiB）。
//
// 为什么不是更大：这些文本要作为 tool result 喂回 LLM，512 KiB 已经是
// 几十万 token 量级，远超任何合理命令的输出；而超限部分对模型无用。
// 需要大输出时让模型用 grep/find 之类的工具做**筛选**。
const maxToolOutputBytes = 512 << 10

// capWriter 是一个带上限的 io.Writer。超过 max 后丢弃新数据并计数。
type capWriter struct {
	w       *strings.Builder
	max     int
	written int
	dropped int64
}

// newCapWriter 返一个容量为 max 的限流 writer。
func newCapWriter(max int) *capWriter {
	if max <= 0 {
		max = maxToolOutputBytes
	}
	return &capWriter{w: &strings.Builder{}, max: max}
}

// Write 实现 io.Writer。
func (c *capWriter) Write(p []byte) (int, error) {
	room := c.max - c.written
	if room <= 0 {
		c.dropped += int64(len(p))
		return len(p), nil // 假成功：报告全部消费，但不真写
	}
	if len(p) <= room {
		n, err := c.w.Write(p)
		c.written += n
		if err != nil {
			return n, err
		}
		return len(p), nil
	}
	// 部分可写：只写前 room 字节，剩下计入 dropped。
	n, err := c.w.Write(p[:room])
	c.written += n
	c.dropped += int64(len(p) - room)
	if err != nil {
		return n, err
	}
	return len(p), nil // 仍报全部消费
}

// String 返回截断后的文本。**被截断时会在尾部追加一行如实说明**，
// 这样模型知道"还有 N 字节没看到"，而不是误以为自己看到了全部。
func (c *capWriter) String() string {
	if c.dropped == 0 {
		return c.w.String()
	}
	note := fmt.Sprintf("\n...[truncated %d bytes; total exceeded %d byte limit]", c.dropped, c.max)
	return trimPartialRune(c.w.String()) + note
}

// Dropped 返被丢弃的字节数（测试与诊断用）。
func (c *capWriter) Dropped() int64 { return c.dropped }

// trimPartialRune 去掉字符串末尾可能不完整的 UTF-8 编码（被字节上限切断的）。
// 没有这一步，截断处会出现半个汉字（豆腐块）。
//
// 实现用 Go 标准的 utf8.DecodeLastRune 而不是手工试长度：手工"回退 3 字节"
// 是不够的 —— 残留的片段本身可能仍是**合法**的短序列（"盘"=3 字节，
// 切在第 2 字节时留下 2 字节，正好是一个合法但错误的字符），
// 于是检查全绿、豆腐块照样出现。DecodeLastRune 按语义解码，遇到
// RuneError/TooShort 就整段砍掉，是唯一正确的做法。
func trimPartialRune(s string) string {
	if s == "" {
		return s
	}
	// utf8.ValidString 快路径：本来就完整就不用动。
	if utf8.ValidString(s) {
		return s
	}
	// 从尾部逐个字符回退，直到剩下的是完整合法序列。
	// 最多回退 4 步：一个 3 字节字符可能被切成 3 个单字节残片。
	rest := s
	for i := 0; i < 4; i++ {
		r, size := utf8.DecodeLastRuneInString(rest)
		if r == utf8.RuneError && size <= 1 {
			// 尾部这个字符是坏的 → 砍掉它
			rest = rest[:len(rest)-1]
			continue
		}
		break
	}
	return rest
}

// 注：read.go 里另有一个 limitOutput(s) —— 那是给"已经成形的一段文本"用的
// （os.ReadFile 的结果等）。本文件的 capWriter 是给**流式**的子进程输出用的，
// 两者场景不同，都必要。不要重复定义。

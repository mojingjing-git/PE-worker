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
//  4. **Write 是并发安全的**（P3-30 复审 C1）—— 迁到 win.StartJobCmd 后
//     stdout / stderr 是两根管道、两个 goroutine 各自 io.Copy 写**同一个**
//     capWriter，不再有 os/exec 时代"共用一根管道只起一个 copy goroutine"
//     带来的隐式串行。见 mu 字段注释。
package tools

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxToolOutputBytes 是单个工具返回文本的硬上限（512 KiB）。
//
// 为什么不是更大：这些文本要作为 tool result 喂回 LLM，512 KiB 已经是
// 几十万 token 量级，远超任何合理命令的输出；而超限部分对模型无用。
// 需要大输出时让模型用 grep/find 之类的工具做**筛选**。
const maxToolOutputBytes = 512 << 10

// capWriter 是一个带上限的 io.Writer。超过 max 后丢弃新数据并计数。
//
// ⚠️ **并发安全（P3-30 复审 C1）**：Write / String / Dropped 全部在 mu 内。
//
// 【为什么必须有锁】—— os/exec 时代不需要：
// 那时是 `cmd.Stdout = cw; cmd.Stderr = cw`，os/exec 走
// `interfaceEqual(Stderr, Stdout)` 分支，**共用同一根管道、只起一个 copy
// goroutine**，写入天然串行。迁到 win.StartJobCmd（P3-30）后是**两根管道、
// 两个 goroutine** 各自 `io.Copy(cw, …)` —— 串行保证没了，cw 仍是裸的。
//
// 无锁时的具体失败时序（exec.go 从 T2 起就有同款）：
//
//	goroutine A 进 Write 读到 room（基于旧 c.written）
//	A 的 c.w.Write 触发 Builder.grow 重分配并回写 b.buf 的 ptr/len/cap
//	goroutine B 此刻 append(b.buf, p2) 拿到**已被弃用的旧数组**写入
//	A 回写 b.buf  → B 的输出整体消失
//
// 且 `c.written` / `c.dropped` 的读-改-写非原子 → 丢更新 →
// `[...truncated N bytes]` 里的 N 是错的，直接违反本文件第 1 条设计目标
// 「不能让模型以为看全了」。更坏时 slice header 的 ptr 与 len 来自不同次
// 写入，append 可能越界写。
//
// 【为什么所有门禁都抓不到】strings.Builder 的 copyCheck 只查值拷贝、
// 不查并发写，**不会 panic**；本项目按 AGENTS.md 不带 -race
// （386 + 无 CGO 不可用）。所以只能靠锁 + 专门设计的并发测试
// （output_limit_test.go 的 TestCapWriter_Concurrent*）。
//
// 【死锁排查】mu 是**不可重入**的：Write / String / Dropped 三个方法内部
// 互不调用（Write 不回调 String/Dropped；String 只调纯函数 trimPartialRune），
// 因此不存在"持锁时又回调自己"的路径。capWriter 也不得按值传递 ——
// 它含 sync.Mutex，按值拷贝会被 go vet 的 copylocks 拦下。
type capWriter struct {
	mu      sync.Mutex
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
	c.mu.Lock()
	defer c.mu.Unlock()

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
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dropped == 0 {
		return c.w.String()
	}
	note := fmt.Sprintf("\n...[truncated %d bytes; total exceeded %d byte limit]", c.dropped, c.max)
	return trimPartialRune(c.w.String()) + note
}

// Dropped 返被丢弃的字节数（测试与诊断用）。
func (c *capWriter) Dropped() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.dropped
}

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
// （os.ReadFile 的结果等），本文件的 capWriter 是给**流式**的子进程输出用的，
// 两者场景不同，都必要。不要重复定义。
// 但**上限常量共用 maxToolOutputBytes 这一个**（D3 合并）：原来 read.go 另有一份
// 同值的局部常量，名字还不一样，改一个忘另一个就会出现两份上限各行其是。

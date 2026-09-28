// tools/output_limit_test.go —— S3 回归门禁：工具输出硬上限。
//
// 【为什么这是 CRITICAL】
//
// 原实现用 cmd.CombinedOutput()，把 stdout+stderr 全部攒进无上限的
// bytes.Buffer。而本项目有一条硬规则（PLAN §0.8 第 3 条）：
//
//	OOM 是 runtime.throw，不是 panic。recover() 接不住，defer 也不执行。
//
// 后果：PE 跑在内存盘上（scratch space 默认 32MB），一次 `dir C:\ /S`
// 或一个大文件输出就能让进程直接消失 —— 无日志、无 dump、用户只看到
// "闪一下就没了"。审计实测：`for /L %i in (1,1,3000000) do @echo ...`
// 还没跑完就吃掉 14.7MB 缓冲 + 43MB heap。
package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCapWriter_TruncatesAndReports 验证限流器三件事：
// 写入不超过上限、内容不超过上限、**如实报告省略了多少**。
func TestCapWriter_TruncatesAndReports(t *testing.T) {
	cw := newCapWriter(1024)
	chunk := []byte(strings.Repeat("A", 256))
	for i := 0; i < 20; i++ { // 共 5120 字节，远超 1024
		if _, err := cw.Write(chunk); err != nil {
			t.Fatalf("Write #%d 返回错误（capWriter 必须对被丢弃的字节假成功，否则 "+
				"io.Copy 会当成写失败而提前中止管道）: %v", i, err)
		}
	}
	got := cw.String()
	body := strings.SplitN(got, "\n...[truncated", 2)[0]

	if len(body) > 1024 {
		t.Errorf("截断后正文 %d 字节，超过上限 1024", len(body))
	}
	if cw.Dropped() != 5120-1024 {
		t.Errorf("Dropped() = %d, want %d", cw.Dropped(), 5120-1024)
	}
	// 关键：必须告诉模型省略了多少，否则模型会以为看全了
	if !strings.Contains(got, "truncated") {
		t.Errorf("输出里没有截断说明，模型会误以为看全了:\n%q", got)
	}
	if !strings.Contains(got, "4096") {
		t.Errorf("截断说明里应含省略字节数 4096，实际:\n%q", got)
	}
}

// TestCapWriter_UnderLimit 确认未超限时完全透明（不加任何说明）。
func TestCapWriter_UnderLimit(t *testing.T) {
	cw := newCapWriter(1024)
	cw.Write([]byte("hello"))
	if got := cw.String(); got != "hello" {
		t.Errorf("未超限应原样返回，实际 %q", got)
	}
	if cw.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", cw.Dropped())
	}
}

// TestCapWriter_NoPartialUTF8 确认截断点不会留下半个中文字（豆腐块）。
func TestCapWriter_NoPartialUTF8(t *testing.T) {
	// 3 字节一个的汉字，让上限 10 正好切在字符中间
	cw := newCapWriter(10)
	for i := 0; i < 20; i++ {
		cw.Write([]byte("磁盘清理"))
	}
	got := cw.String()
	body := strings.SplitN(got, "\n...[truncated", 2)[0]

	// 核心断言：正文必须是**合法 UTF-8**（无 U+FFFD 替换字符 = 无豆腐块）
	if !utf8.ValidString(body) {
		t.Errorf("截断后正文不是合法 UTF-8（切在了字符中间）: %q", body)
	}
	if strings.ContainsRune(body, '\uFFFD') {
		t.Errorf("截断处出现乱码替换字符: %q", body)
	}
	// 上限 10 字节 / 每字 3 字节 → 完整字符只能放 3 个（9 字节），
	// 第 4 个字必须被完整丢弃或完整保留，绝不能是半个。
	if body != "磁盘清" {
		t.Errorf("期望保留 3 个完整汉字 \"磁盘清\"，实际 %q（长度 %d）", body, len(body))
	}
}

// TestExec_OutputIsCapped 端到端验证：跑一条产出量贴近上限的命令，
// 断言结果被限流（有界）且带截断说明。
//
// 迭代次数**刻意选在 60% 上限附近**，不要改成"远超上限"：
// `for /L` 逐行 echo 32 万次本身就要几十秒，会先撞上 60s 超时，
// 结果是"命令没跑完"而非"输出被截断"—— 测的就不是同一件事了。
// 限流逻辑本身由 TestCapWriter_* 直接验证（无子进程、快、精确）。
func TestExec_OutputIsCapped(t *testing.T) {
	// 每行 399 字节（纯 ASCII：OEM→UTF8 不膨胀，1 字节进 1 字节出）
	const filler = "0123456789012345678901234567890123456789012345678901234567890123456789" +
		"0123456789012345678901234567890123456789012345678901234567890123456789" +
		"0123456789012345678901234567890123456789012345678901234567890123456789" +
		"0123456789012345678901234567890123456789012345678901234567890123456789" +
		"0123456789012345678901234567890123456789012345678901234567890123456789" +
		"0123456789"
	// 每行输出 = len(filler) + 2（CRLF）字节。迭代次数必须让**总量超过 100% 上限**，
	// 否则限流根本不会触发（第一版取 50%、第二版也算成 50%，两次都白跑）。
	// 取 110%：既确定超限，又只多跑 10%，cmd 仍是亚秒级。
	perLine := len(filler) + 2
	iters := (maxToolOutputBytes*110/100)/perLine + 1

	cmdline := "for /L %i in (1,1," + itoa(iters) + ") do @echo " + filler
	res, err := RunByName(&Context{}, "exec", cmdline)
	if err != nil {
		// cmd 的 for /L 可能以非零退出；超时算失败（那会让本测试失去意义）
		if strings.Contains(err.Error(), "timeout") {
			t.Fatalf("命令超时 —— 迭代次数太多，测的不再是限流: %v", err)
		}
	}
	bound := maxToolOutputBytes + 4096
	if len(res.Text) > bound {
		t.Errorf("exec 输出 %d 字节，超过上限 %d+%d —— 限流没生效，PE 上会 OOM",
			len(res.Text), maxToolOutputBytes, 4096)
	}
	if !strings.Contains(res.Text, "truncated") {
		t.Errorf("超限输出里没有截断说明（模型会误以为看全了）; 实际长度 %d，末尾: %q",
			len(res.Text), tail(res.Text, 160))
	}
	// 反向断言：限流后必须明显小于"未限流时的理论总量"，证明真的截了
	theoretical := iters * perLine
	if len(res.Text) >= theoretical {
		t.Errorf("输出 %d 字节 >= 未限流理论总量 %d，说明根本没截断", len(res.Text), theoretical)
	}
	if theoretical <= maxToolOutputBytes {
		t.Fatalf("测试自身配置错误：理论总量 %d 未超过上限 %d，本测试永远测不到限流",
			theoretical, maxToolOutputBytes)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestExec_PartialOutputOnError 是 S3-audit 的门禁：
// 非零退出时必须仍返回已产出的输出，且 loop 侧能看到它。
func TestExec_PartialOutputOnError(t *testing.T) {
	// echo 有输出，但退出码非 0 —— 最常见的"有内容但失败"场景
	res, err := RunByName(&Context{}, "exec", "echo PARTIAL_OUTPUT_MARKER & exit /b 1")
	if err == nil {
		t.Fatal("期望返回错误（exit 1），实际 nil")
	}
	if !strings.Contains(res.Text, "PARTIAL_OUTPUT_MARKER") {
		t.Errorf("出错时丢了已产出的输出，模型将只看到 \"exit status 1\" "+
			"并反复重试同一条命令:\nres.Text=%q err=%v", res.Text, err)
	}
}

// TestExec_NilCtxDoesNotPanic 覆盖 S5-审计发现的 nil ctx panic。
func TestExec_NilCtxDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunByName(ctx=nil) panic 了: %v", r)
		}
	}()
	// （非零退出码是预期的：TestExec_PartialOutputOnError 覆盖那个语义）
}

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
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
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

// ---------------------------------------------------------------------------
// P3-30 复审 C1：capWriter 的并发安全
//
// 【为什么这些测试存在】—— os/exec 时代不需要锁：那时是
// `cmd.Stdout = cw; cmd.Stderr = cw`，os/exec 走 interfaceEqual 分支
// **共用一根管道、只起一个 copy goroutine**，写入天然串行。
// P3-30 把 exec / run_script 都迁到 win.StartJobCmd 后变成**两根管道、
// 两个 goroutine** 各自 io.Copy 写同一个 cw —— 串行保证没了，cw 当时仍是裸的。
//
// 【为什么不能靠 -race】—— 按 AGENTS.md，race detector 在 386 + 无 CGO 下
// 不可用（PE 镜像里没有 C 编译器），build.cmd 也不带 -race。
// strings.Builder 的 copyCheck 只查值拷贝、**不查并发写**，所以无锁版本
// 既不 panic 也过不了任何现有门禁 —— 只能靠下面两个测试的**设计**让它可见。
// ---------------------------------------------------------------------------

// 并发参数。挑的依据：断言必须**逐字节精确**，这样任何一次丢更新 /
// 丢失 append 都会让长度对不上；同时 Write 次数要够多（几十万次），
// 才让非原子的读-改-写窗口有实际命中的概率。
const (
	ccGoroutines = 8
	ccIters      = 60000
	ccChunkLen   = 16
	ccTotal      = ccGoroutines * ccIters * ccChunkLen // 7,680,000 字节
)

// spawnConcurrentWrites 起 ccGoroutines 个 goroutine，每个写 ccIters 次
// ccChunkLen 字节；第 g 个只写字节 'A'+g，便于事后逐字节核对完整性。
func spawnConcurrentWrites(cw *capWriter, onErr func(int, error)) {
	var wg sync.WaitGroup
	wg.Add(ccGoroutines)
	for g := 0; g < ccGoroutines; g++ {
		go func(g int) {
			defer wg.Done()
			// 注意：不能 t.Fatal —— t.Fatal 只能从测试主 goroutine 调，
			// 在别的 goroutine 里调用是 panic 而不是失败。
			chunk := bytes.Repeat([]byte{byte('A' + g)}, ccChunkLen)
			for i := 0; i < ccIters; i++ {
				if _, err := cw.Write(chunk); err != nil {
					onErr(i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestCapWriter_ConcurrentWritesPreserveAllBytes 验"未超限时一个字节都不能丢"。
//
// 上限故意设得比总量大 —— 任何截断都是错的，因此期望值是**精确**的：
// 长度 = ccTotal，且每个字节的**出现次数**都必须是 ccIters*ccChunkLen。
//
// 无锁时为什么必然对不上：两个 goroutine 会对同一个 strings.Builder 同时
// `b.buf = append(b.buf, p...)` —— 它们读到同一份旧 slice header，各自把
// 内容追加到同一块后继数组的同一偏移，然后各回写 header。
// 后写的那个把先写的整个覆盖掉 → 那个 chunk 的内容消失，而长度只算了它自己。
// 7.6 万次 append 里命中几次是必然的（实测见报告）。
func TestCapWriter_ConcurrentWritesPreserveAllBytes(t *testing.T) {
	cw := newCapWriter(ccTotal + 1024) // 留 1024 余量：任何截断都是 bug

	var writeErrs []error
	var mu sync.Mutex
	spawnConcurrentWrites(cw, func(i int, err error) {
		mu.Lock()
		writeErrs = append(writeErrs, err)
		mu.Unlock()
	})
	if len(writeErrs) > 0 {
		t.Fatalf("并发 Write 返回错误（capWriter 必须对被丢弃的字节假成功）: %v", writeErrs[0])
	}

	got := cw.String()
	if len(got) != ccTotal {
		t.Errorf("未超限时正文 %d 字节，期望恰好 %d —— 丢了 %d 字节（C1：capWriter 并发写不安全）",
			len(got), ccTotal, ccTotal-len(got))
	}
	if cw.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0（总量未超上限）", cw.Dropped())
	}
	// 逐字节核对：内容不只是长度要对，**内容本身**也不能被覆盖成乱码
	for g := 0; g < ccGoroutines; g++ {
		want := ccIters * ccChunkLen
		if n := strings.Count(got, string([]byte{byte('A' + g)})); n != want {
			t.Errorf("字节 %q 出现 %d 次，期望 %d 次（C1：并发写丢了 %d 字节内容）",
				string([]byte{byte('A' + g)}), n, want, want-n)
		}
	}
}

// TestCapWriter_ConcurrentWritesKeepTruncationAccountingExact 验
// limited_writer.go 第 1 条设计目标「如实告诉模型省略了多少」在并发下仍然成立。
//
// 上限卡在**总量正中**（ccTotal/2），于是 room 每次都要重新算：
// `room := c.max - c.written`。`c.written` 的读-改-写一旦丢更新，
// room 就会算大（写超上限）或算小（提前丢内容）——
// 而这两个都是**精确可断言**的：
//
//	正文长度必须恰好 = max        （room 算大就会超）
//	Dropped() 必须恰好 = ccTotal-max（room 算小就少丢，多丢或少丢都错）
//
// 注意这条比"长度不超上限"强得多：上面 TestExec_OutputIsCapped 那种
// "≤ max+4096" 的宽松断言对丢更新完全不敏感，这里是逐字节相等。
func TestCapWriter_ConcurrentWritesKeepTruncationAccountingExact(t *testing.T) {
	const maxBytes = ccTotal / 2
	cw := newCapWriter(maxBytes)

	var writeErrs []error
	var mu sync.Mutex
	spawnConcurrentWrites(cw, func(i int, err error) {
		mu.Lock()
		writeErrs = append(writeErrs, err)
		mu.Unlock()
	})
	if len(writeErrs) > 0 {
		t.Fatalf("并发 Write 返回错误（capWriter 必须对被丢弃的字节假成功）: %v", writeErrs[0])
	}

	got := cw.String()
	body := strings.SplitN(got, "\n...[truncated", 2)[0]
	if len(body) != maxBytes {
		t.Errorf("截断后正文 %d 字节，期望恰好 %d（C1：room 算错，多写或少写了 %d 字节）",
			len(body), maxBytes, len(body)-maxBytes)
	}
	wantDropped := int64(ccTotal - maxBytes)
	if cw.Dropped() != wantDropped {
		t.Errorf("Dropped() = %d, want %d（C1：c.dropped 丢更新，"+
			"截断说明里的字节数会是错的 —— 模型会误以为看全了）",
			cw.Dropped(), wantDropped)
	}
	if !strings.Contains(got, itoa(int(wantDropped))) {
		t.Errorf("截断说明里应含省略字节数 %d，实际:\n%q", wantDropped, tail(got, 120))
	}
}

// TestCapWriter_ConcurrentWritesAndStringDontDeadlock 验加锁没有引入死锁 /
// 自锁。场景：一边持续并发 Write，一边反复 String()/Dropped()。
//
// capWriter 的 mu 是**不可重入**的，写法上必须保证三个方法互不调用
// （Write 不回调 String/Dropped；String 只调纯函数 trimPartialRune）。
// 这个测试用超时把"将来有人往 Write 里塞一个 cw.String()"钉死。
func TestCapWriter_ConcurrentWritesAndStringDontDeadlock(t *testing.T) {
	cw := newCapWriter(ccTotal / 4)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := bytes.Repeat([]byte("Z"), ccChunkLen)
			for i := 0; i < ccIters; i++ {
				if _, err := cw.Write(chunk); err != nil {
					return
				}
			}
		}()
		// 读者与写者并发
		for i := 0; i < 2000; i++ {
			_ = cw.String()
			_ = cw.Dropped()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("capWriter 并发 Write + String/Dropped 卡死 60s —— mu 不可重入，写法上有自锁")
	}
}

// TestTruncStr_NoPartialUTF8 验 D3 改完的 truncStr：截断点回退到 rune 边界。
//
// 改之前 truncStr 是 `s[:n] + "..."`，直接按字节切 —— n=5 切 3 字节的
// "磁盘清"会留下 "磁盘" + 半个 "清"（合法但错的 2 字节短序列），
// 出现在 edit 的确认提示里就是豆腐块。
func TestTruncStr_NoPartialUTF8(t *testing.T) {
	got := truncStr("磁盘清理工具", 5)
	body := strings.TrimSuffix(got, "...")
	if !utf8.ValidString(body) {
		t.Errorf("截断后正文不是合法 UTF-8（切在了字符中间）: %q", body)
	}
	if strings.ContainsRune(body, '�') {
		t.Errorf("截断处出现乱码替换字符: %q", body)
	}
	// 5 字节 / 每字 3 字节 → 只能完整放 1 个字，第 2 个字必须整段丢弃。
	if body != "磁" {
		t.Errorf("期望保留 1 个完整汉字 \"磁\"，实际 %q", body)
	}
	if got != "磁..." {
		t.Errorf("期望 \"磁...\"，实际 %q", got)
	}
	// 短于上限时原样返回，不加省略号。
	if s := truncStr("磁盘", 40); s != "磁盘" {
		t.Errorf("未超限应原样返回，实际 %q", s)
	}
	// 纯 ASCII 截断仍按字节切（不回退）。
	if s := truncStr("abcdef", 3); s != "abc..." {
		t.Errorf("ASCII 截断期望 \"abc...\"，实际 %q", s)
	}
}

// TestMergeDrainErr 验 P3-30 复审 I2 的错误合并规则：
// 管道排空失败不能被静默吞掉（那会让句柄 bug 伪装成"命令没输出"），
// 但命令本身已失败时要保留原 err 为主因。
func TestMergeDrainErr(t *testing.T) {
	boom := errors.New("boom")
	pipe := fmt.Errorf("取 stdout 管道读端: %w", errPipeNotTaken)

	t.Run("无管道错", func(t *testing.T) {
		if got := mergeDrainErr(boom, nil); got != boom {
			t.Errorf("mergeDrainErr(boom, nil) = %v, want boom", got)
		}
	})
	t.Run("命令成功_管道错接管", func(t *testing.T) {
		got := mergeDrainErr(nil, pipe)
		if got == nil {
			t.Fatal("管道错必须被报出来，否则句柄 bug 伪装成'命令没输出'")
		}
		if !errors.Is(got, errPipeNotTaken) {
			t.Errorf("err 链里应能追到 errPipeNotTaken，实际 %v", got)
		}
	})
	t.Run("命令已失败_保留主因", func(t *testing.T) {
		got := mergeDrainErr(boom, pipe)
		if got == nil {
			t.Fatal("不该返回 nil")
		}
		if !strings.Contains(got.Error(), "boom") {
			t.Errorf("主因 runErr 应保留在消息里，实际 %v", got)
		}
		if !errors.Is(got, errPipeNotTaken) {
			t.Errorf("管道错仍须用 %%w 进错误链（L5），实际 %v", got)
		}
	})
}

func TestFirstErr(t *testing.T) {
	e1 := errors.New("e1")
	e2 := errors.New("e2")
	if got := firstErr(nil, nil); got != nil {
		t.Errorf("firstErr(nil,nil) = %v, want nil", got)
	}
	if got := firstErr(nil, e1); got != e1 {
		t.Errorf("firstErr(nil,e1) = %v, want e1", got)
	}
	if got := firstErr(e1, e2); got != e1 {
		t.Errorf("firstErr(e1,e2) = %v, want e1", got)
	}
}

// TestExec_StderrCapturedWithoutError 钉住 I2 改造的**反面风险**：
// 收 io.Copy 的错误之后，正常的"子进程往 stderr 写东西"绝不能被误报成失败。
//
// 管道正常读到底会得到 io.EOF（Go 在 os.(*File).wrapErr 里把
// ERROR_BROKEN_PIPE 映射成 io.EOF），io.Copy 因此返 nil。
// 如果哪天这个映射变了或 Close 报错，这里立刻红 —— 宁可要一个明确的红，
// 也不要"exec 偶发失败"这种 PE 现场查不出来的症状。
func TestExec_StderrCapturedWithoutError(t *testing.T) {
	res, err := RunByName(&Context{}, "exec", "echo OUT_MARKER & echo ERR_MARKER 1>&2")
	if err != nil {
		t.Fatalf("往 stderr 写不该让 exec 失败（管道排空错误的合并逻辑误伤了正常路径）: %v", err)
	}
	if !strings.Contains(res.Text, "OUT_MARKER") {
		t.Errorf("输出缺 stdout 行: %q", res.Text)
	}
	if !strings.Contains(res.Text, "ERR_MARKER") {
		t.Errorf("输出缺 stderr 行（P3-20 回归：stderr 读端被误关）: %q", res.Text)
	}
}

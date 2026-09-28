//go:build windows

// Package win: wstr_concurrency_test.go —— T0 回归门禁：wstrKeep 的并发 append。
//
// 【为什么这条必须有】
//
//	wstrKeep / wstrKeepSlices 是包级全局 slice，被 UI 线程与 worker goroutine
//	**并发 append**（logx 对每条日志行都调 win.Ptr()）。无锁时实测（386 多核）：
//
//	  2 goroutine × 200,000 → 丢 29.7% – 34.4%
//	  4 goroutine × 200,000 → 丢 59.5% – 64.3%
//
//	丢失一次 append ≠ 少一行日志：那个 *uint16 从此只被 uintptr 引用，
//	GC 立刻可回收 → UI 线程 appendLog 解引用野指针 → 随机花屏/崩溃。
//
// ⚠️ **本测试不是竞态检测器。** 项目禁 `-race`（CGO_ENABLED=0 不可用），
// 所以它只能证明"锁加对了没有"（结构断言），**不能**证明不存在竞态。
// 真正的并发正确性靠上面的实测数据 + 代码审查保证。
package win

import (
	"strconv"
	"sync"
	"testing"
)

// TestWstrKeep_ConcurrentAppendNoLoss 是 T0 的核心门禁：
// N goroutine 并发调 Ptr/Hold/FromCmdline 共 M 次，一个都不能丢。
func TestWstrKeep_ConcurrentAppendNoLoss(t *testing.T) {
	const (
		goroutines = 8
		perG       = 2000
	)
	// ⚠️ 必须断言**增量**：wstrKeep 是包级全局，同包其它测试
	// （TestWstrKeepGrows 已塞了 500 条）已抬高 baseline，写 == M 首次运行必红。
	before := len(wstrKeep)
	beforeSlices := len(wstrKeepSlices)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				tag := "tag-" + strconv.Itoa(g) + "-" + strconv.Itoa(i)
				if p, err := Ptr(tag); err == nil {
					// 顺手 deref 一次，验证指针确实指向我们放进去的内容
					if *p == 0 {
						t.Errorf("Ptr(%q) 返回了指向 NUL 的指针", tag)
						return
					}
				} else {
					t.Errorf("Ptr(%q) = %v", tag, err)
					return
				}
				if _, err := FromCmdline("cmd /c echo " + tag); err != nil {
					t.Errorf("FromCmdline(%q) = %v", tag, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	gotPtr := len(wstrKeep) - before
	gotSlices := len(wstrKeepSlices) - beforeSlices
	want := goroutines * perG

	if gotPtr != want {
		t.Errorf("wstrKeep 丢了 %d 条（期望 %d，实际 %d）—— 并发 append 未受保护？"+
			"\n每丢一条，那个 *uint16 就只被 uintptr 引用，GC 可回收 → UI 线程解引用野指针",
			want-gotPtr, want, gotPtr)
	}
	if gotSlices != want {
		t.Errorf("wstrKeepSlices 丢了 %d 条（期望 %d，实际 %d）—— 并发 append 未受保护？",
			want-gotSlices, want, gotSlices)
	}
}

// TestHold_ConcurrentNoLoss 单独覆盖 Hold 路径。
// Hold 今天没有生产调用方，但它与 Ptr 共用 wstrKeep，漏加锁同样致命。
func TestHold_ConcurrentNoLoss(t *testing.T) {
	const (
		goroutines = 8
		perG       = 2000
	)
	before := len(wstrKeep)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				s := "h" + strconv.Itoa(g) + "-" + strconv.Itoa(i)
				p, err := Ptr(s)
				if err != nil {
					t.Errorf("Ptr: %v", err)
					return
				}
				Hold(p)
			}
		}(g)
	}
	wg.Wait()

	// 每轮调用产生 2 条（Ptr 一次 + Hold 一次）
	got := len(wstrKeep) - before
	want := goroutines * perG * 2
	if got != want {
		t.Errorf("Hold 路径丢了 %d 条（期望 %d，实际 %d）", want-got, want, got)
	}
}

// TestWstrKeep_GrowsAfterPtr 把 M1 的"永不 [:0] 重置"钉成可执行门禁。
//
// 原注释说"即使有'满了回收'的设计也是定时炸弹"，但那是注释 ——
// 没有任何东西会阻止后人真的写一行 `wstrKeep = wstrKeep[:0]`。`r`n//`r`n// 覆盖范围说明（复审订正）：这是**运行期行为检查**，不是代码扫描。`r`n// 能抓到"每次 Ptr 都重置"，抓不到"每 10000 条重置一次"这类条件重置 ——`r`n// 名字不要暗示它比这更强。
func TestWstrKeep_GrowsAfterPtr(t *testing.T) {
	// 记录当前长度，跑一遍常规路径，确认不会被清空
	before := len(wstrKeep)
	if _, err := Ptr("no-reset-probe"); err != nil {
		t.Fatalf("Ptr: %v", err)
	}
	if len(wstrKeep) != before+1 {
		t.Errorf("Ptr 之后 wstrKeep 长度 = %d，期望 %d（+1）—— 有 [:0] 重置？",
			len(wstrKeep), before+1)
	}
}

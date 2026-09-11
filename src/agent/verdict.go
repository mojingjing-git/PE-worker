// Package agent — verdict.go
//
// VERDICT 聚合器（v1 第四轮 L4 硬规则）：
//
//	> "多步对照测试的 VERDICT 必须把每一步的结果都纳入判据，不能只挑
//	>  '重要的'几条。"
//	> 落地为 src/agent/verdict.go。
//
// 用法（测试为主，loop 也可调）：
//
//	vd := NewVerdict("test_anthropic_text")
//	vd.Step("request_ok", err == nil)
//	vd.Step("text_match", resp.Text == "hello")
//	vd.Step("stop_reason", resp.StopReason == "end_turn")
//	if !vd.OK() { t.Fatal(vd.String()) }
//
// 特性：
//	- 每步 OK/FAIL 都记，最后 Print() 时按 PASS/FAIL 列出
//	- 总评：所有 step 都 OK → PASS；任一 FAIL → FAIL
//	- 线程安全（step append 用 mutex）
package agent

import (
	"fmt"
	"strings"
	"sync"
)

// Verdict 是测试/调试用的多步结果汇总。
type Verdict struct {
	mu    sync.Mutex
	name  string
	steps []verdictStep
}

type verdictStep struct {
	name string
	ok   bool
	note string
}

// NewVerdict 构造一个 VERDICT。name 是测试名/场景名。
func NewVerdict(name string) *Verdict {
	return &Verdict{name: name}
}

// Step 记一步结果。note 可选（失败原因/补充信息）。
func (v *Verdict) Step(name string, ok bool, note ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := ""
	if len(note) > 0 {
		n = strings.Join(note, " ")
	}
	v.steps = append(v.steps, verdictStep{name: name, ok: ok, note: n})
}

// StepErr 是 Step 的便捷版：err == nil 记 OK，note 是 err.Error()。
func (v *Verdict) StepErr(name string, err error) {
	if err == nil {
		v.Step(name, true)
		return
	}
	v.Step(name, false, err.Error())
}

// OK 返所有 step 是否都 OK。
func (v *Verdict) OK() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, s := range v.steps {
		if !s.ok {
			return false
		}
	}
	return true
}

// String 渲染最终 VERDICT 块。每步一行，末尾一行总评。
//
//	VERDICT test_anthropic_text
//	[OK]  request_ok
//	[OK]  text_match
//	[FAIL] stop_reason  expect=end_turn got=max_tokens
//	=====> PASS
func (v *Verdict) String() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "VERDICT %s\n", v.name)
	allOK := true
	for _, s := range v.steps {
		mark := "[OK] "
		if !s.ok {
			mark = "[FAIL]"
			allOK = false
		}
		fmt.Fprintf(&b, "%s %s", mark, s.name)
		if s.note != "" {
			fmt.Fprintf(&b, "  %s", s.note)
		}
		b.WriteByte('\n')
	}
	if allOK {
		fmt.Fprintf(&b, "=====> PASS\n")
	} else {
		fmt.Fprintf(&b, "=====> FAIL\n")
	}
	return b.String()
}

// Failures 返所有失败 step 的 name（用于 t.Errorf 只列失败项）。
func (v *Verdict) Failures() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, s := range v.steps {
		if !s.ok {
			out = append(out, s.name)
		}
	}
	return out
}

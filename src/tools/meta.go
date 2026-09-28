// tools/meta.go: help / selftest 两个 meta 工具。
//
// help 列出所有已注册工具名 + 描述 + 风险等级。
// selftest 校验 14 个工具的元信息（注册数 + 描述非空 + Risk 在 enum 内）——
// 描述里承诺了就真查, 少一个工具必须报 FAIL 而不是照样 OK（docs/11 S5-3）。
package tools

import (
	"fmt"
	"strings"
)

type helpTool struct{}

func (helpTool) Name() string        { return "help" }
func (helpTool) Description() string { return "列出所有可用工具名 + 描述 + 风险等级。" }
func (helpTool) Risk() RiskLevel     { return RiskRead }

func (helpTool) Run(_ *Context, _ string) (Result, error) {
	var sb strings.Builder
	sb.WriteString("可用工具 (按字母序):\n")
	for _, t := range All() {
		sb.WriteString(fmt.Sprintf("- %s [%s]: %s\n", t.Name(), t.Risk(), t.Description()))
	}
	return Result{Text: sb.String()}, nil
}

// expectRegisteredTools 是 docs/02 §3 的 14 工具清单长度。
// 改 allToolNames(tools_test.go) 时必须同步改这里。
const expectRegisteredTools = 14

type selftestTool struct{}

func (selftestTool) Name() string { return "selftest" }
func (selftestTool) Description() string {
	return fmt.Sprintf("自检：所有工具的元信息（名/描述/风险）合法 + 注册数 == %d。", expectRegisteredTools)
}
func (selftestTool) Risk() RiskLevel { return RiskRead }

func (selftestTool) Run(_ *Context, _ string) (Result, error) {
	tools := All()
	var problems []string

	if len(tools) != expectRegisteredTools {
		problems = append(problems, fmt.Sprintf("注册数 = %d, 期望 %d", len(tools), expectRegisteredTools))
	}
	badRisk := 0
	for _, t := range tools {
		if t.Name() == "" {
			problems = append(problems, "有空 name 的工具")
		}
		if t.Description() == "" {
			problems = append(problems, fmt.Sprintf("%s 描述为空", t.Name()))
		}
		if r := t.Risk(); r < RiskRead || r > RiskDangerous {
			badRisk++
		}
	}
	if badRisk > 0 {
		problems = append(problems, fmt.Sprintf("%d 个工具 Risk 越界", badRisk))
	}

	// 自检失败必须第一眼可见 —— FAIL 提到首行, 且不与 "OK:" 混排。
	if len(problems) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("registered tools: %d (expected %d)\n", len(tools), expectRegisteredTools))
		sb.WriteString("OK: 工具数 + 元信息校验通过\n")
		return Result{Text: sb.String()}, nil
	}
	var sb strings.Builder
	sb.WriteString("FAIL: selftest 未通过\n")
	for _, p := range problems {
		sb.WriteString("  - " + p + "\n")
	}
	sb.WriteString(fmt.Sprintf("registered tools: %d (expected %d)\n", len(tools), expectRegisteredTools))
	return Result{Text: sb.String()}, nil
}

func init() {
	Register(helpTool{})
	Register(selftestTool{})
}

// tools/meta.go: help / selftest 两个 meta 工具。
//
// help 列出所有已注册工具名 + 描述 + 风险等级。
// selftest 跑 14 个工具的 smoke test（检查注册 + 描述非空 + Risk 在 enum 内）。
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

type selftestTool struct{}

func (selftestTool) Name() string        { return "selftest" }
func (selftestTool) Description() string { return "自检：所有工具的元信息（名/描述/风险）合法 + 注册数 == 14。" }
func (selftestTool) Risk() RiskLevel     { return RiskRead }

func (selftestTool) Run(_ *Context, _ string) (Result, error) {
	tools := All()
	if len(tools) == 0 {
		return Result{Text: "FAIL: 没注册任何工具"}, nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("registered tools: %d\n", len(tools)))
	badRisk := 0
	for _, t := range tools {
		r := t.Risk()
		if r < RiskRead || r > RiskDangerous {
			badRisk++
		}
		if t.Name() == "" {
			sb.WriteString("  FAIL: 空 name\n")
		}
		if t.Description() == "" {
			sb.WriteString(fmt.Sprintf("  FAIL %s: 空描述\n", t.Name()))
		}
	}
	if badRisk > 0 {
		sb.WriteString(fmt.Sprintf("FAIL: %d 工具 Risk 越界\n", badRisk))
	} else {
		sb.WriteString("OK: 所有工具元信息合法\n")
	}
	return Result{Text: sb.String()}, nil
}

func init() {
	Register(helpTool{})
	Register(selftestTool{})
}

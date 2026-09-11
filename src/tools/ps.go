// tools/ps.go: 列进程 (用 win.SnapshotProcs)。
package tools

import (
	"fmt"
	"sort"
	"strings"

	"peagent/src/win"
)

type psTool struct{}

func (psTool) Name() string        { return "ps" }
func (psTool) Description() string { return "列运行中进程。args 可选: <name filter substring>。" }
func (psTool) Risk() RiskLevel     { return RiskRead }

func (psTool) Run(_ *Context, args string) (Result, error) {
	procs, err := win.SnapshotProcesses()
	if err != nil {
		return Result{}, fmt.Errorf("ps: snapshot: %w", err)
	}
	filter := strings.ToLower(strings.TrimSpace(args))

	pids := make([]uint32, 0, len(procs))
	for pid := range procs {
		if filter != "" && !strings.Contains(strings.ToLower(procs[pid].Name), filter) {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-8s %-8s %s\n", "PID", "PPID", "NAME"))
	for _, pid := range pids {
		info := procs[pid]
		sb.WriteString(fmt.Sprintf("%-8d %-8d %s\n", pid, info.PPID, info.Name))
	}
	return Result{Text: sb.String()}, nil
}

func init() {
	Register(psTool{})
}

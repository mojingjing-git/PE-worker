// tools/context.go: 共享的 context helpers（让 exec / run_script 不重复 import context）。
package tools

import (
	"context"
	"time"
)

// timeoutContext 返带超时的 ctx + cancel 函数 + 用于判 "是否超时" 的 sentinel err。
// 所有 exec-style 工具共用。
func timeoutContext(sec int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(sec)*time.Second)
}

// context_DeadlineExceeded 是 context.DeadlineExceeded 的本地别名,
// 让 run_script 看着对称（和 exec 一样比较 cctx.Err()）。
var context_DeadlineExceeded = context.DeadlineExceeded

package logx

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestLevelPrefix 验证 level 前缀映射稳定（避免改文案打乱日志）。
func TestLevelPrefix(t *testing.T) {
	cases := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "[D]"},
		{LevelInfo, "[I]"},
		{LevelWarn, "[W]"},
		{LevelError, "[E]"},
	}
	for _, c := range cases {
		if got := levelPrefix[c.level]; got != c.want {
			t.Errorf("Level %d 前缀 = %q, want %q", c.level, got, c.want)
		}
	}
}

// TestNoHWND_FallsBackToStderr 验证未绑 UI 时降级到 fallback sink。
func TestNoHWND_FallsBackToStderr(t *testing.T) {
	// 确保没残留 hwnd
	mu.Lock()
	oldHWND := hwnd
	hwnd = 0
	mu.Unlock()
	defer func() {
		mu.Lock()
		hwnd = oldHWND
		mu.Unlock()
	}()

	var buf bytes.Buffer
	oldOut := out
	SetOutput(&buf)
	defer SetOutput(oldOut)

	err := Info("hello %s", "world")
	if !errors.Is(err, ErrNoHWND) {
		t.Errorf("期望 ErrNoHWND, 实际 %v", err)
	}
	if !strings.Contains(buf.String(), "[I] hello world") {
		t.Errorf("buf 期望含 '[I] hello world', 实际 %q", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\r\n") {
		t.Errorf("buf 应以 \\r\\n 结尾, 实际 %q", buf.String())
	}
}

// lockedBuf 是 goroutine-safe 的字节缓冲（bytes.Buffer 本身不安全）。
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestConcurrentInfo 验证多 goroutine 并发调 Info 不 race。
func TestConcurrentInfo(t *testing.T) {
	mu.Lock()
	oldHWND := hwnd
	hwnd = 0
	mu.Unlock()
	defer func() {
		mu.Lock()
		hwnd = oldHWND
		mu.Unlock()
	}()

	var buf lockedBuf
	oldOut := out
	SetOutput(&buf)
	defer SetOutput(oldOut)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			Info("concurrent %d", n)
		}(i)
	}
	wg.Wait()
	// 100 行应都进 buf
	lines := bytes.Count([]byte(buf.String()), []byte{'\n'})
	if lines != 100 {
		t.Errorf("buf 应有 100 行, 实际 %d", lines)
	}
}

// TestError 验证 Error level 写 [E] 前缀。
func TestError(t *testing.T) {
	mu.Lock()
	oldHWND := hwnd
	hwnd = 0
	mu.Unlock()
	defer func() {
		mu.Lock()
		hwnd = oldHWND
		mu.Unlock()
	}()

	var buf bytes.Buffer
	oldOut := out
	SetOutput(&buf)
	defer SetOutput(oldOut)

	Error("test error: %d", 42)
	if !strings.Contains(buf.String(), "[E] test error: 42") {
		t.Errorf("buf = %q", buf.String())
	}
}

// TestAllLevels 验证 4 个 level 都能调。
func TestAllLevels(t *testing.T) {
	mu.Lock()
	oldHWND := hwnd
	hwnd = 0
	mu.Unlock()
	defer func() {
		mu.Lock()
		hwnd = oldHWND
		mu.Unlock()
	}()

	var buf bytes.Buffer
	oldOut := out
	SetOutput(&buf)
	defer SetOutput(oldOut)

	Debug("d")
	Info("i")
	Warn("w")
	Error("e")

	s := buf.String()
	for _, prefix := range []string{"[D] d", "[I] i", "[W] w", "[E] e"} {
		if !strings.Contains(s, prefix) {
			t.Errorf("buf 应含 %q, 实际 %q", prefix, s)
		}
	}
}

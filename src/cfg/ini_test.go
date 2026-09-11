package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvider_Anthropic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	os.WriteFile(path, []byte(`[llm]
base = https://api.anthropic.com
model = claude-3-5-sonnet-20241022
provider = anthropic
key = sk-ant-test
`), 0644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", c.LLM.Provider)
	}
	if c.LLM.Base != "https://api.anthropic.com" {
		t.Errorf("Base = %q", c.LLM.Base)
	}
}

func TestProvider_DeepSeek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	os.WriteFile(path, []byte(`[llm]
base = https://api.deepseek.com/v1
model = deepseek-chat
provider = deepseek
`), 0644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Provider != "deepseek" {
		t.Errorf("Provider = %q", c.LLM.Provider)
	}
}

func TestDefault_HasAllFields(t *testing.T) {
	c := Default()
	if c.LLM.KeyFile != "smith.key" {
		t.Errorf("KeyFile default = %q", c.LLM.KeyFile)
	}
	if c.LLM.Timeout != 120 {
		t.Errorf("Timeout default = %d, want 120", c.LLM.Timeout)
	}
	if !c.Agent.Confirm {
		t.Error("Confirm default should be true")
	}
	if c.Agent.MaxTurns != 10 {
		t.Errorf("MaxTurns default = %d, want 10", c.Agent.MaxTurns)
	}
	if c.Agent.ImgHistory != 2 {
		t.Errorf("ImgHistory default = %d, want 2", c.Agent.ImgHistory)
	}
	if c.UI.FontSize != 12 {
		t.Errorf("FontSize default = %d, want 12", c.UI.FontSize)
	}
}

func TestLoad_Sample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ini")
	content := `; comment
# also comment
[llm]
base = https://api.openai.com/v1/chat/completions
model = gpt-4
keyfile = smith.key
key = "sk-test 1234"
vision = 1
timeout = 90

[agent]
confirm = 0
whitelist = exec, read , write
maxturns = 5

[ui]
font = Consolas
fontsize = 14
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Base != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("Base = %q", c.LLM.Base)
	}
	if c.LLM.Model != "gpt-4" {
		t.Errorf("Model = %q", c.LLM.Model)
	}
	if c.LLM.KeyFile != "smith.key" {
		t.Errorf("KeyFile = %q", c.LLM.KeyFile)
	}
	if c.LLM.Key != "sk-test 1234" {
		t.Errorf("Key (引号应剥) = %q", c.LLM.Key)
	}
	if !c.LLM.Vision {
		t.Errorf("Vision 应 true")
	}
	if c.LLM.Timeout != 90 {
		t.Errorf("Timeout = %d", c.LLM.Timeout)
	}
	if c.Agent.Confirm {
		t.Errorf("Confirm 应 false")
	}
	if len(c.Agent.Whitelist) != 3 {
		t.Errorf("Whitelist 长度 = %d, 期望 3", len(c.Agent.Whitelist))
	}
	if c.Agent.Whitelist[0] != "exec" || c.Agent.Whitelist[1] != "read" || c.Agent.Whitelist[2] != "write" {
		t.Errorf("Whitelist = %v", c.Agent.Whitelist)
	}
	if c.Agent.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d", c.Agent.MaxTurns)
	}
	if c.UI.Font != "Consolas" {
		t.Errorf("Font = %q", c.UI.Font)
	}
	if c.UI.FontSize != 14 {
		t.Errorf("FontSize = %d", c.UI.FontSize)
	}
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.ini")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.KeyFile != "smith.key" {
		t.Errorf("KeyFile 默认 = %q", c.LLM.KeyFile)
	}
	if c.LLM.Timeout != 120 {
		t.Errorf("Timeout 默认 = %d", c.LLM.Timeout)
	}
	if c.LLM.Vision {
		t.Errorf("Vision 默认应 false")
	}
	if !c.Agent.Confirm {
		t.Errorf("Confirm 默认应 true")
	}
	if c.Agent.MaxTurns != 10 {
		t.Errorf("MaxTurns 默认 = %d", c.Agent.MaxTurns)
	}
	if c.Agent.ImgHistory != 2 {
		t.Errorf("ImgHistory 默认 = %d", c.Agent.ImgHistory)
	}
	if c.UI.FontSize != 12 {
		t.Errorf("FontSize 默认 = %d", c.UI.FontSize)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("Z:\\does\\not\\exist\\nope.ini")
	if err == nil {
		t.Fatal("期望 ErrFileNotFound")
	}
}

func TestLoad_InvalidBool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.ini")
	os.WriteFile(path, []byte("[llm]\nvision = maybe\n"), 0644)
	_, err := Load(path)
	if err == nil {
		t.Fatal("期望 parseBool err")
	}
}

func TestLoad_InvalidInt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad2.ini")
	os.WriteFile(path, []byte("[agent]\nmaxturns = many\n"), 0644)
	_, err := Load(path)
	if err == nil {
		t.Fatal("期望 parseInt err")
	}
}

func TestLoad_MissingEquals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad3.ini")
	os.WriteFile(path, []byte("[llm]\nbase https://example.com\n"), 0644)
	_, err := Load(path)
	if err == nil {
		t.Fatal("期望 ErrNoEquals")
	}
}

func TestLoad_UnknownSection_Ignored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.ini")
	os.WriteFile(path, []byte("[llm]\nbase = x\n[unknown]\nfoo = bar\n"), 0644)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("未知 section 不应报错: %v", err)
	}
	if c.LLM.Base != "x" {
		t.Errorf("Base 错: %q", c.LLM.Base)
	}
}

func TestUnquote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`"hello"`, "hello"},
		{`'world'`, "world"},
		{`plain`, "plain"},
		{`"mismatch'`, `"mismatch'`}, // 首尾不匹配不剥
		{`""`, ""},
		{`"with spaces"`, "with spaces"},
		{`"  trim me  "`, "  trim me  "}, // 引号内不 trim
	}
	for _, c := range cases {
		if got := unquote(c.in); got != c.want {
			t.Errorf("unquote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseBool(t *testing.T) {
	cases := []struct {
		in    string
		want  bool
		isErr bool
	}{
		{"1", true, false},
		{"0", false, false},
		{"true", true, false},
		{"false", false, false},
		{"yes", true, false},
		{"no", false, false},
		{"on", true, false},
		{"off", false, false},
		{"", false, false},
		{"maybe", false, true},
	}
	for _, c := range cases {
		got, err := parseBool(c.in)
		if c.isErr {
			if err == nil {
				t.Errorf("parseBool(%q) 应返 err", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBool(%q) err = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseBool(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

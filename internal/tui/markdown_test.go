package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderAssistantMarkdownWrapsCJKWithoutChangingListStructure(t *testing.T) {
	text := "这是一个用于检查中文长句换行行为的测试文本，应该按照终端宽度自然换行，而不是每个字单独占一行。"
	out := ansi.Strip(renderAssistant(text, 30, false))
	lines := nonEmptyLines(out)
	if len(lines) < 2 {
		t.Fatalf("long Chinese paragraph was not wrapped: %q", out)
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got > 30 {
			t.Fatalf("wrapped Chinese line width = %d: %q", got, line)
		}
		if cjkRuneCount(line) == 1 {
			t.Fatalf("Chinese text was split into a single-character line: %q", line)
		}
	}

	list := ansi.Strip(renderAssistant(
		"- 第一项中文内容应该保留为一个列表项并允许自然换行\n- 第二项中文内容也应该保留为另一个列表项",
		30,
		false,
	))
	if !strings.Contains(list, "第一项中文内容") || !strings.Contains(list, "第二项中文内容") {
		t.Fatalf("Chinese list content was lost: %q", list)
	}
	first := strings.Index(list, "第一项中文内容")
	second := strings.Index(list, "第二项中文内容")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("Chinese list order changed: %q", list)
	}
	for _, line := range nonEmptyLines(list) {
		if got := ansi.StringWidth(line); got > 30 {
			t.Fatalf("Chinese list line width = %d: %q", got, line)
		}
	}
}

func TestRenderAssistantMarkdownPreservesEnglishMarkdownAndCodeBlock(t *testing.T) {
	text := "- first item\n- second item\n\n```go\nfmt.Println(\"hello\")\n```"
	out := ansi.Strip(renderAssistant(text, 30, false))
	if !strings.Contains(out, "first item") || !strings.Contains(out, "second item") {
		t.Fatalf("English list content was lost: %q", out)
	}
	if !strings.Contains(out, "fmt.Println(\"hello\")") {
		t.Fatalf("code block content changed: %q", out)
	}
	if strings.Count(out, "fmt.Println") != 1 {
		t.Fatalf("code block was duplicated or split: %q", out)
	}
}

func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func cjkRuneCount(text string) int {
	count := 0
	for _, r := range text {
		if containsCJK(string(r)) {
			count++
		}
	}
	return count
}

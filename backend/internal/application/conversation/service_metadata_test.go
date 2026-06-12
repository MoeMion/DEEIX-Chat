package conversation

import (
	"errors"
	"strings"
	"testing"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
)

func TestBuildConversationMetadataMessagesTruncatesToBudget(t *testing.T) {
	userMsg := model.Message{Content: strings.Repeat("用户输入内容", 6000)}
	assistantMsg := model.Message{Content: strings.Repeat("助手回复内容", 6000)}

	got := buildConversationMetadataMessages(userMsg, assistantMsg)

	if tokens := estimateTokens(got); tokens > conversationMetadataMessageMaxTokens {
		t.Fatalf("metadata messages exceeded budget: got %d, want <= %d", tokens, conversationMetadataMessageMaxTokens)
	}
	if !strings.HasPrefix(got, "user:\n") {
		previewEnd := 32
		if len(got) < previewEnd {
			previewEnd = len(got)
		}
		t.Fatalf("expected metadata messages to keep leading user content, got %q", got[:previewEnd])
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatal("expected metadata messages to mark truncated content")
	}
}

func TestParseGeneratedConversationTitleHandlesLooseJSON(t *testing.T) {
	cases := map[string]string{
		`{"title":"项目协作规范说明文档"}`:                       "项目协作规范说明文档",
		"```markdown\n{\"title\":\"项目协作规范说明文档\"}\n```": "项目协作规范说明文档",
		"```json\n{\"title\":\"项目协作规范说明文档\"}\n```":     "项目协作规范说明文档",
		`{"title": 项目协作规范说明文档}`:                        "项目协作规范说明文档",
		`{title: 项目协作规范说明文档}`:                          "项目协作规范说明文档",
		`标题如下：{ "title": "项目协作规范说明文档" }`:               "项目协作规范说明文档",
	}
	for raw, want := range cases {
		got := sanitizeGeneratedConversationTitle(parseGeneratedConversationTitle(raw))
		if got != want {
			t.Fatalf("unexpected title for %q: got %q, want %q", raw, got, want)
		}
	}
}

func TestParseGeneratedConversationTitleRejectsDirtyOutput(t *testing.T) {
	cases := []string{
		`title: 项目协作规范说明文档`,
		`这是标题：项目协作规范说明文档`,
		`{"subtitle": 项目协作规范说明文档}`,
	}
	for _, raw := range cases {
		if got := sanitizeGeneratedConversationTitle(parseGeneratedConversationTitle(raw)); got != "" {
			t.Fatalf("expected dirty title output to be rejected for %q, got %q", raw, got)
		}
	}
}

func TestParseGeneratedConversationLabelsHandlesLooseJSON(t *testing.T) {
	cases := map[string][]string{
		`{"labels":["技术","运维"]}`:                     {"技术", "运维"},
		"```json\n{\"labels\":[\"技术\",\"运维\"]}\n```": {"技术", "运维"},
		`标签如下：{ "labels": ["技术", "运维"] }`:            {"技术", "运维"},
		`{labels: [技术, 运维]}`:                         {"技术", "运维"},
		`{tags: ["技术", "运维"]}`:                       {"技术", "运维"},
	}
	for raw, want := range cases {
		got := sanitizeGeneratedConversationLabels(parseGeneratedConversationLabels(raw))
		if len(got) != len(want) {
			t.Fatalf("unexpected labels length for %q: got %#v, want %#v", raw, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("unexpected labels for %q: got %#v, want %#v", raw, got, want)
			}
		}
	}
}

func TestConversationTitleFromFirstUserMessage(t *testing.T) {
	cases := map[string]string{
		"  这是一条很长的第一条用户消息，用来测试标题截断  ":        "这是一条很长的第一条用户消息，用来测试标",
		"\n\nhello   world   from   DEEIX\n": "hello world from DEE",
		"\"简短标题\"":                           "简短标题",
		"   ":                                "",
	}
	for input, want := range cases {
		if got := conversationTitleFromFirstUserMessage(input); got != want {
			t.Fatalf("unexpected first-message title for %q: got %q, want %q", input, got, want)
		}
	}
}

func TestBuildConversationTitleMessagesUsesCompletedTranscript(t *testing.T) {
	messages := []model.Message{
		{Role: "system", Content: "系统提示词"},
		{Role: "user", Content: "第一轮问题", Status: "completed"},
		{Role: "assistant", Content: "第一轮回答", Status: "completed"},
		{Role: "assistant", Content: "还在生成的回答", Status: "pending"},
		{Role: "tool", Content: "工具结果", Status: "completed"},
		{Role: "user", Content: "后续目标变化", Status: "completed"},
	}

	got := buildConversationTitleMessages(messages)

	if strings.Contains(got, "系统提示词") || strings.Contains(got, "工具结果") || strings.Contains(got, "还在生成的回答") {
		t.Fatalf("expected title messages to include only completed user/assistant transcript, got %q", got)
	}
	if !strings.Contains(got, "user:\n第一轮问题") || !strings.Contains(got, "assistant:\n第一轮回答") || !strings.Contains(got, "user:\n后续目标变化") {
		t.Fatalf("expected title messages to keep completed conversation content, got %q", got)
	}
}

func TestBuildConversationTitleMessagesPrioritizesLatestTranscript(t *testing.T) {
	messages := []model.Message{
		{Role: "user", Content: strings.Repeat("很早以前的问题", 6000), Status: "completed"},
		{Role: "assistant", Content: "很早以前的回答", Status: "completed"},
		{Role: "user", Content: "最新目标是重新整理订阅方案", Status: "completed"},
		{Role: "assistant", Content: "围绕最新目标继续分析", Status: "completed"},
	}

	got := buildConversationTitleMessages(messages)

	if strings.Contains(got, "很早以前的问题") {
		t.Fatalf("expected title messages to drop oldest content when over budget, got %q", got)
	}
	if !strings.Contains(got, "最新目标是重新整理订阅方案") || !strings.Contains(got, "围绕最新目标继续分析") {
		t.Fatalf("expected title messages to keep latest transcript, got %q", got)
	}
}

func TestConversationTitleFromMessagesPrefersLatestUserMessage(t *testing.T) {
	messages := []model.Message{
		{Role: "user", Content: "早期主题是部署配置", Status: "completed"},
		{Role: "assistant", Content: "助手先说了一段话", Status: "completed"},
		{Role: "user", Content: "最新主题是订阅方案", Status: "completed"},
	}

	if got := conversationTitleFromMessages(messages); got != "最新主题是订阅方案" {
		t.Fatalf("expected fallback title from latest user message, got %q", got)
	}
}

func TestConversationMetadataFallsBackToFirstUserMessageTitle(t *testing.T) {
	resolvedTitle := resolveConversationMetadataTitle(
		shouldAutoReplaceConversationTitle("新对话"),
		"",
		"设置为跟随后，Grok 4.3 对话标题没有自动生成",
	)
	if resolvedTitle == "" || resolvedTitle == "新对话" {
		t.Fatalf("expected first user message fallback title, got %q", resolvedTitle)
	}
}

func TestShouldAutoReplaceConversationTitleIncludesEnglishNewChat(t *testing.T) {
	if !shouldAutoReplaceConversationTitle("New chat") {
		t.Fatal("expected English localized new chat title to be replaceable")
	}
}

func TestConversationMetadataErrorDoesNotLeakWhenEitherTaskSucceeds(t *testing.T) {
	titleErr := errors.New("title failed")
	labelsErr := errors.New("labels failed")

	if err := resolveConversationMetadataError("有效标题", "", nil, labelsErr); err != nil {
		t.Fatalf("expected labels error not to fail metadata when title exists, got %v", err)
	}
	if err := resolveConversationMetadataError("", `["技术"]`, titleErr, nil); err != nil {
		t.Fatalf("expected title error not to fail metadata when labels exist, got %v", err)
	}
	if err := resolveConversationMetadataError("", "", titleErr, labelsErr); !errors.Is(err, titleErr) {
		t.Fatalf("expected first task error when nothing is generated, got %v", err)
	}
}

func TestShouldGenerateConversationMetadataAfterFailedFirstTurn(t *testing.T) {
	conversation := model.Conversation{
		Title:        "新会话",
		LabelsJSON:   "[]",
		MessageCount: 2,
	}

	if !shouldGenerateConversationMetadata(conversation) {
		t.Fatal("expected placeholder metadata to be generated even when failed messages already exist")
	}
}

func TestConversationLabelsEmpty(t *testing.T) {
	emptyCases := []string{"", "null", "[]", "  []  "}
	for _, value := range emptyCases {
		if !conversationLabelsEmpty(value) {
			t.Fatalf("expected labels %q to be empty", value)
		}
	}
	if conversationLabelsEmpty(`["技术"]`) {
		t.Fatal("expected non-empty labels to be preserved")
	}
}

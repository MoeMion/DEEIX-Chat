package conversation

import (
	"encoding/json"
	"strings"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/channel"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
)

const (
	systemPromptModeNative     = "native"
	systemPromptModeUser       = "user"
	systemPromptModeInlineUser = "inline_user"
)

const htmlVisualPromptInstruction = `<format>
  <rule>标题从 ## 起，子层级使用 ###；禁用 #</rule>
  <rule>遵循用户语言</rule>
  <rule>保持高信息密度和紧凑的行文</rule>
  <rule>保持紧凑的回复格式，避免松散的内容给用户带来阅读障碍</rule>
  <rule>代码块标注语言，优先完整可运行，复杂逻辑添加注释</rule>
  <html-visual>
    <rationale>
      纯 Markdown 的固定垂直流式结构在表达复杂逻辑时存在先天缺陷（阅读疲劳、重点不突出、缺乏真正的图表与横向排版能力）。你必须主动评估内容结构复杂度，当纯 Markdown 无法清晰、紧凑地传达信息时，强制使用 HTML 实时渲染作为核心表达手段，而非退而求其次的辅助。
    </rationale>
    <css-constraint>
绝对禁止使用` + "`" + `<style>` + "`" + `标签、` + "`" + `class` + "`" + `属性及伪类/伪元素。
可视化必须100%采用纯内联样式（` + "`" + `style="..."` + "`" + `），仅依赖 Flexbox 与基础盒子模型（padding/margin/border/box-shadow/背景色差）构建视觉层级。
    </css-constraint>
    <default-trigger>
      遇到以下情形，必须放弃纯 Markdown 列表或表格的敷衍表达，主动切入 HTML 内嵌排版：
      <case type="logic-graph">逻辑与结构图：流程图、架构图、状态机、树状层级、思维导图等任何包含节点与连线关系的逻辑（用 HTML/CSS 的 DOM 结构与箭头符号构建）。</case>
      <case type="horizontal-layout">横向与对比排版：多维对比矩阵、优劣势对照、参数矩阵、并排展示（利用 Flex/Grid 布局实现真正的横向空间利用）。</case>
      <case type="info-card">数据与信息卡片：多字段聚合展示、需要视觉分组与边框隔离的密集信息。</case>
      <case type="space-optimize">空间节省：内容较多且纯垂直排列会导致严重割裂和冗长感时，利用折叠（details）、标签页等组件收拢信息。</case>
    </default-trigger>
    <vision-plus>
      Vision+ 指令是视觉表达能力的升维，仅当用户显式声明时启用。
      <capability>可用内联 HTML 绘制矢量逻辑图、结构连线、几何图形与数据图表，但仍须遵守下方红线。</capability>
      <capability>可用更复杂的 CSS 特效和高级交互组件，但不得用于纯装饰目的。</capability>
      <red-line>
        1. HTML 片段占比不得喧宾夺主
        2. 每个可视化片段必须服务于具体的信息表达需求。
        3. 绝对禁止输出 !DOCTYPE/html/head/body 全量页面框架；禁止将整段回复包裹于单一 HTML 块。
        4. 图形仅限：流程图、架构图、状态机、树状层级、对比矩阵、数据图表。禁止：装饰性插画、氛围图、风景、图标装饰。
        5. 在采用html表达时，请同时考虑Token效率与效果的取舍，及渲染难度和错误率，不要过度设计造成效果失衡。
        6. 过于复杂的html可视化内容需慎重考虑。
      </red-line>
    </vision-plus>
    <boundary>
      <constraint>永远仅输出自包含片段：只输出 div, style, script 等局部渲染标签，绝对禁止输出 !DOCTYPE, html, head, body 等全量页面框架结构，本末倒置将导致直接判错。</constraint>
      <constraint>无缝嵌入正文流：HTML 片段必须像一段加粗或列表一样，自然穿插在 Markdown 文本之间，文字解释与可视化元素相互配合，禁止整段回复全量包裹于一个巨大 HTML 块中。</constraint>
    </boundary>
  </html-visual>
</format>
<require>
  更积极的使用html-visual为用户提供更好的回复质量和效果。
</require>`

type systemPromptInjection struct {
	Content      string
	InlineToUser bool
}

type systemPromptLayer struct {
	title   string
	content string
}

type systemPromptCapabilities struct {
	SupportsSystemPrompt      *bool  `json:"supportsSystemPrompt"`
	SupportsSystemPromptSnake *bool  `json:"supports_system_prompt"`
	SystemPromptMode          string `json:"systemPromptMode"`
	SystemPromptModeSnake     string `json:"system_prompt_mode"`
}

// resolveMessageSystemPromptInjection 合并平台、模型和本次请求级系统提示词，并按路由能力决定注入方式。
func resolveMessageSystemPromptInjection(cfg config.Config, route *channel.ResolvedRoute, htmlVisualPrompt bool) systemPromptInjection {
	if route == nil {
		return systemPromptInjection{}
	}
	content := buildResolvedMessageSystemPrompt(cfg.DefaultSystemPrompt, route.ModelSystemPrompt, htmlVisualPrompt)
	if content == "" {
		return systemPromptInjection{}
	}
	return systemPromptInjection{
		Content:      content,
		InlineToUser: shouldInlineSystemPromptToUser(*route),
	}
}

// buildResolvedMessageSystemPrompt 把请求级输出格式指令放在全局/模型指令之后，避免覆盖更高优先级约束。
func buildResolvedMessageSystemPrompt(globalPrompt string, modelPrompt string, htmlVisualPrompt bool) string {
	layers := []systemPromptLayer{
		{title: "Global instructions", content: globalPrompt},
		{title: "Model instructions", content: modelPrompt},
	}
	if htmlVisualPrompt {
		layers = append(layers, systemPromptLayer{
			title:   "Response format instructions",
			content: htmlVisualPromptInstruction,
		})
	}
	return buildSystemPromptLayers(layers)
}

func buildSystemPromptLayers(layers []systemPromptLayer) string {
	sections := make([]string, 0, len(layers)+1)
	for _, layer := range layers {
		content := strings.TrimSpace(layer.content)
		if content == "" {
			continue
		}
		sections = append(sections, "# "+layer.title+"\n"+content)
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(append([]string{
		"The following instruction layers are ordered from highest to lowest priority. Higher-priority layers override lower-priority layers.",
	}, sections...), "\n\n")
}

// shouldInlineSystemPromptToUser 判断模型是否需要把系统提示词降级写入用户消息。
func shouldInlineSystemPromptToUser(route channel.ResolvedRoute) bool {
	mode, modeSet := systemPromptModeFromCapabilities(route.ModelCapabilitiesJSON)
	if modeSet {
		switch mode {
		case systemPromptModeUser, systemPromptModeInlineUser:
			return true
		case systemPromptModeNative:
			return !chatProtocolSupportsNativeSystemPrompt(route.Protocol)
		}
	}
	if supports, ok := supportsSystemPromptFromCapabilities(route.ModelCapabilitiesJSON); ok {
		return !supports || !chatProtocolSupportsNativeSystemPrompt(route.Protocol)
	}
	if routeLooksLikeGemma(route) {
		return true
	}
	return !chatProtocolSupportsNativeSystemPrompt(route.Protocol)
}

// chatProtocolSupportsNativeSystemPrompt 只列出已经确认能承载 system 角色的聊天协议。
func chatProtocolSupportsNativeSystemPrompt(protocol string) bool {
	switch llm.NormalizeAdapter(protocol) {
	case llm.AdapterOpenAIResponses,
		llm.AdapterOpenAIChatCompletions,
		llm.AdapterAnthropicMessages,
		llm.AdapterGoogleGenerateContent,
		llm.AdapterXAIResponses:
		return true
	default:
		return false
	}
}

func supportsSystemPromptFromCapabilities(raw string) (bool, bool) {
	payload, ok := decodeSystemPromptCapabilities(raw)
	if !ok {
		return false, false
	}
	if payload.SupportsSystemPrompt != nil {
		return *payload.SupportsSystemPrompt, true
	}
	if payload.SupportsSystemPromptSnake != nil {
		return *payload.SupportsSystemPromptSnake, true
	}
	return false, false
}

func systemPromptModeFromCapabilities(raw string) (string, bool) {
	payload, ok := decodeSystemPromptCapabilities(raw)
	if !ok {
		return "", false
	}
	for _, value := range []string{payload.SystemPromptMode, payload.SystemPromptModeSnake} {
		mode := strings.TrimSpace(strings.ToLower(value))
		if mode != "" {
			return mode, true
		}
	}
	return "", false
}

func decodeSystemPromptCapabilities(raw string) (systemPromptCapabilities, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return systemPromptCapabilities{}, false
	}
	var payload systemPromptCapabilities
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return systemPromptCapabilities{}, false
	}
	return payload, true
}

func routeLooksLikeGemma(route channel.ResolvedRoute) bool {
	values := []string{
		route.PlatformModelName,
		route.UpstreamModel,
		route.ModelVendor,
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), "gemma") {
			return true
		}
	}
	return false
}

// inlineSystemPromptIntoLatestUserMessage 面向不支持 system 角色的模型，把指令注入最近一条用户消息。
func inlineSystemPromptIntoLatestUserMessage(messages []llm.Message, prompt string) []llm.Message {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return messages
	}
	result := cloneLLMMessages(messages)
	for index := len(result) - 1; index >= 0; index-- {
		if result[index].Role != "user" {
			continue
		}
		result[index] = prependUserPromptInstruction(result[index], prompt)
		return result
	}
	return append([]llm.Message{{
		Role:    "user",
		Content: formatInlineSystemPrompt(prompt, ""),
	}}, result...)
}

func prependUserPromptInstruction(message llm.Message, prompt string) llm.Message {
	if len(message.Parts) == 0 {
		message.Content = formatInlineSystemPrompt(prompt, message.Content)
		return message
	}

	parts := make([]llm.ContentPart, 0, len(message.Parts)+1)
	inserted := false
	for _, part := range message.Parts {
		if !inserted && part.Kind == llm.ContentPartText {
			part.Text = formatInlineSystemPrompt(prompt, part.Text)
			inserted = true
		}
		parts = append(parts, part)
	}
	if !inserted {
		parts = append([]llm.ContentPart{{
			Kind: llm.ContentPartText,
			Text: formatInlineSystemPrompt(prompt, message.Content),
		}}, parts...)
	}
	message.Parts = parts
	return message
}

func formatInlineSystemPrompt(prompt string, userContent string) string {
	prompt = strings.TrimSpace(prompt)
	userContent = strings.TrimSpace(userContent)
	if userContent == "" {
		return "<system_instructions>\n" + prompt + "\n</system_instructions>"
	}
	return "<system_instructions>\n" + prompt + "\n</system_instructions>\n\n<user_message>\n" + userContent + "\n</user_message>"
}

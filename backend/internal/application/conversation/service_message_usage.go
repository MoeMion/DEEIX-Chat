package conversation

import (
	"strings"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
)

type messageUsageAccumulator struct {
	observedUsage                   llm.Usage
	estimatedUnobservedInputTokens  int64
	currentCallEstimatedInputTokens int64
}

func (a *messageUsageAccumulator) beginCall(input llm.GenerateInput) {
	a.currentCallEstimatedInputTokens = estimateGenerateInputTokens(input)
}

func (a *messageUsageAccumulator) finishCall(observedInput bool) {
	if observedInput {
		a.currentCallEstimatedInputTokens = 0
		return
	}
	if a.currentCallEstimatedInputTokens <= 0 {
		return
	}
	a.estimatedUnobservedInputTokens += a.currentCallEstimatedInputTokens
	a.currentCallEstimatedInputTokens = 0
}

func (a *messageUsageAccumulator) addObservedUsage(delta llm.Usage) llm.Usage {
	if delta == (llm.Usage{}) {
		return a.observedUsage
	}
	a.observedUsage = addLLMUsage(a.observedUsage, delta)
	if delta.InputTokens > 0 {
		a.currentCallEstimatedInputTokens = 0
	}
	return a.observedUsage
}

func (a *messageUsageAccumulator) setObservedUsage(usage llm.Usage) {
	a.observedUsage = usage
	if usage.InputTokens > 0 {
		a.currentCallEstimatedInputTokens = 0
	}
}

func (a *messageUsageAccumulator) usage() llm.Usage {
	return a.observedUsage
}

func (a *messageUsageAccumulator) interruptedInputTokens() int64 {
	return a.observedUsage.InputTokens + a.estimatedUnobservedInputTokens + a.currentCallEstimatedInputTokens
}

func (a *messageUsageAccumulator) effectiveInputTokens(promptFallback int64) int64 {
	inputTokens := a.observedUsage.InputTokens + a.estimatedUnobservedInputTokens
	if inputTokens > 0 {
		return inputTokens
	}
	if promptFallback > 0 {
		return promptFallback
	}
	return 0
}

func resolveObservedOrEstimatedOutputTokens(observedTokens int64, assistantText string) int64 {
	return resolveObservedOrEstimatedTokens(observedTokens, estimateTokens(assistantText))
}

func resolveObservedOrEstimatedTokens(observedTokens int64, estimatedTokens int64) int64 {
	if observedTokens > 0 {
		return observedTokens
	}
	if estimatedTokens > 0 {
		return estimatedTokens
	}
	return 0
}

func resolveObservedOrHigherEstimatedOutputTokens(observedTokens int64, assistantText string) int64 {
	return resolveObservedOrHigherEstimatedTokens(observedTokens, estimateTokens(assistantText))
}

func resolveObservedOrHigherEstimatedTokens(observedTokens int64, estimatedTokens int64) int64 {
	if estimatedTokens > observedTokens {
		return estimatedTokens
	}
	if observedTokens > 0 {
		return observedTokens
	}
	return 0
}

func estimateGenerateInputTokens(input llm.GenerateInput) int64 {
	tokens := estimatePromptTokens(input.Messages)
	if instructions := strings.TrimSpace(input.Instructions); instructions != "" {
		tokens += estimateTokens(instructions) + 4
	}
	if !input.DisableTools {
		tokens += estimateToolDefinitionTokens(input.Tools)
	}
	return tokens
}

func estimateToolDefinitionTokens(tools []llm.ToolDefinition) int64 {
	if len(tools) == 0 {
		return 0
	}
	var tokens int64 = 2
	for _, tool := range tools {
		tokens += estimateTokens(tool.Name)
		tokens += estimateTokens(tool.Description)
		tokens += estimateTokens(string(tool.InputSchema))
		tokens += 12
	}
	return tokens
}

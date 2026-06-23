package conversation

import (
	"testing"
	"time"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
)

func TestCanceledGenerationWithObservedUsageIsRetainedForBilling(t *testing.T) {
	input := persistInterruptedMessageGenerationInput{
		UserMessage:          &model.Message{},
		AssistantMessage:     &model.Message{},
		EstimatedInputTokens: 12,
		Usage:                llm.Usage{InputTokens: 40, ReasoningTokens: 6},
		AssistantLatency:     25,
		Error:                ErrMessageGenerationCanceled,
		StartedAt:            time.Now(),
	}

	if !shouldPersistInterruptedMessageGeneration(input) {
		t.Fatal("expected canceled generation with observed usage to be retained")
	}
	metrics := resolveInterruptedMessageGenerationMetrics(input)
	if metrics.InputTokens != 40 || metrics.ReasoningTokens != 6 {
		t.Fatalf("expected observed usage to be preserved, got %#v", metrics)
	}
	if status := retainedGenerationStatus(input.Error); status != "canceled" {
		t.Fatalf("expected canceled status, got %q", status)
	}
}

func TestCanceledGenerationWithoutUsageOrOutputIsNotRetained(t *testing.T) {
	input := persistInterruptedMessageGenerationInput{
		UserMessage:      &model.Message{},
		AssistantMessage: &model.Message{},
		Error:            ErrMessageGenerationCanceled,
		StartedAt:        time.Now(),
	}

	if shouldPersistInterruptedMessageGeneration(input) {
		t.Fatal("expected empty canceled generation to stay non-billable")
	}
}

func TestCanceledGenerationAfterUpstreamCallUsesEstimatedInputFallback(t *testing.T) {
	input := persistInterruptedMessageGenerationInput{
		UserMessage:          &model.Message{},
		AssistantMessage:     &model.Message{},
		EstimatedInputTokens: 32,
		UpstreamCallStarted:  true,
		Error:                ErrMessageGenerationCanceled,
		StartedAt:            time.Now(),
	}

	if !shouldPersistInterruptedMessageGeneration(input) {
		t.Fatal("expected canceled upstream call to be retained with estimated input usage")
	}
	metrics := resolveInterruptedMessageGenerationMetrics(input)
	if metrics.InputTokens != 32 || metrics.OutputTokens != 0 {
		t.Fatalf("expected estimated input fallback without output charge, got %#v", metrics)
	}
}

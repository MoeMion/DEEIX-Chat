package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/llm"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
)

const (
	toolResultModelBudgetChars        = 12000
	toolResultReferenceThresholdChars = 50000
	toolResultReferencePreviewChars   = 2000
	toolResultAggregateBudgetChars    = 200000
)

const (
	toolResultBudgetRetainedIn = "server-side tool call record"
	toolResultOpaquePreviewMax = 512
)

type executeAssistantToolCallsInput struct {
	UserID         uint
	ConversationID uint
	MessageID      uint
	RequestID      string
	RunID          string
	ToolCalls      []llm.ToolCall
	ToolCallLimit  int
	TraceRecorder  *messageTraceRecorder
	ToolNameMap    map[string]string
	MCPConfigs     map[string]mcp.CallConfig
	ToolSchemas    map[string]json.RawMessage
	Ledger         *toolExecutionLedger
}

type executeAssistantToolCallsResult struct {
	Rows                  []model.ToolCall
	ToolResults           []llm.ToolResult
	ExecutedToolCalls     []llm.ToolCall
	PersistedToolCallKeys map[string]struct{}
	FatalErr              error
}

type toolExecutionRecord struct {
	row    model.ToolCall
	result llm.ToolResult
}

type toolExecutionSlot struct {
	row       model.ToolCall
	result    llm.ToolResult
	persisted bool
}

type toolExecutionLedger struct {
	records map[string]toolExecutionRecord
}

func newToolExecutionLedger() *toolExecutionLedger {
	return &toolExecutionLedger{records: map[string]toolExecutionRecord{}}
}

func (s *Service) executeAssistantToolCalls(ctx context.Context, input executeAssistantToolCallsInput) executeAssistantToolCallsResult {
	toolCalls := input.ToolCalls
	if input.ToolCallLimit > 0 && len(toolCalls) > input.ToolCallLimit {
		toolCalls = toolCalls[:input.ToolCallLimit]
	}
	if len(toolCalls) == 0 {
		return executeAssistantToolCallsResult{}
	}
	executedToolCalls := append([]llm.ToolCall(nil), toolCalls...)
	if input.TraceRecorder != nil {
		summary, markdown, payload := buildToolTrace(buildRequestedToolCallRows(toolCalls, input.ToolNameMap, input.RunID))
		input.TraceRecorder.syncToolSection(summary, markdown, payload, messageTraceStatusStreaming)
	}

	slots := make([]toolExecutionSlot, len(toolCalls))
	var fatalErr error
	for i, item := range toolCalls {
		modelToolName := strings.TrimSpace(item.ToolName)
		executionToolName := resolveExecutionToolName(modelToolName, input.ToolNameMap)
		row := model.ToolCall{
			MessageID:      input.MessageID,
			ConversationID: input.ConversationID,
			UserID:         input.UserID,
			RunID:          input.RunID,
			ToolCallID:     strings.TrimSpace(item.ToolCallID),
			ToolType:       normalizeToolType(item.ToolType),
			ToolName:       executionToolName,
			Status:         "requested",
			LatencyMS:      0,
			InputJSON:      strings.TrimSpace(item.ArgumentsJSON),
			OutputJSON:     "",
			ErrorJSON:      "",
		}

		mcpConfig := resolveMCPConfig(modelToolName, input.MCPConfigs)
		if mcpConfig == nil {
			row.Status = "error"
			row.ErrorJSON = toolNotEnabledForRunMessage(modelToolName)
			slots[i] = toolExecutionSlot{
				row:    row,
				result: buildToolResultForModel(row, modelToolName, false),
			}
			if fatalErr == nil {
				fatalErr = fmt.Errorf("model requested tool %q, but it is not enabled for this run", modelToolName)
			}
			if input.Ledger != nil {
				input.Ledger.store(row.ToolName, row.InputJSON, toolExecutionRecord{row: row, result: slots[i].result})
			}
			continue
		}

		normalizedInput, validationErr := normalizeToolArguments(row.InputJSON, input.ToolSchemas[modelToolName])
		if validationErr != nil {
			row.Status = "error"
			row.ErrorJSON = validationErr.Error()
			slots[i] = toolExecutionSlot{
				row:    row,
				result: buildToolResultForModel(row, modelToolName, false),
			}
			if input.Ledger != nil {
				input.Ledger.store(row.ToolName, row.InputJSON, toolExecutionRecord{row: row, result: slots[i].result})
			}
			continue
		}
		row.InputJSON = normalizedInput

		if input.Ledger != nil {
			if previous, ok := input.Ledger.lookup(row.ToolName, row.InputJSON); ok {
				slot := buildRepeatedToolSlot(row, modelToolName, previous)
				persisted := s.persistToolCallResult(ctx, &slot.row)
				slot.result = buildToolResultForModel(slot.row, modelToolName, persisted)
				slot.persisted = persisted
				slots[i] = slot
				continue
			}
		}

		toolStartedAt := time.Now()
		outputJSON, executeErr := s.executeToolCall(ctx, ExecuteToolInput{
			UserID:         input.UserID,
			ConversationID: input.ConversationID,
			RequestID:      strings.TrimSpace(input.RequestID),
			ToolName:       row.ToolName,
			ArgumentsJSON:  row.InputJSON,
			MCPConfig:      mcpConfig,
		})
		row.LatencyMS = time.Since(toolStartedAt).Milliseconds()
		if row.LatencyMS < 0 {
			row.LatencyMS = 0
		}
		if executeErr != nil {
			row.Status = "error"
			row.ErrorJSON = strings.TrimSpace(executeErr.Error())
		} else {
			row.Status = "success"
			row.OutputJSON = strings.TrimSpace(outputJSON)
			if row.OutputJSON == "" {
				row.OutputJSON = "{}"
			}
		}
		persisted := s.persistToolCallResult(ctx, &row)
		result := buildToolResultForModel(row, modelToolName, persisted)
		slots[i] = toolExecutionSlot{
			row:       row,
			result:    result,
			persisted: persisted,
		}
		if input.Ledger != nil {
			input.Ledger.store(row.ToolName, row.InputJSON, toolExecutionRecord{row: row, result: result})
		}
	}

	rows := make([]model.ToolCall, 0, len(slots))
	toolResults := make([]llm.ToolResult, 0, len(slots))
	persistedToolCallKeys := make(map[string]struct{})
	enforceToolResultAggregateBudget(slots)
	for _, slot := range slots {
		rows = append(rows, slot.row)
		toolResults = append(toolResults, slot.result)
		if slot.persisted {
			persistedToolCallKeys[toolCallPersistenceKey(slot.row)] = struct{}{}
		}
	}
	if input.TraceRecorder != nil {
		summary, markdown, payload := buildToolTrace(rows)
		input.TraceRecorder.appendToolSection(summary, markdown, payload, messageTraceStatusCompleted)
		input.TraceRecorder.completeTools()
	}
	return executeAssistantToolCallsResult{
		Rows:                  rows,
		ToolResults:           toolResults,
		ExecutedToolCalls:     executedToolCalls,
		PersistedToolCallKeys: persistedToolCallKeys,
		FatalErr:              fatalErr,
	}
}

func toolExecutionHasError(rows []model.ToolCall) bool {
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Status), "error") {
			return true
		}
	}
	return false
}

func buildRequestedToolCallRows(toolCalls []llm.ToolCall, toolNameMap map[string]string, runID string) []model.ToolCall {
	rows := make([]model.ToolCall, 0, len(toolCalls))
	for _, item := range toolCalls {
		modelToolName := strings.TrimSpace(item.ToolName)
		rows = append(rows, model.ToolCall{
			RunID:      runID,
			ToolCallID: strings.TrimSpace(item.ToolCallID),
			ToolType:   normalizeToolType(item.ToolType),
			ToolName:   resolveExecutionToolName(modelToolName, toolNameMap),
			Status:     "requested",
			InputJSON:  strings.TrimSpace(item.ArgumentsJSON),
		})
	}
	return rows
}

func buildRepeatedToolSlot(row model.ToolCall, modelToolName string, previous toolExecutionRecord) toolExecutionSlot {
	row.LatencyMS = 0
	switch previous.row.Status {
	case "success", "reused":
		row.Status = "reused"
		row.OutputJSON = previous.row.OutputJSON
		result := previous.result
		result.ToolCallID = row.ToolCallID
		result.ToolName = modelToolName
		result.Status = "success"
		return toolExecutionSlot{
			row:    row,
			result: result,
		}
	default:
		row.Status = "error"
		row.ErrorJSON = "same tool call already failed in this run; adjust arguments, choose another source, or answer from available results"
		return toolExecutionSlot{
			row: row,
			result: llm.ToolResult{
				ToolCallID: row.ToolCallID,
				ToolName:   modelToolName,
				Status:     row.Status,
				Error:      row.ErrorJSON,
			},
		}
	}
}

func (s *Service) persistToolCallResult(ctx context.Context, row *model.ToolCall) bool {
	if s == nil || s.repo == nil || row == nil {
		return false
	}
	if err := s.repo.CreateConversationToolCall(ctx, row); err != nil {
		return false
	}
	return row.ID > 0
}

func buildToolResultForModel(row model.ToolCall, modelToolName string, persisted bool) llm.ToolResult {
	return llm.ToolResult{
		ToolCallID: row.ToolCallID,
		ToolName:   modelToolName,
		OutputJSON: budgetToolOutputForModel(row, toolResultModelBudgetChars, persisted),
		Status:     row.Status,
		Error:      row.ErrorJSON,
	}
}

func enforceToolResultAggregateBudget(slots []toolExecutionSlot) {
	total := 0
	for _, slot := range slots {
		total += len([]rune(strings.TrimSpace(slot.result.OutputJSON)))
	}
	if total <= toolResultAggregateBudgetChars {
		return
	}
	candidates := make([]int, 0, len(slots))
	for index, slot := range slots {
		if !slot.persisted || strings.TrimSpace(slot.row.OutputJSON) == "" || strings.HasPrefix(strings.TrimSpace(slot.result.OutputJSON), "<persisted-tool-output") {
			continue
		}
		status := strings.TrimSpace(slot.row.Status)
		if status != "success" && status != "reused" {
			continue
		}
		candidates = append(candidates, index)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := len([]rune(strings.TrimSpace(slots[candidates[i]].result.OutputJSON)))
		right := len([]rune(strings.TrimSpace(slots[candidates[j]].result.OutputJSON)))
		return left > right
	})
	for _, index := range candidates {
		if total <= toolResultAggregateBudgetChars {
			break
		}
		current := strings.TrimSpace(slots[index].result.OutputJSON)
		replacement := buildPersistedToolOutputForModel(slots[index].row, toolResultReferencePreviewChars)
		slots[index].result.OutputJSON = replacement
		total = total - len([]rune(current)) + len([]rune(replacement))
	}
}

func toolNotEnabledForRunMessage(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return "tool is not enabled for this run"
	}
	return fmt.Sprintf("tool %s is not enabled for this run", name)
}

func budgetToolOutputForModel(row model.ToolCall, maxChars int, persisted bool) string {
	value := strings.TrimSpace(row.OutputJSON)
	if value == "" || maxChars <= 0 || len([]rune(value)) <= maxChars {
		return value
	}
	if persisted && len([]rune(value)) > toolResultReferenceThresholdChars {
		return buildPersistedToolOutputForModel(row, toolResultReferencePreviewChars)
	}
	modelText, metadata := budgetToolOutputText(value, maxChars, persisted)
	if truncated, _ := metadata["truncated_for_model"].(bool); !truncated {
		return modelText
	}
	note := "\n\n[Tool result truncated for model context.]"
	if persisted {
		note = "\n\n[Tool result truncated for model context. The full result is retained in the " + toolResultBudgetRetainedIn + ".]"
	}
	payload := map[string]interface{}{
		"content": []map[string]string{{
			"type": "text",
			"text": modelText + note,
		}},
		"structuredContent": metadata,
	}
	if encoded, err := json.Marshal(payload); err == nil {
		return string(encoded)
	}
	return modelText
}

func buildPersistedToolOutputForModel(row model.ToolCall, previewChars int) string {
	output := strings.TrimSpace(row.OutputJSON)
	preview := firstCharsAtLineBoundary(output, previewChars)
	size := len([]rune(output))
	toolCallID := strings.TrimSpace(row.ToolCallID)
	runID := strings.TrimSpace(row.RunID)
	toolName := strings.TrimSpace(row.ToolName)
	var builder strings.Builder
	builder.WriteString(`<persisted-tool-output`)
	if toolCallID != "" {
		builder.WriteString(` id="`)
		builder.WriteString(xmlEscapeAttr(toolCallID))
		builder.WriteString(`"`)
	}
	if runID != "" {
		builder.WriteString(` run_id="`)
		builder.WriteString(xmlEscapeAttr(runID))
		builder.WriteString(`"`)
	}
	if toolName != "" {
		builder.WriteString(` tool="`)
		builder.WriteString(xmlEscapeAttr(toolName))
		builder.WriteString(`"`)
	}
	builder.WriteString(">\n")
	builder.WriteString(fmt.Sprintf("Output too large (%d characters). Full output is stored outside the model context in the conversation tool result store.\n\n", size))
	builder.WriteString(fmt.Sprintf("Preview (first %d characters):\n", previewChars))
	builder.WriteString(xmlEscapeText(preview))
	if len([]rune(output)) > len([]rune(preview)) {
		builder.WriteString("\n...")
	}
	builder.WriteString("\n</persisted-tool-output>")
	return builder.String()
}

func firstCharsAtLineBoundary(value string, maxChars int) string {
	text := strings.TrimSpace(value)
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	truncated := string(runes[:maxChars])
	if lastNewline := strings.LastIndex(truncated, "\n"); lastNewline > maxChars/2 {
		truncated = truncated[:lastNewline]
	}
	return strings.TrimSpace(truncated)
}

func budgetToolOutputText(value string, maxChars int, persisted bool) (string, map[string]interface{}) {
	normalized := strings.TrimSpace(value)
	contentType := "text"
	if jsonText, ok := normalizedToolOutputJSON(normalized); ok {
		normalized = jsonText
		contentType = "json"
	}
	originalChars := len([]rune(value))
	normalizedChars := len([]rune(normalized))
	metadata := map[string]interface{}{
		"truncated_for_model": true,
		"original_chars":      originalChars,
		"model_chars":         maxChars,
		"content_type":        contentType,
		"selection":           "head_tail",
	}
	if persisted {
		metadata["retained_in"] = toolResultBudgetRetainedIn
	}
	if contentType != "json" && looksLikeOpaqueToolOutput(normalized) {
		contentType = "opaque"
		metadata["content_type"] = contentType
		metadata["selection"] = "metadata_preview"
		return opaqueToolOutputPreview(normalized, originalChars), metadata
	}
	if normalizedChars <= maxChars {
		metadata["model_chars"] = normalizedChars
		metadata["selection"] = "normalized"
		metadata["truncated_for_model"] = false
		return normalized, metadata
	}
	return headTailToolOutput(normalized, maxChars), metadata
}

func normalizedToolOutputJSON(value string) (string, bool) {
	var payload interface{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return "", false
	}
	formatted, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(formatted))
	return text, text != ""
}

func looksLikeOpaqueToolOutput(value string) bool {
	text := strings.TrimSpace(value)
	runes := []rune(text)
	if len(runes) < 1024 || strings.ContainsAny(text, " \n\t{}[],:") {
		return false
	}
	base64ish := 0
	for _, r := range runes {
		if (r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '+' || r == '/' || r == '=' || r == '-' || r == '_' {
			base64ish++
		}
	}
	return float64(base64ish)/float64(len(runes)) > 0.95
}

func opaqueToolOutputPreview(value string, originalChars int) string {
	preview := headTailToolOutput(value, toolResultOpaquePreviewMax)
	return fmt.Sprintf("Large opaque tool result omitted from model context.\nOriginal characters: %d\nPreview:\n%s", originalChars, preview)
}

func headTailToolOutput(value string, maxChars int) string {
	runes := []rune(strings.TrimSpace(value))
	if maxChars <= 0 || len(runes) <= maxChars {
		return string(runes)
	}
	const separatorTemplate = "\n\n[... %d chars omitted ...]\n\n"
	separator := fmt.Sprintf(separatorTemplate, len(runes)-maxChars)
	separatorChars := len([]rune(separator))
	if separatorChars >= maxChars {
		return strings.TrimSpace(string(runes[:maxChars]))
	}
	available := maxChars - separatorChars
	headChars := available / 2
	tailChars := available - headChars
	head := strings.TrimSpace(string(runes[:headChars]))
	tail := strings.TrimSpace(string(runes[len(runes)-tailChars:]))
	return head + separator + tail
}

func (l *toolExecutionLedger) lookup(toolName string, argumentsJSON string) (toolExecutionRecord, bool) {
	if l == nil {
		return toolExecutionRecord{}, false
	}
	record, ok := l.records[toolExecutionKey(toolName, argumentsJSON)]
	return record, ok
}

func (l *toolExecutionLedger) store(toolName string, argumentsJSON string, record toolExecutionRecord) {
	if l == nil {
		return
	}
	l.records[toolExecutionKey(toolName, argumentsJSON)] = record
}

func toolExecutionKey(toolName string, argumentsJSON string) string {
	return strings.ToLower(strings.TrimSpace(toolName)) + "\x00" + canonicalToolArguments(argumentsJSON)
}

func toolCallPersistenceKey(row model.ToolCall) string {
	if value := strings.TrimSpace(row.ToolCallID); value != "" {
		return "id:" + value
	}
	return "tool:" + strings.ToLower(strings.TrimSpace(row.ToolName)) + "\x00" + canonicalToolArguments(row.InputJSON)
}

func mergeToolCallPersistenceKeys(target *map[string]struct{}, source map[string]struct{}) {
	if target == nil || len(source) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]struct{}, len(source))
	}
	for key := range source {
		if strings.TrimSpace(key) != "" {
			(*target)[key] = struct{}{}
		}
	}
}

func canonicalToolArguments(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "{}"
	}
	var payload interface{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return value
	}
	normalized, err := json.Marshal(payload)
	if err != nil {
		return value
	}
	return string(normalized)
}

func resolveExecutionToolName(toolName string, toolNameMap map[string]string) string {
	value := strings.TrimSpace(toolName)
	if value == "" {
		return ""
	}
	if mapped := strings.TrimSpace(toolNameMap[value]); mapped != "" {
		return mapped
	}
	return value
}

func resolveMCPConfig(toolName string, configs map[string]mcp.CallConfig) *mcp.CallConfig {
	value := strings.TrimSpace(toolName)
	if value == "" || len(configs) == 0 {
		return nil
	}
	cfg, ok := configs[value]
	if !ok {
		return nil
	}
	return &cfg
}

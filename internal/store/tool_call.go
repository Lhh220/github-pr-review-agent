package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ToolCallLog struct {
	ID         uint64         `json:"id"`
	TaskID     uint64         `json:"task_id"`
	ToolName   string         `json:"tool_name"`
	Input      map[string]any `json:"input"`
	Output     string         `json:"output"`
	Status     string         `json:"status"`
	Error      string         `json:"error,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	CreatedAt  time.Time      `json:"created_at"`
}

type NewToolCallLog struct {
	TaskID     uint64
	ToolName   string
	Input      string
	Output     string
	Error      string
	DurationMS int64
}

func (s *Store) CreateToolCallLog(ctx context.Context, input NewToolCallLog) (*ToolCallLog, error) {
	input.ToolName = strings.TrimSpace(input.ToolName)
	if input.TaskID == 0 {
		return nil, fmt.Errorf("tool call task id is required")
	}
	if input.ToolName == "" {
		return nil, fmt.Errorf("tool call name is required")
	}
	if input.DurationMS < 0 {
		return nil, fmt.Errorf("tool call duration cannot be negative")
	}
	status := "success"
	if strings.TrimSpace(input.Error) != "" {
		status = "error"
	}

	inputJSON, err := toolCallInputJSON(input.Input)
	if err != nil {
		return nil, fmt.Errorf("encode tool call input: %w", err)
	}
	outputJSON, err := json.Marshal(input.Output)
	if err != nil {
		return nil, fmt.Errorf("encode tool call output: %w", err)
	}

	result, err := s.db.ExecContext(ctx, `
INSERT INTO tool_call_log
    (task_id, tool_name, input_json, output_json, status, error, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.TaskID,
		input.ToolName,
		inputJSON,
		outputJSON,
		status,
		nullableString(input.Error),
		input.DurationMS,
	)
	if err != nil {
		return nil, fmt.Errorf("insert tool call log: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get tool call log id: %w", err)
	}
	var loggedInput map[string]any
	if err := json.Unmarshal(inputJSON, &loggedInput); err != nil {
		return nil, fmt.Errorf("decode inserted tool call input: %w", err)
	}
	return &ToolCallLog{
		ID:         uint64(id),
		TaskID:     input.TaskID,
		ToolName:   input.ToolName,
		Input:      loggedInput,
		Output:     input.Output,
		Status:     status,
		Error:      input.Error,
		DurationMS: input.DurationMS,
		CreatedAt:  time.Now(),
	}, nil
}

func (s *Store) ListToolCallLogs(ctx context.Context, taskID uint64) ([]ToolCallLog, error) {
	if taskID == 0 {
		return nil, fmt.Errorf("tool call task id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, tool_name, input_json, output_json, status,
       COALESCE(error, ''), duration_ms, created_at
FROM tool_call_log
WHERE task_id = ?
ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list tool call logs: %w", err)
	}
	defer rows.Close()

	logs := make([]ToolCallLog, 0)
	for rows.Next() {
		var log ToolCallLog
		var inputJSON []byte
		var outputJSON []byte
		if err := rows.Scan(
			&log.ID,
			&log.TaskID,
			&log.ToolName,
			&inputJSON,
			&outputJSON,
			&log.Status,
			&log.Error,
			&log.DurationMS,
			&log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan tool call log: %w", err)
		}
		if err := json.Unmarshal(inputJSON, &log.Input); err != nil {
			return nil, fmt.Errorf("decode tool call input: %w", err)
		}
		if err := json.Unmarshal(outputJSON, &log.Output); err != nil {
			return nil, fmt.Errorf("decode tool call output: %w", err)
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool call logs: %w", err)
	}
	return logs, nil
}

func toolCallInputJSON(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []byte(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err == nil {
		return []byte(trimmed), nil
	}
	return json.Marshal(map[string]string{"raw": value})
}

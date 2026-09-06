package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
)

// Tool is implemented by capabilities that an agent can invoke through the LLM.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Execute(ctx context.Context, input map[string]any) (string, error)
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		if tool == nil {
			return nil, fmt.Errorf("register agent tool: tool is nil")
		}
		name := strings.TrimSpace(tool.Name())
		if name == "" {
			return nil, fmt.Errorf("register agent tool: name is required")
		}
		if tool.Description() == "" {
			return nil, fmt.Errorf("register agent tool %q: description is required", name)
		}
		if len(tool.Schema()) == 0 {
			return nil, fmt.Errorf("register agent tool %q: schema is required", name)
		}
		if _, exists := registry.tools[name]; exists {
			return nil, fmt.Errorf("register agent tool %q: tool already registered", name)
		}
		registry.tools[name] = tool
	}
	return registry, nil
}

func (r *Registry) Definitions() []llm.ToolDefinition {
	if r == nil || len(r.tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	definitions := make([]llm.ToolDefinition, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		definitions = append(definitions, llm.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
	}
	return definitions
}

func (r *Registry) Execute(ctx context.Context, name, arguments string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("execute agent tool: registry is empty")
	}
	tool, exists := r.tools[strings.TrimSpace(name)]
	if !exists {
		return "", fmt.Errorf("agent tool %q is not registered", name)
	}

	input := map[string]any{}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return "", fmt.Errorf("decode agent tool %q arguments: %w", name, err)
		}
	}
	return tool.Execute(ctx, input)
}

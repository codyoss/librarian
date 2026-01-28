// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcloudcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Wrapper for the client.Models service
type ClientWrapper struct {
	Models *genai.Models
}

func (w *ClientWrapper) GenerateContent(ctx context.Context, model string, parts []*genai.Part, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	// The SDK expects contents as []*genai.Content
	contents := []*genai.Content{{
		Role:  "user",
		Parts: parts,
	}}
	return w.Models.GenerateContent(ctx, model, contents, config)
}

// Suggestion represents the AI's decision
type Suggestion struct {
	Decision       string `json:"decision"` // NEXT or DONE
	NextSubcommand string `json:"next_subcommand,omitempty"`
	FinalCommand   string `json:"final_command,omitempty"`
}

// GenAIClient interface for mocking
type GenAIClient interface {
	GenerateContent(ctx context.Context, model string, parts []*genai.Part, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// Advisor uses GenAI to navigate gcloud help
type Advisor struct {
	client GenAIClient
	model  string
}

// New creates a new Advisor
func NewAdvisor(client GenAIClient, modelName string) *Advisor {
	return &Advisor{
		client: client,
		model:  modelName,
	}
}

// SuggestNextStep asks the AI what to do next
func (a *Advisor) SuggestNextStep(ctx context.Context, currentPath []string, helpOutput string, meta *serviceMetadata) (*Suggestion, error) {
	prompt := fmt.Sprintf(`You are an expert at mapping Google Cloud API services to gcloud CLI commands.

GOAL: Identify the gcloud command that corresponds to the following API Service Method.

API SERVICE:
Name: %s
Description: %s

API METHOD:
Name: %s
Description: %s

CURRENT GCLOUD CONTEXT:
Command Path: gcloud %s
Help Output (truncated):
%s

INSTRUCTIONS:
1. Read the Help Output to identify available subcommands or command groups.
2. Determine if one of the subcommands is the correct path to the requested API method.
3. If the current command path plus a subcommand *is* the final operation (e.g. 'secrets create'), choose DONE.
4. If we need to go deeper (e.g. 'secrets' -> 'versions'), choose NEXT and valid subcommand.
5. If the current output shows the command itself is the target, choose DONE.

Respond with valid JSON matching this schema:
{
  "decision": "NEXT" or "DONE",
  "next_subcommand": "string (the exact subcommand to append)",
  "final_command": "string (the full constructed command, e.g. 'gcloud secrets create')"
}
`,
		meta.Name, meta.Description,
		meta.MethodName, meta.MethodDescription,
		strings.Join(currentPath, " "),
		truncateHelp(helpOutput),
	)

	resp, err := a.client.GenerateContent(ctx, a.model, []*genai.Part{
		{Text: prompt},
	}, &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("genai error: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content returned from AI")
	}

	part := resp.Candidates[0].Content.Parts[0]
	text := part.Text
	if text == "" {
		// Handle case where it might be byte data or something else if types differ, but usually it's Text.
		return nil, fmt.Errorf("empty text response")
	}

	// Sanitize markdown code blocks if present
	text = cleanJSON(text)

	var s Suggestion
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w (text: %s)", err, text)
	}

	return &s, nil
}

func truncateHelp(h string) string {
	// Truncate if too long to save context window, though 2.5 pro has huge context.
	// But let's keep it reasonable around 20k chars maybe?
	if len(h) > 50000 {
		return h[:50000] + "\n... (truncated)"
	}
	return h
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

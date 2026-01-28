// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
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

	"google.golang.org/genai"
)

// FlagMapping represents the mapping between a gcloud flag and a JSON field path
type FlagMapping struct {
	// Flag is the gcloud flag name (e.g. "--file-content")
	Flag string `json:"flag,omitempty"`
	// Pos is the 0-indexed position of the argument if it is a positional argument
	Pos *int `json:"pos,omitempty"`
	// FieldPath is the dot-separated path in the JSON schema (e.g. "payload.data")
	FieldPath string `json:"field_path"`
	// Choices is a list of allowed values for the flag (e.g. ["automatic", "user-managed"])
	// This is used for enum-like flags where the input JSON field might be an object key or matching string.
	Choices []string `json:"choices,omitempty"`
}

// Mapper uses GenAI to map gcloud flags to JSON schema fields
type Mapper struct {
	client GenAIClient
	model  string
}

// NewMapper creates a new Mapper
func NewMapper(client GenAIClient, modelName string) *Mapper {
	return &Mapper{
		client: client,
		model:  modelName,
	}
}

// MapFlags maps gcloud flags to JSON schema fields
func (m *Mapper) MapFlags(ctx context.Context, schema string, helpOutput string) ([]FlagMapping, error) {
	prompt := fmt.Sprintf(`You are an expert at mapping Google Cloud CLI (gcloud) flags to API Request JSON schemas.

GOAL: Map the available gcloud flags to their corresponding fields in the API Request JSON Schema.

INPUTS:

1. JSON SCHEMA (Target API Request):
%s

2. GCLOUD COMMAND HELP (Source of Flags):
%s

INSTRUCTIONS:
1. Analyze the JSON Schema to understand the structure of the API request.
2. Analyze the gcloud command help to identify available flags and their descriptions.
3. For each relevant flag in the gcloud command, find the corresponding field in the JSON Schema.
   - Ignore global gcloud flags (e.g., --project, --format, --help, --verbosity).
   - specific flags that set top-level resources (like parent resources) are important.
   - Positional arguments might also map to fields (e.g. the first argument might be 'parent' or 'name'). If there are positional arguments specified in the help, map them using the "pos" field (0-indexed).
   - If a flag has a limited set of allowed values (enums), list them in the "choices" field.
     For example, if --replication-policy can be "automatic" or "user-managed", list those.
     This is crucial for flags that map to "oneOf" fields or enum fields in the request.
4. Omit file-based flags (e.g. those ending in '-file' like --replication-policy-file) unless they correspond to a unique field in the schema designated for file input. If a file-based flag is merely an alternative way to provide input for a field that has a primary non-file flag, exclude it.

5. Output a JSON array of mappings.

RESPONSE FORMAT:
Strictly a JSON array of objects, with no markdown formatting.
[
  {
    "flag": "--flag-name",
    "field_path": "path.to.field",
    "choices": ["val1", "val2"]
  },
  {
    "pos": 0,
    "field_path": "name"
  }
]
`,
		schema,
		truncateHelp(helpOutput),
	)

	resp, err := m.client.GenerateContent(ctx, m.model, []*genai.Part{
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
		return nil, fmt.Errorf("empty text response")
	}

	text = cleanJSON(text)

	var mappings []FlagMapping
	if err := json.Unmarshal([]byte(text), &mappings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return mappings, nil
}

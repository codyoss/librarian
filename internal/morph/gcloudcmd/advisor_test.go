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
	"testing"

	"google.golang.org/genai"
)

type MockGenAI struct {
	Response *genai.GenerateContentResponse
	Err      error
}

func (m *MockGenAI) GenerateContent(ctx context.Context, model string, parts []*genai.Part, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return m.Response, m.Err
}

func TestSuggestNextStep(t *testing.T) {
	mockResp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: `{"decision": "NEXT", "next_subcommand": "secrets"}`},
					},
				},
			},
		},
	}

	client := &MockGenAI{Response: mockResp}
	a := NewAdvisor(client, "dummy-model")

	meta := &serviceMetadata{
		Name: "Secret Manager",
	}

	suggestion, err := a.SuggestNextStep(context.Background(), []string{}, "usage: gcloud ...", meta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if suggestion.Decision != "NEXT" {
		t.Errorf("got decision %s, want NEXT", suggestion.Decision)
	}
	if suggestion.NextSubcommand != "secrets" {
		t.Errorf("got subcommand %s, want secrets", suggestion.NextSubcommand)
	}
}

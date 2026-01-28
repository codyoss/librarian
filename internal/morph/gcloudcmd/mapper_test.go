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

func TestMapFlags(t *testing.T) {
	mockResp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: `[{"flag": "--name", "field_path": "name"}, {"pos": 0, "field_path": "parent"}]`},
					},
				},
			},
		},
	}

	client := &MockGenAI{Response: mockResp}
	m := NewMapper(client, "dummy-model")

	mappings, err := m.MapFlags(context.Background(), "{}", "usage: gcloud ...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mappings) != 2 {
		t.Fatalf("got %d mappings, want 2", len(mappings))
	}

	if mappings[0].Flag != "--name" {
		t.Errorf("got flag %s, want --name", mappings[0].Flag)
	}
	if mappings[0].FieldPath != "name" {
		t.Errorf("got field_path %s, want name", mappings[0].FieldPath)
	}

	if mappings[1].Pos == nil || *mappings[1].Pos != 0 {
		t.Errorf("got pos %v, want 0", mappings[1].Pos)
	}
	if mappings[1].FieldPath != "parent" {
		t.Errorf("got field_path %s, want parent", mappings[1].FieldPath)
	}
}

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

package morph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/config"
)

func TestGenerateCurl(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		wantBody bool
	}{
		{
			name: "NoBody",
			data: map[string]any{
				"foo": "bar", // URL param
			},
			wantBody: false,
		},
		{
			name: "WithBody",
			data: map[string]any{
				"foo": "bar", // URL param
				"baz": "qux", // Body param
			},
			wantBody: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outDir := t.TempDir()
			inputMsg := &api.Message{
				ID: "TestMsg",
				Fields: []*api.Field{
					{Name: "foo", JSONName: "foo", Typez: api.STRING_TYPE},
					{Name: "baz", JSONName: "baz", Typez: api.STRING_TYPE},
				},
			}

			method := &api.Method{
				Name: "TestMethod",
				Service: &api.Service{
					DefaultHost: "example.com",
				},
				InputTypeID: "TestMsg",
				InputType:   inputMsg,
				PathInfo: &api.PathInfo{
					Bindings: []*api.PathBinding{
						{
							Verb: "GET",
							PathTemplate: &api.PathTemplate{
								Segments: []api.PathSegment{
									{Literal: strPtr("v1")},
									{Variable: &api.PathVariable{FieldPath: []string{"foo"}}},
								},
							},
						},
					},
				},
			}

			apiState := &api.API{
				State: &api.APIState{
					MessageByID: map[string]*api.Message{
						"TestMsg": inputMsg,
					},
				},
			}

			rawData, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatalf("Marshal data: %v", err)
			}

			err = GenerateCurl(context.Background(), &CurlInput{
				ReqData: rawData,
				API:     apiState,
				Method:  method,
				OutDir:  outDir,
				Config:  &config.Config{},
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			content, err := os.ReadFile(filepath.Join(outDir, "curl.sh"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			got := string(content)

			if tc.wantBody {
				if !strings.Contains(got, "-d '") {
					t.Errorf("Expected body flag -d, got:\n%s", got)
				}
				if !strings.Contains(got, `{"baz":"qux"}`) {
					t.Errorf("Expected unescaped JSON body {\"baz\":\"qux\"}, got:\n%s", got)
				}
			} else {
				if strings.Contains(got, "-d '") {
					t.Errorf("Unexpected body flag -d, got:\n%s", got)
				}
			}

			if !strings.Contains(got, "https://example.com/v1/bar") {
				t.Errorf("Expected URL with path params substituted, got:\n%s", got)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}

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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/googleapis/librarian/internal/sidekick/api"
)

func TestGenerateGo(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		wantInit string
	}{
		{
			name: "Basic",
			data: map[string]any{
				"foo": "bar",
			},
			wantInit: `Foo: "bar",`,
		},
		{
			name: "WithJSONName",
			data: map[string]any{
				"baz": "qux",
			},
			wantInit: `Baz: "qux",`,
		},
		{
			name: "Integers",
			data: map[string]any{
				"id": 123,
			},
			wantInit: `Id: 123,`,
		},
		{
			name: "Nested",
			data: map[string]any{
				"child": map[string]any{
					"foo": "childBar",
				},
			},
			wantInit: `Child: &librarypb.ChildMsg{Foo: "childBar",},`,
		},
		{
			name: "Repeated",
			data: map[string]any{
				"items": []any{"a", "b"},
			},
			wantInit: `Items: []string{"a", "b",},`,
		},
		{
			name: "Map",
			data: map[string]any{
				"labels": map[string]any{
					"k1": "v1",
				},
			},
			wantInit: `Labels: map[string]string{"k1": "v1",},`,
		},
		{
			name: "OneOf",
			data: map[string]any{
				"str_val": "choice1",
			},
			wantInit: `Choice: &librarypb.MsgWithOneOf_StrVal{StrVal: "choice1",},`,
		},
		{
			name: "OneOfEmpty",
			data: map[string]any{
				"empty_msg": map[string]any{},
			},
			wantInit: `Choice: &librarypb.MsgWithOneOf_EmptyMsg{EmptyMsg: &librarypb.EmptyMsg{},},`,
		},
		{
			name: "NestedMessage",
			data: map[string]any{
				"nested": map[string]any{
					"foo": "bar",
				},
			},
			wantInit: `Nested: &librarypb.Parent_Child{Foo: "bar",},`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outDir := t.TempDir()
			serviceDir := t.TempDir()

			// Create dummy BUILD.bazel
			buildContent := `
go_gapic_library(
    name = "library_go_gapic",
    importpath = "cloud.google.com/go/library/apiv1;library",
    service_yaml = "library_v1.yaml",
    transport = "grpc+rest",
)

go_grpc_library(
    name = "library_go_grpc",
    importpath = "cloud.google.com/go/library/apiv1/librarypb",
)
`
			if err := os.WriteFile(filepath.Join(serviceDir, "BUILD.bazel"), []byte(buildContent), 0644); err != nil {
				t.Fatalf("WriteFile BUILD.bazel: %v", err)
			}

			childMsg := &api.Message{
				ID:   "ChildMsg",
				Name: "ChildMsg",
				Fields: []*api.Field{
					{Name: "foo", Typez: api.STRING_TYPE},
				},
			}

			inputMsg := &api.Message{
				ID:   "TestMsg",
				Name: "TestMsg",
				Fields: []*api.Field{
					{Name: "foo", Typez: api.STRING_TYPE},
					{Name: "baz", JSONName: "baz", Typez: api.STRING_TYPE},
					{Name: "id", Typez: api.INT32_TYPE},
					{Name: "child", Typez: api.MESSAGE_TYPE, MessageType: childMsg},
					{Name: "items", Typez: api.STRING_TYPE, Repeated: true},
					{
						Name:  "labels",
						Typez: api.MESSAGE_TYPE, // Map entries are messages
						Map:   true,
						MessageType: &api.Message{
							Fields: []*api.Field{
								{Name: "key", Typez: api.STRING_TYPE},
								{Name: "value", Typez: api.STRING_TYPE},
							},
						},
					},
				},
			}

			oneOf := &api.OneOf{
				Name: "choice",
			}
			msgWithOneOf := &api.Message{
				ID:   "MsgWithOneOf",
				Name: "MsgWithOneOf",
				Fields: []*api.Field{
					{
						Name:    "str_val",
						Typez:   api.STRING_TYPE,
						IsOneOf: true,
						Group:   oneOf,
					},
					{
						Name:    "int_val",
						Typez:   api.INT32_TYPE,
						IsOneOf: true,
						Group:   oneOf,
					},
					{
						Name:        "empty_msg",
						Typez:       api.MESSAGE_TYPE,
						IsOneOf:     true,
						Group:       oneOf,
						MessageType: &api.Message{Name: "EmptyMsg"},
					},
				},
				OneOfs: []*api.OneOf{oneOf},
			}
			// Link parent
			for _, f := range msgWithOneOf.Fields {
				f.Parent = msgWithOneOf
			}

			// MsgWithCollision
			oneOfCol := &api.OneOf{Name: "choice"}
			msgWithCollision := &api.Message{
				ID:   "MsgWithCollision",
				Name: "MsgWithCollision",
				Fields: []*api.Field{
					{
						Name:    "str_val",
						Typez:   api.STRING_TYPE,
						IsOneOf: true,
						Group:   oneOfCol,
					},
				},
				OneOfs: []*api.OneOf{oneOfCol},
				Messages: []*api.Message{
					{Name: "StrVal"}, // Collision
				},
			}
			for _, f := range msgWithCollision.Fields {
				f.Parent = msgWithCollision
			}

			// Parent/Child
			child := &api.Message{
				Name: "Child",
				Fields: []*api.Field{
					{Name: "foo", Typez: api.STRING_TYPE},
				},
			}
			parent := &api.Message{
				ID:   "Parent",
				Name: "Parent",
				Fields: []*api.Field{
					{
						Name:        "nested",
						Typez:       api.MESSAGE_TYPE,
						MessageType: child,
					},
				},
			}
			child.Parent = parent // Link parent

			method := &api.Method{
				Name:        "TestMethod",
				InputTypeID: "TestMsg",
				InputType:   inputMsg,
				Service: &api.Service{
					Name: "LibraryClient",
				},
			}

			if tc.name == "OneOf" || tc.name == "OneOfEmpty" {
				method.InputType = msgWithOneOf
				method.InputTypeID = "MsgWithOneOf"
			} else if tc.name == "OneOfCollision" {
				method.InputType = msgWithCollision
				method.InputTypeID = "MsgWithCollision"
			} else if tc.name == "NestedMessage" {
				method.InputType = parent
				method.InputTypeID = "Parent"
			}
			rawData, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatalf("Marshal data: %v", err)
			}

			err = GenerateGo(&generateGoInput{
				ReqData:    rawData,
				API:        &api.API{},
				Method:     method,
				OutDir:     outDir,
				ServiceDir: serviceDir,
			})
			if err != nil {
				t.Fatalf("GenerateGo: %v", err)
			}

			content, err := os.ReadFile(filepath.Join(outDir, "main.go"))
			if err != nil {
				t.Fatalf("ReadFile main.go: %v", err)
			}
			got := normalize(string(content))
			want := normalize(tc.wantInit)

			if !strings.Contains(got, want) {
				t.Errorf("Expected initialization content:\n%s\nGot:\n%s", want, got)
			}

			// Check imports
			if !strings.Contains(got, `"cloud.google.com/go/library/apiv1"`) {
				t.Errorf("Expected gapic import")
			}
			if !strings.Contains(got, `"cloud.google.com/go/library/apiv1/librarypb"`) {
				t.Errorf("Expected proto import")
			}
		})
	}
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), "")
}

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
package convert

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/googleapis/librarian/internal/sidekick/api"
)

func TestToJSONSchema(t *testing.T) {
	// Common schemas for reuse
	stringSchema := &jsonschema.Schema{Type: "string"}
	intSchema := &jsonschema.Schema{Type: "integer"}
	boolSchema := &jsonschema.Schema{Type: "boolean"}

	tests := []struct {
		name string
		msg  *api.Message
		want *jsonschema.Schema
	}{
		{
			name: "NilMessage",
			msg:  nil,
			want: &jsonschema.Schema{},
		},
		{
			name: "BasicTypes",
			msg: &api.Message{
				ID: "BasicMsg",
				Fields: []*api.Field{
					{Name: "field_str", JSONName: "fieldStr", Typez: api.STRING_TYPE},
					{Name: "field_int", JSONName: "fieldInt", Typez: api.INT32_TYPE},
					{Name: "field_bool", JSONName: "fieldBool", Typez: api.BOOL_TYPE},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"fieldStr":  stringSchema,
					"fieldInt":  intSchema,
					"fieldBool": boolSchema,
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "RequiredField",
			msg: &api.Message{
				ID: "RequiredMsg",
				Fields: []*api.Field{
					{
						Name:     "req_field",
						JSONName: "reqField",
						Typez:    api.STRING_TYPE,
						Behavior: []api.FieldBehavior{api.FIELD_BEHAVIOR_REQUIRED},
					},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"reqField": stringSchema,
				},
				Required:    []string{"reqField"},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "OutputOnlyField",
			msg: &api.Message{
				ID: "OutputOnlyMsg",
				Fields: []*api.Field{
					{
						Name:     "out_field",
						JSONName: "outField",
						Typez:    api.STRING_TYPE,
						Behavior: []api.FieldBehavior{api.FIELD_BEHAVIOR_OUTPUT_ONLY},
					},
					{
						Name:     "reg_field",
						JSONName: "regField",
						Typez:    api.STRING_TYPE,
					},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"regField": stringSchema,
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "RepeatedField",
			msg: &api.Message{
				ID: "RepeatedMsg",
				Fields: []*api.Field{
					{
						Name:     "items",
						JSONName: "items",
						Typez:    api.STRING_TYPE,
						Repeated: true,
					},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"items": {
						Type:  "array",
						Items: stringSchema,
					},
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "NestedMessage",
			msg: func() *api.Message {
				child := &api.Message{
					ID: "ChildMsg",
					Fields: []*api.Field{
						{Name: "child_val", JSONName: "childVal", Typez: api.STRING_TYPE},
					},
				}
				parent := &api.Message{
					ID: "ParentMsg",
					Fields: []*api.Field{
						{
							Name:        "child",
							JSONName:    "child",
							Typez:       api.MESSAGE_TYPE,
							MessageType: child,
						},
					},
				}
				return parent
			}(),
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"child": {Ref: "#/definitions/ChildMsg"},
				},
				Definitions: map[string]*jsonschema.Schema{
					"ChildMsg": {
						Type: "object",
						Properties: map[string]*jsonschema.Schema{
							"childVal": stringSchema,
						},
					},
				},
			},
		},
		{
			name: "RecursiveMessage",
			msg: func() *api.Message {
				msg := &api.Message{ID: "RecursiveMsg"}
				msg.Fields = []*api.Field{
					{
						Name:        "self",
						JSONName:    "self",
						Typez:       api.MESSAGE_TYPE,
						MessageType: msg,
					},
				}
				return msg
			}(),
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"self": {Ref: "#"},
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "MapField",
			msg: &api.Message{
				ID: "MapMsg",
				Fields: []*api.Field{
					{
						Name:          "labels",
						JSONName:      "labels",
						Documentation: "A map of labels",
						Map:           true,
						MessageType: &api.Message{
							Fields: []*api.Field{
								{Name: "key", Typez: api.STRING_TYPE},
								{
									Name:          "value",
									Typez:         api.STRING_TYPE,
									Documentation: "Value of the label",
								},
							},
						},
					},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"labels": {
						Type:        "object",
						Description: "A map of labels",
						AdditionalProperties: &jsonschema.Schema{
							Type:        "string",
							Description: "Value of the label",
						},
					},
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
		{
			name: "EnumField",
			msg: &api.Message{
				ID: "EnumMsg",
				Fields: []*api.Field{
					{
						Name:     "status",
						JSONName: "status",
						Typez:    api.ENUM_TYPE,
						EnumType: &api.Enum{
							Values: []*api.EnumValue{
								{Name: "UNKNOWN"},
								{Name: "ACTIVE"},
							},
						},
					},
				},
			},
			want: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"status": {
						Enum: []any{"UNKNOWN", "ACTIVE"},
					},
				},
				Definitions: map[string]*jsonschema.Schema{},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ToJSONSchema(tc.msg)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ToJSONSchema() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

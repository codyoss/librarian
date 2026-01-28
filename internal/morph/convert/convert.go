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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/config"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"gopkg.in/yaml.v3"
)

const (
	serviceConfigType  = "type"
	serviceConfigValue = "google.api.Service"
)

func ToSideKickAPI(googleapisRoot string, protobufRoot string, specSource string) (*api.API, error) {
	slog.Info("Converting to SideKick API", "googleapis-root", googleapisRoot, "protobuf-root", protobufRoot, "spec-source", specSource)
	serviceConfig, err := findServiceConfigIn(filepath.Join(googleapisRoot, specSource))
	if err != nil {
		return nil, err
	}
	slog.Info("Service config found", "service-config", serviceConfig)
	config := &config.Config{
		General: config.GeneralConfig{
			SpecificationFormat: "protobuf",
			ServiceConfig:       filepath.Join(googleapisRoot, specSource, serviceConfig),
			SpecificationSource: specSource,
		},
		Source: map[string]string{
			"googleapis-root": googleapisRoot,
			"protobuf-src":    protobufRoot,
		},
		Codec: map[string]string{},
	}
	api, err := parser.CreateModel(config)
	if err != nil {
		return nil, err
	}
	slog.Info("API Model created")
	return api, nil
}

func ToJSONSchema(msg *api.Message) *jsonschema.Schema {
	if msg == nil {
		return &jsonschema.Schema{}
	}
	w := &schemaWalker{
		defs:   make(map[string]*jsonschema.Schema),
		refMap: make(map[string]string),
	}

	// Register root
	w.refMap[msg.ID] = "#"

	// Build root directly
	root := w.buildObject(msg)
	root.Definitions = w.defs
	return root
}

type schemaWalker struct {
	defs   map[string]*jsonschema.Schema
	refMap map[string]string
}

func (w *schemaWalker) getRef(msg *api.Message) *jsonschema.Schema {
	if msg == nil {
		return &jsonschema.Schema{}
	}
	if ref, ok := w.refMap[msg.ID]; ok {
		return &jsonschema.Schema{Ref: ref}
	}

	ref := "#/definitions/" + msg.ID
	w.refMap[msg.ID] = ref

	// Build schema
	s := w.buildObject(msg)
	w.defs[msg.ID] = s

	return &jsonschema.Schema{Ref: ref}
}

func (w *schemaWalker) buildObject(msg *api.Message) *jsonschema.Schema {
	s := &jsonschema.Schema{
		Type:       "object",
		Properties: make(map[string]*jsonschema.Schema),
	}
	if msg.Documentation != "" {
		s.Description = msg.Documentation
	}

	for _, f := range msg.Fields {
		if slices.Contains(f.Behavior, api.FIELD_BEHAVIOR_OUTPUT_ONLY) {
			continue
		}
		schema := w.buildField(f)
		s.Properties[f.JSONName] = schema
		if f.DocumentAsRequired() {
			s.Required = append(s.Required, f.JSONName)
		}
	}
	return s
}

func (w *schemaWalker) buildField(f *api.Field) *jsonschema.Schema {
	if f.Map {
		valueSchema := &jsonschema.Schema{} // default to any
		if f.MessageType != nil {
			for _, subF := range f.MessageType.Fields {
				if subF.Name == "value" {
					valueSchema = w.buildFieldType(subF)
					if subF.Documentation != "" {
						valueSchema.Description = subF.Documentation
					}
					break
				}
			}
		}
		return &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: valueSchema,

			Description: f.Documentation,
		}
	}

	s := w.buildFieldType(f)
	if f.Documentation != "" {
		s.Description = f.Documentation
	}

	if f.Repeated {
		return &jsonschema.Schema{
			Type:        "array",
			Items:       s,
			Description: f.Documentation,
		}
	}

	return s
}

func (w *schemaWalker) buildFieldType(f *api.Field) *jsonschema.Schema {
	switch f.Typez {
	case api.DOUBLE_TYPE, api.FLOAT_TYPE:
		return &jsonschema.Schema{Type: "number"}
	case api.INT64_TYPE, api.UINT64_TYPE, api.INT32_TYPE, api.FIXED64_TYPE, api.FIXED32_TYPE,
		api.UINT32_TYPE, api.SFIXED32_TYPE, api.SFIXED64_TYPE, api.SINT32_TYPE, api.SINT64_TYPE:
		return &jsonschema.Schema{Type: "integer"}
	case api.BOOL_TYPE:
		return &jsonschema.Schema{Type: "boolean"}
	case api.STRING_TYPE:
		return &jsonschema.Schema{Type: "string"}
	case api.BYTES_TYPE:
		return &jsonschema.Schema{Type: "string", ContentEncoding: "base64"}
	case api.MESSAGE_TYPE, api.GROUP_TYPE:
		return w.getRef(f.MessageType)
	case api.ENUM_TYPE:
		if f.EnumType == nil {
			return &jsonschema.Schema{Type: "string"}
		}
		var vals []any
		for _, v := range f.EnumType.Values {
			vals = append(vals, v.Name)
		}
		return &jsonschema.Schema{Enum: vals}
	default:
		slog.Debug("Unknown type, defaulting to string", "type", f.Typez)
		return &jsonschema.Schema{Type: "string"} // Fallback
	}
}

// findServiceConfigIn detects the service config in a given path.
//
// Returns the file name (relative to the given path) if the following criteria
// are met:
//
// 1. the file ends with `.yaml` and it is a valid yaml file.
//
// 2. the file contains `type: google.api.Service`.
func findServiceConfigIn(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("failed to read dir %q: %w", path, err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		bytes, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return "", err
		}
		var configMap map[string]interface{}
		if err := yaml.Unmarshal(bytes, &configMap); err != nil {
			return "", err
		}
		if value, ok := configMap[serviceConfigType].(string); ok && value == serviceConfigValue {
			return entry.Name(), nil
		}
	}
	slog.Info("no service config found; assuming proto-only package", "path", path)
	return "", nil
}

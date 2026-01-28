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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/config"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"github.com/urfave/cli/v3"
	"google.golang.org/genai"
)

const systemPrompt = `Role: You are a deterministic JSON Generation Engine. Your sole purpose is to transform a provided JSON Schema into a valid JSON instance.

Rules of Engagement:

- Output Format: You must output ONLY the raw JSON object. Do not include markdown code blocks, introductory text, or concluding remarks.
- Field Requirements: You MUST populate every property listed in the required array of the schema.
- Minimization & The Empty Object Rule: Generally, do NOT include properties not listed in the required array. 
  -Edge Case: If a property is required and is of type object, but that object's definition contains no required fields of its own, you MUST select and populate at least one property from its properties list. Choose the property that is most essential to the resource (e.g., a name, a type, or a configuration).
- Strict Description Following: If a property's description defines a specific format, you must follow it exactly.	
- Wildcard Handling: If a format example in a description contains a *, you must replace that * with an appropriate, realistic value.

Constraint: If the schema is invalid or impossible to satisfy, return an empty JSON object {} and nothing else.
`

var generateRequestCommand = &cli.Command{
	Name:  "generate-request",
	Usage: "create a sample request for a method",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "method",
			Usage: "the method to morph the request into",
		},
		&cli.StringFlag{
			Name:  "context",
			Usage: "additional context to provide to the model",
		},
		&cli.StringFlag{
			Name:  "service-root",
			Usage: "the root of the service",
		},
		&cli.StringFlag{
			Name:  "googleapis-root",
			Usage: "the root of the googleapis repository",
		},
		&cli.StringFlag{
			Name:  "protobuf-root",
			Usage: "the root of the protobuf repository",
		},
		&cli.StringFlag{
			Name:  "spec-source",
			Usage: "the source of the spec",
		},
	},
	Action: runGenerateRequest,
}

type generateRequestInput struct {
	API               *api.API
	Config            *config.Config
	Method            *api.Method
	Request           *api.Message
	AdditionalContext string
}

func runGenerateRequest(ctx context.Context, cmd *cli.Command) error {
	methodName := cmd.String("method")
	googleapisRoot := cmd.String("googleapis-root")
	protobufRoot := cmd.String("protobuf-root")
	additionalContext := cmd.String("context")
	specSource := cmd.String("spec-source")
	slog.Info("Generating request", "method", methodName, "googleapis-root", googleapisRoot, "protobuf-root", protobufRoot, "spec-source", specSource, "additional-context", additionalContext)
	serviceConfig, err := findServiceConfigIn(filepath.Join(googleapisRoot, specSource))
	if err != nil {
		return err
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
		return err
	}
	slog.Info("API Model created")
	method, ok := api.State.MethodByID[methodName]
	if !ok {
		return fmt.Errorf("method %s not found", methodName)
	}
	slog.Info("Generate sample request", "method", method)
	request, ok := api.State.MessageByID[method.InputTypeID]
	if !ok {
		return fmt.Errorf("request %s not found", method.InputTypeID)
	}
	out, err := generateRequest(ctx, &generateRequestInput{
		API:               api,
		Config:            config,
		Method:            method,
		Request:           request,
		AdditionalContext: additionalContext,
	})
	if err != nil {
		return err
	}
	slog.Info("Write request to file", "file", "request.json")
	if err := os.WriteFile(filepath.Join("/usr/local/google/home/codyoss/oss/librarian/out", "request.json"), []byte(out), 0666); err != nil {
		return err
	}
	return err
}

// generateRequest takes the input and uses generative AI to create a JSON request message
// for the described method.
func generateRequest(ctx context.Context, in *generateRequestInput) (string, error) {
	slog.Info("Generating JSON schema", "id", in.Request.ID)
	schema := messageToJSONSchema(in.Request)
	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}

	slog.Info("Create geni Client")
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create genai client: %w", err)
	}

	systemInstruction := systemPrompt
	if in.AdditionalContext != "" {
		systemInstruction += "\n\nAdditional Context: " + in.AdditionalContext
	}

	slog.Info("Use AI to generate request")
	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-pro", genai.Text(string(b)), &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: systemInstruction},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates returned")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			fmt.Println(part.Text)
			return part.Text, nil
		}
	}
	return "", fmt.Errorf("no text response")
}

// messageToJSONSchema converts a message into a JSON schema recursively.
func messageToJSONSchema(msg *api.Message) *jsonschema.Schema {
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

func ptr[T any](v T) *T {
	return &v
}

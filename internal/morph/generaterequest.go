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

	"github.com/googleapis/librarian/internal/morph/convert"
	"github.com/googleapis/librarian/internal/sidekick/api"
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
	api, err := convert.ToSideKickAPI(googleapisRoot, protobufRoot, specSource)
	if err != nil {
		return err
	}
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
		Method:            method,
		Request:           request,
		AdditionalContext: additionalContext,
	})
	if err != nil {
		return err
	}
	slog.Info("Write request to file", "file", "request.json")
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "request.json"), []byte(out), 0666); err != nil {
		return err
	}
	return nil
}

// generateRequest takes the input and uses generative AI to create a JSON request message
// for the described method.
func generateRequest(ctx context.Context, in *generateRequestInput) (string, error) {
	slog.Info("Generating JSON schema", "id", in.Request.ID)
	schema := convert.ToJSONSchema(in.Request)
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

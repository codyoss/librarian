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
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/googleapis/librarian/internal/morph/convert"
	"github.com/urfave/cli/v3"
	"google.golang.org/genai"
)

var MapFlagsCommand = &cli.Command{
	Name:  "map-gcloud-flags",
	Usage: "Map gcloud flags to API request fields",
	Flags: []cli.Flag{
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
		&cli.StringFlag{
			Name:  "method",
			Usage: "the method to morph the request into",
		},
		&cli.StringFlag{
			Name:  "gcloud-command",
			Usage: "the target gcloud command (e.g. 'secrets create')",
		},
		&cli.StringFlag{
			Name:  "project",
			Usage: "GCP Project ID for GenAI (optional if using ADC/Environment)",
		},
		&cli.StringFlag{
			Name:  "model",
			Value: "gemini-2.5-pro",
			Usage: "GenAI Model Name",
		},
		&cli.BoolFlag{
			Name:  "verbose",
			Usage: "Enable verbose logging",
		},
	},
	Action: actionMapFlags,
}

func actionMapFlags(ctx context.Context, cmd *cli.Command) error {
	verbose := cmd.Bool("verbose")
	methodName := cmd.String("method")
	googleapisRoot := cmd.String("googleapis-root")
	protobufRoot := cmd.String("protobuf-root")
	specSource := cmd.String("spec-source")
	gcloudCmd := cmd.String("gcloud-command")
	projectID := cmd.String("project")
	modelName := cmd.String("model")
	logLevel := slog.Level(math.MaxInt)

	if verbose {
		logLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})
	slog.SetDefault(slog.New(handler))

	if gcloudCmd == "" {
		return fmt.Errorf("gcloud-command is required")
	}

	slog.Info("Mapping gcloud flags", "method", methodName, "command", gcloudCmd)
	api, err := convert.ToSideKickAPI(googleapisRoot, protobufRoot, specSource)
	if err != nil {
		return err
	}
	method, ok := api.State.MethodByID[methodName]
	if !ok {
		return fmt.Errorf("method %s not found", methodName)
	}

	request, ok := api.State.MessageByID[method.InputTypeID]
	if !ok {
		return fmt.Errorf("request %s not found", method.InputTypeID)
	}

	slog.Info("Generating JSON schema")
	schema := convert.ToJSONSchema(request)
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}

	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" {
		return fmt.Errorf("no project ID provided or detected with GOOGLE_CLOUD_PROJECT")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project: projectID,
	})
	if err != nil {
		return fmt.Errorf("failed to create GenAI client: %w", err)
	}

	mapper := NewMapper(&ClientWrapper{Models: client.Models}, modelName)
	exp := NewExplorer(&GcloudRunner{})

	cmdParts := strings.Fields(gcloudCmd)
	if len(cmdParts) > 0 && cmdParts[0] == "gcloud" {
		cmdParts = cmdParts[1:]
	}

	slog.Info("Getting help for command", "command", gcloudCmd)
	helpOut, err := exp.GetHelp(ctx, cmdParts)
	if err != nil {
		return fmt.Errorf("failed to get help for command: %w", err)
	}

	slog.Info("Mapping flags...")
	mappings, err := mapper.MapFlags(ctx, string(schemaBytes), helpOut)
	if err != nil {
		return fmt.Errorf("failed to map flags: %w", err)
	}

	slog.Info("Mapping complete")
	output := struct {
		Command    string        `json:"command"`
		MessageID  string        `json:"message_id"`
		Properties []FlagMapping `json:"properties"`
	}{
		Command:    gcloudCmd,
		MessageID:  request.ID,
		Properties: mappings,
	}

	out, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal output: %w", err)
	}

	// TODO: Make this configurable if needed, matching other commands for now.
	outDir := "out"
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	outPath := filepath.Join(outDir, "gcloud-map.json")
	slog.Info("Writing output to file", "path", outPath)
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	return nil
}

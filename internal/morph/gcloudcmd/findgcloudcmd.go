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
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"time"

	"github.com/googleapis/librarian/internal/morph/convert"
	"github.com/urfave/cli/v3"
	"google.golang.org/genai"
)

var FindCommand = &cli.Command{
	Name:  "find-gcloud-command",
	Usage: "Find the gcloud command for a given API service definition",
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
	Action: action,
}

// serviceMetadata represents the content of service.json
type serviceMetadata struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	MethodName        string `json:"method_name"`
	MethodDescription string `json:"method_description"`
}

func action(ctx context.Context, cmd *cli.Command) error {
	verbose := cmd.Bool("verbose")
	methodName := cmd.String("method")
	googleapisRoot := cmd.String("googleapis-root")
	protobufRoot := cmd.String("protobuf-root")
	projectID := cmd.String("project")
	modelName := cmd.String("model")
	specSource := cmd.String("spec-source")
	logLevel := slog.Level(math.MaxInt)

	if verbose {
		logLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})
	slog.SetDefault(slog.New(handler))

	slog.Info("Finding gcloud command", "method", methodName, "googleapis-root", googleapisRoot, "protobuf-root", protobufRoot, "spec-source", specSource)
	api, err := convert.ToSideKickAPI(googleapisRoot, protobufRoot, specSource)
	if err != nil {
		return err
	}
	method, ok := api.State.MethodByID[methodName]
	if !ok {
		return fmt.Errorf("method %s not found", methodName)
	}
	meta := &serviceMetadata{
		Name:              method.Service.Name,
		Description:       method.Service.Documentation,
		MethodName:        method.Name,
		MethodDescription: method.Documentation,
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

	adv := NewAdvisor(&ClientWrapper{Models: client.Models}, modelName)
	exp := NewExplorer(&GcloudRunner{})

	currentCmd := []string{}
	slog.Info("Starting exploration...")
	slog.Info("Target", "service", meta.Name, "method", meta.MethodName)

	// Have a max steps to avoid infinite loops
	maxSteps := 10
	for i := range maxSteps {
		slog.Info("Checking command", "step", i+1, "command", fmt.Sprintf("gcloud %s", fmtCmd(currentCmd)))

		helpOut, err := exp.GetHelp(ctx, currentCmd)
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to get help for 'gcloud %s'", fmtCmd(currentCmd)), "error", err)
			break
		}

		suggestion, err := adv.SuggestNextStep(ctx, currentCmd, helpOut, meta)
		if err != nil {
			return fmt.Errorf("advisor failed: %w", err)
		}

		slog.Info("Advisor says", "decision", suggestion.Decision)

		switch suggestion.Decision {
		case "DONE":
			final := suggestion.FinalCommand
			if final == "" && suggestion.NextSubcommand != "" {
				// Maybe they meant the next subcommand IS the final one?
				// But let's assume they provided FinalCommand as requested.
				// If not, construct it.
				final = fmt.Sprintf("gcloud %s %s", fmtCmd(currentCmd), suggestion.NextSubcommand)
			} else if final == "" {
				final = fmt.Sprintf("gcloud %s", fmtCmd(currentCmd))
			}
			fmt.Println(final)
			return nil
		case "NEXT":
			if suggestion.NextSubcommand == "" {
				return fmt.Errorf("advisor said NEXT but provided no subcommand")
			}
			currentCmd = append(currentCmd, suggestion.NextSubcommand)
		default:
			return fmt.Errorf("unknown decision: %s", suggestion.Decision)
		}

		// Sleep a bit to avoid rate limits if any, though standard quota is usually fine.
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("max steps reached without finding exact command")
}

func fmtCmd(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

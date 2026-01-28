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

// Package morph provides a tool to morph a request into a sample.
package morph

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/googleapis/librarian/internal/morph/convert"
	"github.com/googleapis/librarian/internal/morph/gcloudcmd"
	"github.com/urfave/cli/v3"
)

const (
	serviceConfigType  = "type"
	serviceConfigValue = "google.api.Service"
)

// Run runs the morph command.
func Run(ctx context.Context, args ...string) error {
	cmd := &cli.Command{
		Name:      "morph",
		Usage:     "morph a request into a sample",
		UsageText: "morph [command]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "request",
				Usage: "the file with the request to morph",
			},
			&cli.StringFlag{
				Name:  "output-type",
				Usage: "the type of output to generate",
				Value: "curl",
			},
			&cli.StringFlag{
				Name:  "method",
				Usage: "the method to morph the request into",
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
			&cli.StringFlag{
				Name:  "gcloud-mapping",
				Usage: "the mapping file for gcloud output",
			},
		},
		Commands: []*cli.Command{
			generateRequestCommand,
			gcloudcmd.FindCommand,
			gcloudcmd.MapFlagsCommand,
		},
		Action: run,
	}
	return cmd.Run(ctx, args)
}

func run(ctx context.Context, cmd *cli.Command) error {
	methodName := cmd.String("method")
	googleapisRoot := cmd.String("googleapis-root")
	protobufRoot := cmd.String("protobuf-root")
	specSource := cmd.String("spec-source")
	outputType := cmd.String("output-type")
	gcloudMappingsFile := cmd.String("gcloud-mapping")
	slog.Info("Creating API Model", "method", methodName, "googleapis-root", googleapisRoot, "protobuf-root", protobufRoot, "spec-source", specSource)
	api, err := convert.ToSideKickAPI(googleapisRoot, protobufRoot, specSource)
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
	slog.Info("Request", "request", request.Fields)
	for _, field := range request.Fields {
		slog.Info("Field", "field", field)
	}
	reqData, err := os.ReadFile(cmd.String("request"))
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	switch outputType {
	case "go":
		err = GenerateGo(&generateGoInput{
			API:        api,
			Method:     method,
			ReqData:    reqData,
			ServiceDir: filepath.Join(googleapisRoot, specSource),
			OutDir:     filepath.Join(dir, "out"),
		})
		if err != nil {
			return err
		}
	case "gcloud":
		err = GenerateGcloud(ctx, &GcloudInput{
			API:         api,
			Method:      method,
			ReqData:     reqData,
			OutDir:      filepath.Join(dir, "out"),
			MappingFile: gcloudMappingsFile,
		})
	default:
		err = GenerateCurl(ctx, &CurlInput{
			API:     api,
			Method:  method,
			OutDir:  filepath.Join(dir, "out"),
			ReqData: reqData,
		})
	}
	if err != nil {
		return err
	}
	return nil
}

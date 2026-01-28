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
	"strings"

	"github.com/googleapis/librarian/internal/sidekick/config"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
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
		Commands: []*cli.Command{
			generateRequestCommand,
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
	slog.Info("Creating API Model", "method", methodName, "googleapis-root", googleapisRoot, "protobuf-root", protobufRoot, "spec-source", specSource)
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
	slog.Info("Request", "request", request.Fields)
	for _, field := range request.Fields {
		slog.Info("Field", "field", field)
	}
	rawData, err := os.ReadFile(cmd.String("request"))
	if err != nil {
		return err
	}
	switch outputType {
	case "go":
		err = GenerateGo(&generateGoInput{
			API:        api,
			Method:     method,
			Config:     config,
			RawData:    rawData,
			ServiceDir: filepath.Join(googleapisRoot, specSource),
			OutDir:     "/usr/local/google/home/codyoss/oss/librarian/out",
		})
		if err != nil {
			return err
		}
	default:
		err = GenerateCurl(ctx, &CurlInput{
			API:     api,
			Method:  method,
			OutDir:  "/usr/local/google/home/codyoss/oss/librarian/out",
			Config:  config,
			RawData: rawData,
		})
	}
	if err != nil {
		return err
	}
	return nil
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

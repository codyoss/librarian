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

// Package golang generates Go GAPIC clients from the sidekick model.
//
// This package is the native replacement for the external protoc-gen-go_gapic
// plugin. It covers the GAPIC surface only; the *.pb.go message and gRPC stubs
// are still produced by protoc.
//
// The correctness bar is byte-for-byte equality with the code already checked
// into google-cloud-go. Two consequences follow, and both are load-bearing:
//
//   - Nothing here may read the wall clock, the environment, or the filesystem
//     layout of the machine. Everything that reaches the output must come from
//     the model or the config. The copyright year in particular comes from
//     config, never from [time.Now].
//   - Nothing here may iterate a map to produce output. Go randomizes map
//     order, so a map range that reaches a template makes the output
//     nondeterministic in a way that only shows up intermittently. Sort keys
//     into a slice first.
package golang

import (
	"context"
	"embed"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/language"
	"github.com/googleapis/librarian/internal/sidekick/parser"
)

//go:embed all:templates
var templates embed.FS

// Generate generates the Go GAPIC surface for model into outdir.
//
// The signature deliberately matches rust.Generate and codec_sample.Generate.
// There is no shared codec interface in internal/sidekick, so this is a
// convention rather than a constraint, but matching it keeps the call site in
// internal/librarian/golang uniform with the other languages.
func Generate(ctx context.Context, model *api.API, outdir string, cfg *parser.ModelConfig) error {
	if _, err := AnnotateModel(model, cfg); err != nil {
		return err
	}
	provider := func(name string) (string, error) {
		contents, err := templates.ReadFile(name)
		if err != nil {
			return "", err
		}
		return string(contents), nil
	}
	// Stage 1: Generate package-level files (excluding per-service templates).
	allFiles := language.WalkTemplatesDir(templates, "templates/gapic")
	var packageFiles []language.GeneratedFile
	for _, f := range allFiles {
		base := filepath.Base(f.TemplatePath)
		if base == "service_client.go.mustache" ||
			base == "client_example_test.go.mustache" ||
			base == "client_example_go123_test.go.mustache" ||
			base == "snippet.go.mustache" {
			continue
		}
		if base == "gapic_metadata.json.mustache" && (cfg == nil || cfg.Codec == nil || cfg.Codec["metadata"] != "true") {
			continue
		}
		mAnn, _ := model.Codec.(*ModelAnnotation)
		if base == "operations.go.mustache" && (mAnn == nil || !mAnn.HasCustomOp) {
			continue
		}
		packageFiles = append(packageFiles, f)
	}
	if err := language.GenerateFromModel(outdir, model, provider, packageFiles); err != nil {
		return err
	}

	// Stage 2: Generate per-service client and example files dynamically.
	for _, s := range model.Services {
		sAnn, ok := s.Codec.(*ServiceAnnotation)
		if !ok || sAnn == nil || sAnn.FileName == "" {
			continue
		}
		gen := language.GeneratedFile{
			TemplatePath: "templates/gapic/service_client.go.mustache",
			OutputPath:   sAnn.FileName,
		}
		if err := language.GenerateService(outdir, s, provider, gen); err != nil {
			return err
		}

		if sAnn.ExampleTestFileName != "" {
			exGen := language.GeneratedFile{
				TemplatePath: "templates/gapic/client_example_test.go.mustache",
				OutputPath:   sAnn.ExampleTestFileName,
			}
			if err := language.GenerateService(outdir, s, provider, exGen); err != nil {
				return err
			}
		}

		if sAnn.ExampleGo123TestFileName != "" {
			ex123Gen := language.GeneratedFile{
				TemplatePath: "templates/gapic/client_example_go123_test.go.mustache",
				OutputPath:   sAnn.ExampleGo123TestFileName,
			}
			if err := language.GenerateService(outdir, s, provider, ex123Gen); err != nil {
				return err
			}
		}
	}

	// Stage 3: Generate snippets if configured.
	if cfg.Codec["omit-snippets"] != "true" && cfg.Codec["snippets-out-dir"] != "" {
		if err := GenerateSnippets(model, cfg.Codec["snippets-out-dir"], provider); err != nil {
			return err
		}
	}

	return formatGoFiles(outdir)
}

func formatGoFiles(outdir string) error {
	return filepath.WalkDir(outdir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		formatted, err := format.Source(b)
		if err != nil {
			return fmt.Errorf("failed to format %s: %w", p, err)
		}
		return os.WriteFile(p, formatted, 0o644)
	})
}

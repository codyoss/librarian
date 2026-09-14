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
	provider := func(name string) (string, error) {
		contents, err := templates.ReadFile(name)
		if err != nil {
			return "", err
		}
		return string(contents), nil
	}
	generatedFiles := language.WalkTemplatesDir(templates, "templates/gapic")
	return language.GenerateFromModel(outdir, model, provider, generatedFiles)
}

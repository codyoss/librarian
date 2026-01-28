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
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/googleapis/librarian/internal/morph/gcloudcmd"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/tidwall/gjson"
)

//go:embed gcloud.sh.mustache
var gcloudTemplate string

// GcloudInput contains the input for generating a gcloud command.
type GcloudInput struct {
	ReqData     []byte
	API         *api.API
	Method      *api.Method
	OutDir      string
	MappingFile string
}

type gcloudData struct {
	Command        string
	PositionalArgs []string
	Flags          []*gcloudFlag
}

type gcloudFlag struct {
	Name   string
	Value  string
	IsLast bool
}

type gcloudMappingFile struct {
	Command    string                  `json:"command"`
	MessageID  string                  `json:"message_id"`
	Properties []gcloudcmd.FlagMapping `json:"properties"`
}

// GenerateGcloud generates a gcloud command using the mapping file.
func GenerateGcloud(ctx context.Context, in *GcloudInput) error {
	mappingBytes, err := os.ReadFile(in.MappingFile)
	if err != nil {
		return fmt.Errorf("failed to read mapping file: %w", err)
	}

	var mapping gcloudMappingFile
	if err := json.Unmarshal(mappingBytes, &mapping); err != nil {
		return fmt.Errorf("failed to unmarshal mapping file: %w", err)
	}

	slog.Info("Loaded gcloud mapping", "command", mapping.Command)

	// Parse request data using gjson for flexible path access
	jsonStr := string(in.ReqData)

	var positionalArgs []string
	// Allow for gaps in positional args if needed, but usually they should be sequential 0..N
	// Map pos -> value
	posMap := make(map[int]string)

	var flags []*gcloudFlag

	decomposed, usedFields := decomposePathParams(in, jsonStr)

	for _, prop := range mapping.Properties {
		if usedFields[prop.FieldPath] {
			continue
		}

		result := gjson.Get(jsonStr, prop.FieldPath)
		if !result.Exists() {
			// If not in JSON, check if it's in decomposed params
			// e.g. field_path might be "parent", but we want to map "project" and "location" from it?
			// Actually the mapping file maps "parent" -> "--location" in some cases.
			// But the user wants us to automagically add flags from decomposed params too.
			// "Even if it is not listed in our mappings we can always also always assume there is a project flag."

			// If field_path points to "parent", and we have decomposed "parent" into "project" and "location",
			// we should use those values if the flag corresponds to them.
		}

		value := result.String()
		if result.IsArray() {
			var items []string
			result.ForEach(func(_, item gjson.Result) bool {
				if item.IsObject() {
					// Heuristic: if object has 1 field, take its value
					count := 0
					var singleVal string
					item.ForEach(func(_, v gjson.Result) bool {
						singleVal = v.String()
						count++
						return true
					})
					if count == 1 {
						items = append(items, singleVal)
					} else {
						items = append(items, item.String())
					}
				} else {
					items = append(items, item.String())
				}
				return true
			})
			if len(items) > 0 {
				value = strings.Join(items, ",")
			}
		}

		if len(prop.Choices) > 0 {
			// If we have choices, we check if the value (or keys in value) matches any choice.
			if result.IsObject() {
				// For object, check if any key matches a choice
				// e.g. "replication": {"automatic": {}} -> "automatic"
				// or "policy": {"automatic": {}} -> "automatic"
				// We iterate over keys in the object.
				result.ForEach(func(key, val gjson.Result) bool {
					keyStr := key.String()
					normKey := normalizeChoice(keyStr)
					for _, choice := range prop.Choices {
						if normKey == normalizeChoice(choice) {
							value = choice
							return false // stop iteration
						}
					}
					return true // continue
				})
			} else {
				// For string/primitive, check if value matches a choice
				valStr := result.String()
				normVal := normalizeChoice(valStr)
				for _, choice := range prop.Choices {
					if normVal == normalizeChoice(choice) {
						value = choice
						break
					}
				}
			}
			// If matched, we successfully resolved to a simple string value (the choice).
			// If NOT matched, we fall back to original value logic (maybe it's a complex object or just a string).
		}

		if (result.IsObject() || result.IsArray()) && value == result.String() {
			// Compact JSON for CLI usage, ONLY if we didn't resolve to a simple choice above.
			raw := result.Raw
			dst := &bytes.Buffer{}
			if err := json.Compact(dst, []byte(raw)); err == nil {
				value = dst.String()
			}
		}

		if prop.Pos != nil {
			posMap[*prop.Pos] = value
		} else if prop.Flag != "" {
			// Skip empty values
			if value == "" || value == "null" {
				continue
			}
			flags = append(flags, &gcloudFlag{
				Name:  prop.Flag,
				Value: value,
			})
		}
	}

	// Inject decomposed path params as flags if they aren't already present
	for k, v := range decomposed {
		// user requested: "we can always also always assume there is a project flag"
		// and "try associate the field with bindings to these indivdual segements"
		if v == "" {
			continue
		}

		// Check if flag already exists
		exists := false
		flagName := "--" + k
		for _, f := range flags {
			if f.Name == flagName {
				exists = true
				break
			}
		}

		if !exists {
			flags = append(flags, &gcloudFlag{
				Name:  flagName,
				Value: v,
			})
		}
	}

	// Reconstruct positional args in order
	var positions []int
	for pos := range posMap {
		positions = append(positions, pos)
	}
	sort.Ints(positions)

	for _, pos := range positions {
		positionalArgs = append(positionalArgs, posMap[pos])
	}

	// Sort flags for deterministic output
	sort.Slice(flags, func(i, j int) bool {
		return flags[i].Name < flags[j].Name
	})

	if len(flags) > 0 {
		flags[len(flags)-1].IsLast = true
	}

	gd := &gcloudData{
		Command:        mapping.Command,
		PositionalArgs: positionalArgs,
		Flags:          flags,
	}

	s, err := mustache.Render(gcloudTemplate, gd)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	if err := os.MkdirAll(in.OutDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	outFile := filepath.Join(in.OutDir, "gcloud.sh")
	if err := os.WriteFile(outFile, []byte(s), 0755); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	slog.Info("Generated gcloud command", "file", outFile)
	return nil
}

// decomposePathParams extracts path parameters from the request data based on the method's path template.
// It returns a map of segment variable field paths to their values, and a set of used fields.
func decomposePathParams(in *GcloudInput, jsonStr string) (map[string]string, map[string]bool) {
	if in.Method == nil || in.Method.PathInfo == nil || len(in.Method.PathInfo.Bindings) == 0 {
		return nil, nil
	}

	var bestDecomposed map[string]string
	var bestUsedFields map[string]bool

	data := gjson.Parse(jsonStr)

	// Iterate over all bindings to find the one that matches best (extracts most variables)
	for _, binding := range in.Method.PathInfo.Bindings {
		decomposed := make(map[string]string)
		usedFields := make(map[string]bool)
		matchFailed := false

		// Iterate over segments to find variables and corresponding values in the request
		for _, segment := range binding.PathTemplate.Segments {
			if segment.Variable == nil {
				continue
			}

			// The variable field path tells us where in the request object to look for the full value.
			// e.g. "parent"
			if len(segment.Variable.FieldPath) == 0 {
				continue
			}
			fieldPath := segment.Variable.FieldPath[0] // Simplified: assumes single field path for now
			usedFields[fieldPath] = true

			result := data.Get(fieldPath)
			if !result.Exists() {
				matchFailed = true
				break
			}

			fullValue := result.String()

			// We match fullValue against the variable's segments.
			values := matchPath(fullValue, segment.Variable.Segments)
			if values == nil {
				matchFailed = true
				break
			}

			for k, v := range values {
				decomposed[k] = v
			}
		}

		if matchFailed {
			continue
		}

		// Prefer the binding that extracted more variables
		if len(decomposed) > len(bestDecomposed) {
			bestDecomposed = decomposed
			bestUsedFields = usedFields
		}
	}

	return bestDecomposed, bestUsedFields
}

// matchPath matches a value against a list of segments and returns captured variables.
// It assumes segments are like ["projects", "*", "locations", "*"].
// It returns map["project"] = "p1", map["location"] = "l1".
// Note: It singularizes the keys (removes trailing 's') as requested.
func matchPath(value string, segments []string) map[string]string {
	parts := strings.Split(value, "/")
	captured := make(map[string]string)

	// Simple matching: iterate parts and segments.
	// If segment is literal, it must match.
	// If segment is wildcard, we capture it using the previous literal as key.

	partIdx := 0
	segmentIdx := 0

	var lastLiteral string

	for partIdx < len(parts) && segmentIdx < len(segments) {
		seg := segments[segmentIdx]
		part := parts[partIdx]

		if seg == "*" || seg == "**" {
			// Wildcard match.
			if lastLiteral != "" {
				// Singularize key
				key := strings.TrimSuffix(lastLiteral, "s")
				captured[key] = part
			}
			partIdx++
			segmentIdx++
		} else {
			// Literal match
			if part == seg {
				lastLiteral = seg
				partIdx++
				segmentIdx++
			} else {
				// Mismatch, maybe this binding doesn't apply or we lost sync?
				// simple fallback: increment both?
				return nil // exact match required for structure
			}
		}
	}

	// Ensure we matched all segments of the template
	if segmentIdx < len(segments) {
		return nil
	}

	return captured
}

func normalizeChoice(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", "")
	return strings.ReplaceAll(s, "_", "")
}

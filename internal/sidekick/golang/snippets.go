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

package golang

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/language"
)

// SnippetMetadataFile represents the top-level structure of a snippet metadata file.
type SnippetMetadataFile struct {
	ClientLibrary SnippetClientLibrary `json:"clientLibrary"`
	Snippets      []*SnippetEntry      `json:"snippets"`
}

// SnippetClientLibrary represents the client library metadata in a snippet metadata file.
type SnippetClientLibrary struct {
	APIs     []SnippetAPI `json:"apis"`
	Language string       `json:"language"`
	Name     string       `json:"name"`
	Version  string       `json:"version"`
}

// SnippetAPI describes an API entry within snippet metadata.
type SnippetAPI struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// SnippetEntry describes a single code snippet within snippet metadata.
type SnippetEntry struct {
	ClientMethod SnippetClientMethod `json:"clientMethod"`
	Description  string              `json:"description"`
	File         string              `json:"file"`
	Language     string              `json:"language"`
	Origin       string              `json:"origin"`
	RegionTag    string              `json:"regionTag"`
	Segments     []SnippetSegment    `json:"segments"`
	Title        string              `json:"title"`
}

// SnippetClientMethod describes the client method associated with a snippet.
type SnippetClientMethod struct {
	Client     SnippetNamedEntity `json:"client"`
	FullName   string             `json:"fullName"`
	Method     SnippetMethodInfo  `json:"method"`
	Parameters []SnippetParameter `json:"parameters"`
	ResultType string             `json:"resultType,omitempty"`
	ShortName  string             `json:"shortName"`
}

// SnippetNamedEntity describes a named entity with full and short names.
type SnippetNamedEntity struct {
	FullName  string `json:"fullName"`
	ShortName string `json:"shortName"`
}

// SnippetMethodInfo describes the proto method information for a snippet.
type SnippetMethodInfo struct {
	FullName  string             `json:"fullName"`
	Service   SnippetNamedEntity `json:"service"`
	ShortName string             `json:"shortName"`
}

// SnippetParameter describes an input parameter to a client method.
type SnippetParameter struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SnippetSegment defines a line segment range for a snippet.
type SnippetSegment struct {
	End   int    `json:"end"`
	Start int    `json:"start"`
	Type  string `json:"type"`
}

func computeSnippetResultType(mAnn *MethodAnnotation) string {
	if mAnn.IsEmpty {
		return ""
	}
	if mAnn.IsLRO {
		return mAnn.OperationType
	}
	if mAnn.IsPaged {
		return mAnn.IteratorType
	}
	if mAnn.IsUnary {
		return "*" + mAnn.ResponseType
	}
	return ""
}

// GenerateSnippets renders snippet main.go files and emits snippet_metadata.<protoPkg>.json.
func GenerateSnippets(model *api.API, snippetsDir string, provider language.TemplateProvider) error {
	mAnn, ok := model.Codec.(*ModelAnnotation)
	if !ok || mAnn == nil {
		return fmt.Errorf("model annotation missing for snippets")
	}

	protoPkg := mAnn.ProtoPackage
	lastDot := strings.LastIndex(protoPkg, ".")
	apiVersion := protoPkg[lastDot+1:]

	metadata := &SnippetMetadataFile{
		ClientLibrary: SnippetClientLibrary{
			APIs: []SnippetAPI{
				{
					ID:      protoPkg,
					Version: apiVersion,
				},
			},
			Language: "GO",
			Name:     mAnn.ImportPath,
			Version:  "$VERSION",
		},
	}

	var services []*api.Service
	for _, s := range model.Services {
		sAnn, ok := s.Codec.(*ServiceAnnotation)
		if !ok || sAnn == nil {
			continue
		}
		services = append(services, s)
	}
	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	for _, s := range services {
		sAnn := s.Codec.(*ServiceAnnotation)
		clientShortName := sAnn.ClientName
		hostPrefix, _, _ := strings.Cut(s.DefaultHost, ".")

		var methods []*api.Method
		for _, m := range sAnn.ExampleMethods {
			if m.ClientSideStreaming || m.ServerSideStreaming {
				continue
			}
			methods = append(methods, m)
		}
		sort.Slice(methods, func(i, j int) bool {
			return methods[i].Name < methods[j].Name
		})

		for _, m := range methods {
			methAnn, ok := m.Codec.(*MethodAnnotation)
			if !ok || methAnn == nil {
				continue
			}

			relPath := filepath.Join(clientShortName, m.Name, "main.go")
			gen := language.GeneratedFile{
				TemplatePath: "templates/gapic/snippet.go.mustache",
				OutputPath:   relPath,
			}
			if err := language.GenerateMethod(snippetsDir, m, provider, gen); err != nil {
				return fmt.Errorf("failed to generate snippet for %s.%s: %w", s.Name, m.Name, err)
			}

			fullPath := filepath.Join(snippetsDir, relPath)
			rawBytes, err := os.ReadFile(fullPath)
			if err != nil {
				return err
			}
			formatted, err := format.Source(rawBytes)
			if err != nil {
				return fmt.Errorf("failed to format snippet %s: %w", fullPath, err)
			}
			if err := os.WriteFile(fullPath, formatted, 0o644); err != nil {
				return err
			}

			lines := strings.Split(strings.TrimSuffix(string(formatted), "\n"), "\n")
			endLine := len(lines)

			parentProtoPkg := protoPkg
			parentName := s.Name
			if m.SourceServiceID != "" && strings.HasPrefix(m.SourceServiceID, ".google.") && m.SourceServiceID != s.ID {
				trimmed := strings.TrimPrefix(m.SourceServiceID, ".")
				dot := strings.LastIndex(trimmed, ".")
				if dot > 0 {
					parentProtoPkg = trimmed[:dot]
					parentName = trimmed[dot+1:]
				}
			}

			resultType := computeSnippetResultType(methAnn)

			entry := &SnippetEntry{
				ClientMethod: SnippetClientMethod{
					Client: SnippetNamedEntity{
						FullName:  fmt.Sprintf("%s.%s", protoPkg, clientShortName),
						ShortName: clientShortName,
					},
					FullName: fmt.Sprintf("%s.%s.%s", protoPkg, clientShortName, m.Name),
					Method: SnippetMethodInfo{
						FullName: fmt.Sprintf("%s.%s.%s", parentProtoPkg, parentName, m.Name),
						Service: SnippetNamedEntity{
							FullName:  fmt.Sprintf("%s.%s", parentProtoPkg, parentName),
							ShortName: parentName,
						},
						ShortName: m.Name,
					},
					Parameters: []SnippetParameter{
						{Name: "ctx", Type: "context.Context"},
						{Name: "req", Type: methAnn.RequestType},
						{Name: "opts", Type: "...gax.CallOption"},
					},
					ResultType: resultType,
					ShortName:  m.Name,
				},
				Description: methAnn.SnippetDescription,
				File:        fmt.Sprintf("%s/%s/main.go", clientShortName, m.Name),
				Language:    "GO",
				Origin:      "API_DEFINITION",
				RegionTag:   methAnn.RegionTag,
				Segments: []SnippetSegment{
					{
						End:   endLine,
						Start: 18,
						Type:  "FULL",
					},
				},
				Title: fmt.Sprintf("%s %s Sample", hostPrefix, m.Name),
			}
			metadata.Snippets = append(metadata.Snippets, entry)
		}
	}

	metadataPath := filepath.Join(snippetsDir, fmt.Sprintf("snippet_metadata.%s.json", protoPkg))
	return writeSnippetMetadata(metadataPath, metadata)
}

func writeSnippetMetadata(path string, data *SnippetMetadataFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return err
	}
	content := buf.Bytes()
	if len(content) > 0 && content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	return os.WriteFile(path, content, 0o644)
}

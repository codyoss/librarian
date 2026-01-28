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
	_ "embed"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cbroglie/mustache"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/config"
	"github.com/googleapis/librarian/internal/sidekick/language"
)

//go:embed curl.sh.mustache
var curlTemplate string

// CurlInput contains the input for generating a curl command.
type CurlInput struct {
	ReqData []byte
	API     *api.API
	OutDir  string
	Config  *config.Config
	Method  *api.Method
}

type curlData struct {
	Verb            string
	Host            string
	Path            string
	Body            string
	QueryParameters []*queryParam
}

type queryParam struct {
	Name  string
	Value any
}

// GenerateCurl generates a curl command from the model.
func GenerateCurl(ctx context.Context, in *CurlInput) error {
	pp := language.PathParams(in.Method, in.API.State)
	query := language.QueryParams(in.Method, in.Method.PathInfo.Bindings[0])

	data := map[string]any{}
	if err := json.Unmarshal(in.ReqData, &data); err != nil {
		return err
	}
	binding := in.Method.PathInfo.Bindings[0]
	verb := binding.Verb
	var path string
	for _, segment := range binding.PathTemplate.Segments {
		if segment.Literal != nil {
			path += "/" + *segment.Literal
		}
		if segment.Variable != nil {
			for _, fieldPath := range segment.Variable.FieldPath {
				path += "/" + data[fieldPath].(string)
			}
		}
	}
	// For each query parameter and path parameter, delete it from the data map.
	for _, param := range pp {
		delete(data, param.Name)
	}
	var params []*queryParam
	for _, param := range query {
		name := param.Name
		if !param.NameEqualJSONName() {
			name = param.JSONName
		}
		params = append(params, &queryParam{
			Name:  name,
			Value: data[param.Name],
		})
		delete(data, param.Name)
	}
	// TODO: check body field path
	var body []byte
	if len(data) > 0 {
		var err error
		body, err = json.Marshal(data)
		if err != nil {
			return err
		}
	}
	cr := &curlData{
		Verb:            verb,
		Host:            in.Method.Service.DefaultHost,
		Path:            path,
		Body:            string(body),
		QueryParameters: params,
	}
	slog.Info("Generated curl command", "data", cr)
	s, err := mustache.Render(curlTemplate, cr)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(in.OutDir, "curl.sh"), []byte(s), 0666); err != nil {
		return err
	}
	slog.Info("Generated curl command", "data", s)
	return nil
}

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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// GoBazelConfig holds configuration extracted from the Go rules in a googleapis BUILD.bazel file.
// The fields are from the Go rules in a API version BUILD.bazel file.
// E.g., googleapis/google/cloud/asset/v1/BUILD.bazel
// Note that not all fields are present in every Bazel rule usage.
type GoBazelConfig struct {
	// The fields below are all from the go_gapic_library rule.
	grpcServiceConfig string
	gapicImportPath   string
	protoImportPath   string
	metadata          bool
	releaseLevel      string
	restNumericEnums  bool
	serviceYAML       string
	transport         string
	diregapic         bool

	// Meta configuration
	hasGoGRPC bool

	// Whether this library has a GAPIC rule at all.
	hasGAPIC bool
}

// parseBazelConfig reads a BUILD.bazel file from the given directory and extracts the
// relevant configuration from the go_gapic_library and go_proto_library rules.
func parseBazelConfig(dir string) (*GoBazelConfig, error) {
	c := &GoBazelConfig{}
	fp := filepath.Join(dir, "BUILD.bazel")
	data, err := os.ReadFile(fp)
	if err != nil {
		return nil, fmt.Errorf("failed to read BUILD.bazel file %s: %w", fp, err)
	}
	content := string(data)

	// First, find the go_gapic_library block.
	re := regexp.MustCompile(`go_gapic_library\((?s:.)*?\)`)
	gapicLibraryBlock := re.FindString(content)
	if gapicLibraryBlock != "" {
		// GAPIC build target
		c.hasGAPIC = true
		c.grpcServiceConfig = findString(gapicLibraryBlock, "grpc_service_config")
		c.gapicImportPath = findString(gapicLibraryBlock, "importpath")
		c.releaseLevel = findString(gapicLibraryBlock, "release_level")
		// If the service config is actually a bazel target instead of a file, just assume there's a file with the same name.
		c.serviceYAML = strings.TrimPrefix(findString(gapicLibraryBlock, "service_yaml"), ":")
		c.transport = findString(gapicLibraryBlock, "transport")
		if c.metadata, err = findBool(gapicLibraryBlock, "metadata"); err != nil {
			return nil, fmt.Errorf("failed to parse BUILD.bazel file %s: %w", fp, err)
		}
		if c.restNumericEnums, err = findBool(gapicLibraryBlock, "rest_numeric_enums"); err != nil {
			return nil, fmt.Errorf("failed to parse BUILD.bazel file %s: %w", fp, err)
		}
		if c.diregapic, err = findBool(gapicLibraryBlock, "diregapic"); err != nil {
			return nil, fmt.Errorf("failed to parse BUILD.bazel file %s: %w", fp, err)
		}
	}

	// We are currently migrating go_proto_library to go_grpc_library.
	// Only one is expect to be present
	re = regexp.MustCompile(`go_grpc_library\((?s:.)*?\)`)
	grpcLibraryBlock := re.FindString(content)
	if grpcLibraryBlock != "" {
		c.hasGoGRPC = true
		c.protoImportPath = findString(grpcLibraryBlock, "importpath")
	}
	goProtoLibraryPattern := regexp.MustCompile(`go_proto_library\((?s:.)*?\)`)
	goProtoLibraryBlock := goProtoLibraryPattern.FindString(content)
	if goProtoLibraryBlock != "" {
		if c.hasGoGRPC {
			return nil, fmt.Errorf("misconfiguration in BUILD.bazel file, only one of go_grpc_library and go_proto_library rules should be present: %s", fp)
		}
		if strings.Contains(goProtoLibraryBlock, "@io_bazel_rules_go//proto:go_grpc") {
			return nil, fmt.Errorf("BUILD.bazel uses legacy gRPC plugin (@io_bazel_rules_go//proto:go_grpc) which is no longer supported: %s", fp)
		}
	}
	slog.Debug("bazel config loaded", "conf", fmt.Sprintf("%+v", c))
	return c, nil
}

func findString(content, name string) string {
	re := regexp.MustCompile(fmt.Sprintf(`%s\s*=\s*"([^"]+)"`, name))
	if match := re.FindStringSubmatch(content); len(match) > 1 {
		return match[1]
	}
	slog.Debug("failed to find string attr in BUILD.bazel", "name", name)
	return ""
}

func findBool(content, name string) (bool, error) {
	re := regexp.MustCompile(fmt.Sprintf(`%s\s*=\s*(\w+)`, name))
	if match := re.FindStringSubmatch(content); len(match) > 1 {
		if b, err := strconv.ParseBool(match[1]); err == nil {
			return b, nil
		}
		return false, fmt.Errorf("failed to parse bool attr in BUILD.bazel: %q, got: %q", name, match[1])
	}
	slog.Debug("failed to find bool attr in BUILD.bazel", "name", name)
	return false, nil
}

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
	"regexp"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

var literalVarRegex = regexp.MustCompile(`([^/]+)/{[^}]+\}`)
var varNameRegex = regexp.MustCompile(`{([^=}\s]+)`)

func buildHeuristicVocabulary(methods []*descriptorpb.MethodDescriptorProto) map[string]bool {
	resourceCollections := make(map[string]bool)

	resourceCollections["projects"] = true
	resourceCollections["locations"] = true
	resourceCollections["folders"] = true
	resourceCollections["organizations"] = true
	resourceCollections["billingAccounts"] = true

	discoveryExactVerbs := map[string]struct{}{
		"get":            {},
		"list":           {},
		"aggregatedlist": {},
		"create":         {},
		"update":         {},
		"delete":         {},
		"patch":          {},
		"insert":         {},
	}
	discoverySuffixes := []string{
		".get", ".list", ".create", ".update", ".delete", ".patch", ".insert",
	}

	crudPrefixes := []string{
		"get", "list", "create", "update", "delete", "patch", "insert",
	}

	for _, m := range methods {
		nameLower := strings.ToLower(m.GetName())

		var isCRUDPrefix bool
		for _, prefix := range crudPrefixes {
			if strings.HasPrefix(nameLower, prefix) {
				isCRUDPrefix = true
				break
			}
		}

		_, isDiscoveryExact := discoveryExactVerbs[nameLower]

		var isDiscoverySuffix bool
		for _, suffix := range discoverySuffixes {
			if strings.HasSuffix(nameLower, suffix) {
				isDiscoverySuffix = true
				break
			}
		}

		if !isCRUDPrefix && !isDiscoveryExact && !isDiscoverySuffix {
			continue
		}

		if m.GetOptions() == nil {
			continue
		}

		eHTTP := proto.GetExtension(m.GetOptions(), annotations.E_Http)
		if eHTTP == nil {
			continue
		}

		h, ok := eHTTP.(*annotations.HttpRule)
		if !ok || h == nil {
			continue
		}

		patterns := getHTTPPatterns(h)
		for _, pattern := range patterns {
			matches := literalVarRegex.FindAllStringSubmatch(pattern, -1)
			for _, match := range matches {
				if len(match) < 2 {
					continue
				}
				literal := match[1]

				if idx := strings.Index(literal, ":"); idx != -1 {
					literal = literal[:idx]
				}

				if isVersionString(literal) {
					continue
				}
				resourceCollections[literal] = true
			}
		}
	}

	return resourceCollections
}

func getHTTPPatterns(h *annotations.HttpRule) []string {
	var patterns []string

	extract := func(pattern string) {
		if pattern != "" {
			patterns = append(patterns, pattern)
		}
	}

	switch p := h.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		extract(p.Get)
	case *annotations.HttpRule_Put:
		extract(p.Put)
	case *annotations.HttpRule_Post:
		extract(p.Post)
	case *annotations.HttpRule_Delete:
		extract(p.Delete)
	case *annotations.HttpRule_Patch:
		extract(p.Patch)
	}

	for _, rule := range h.GetAdditionalBindings() {
		patterns = append(patterns, getHTTPPatterns(rule)...)
	}

	return patterns
}

func isVersionString(s string) bool {
	if !strings.HasPrefix(s, "v") {
		return false
	}
	if len(s) < 2 {
		return false
	}
	if s[1] >= '0' && s[1] <= '9' {
		return true
	}
	return false
}

func identifyHeuristicTarget(m *descriptorpb.MethodDescriptorProto, h *annotations.HttpRule, vocabulary map[string]bool) (*heuristicTarget, error) {
	patterns := getHTTPPatterns(h)
	if len(patterns) == 0 {
		return nil, nil
	}

	pattern := patterns[0]
	segments := splitPathSegments(pattern)

	for i := len(segments) - 1; i >= 0; i-- {
		seg := segments[i]

		if !strings.Contains(seg, "{") || i == 0 || strings.Contains(segments[i-1], "{") {
			continue
		}

		token := segments[i-1]
		if strings.Contains(token, ":") {
			continue
		}

		if !vocabulary[token] && !isVersionString(token) {
			continue
		}

		firstIndex := i
		if vocabulary[token] {
			firstIndex = i - 1
			for firstIndex > 0 {
				prevSeg := segments[firstIndex-1]
				if !strings.Contains(prevSeg, "{") {
					if isVersionString(prevSeg) {
						break
					}
					firstIndex--
					continue
				}

				if firstIndex < 2 || strings.Contains(segments[firstIndex-2], "{") {
					break
				}
				prevLiteralVal := segments[firstIndex-2]
				if vocabulary[prevLiteralVal] {
					firstIndex -= 2
					continue
				}
				if isVersionString(prevLiteralVal) {
					firstIndex--
				}
				break
			}
		}

		disconnected := false
		for k := 0; k < firstIndex; k++ {
			if strings.Contains(segments[k], "{") {
				disconnected = true
				break
			}
		}
		if disconnected {
			continue
		}

		var fields []string
		var formatSegments []string

		targetSegments := segments[firstIndex : i+1]
		for _, s := range targetSegments {
			if strings.Contains(s, "{") {
				match := varNameRegex.FindStringSubmatch(s)
				if len(match) > 1 {
					fields = append(fields, match[1])
				}
				formatSegments = append(formatSegments, "%v")
			} else {
				token := s
				if idx := strings.Index(token, ":"); idx != -1 {
					token = token[:idx]
				}
				if !isVersionString(token) {
					formatSegments = append(formatSegments, token)
				}
			}
		}

		return &heuristicTarget{
			Format:     strings.Join(formatSegments, "/"),
			FieldNames: fields,
		}, nil
	}

	return nil, nil
}

func splitPathSegments(pattern string) []string {
	var segments []string
	var current strings.Builder
	inVar := false

	for _, r := range pattern {
		switch r {
		case '{':
			inVar = true
			current.WriteRune(r)
		case '}':
			inVar = false
			current.WriteRune(r)
		case '/':
			if !inVar {
				segments = append(segments, current.String())
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	segments = append(segments, current.String())
	return segments
}

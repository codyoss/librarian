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
	_ "embed"
	"encoding/json"
	"fmt"
	"go/format"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/cbroglie/mustache"
	"github.com/googleapis/librarian/internal/sidekick/api"
)

//go:embed main.go.mustache
var goTemplate string

type generateGoInput struct {
	ReqData    []byte
	API        *api.API
	OutDir     string
	Method     *api.Method
	ServiceDir string
}

type goData struct {
	Imports     []string
	PackageName string
	ServiceName string
	MethodName  string
	RequestName string
	RequestInit string
}

type FieldNode struct {
	Name        string
	TypeName    string
	Value       any
	IsMessage   bool
	IsRepeated  bool
	IsMap       bool
	IsPrimitive bool
	Children    []*FieldNode
	Items       []*FieldNode
	Entries     []*MapEntry
}

type MapEntry struct {
	Key   string
	Value *FieldNode
}

func GenerateGo(in *generateGoInput) error {
	slog.Info("DEBUG MAIN TEMPLATE", "goTemplate", goTemplate) // Keep or remove? Remove.
	slog.Info("Generating Go code", "method", in.Method.Name)
	slog.Info("Parse Bazel Config")
	bazelConfig, err := parseBazelConfig(in.ServiceDir)
	if err != nil {
		return err
	}
	slog.Info("Bazel Config", "conf", fmt.Sprintf("%+v", bazelConfig))
	gapicImportPathParts := strings.Split(bazelConfig.gapicImportPath, ";")

	data := map[string]any{}
	if err := json.Unmarshal(in.ReqData, &data); err != nil {
		return err
	}

	reqInit, imports, err := buildRequestInit(in.Method.InputType, data, bazelConfig.protoImportPath)
	if err != nil {
		return err
	}

	allImports := []string{bazelConfig.gapicImportPath, bazelConfig.protoImportPath}
	allImports = append(allImports, imports...)

	for _, imp := range allImports {
		if strings.Contains(imp, ";") {
			parts := strings.Split(imp, ";")
			imports = append(imports, parts[0])
		} else {
			imports = append(imports, imp)
		}
	}

	goData := &goData{
		Imports:     removeDuplicateStr(imports),
		PackageName: gapicImportPathParts[1],
		ServiceName: reduceServName(in.Method.Service.Name, gapicImportPathParts[1]),
		MethodName:  in.Method.Name,
		RequestName: in.Method.InputType.Name,
		RequestInit: reqInit.Render(),
	}

	slog.Info("Generated Go data", "data", goData, "req", in.Method.InputType.Name)

	tmpl, err := mustache.ParseString(goTemplate)
	if err != nil {
		return err
	}

	s, err := tmpl.Render(goData)
	if err != nil {
		return err
	}

	formatted, err := format.Source([]byte(s))
	if err != nil {
		slog.Error("Failed to format generated Go code", "error", err)
		// Write raw content for debugging
		if writeErr := os.WriteFile(filepath.Join(in.OutDir, "main.go"), []byte(s), 0666); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("formatting failed: %w\nSrc:\n%s", err, s)
	}

	if err := os.WriteFile(filepath.Join(in.OutDir, "main.go"), formatted, 0666); err != nil {
		return err
	}
	return nil
}

func (n *FieldNode) Render() string {
	var sb strings.Builder
	if n.IsMessage {
		sb.WriteString("&")
		sb.WriteString(n.TypeName)
		sb.WriteString("{\n")
		for _, child := range n.Children {
			sb.WriteString(child.Name)
			sb.WriteString(": ")
			sb.WriteString(child.Render())
			sb.WriteString(",\n")
		}
		sb.WriteString("}")
	} else if n.IsRepeated {
		sb.WriteString(n.TypeName)
		sb.WriteString("{\n")
		for _, item := range n.Items {
			sb.WriteString(item.Render())
			sb.WriteString(",\n")
		}
		sb.WriteString("}")
	} else if n.IsMap {
		sb.WriteString(n.TypeName)
		sb.WriteString("{\n")
		for _, entry := range n.Entries {
			sb.WriteString(fmt.Sprintf("%q", entry.Key))
			sb.WriteString(": ")
			sb.WriteString(entry.Value.Render())
			sb.WriteString(",\n")
		}
		sb.WriteString("}")
	} else if n.IsPrimitive {
		if s, ok := n.Value.(string); ok {
			sb.WriteString(s)
		} else {
			sb.WriteString(fmt.Sprintf("%v", n.Value))
		}
	}
	return sb.String()
}

func buildRequestInit(msg *api.Message, data map[string]any, protoPkg string) (*FieldNode, []string, error) {
	// Calculate proto package name from import path
	protoPkgParts := strings.Split(protoPkg, "/")
	protoPkgName := protoPkgParts[len(protoPkgParts)-1]
	if strings.Contains(protoPkgName, ";") {
		parts := strings.Split(protoPkgName, ";")
		protoPkgName = parts[1]
	}

	root := &FieldNode{
		IsMessage: true,
		Children:  []*FieldNode{},
		TypeName:  getGoTypeName(msg, protoPkgName),
	}
	var allImports []string

	if len(data) == 0 {
		return root, nil, nil
	}

	for _, field := range msg.Fields {
		val, ok := data[field.Name]
		if !ok {
			if field.JSONName != "" {
				val, ok = data[field.JSONName]
			}
			if !ok {
				continue
			}
		}
		slog.Info("Field processing", "field", field.Name, "val", val, "type", fmt.Sprintf("%T", val))

		goFieldName := toPascalCase(field.Name)
		node := &FieldNode{Name: goFieldName}

		if field.Repeated {
			node.IsRepeated = true
			sliceVal, ok := val.([]any)
			if !ok {
				continue
			}

			elemType := ""
			// Determine element type name
			switch field.Typez {
			case api.STRING_TYPE:
				elemType = "string"
			case api.INT32_TYPE, api.SINT32_TYPE, api.SFIXED32_TYPE:
				elemType = "int32"
			case api.INT64_TYPE, api.SINT64_TYPE, api.SFIXED64_TYPE:
				elemType = "int64"
			case api.UINT32_TYPE, api.FIXED32_TYPE:
				elemType = "uint32"
			case api.UINT64_TYPE, api.FIXED64_TYPE:
				elemType = "uint64"
			case api.BOOL_TYPE:
				elemType = "bool"
			case api.MESSAGE_TYPE:
				if field.MessageType != nil {
					elemType = "*" + getGoTypeName(field.MessageType, protoPkgName)
				}
			}
			node.TypeName = "[]" + elemType

			for _, item := range sliceVal {
				childNode := &FieldNode{}
				if field.Typez == api.MESSAGE_TYPE {
					subData, ok := item.(map[string]any)
					if !ok {
						continue
					}
					subNode, subImports, err := buildRequestInit(field.MessageType, subData, protoPkg)
					if err != nil {
						return nil, nil, err
					}
					allImports = append(allImports, subImports...)
					childNode = subNode
					childNode.TypeName = strings.TrimPrefix(elemType, "*")
				} else {
					childNode.IsPrimitive = true
					childNode.Value = formatPrimitive(item, elemType)
				}
				node.Items = append(node.Items, childNode)
			}
		} else if field.Map {
			node.IsMap = true
			mapVal, ok := val.(map[string]any)
			if !ok {
				continue
			}

			if field.MessageType == nil {
				continue
			}
			// Find key/value fields
			var valueField *api.Field
			for _, f := range field.MessageType.Fields {
				if f.Name == "value" {
					valueField = f
					break
				}
			}
			if valueField == nil {
				continue
			}

			valTypeStr := "string"
			isMsgVal := false
			switch valueField.Typez {
			case api.STRING_TYPE:
				valTypeStr = "string"
			case api.MESSAGE_TYPE:
				if valueField.MessageType != nil {
					valTypeStr = "*" + getGoTypeName(valueField.MessageType, protoPkgName)
					isMsgVal = true
				}
			}

			node.TypeName = fmt.Sprintf("map[string]%s", valTypeStr)

			for k, v := range mapVal {
				entry := &MapEntry{Key: k}
				valNode := &FieldNode{}
				if isMsgVal {
					subData, ok := v.(map[string]any)
					if !ok {
						continue
					}
					subNode, subImports, err := buildRequestInit(valueField.MessageType, subData, protoPkg)
					if err != nil {
						return nil, nil, err
					}
					allImports = append(allImports, subImports...)
					valNode = subNode
					valNode.TypeName = strings.TrimPrefix(valTypeStr, "*")
				} else {
					valNode.IsPrimitive = true
					valNode.Value = formatPrimitive(v, valTypeStr)
				}
				entry.Value = valNode
				node.Entries = append(node.Entries, entry)
			}

		} else {
			// Singular field
			childNode := &FieldNode{}
			switch field.Typez {
			case api.MESSAGE_TYPE:
				if field.MessageType == nil {
					continue
				}
				subData, ok := val.(map[string]any)
				if !ok {
					continue
				}
				subNode, subImports, err := buildRequestInit(field.MessageType, subData, protoPkg)
				if err != nil {
					return nil, nil, err
				}
				allImports = append(allImports, subImports...)
				childNode = subNode
				childNode.TypeName = getGoTypeName(field.MessageType, protoPkgName)
			default:
				childNode.IsPrimitive = true
				typeStr := "string" // default
				switch field.Typez {
				case api.INT32_TYPE, api.SINT32_TYPE, api.SFIXED32_TYPE:
					typeStr = "int32"
				case api.INT64_TYPE, api.SINT64_TYPE, api.SFIXED64_TYPE:
					typeStr = "int64"
				case api.UINT32_TYPE, api.FIXED32_TYPE:
					typeStr = "uint32"
				case api.UINT64_TYPE, api.FIXED64_TYPE:
					typeStr = "uint64"
				case api.BOOL_TYPE:
					typeStr = "bool"
				}
				childNode.Value = formatPrimitive(val, typeStr)
			}

			if childNode.IsPrimitive {
				node.IsPrimitive = true
				node.Value = childNode.Value
				slog.Info("Singular Primitive Node", "name", node.Name, "val", node.Value, "is_prim", node.IsPrimitive)
			} else if childNode.IsMessage {
				node.IsMessage = true
				node.TypeName = childNode.TypeName
				node.Children = childNode.Children
			}
		}
		if field.IsOneOf && field.Group != nil {
			typeName := toPascalCase(field.Name)
			// Check for collision with nested messages
			for _, nested := range msg.Messages {
				if nested.Name == typeName {
					typeName += "_"
					break
				}
			}
			wrapperTypeName := fmt.Sprintf("%s.%s_%s", protoPkgName, msg.Name, typeName)

			wrapperNode := &FieldNode{
				Name:      toPascalCase(field.Group.Name),
				TypeName:  wrapperTypeName,
				IsMessage: true,
				Children:  []*FieldNode{node},
			}
			root.Children = append(root.Children, wrapperNode)
		} else {
			root.Children = append(root.Children, node)
		}
	}

	return root, allImports, nil
}

func getGoTypeName(msg *api.Message, protoPkgName string) string {
	typeName := msg.Name
	parent := msg.Parent
	for parent != nil {
		if parent.Parent == nil && parent.ServicePlaceholder {
			break
		}
		typeName = parent.Name + "_" + typeName
		parent = parent.Parent
	}
	return fmt.Sprintf("%s.%s", protoPkgName, typeName)
}

func formatPrimitive(val any, typeName string) string {
	res := ""
	switch typeName {
	case "string":
		res = fmt.Sprintf("%q", val)
	case "int32", "int64":
		if v, ok := val.(float64); ok {
			res = fmt.Sprintf("%d", int64(v))
		} else {
			res = fmt.Sprintf("%v", val)
		}
	case "uint32", "uint64":
		if v, ok := val.(float64); ok {
			res = fmt.Sprintf("%d", uint64(v))
		} else {
			res = fmt.Sprintf("%v", val)
		}
	case "bool":
		res = fmt.Sprintf("%v", val)
	default:
		res = fmt.Sprintf("%v", val)
	}
	slog.Info("formatPrimitive", "val", val, "type", typeName, "res", res)
	return res
}

func toPascalCase(s string) string {
	// Very basic implementation, should use a library if available or robust logic
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func removeDuplicateStr(strSlice []string) []string {
	allKeys := make(map[string]bool)
	list := []string{}
	for _, item := range strSlice {
		if _, value := allKeys[item]; !value {
			allKeys[item] = true
			list = append(list, item)
		}
	}
	return list
}

// reduceServName removes redundant components from the service name.
// For example, FooServiceV2 -> Foo.
// The returned name is used as part of longer names, like FooClient.
// If the package name and the service name is the same,
// reduceServName returns empty string, so we get foo.Client instead of foo.FooClient.
func reduceServName(svc, pkg string) string {
	// remove trailing version
	if p := strings.LastIndexByte(svc, 'V'); p >= 0 {
		isVer := true
		for _, r := range svc[p+1:] {
			if !unicode.IsDigit(r) {
				isVer = false
				break
			}
		}
		if isVer {
			svc = svc[:p]
		}
	}

	svc = strings.TrimSuffix(svc, "Service")
	if strings.EqualFold(svc, pkg) {
		svc = ""
	}

	// This is a special case for IAM and should not be
	// extended to support any new API name containing
	// an acronym.
	//
	// In order to avoid a breaking change for IAM
	// clients, we must keep consistent identifier casing.
	if strings.Contains(svc, "IAM") {
		svc = strings.ReplaceAll(svc, "IAM", "Iam")
	}

	return svc
}

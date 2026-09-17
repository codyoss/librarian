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
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/googleapis/librarian/internal/serviceconfig"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func snakeToCamel(s string) string {
	var sb strings.Builder
	up := true
	for _, r := range s {
		if r == '_' {
			up = true
		} else if up && unicode.IsDigit(r) {
			sb.WriteRune('_')
			sb.WriteRune(r)
			up = false
		} else if up {
			sb.WriteRune(unicode.ToUpper(r))
			up = false
		} else {
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}

var (
	wellKnownStringTypes = []string{
		".google.protobuf.FieldMask",
		".google.protobuf.Timestamp",
		".google.protobuf.Duration",
		".google.protobuf.StringValue",
		".google.protobuf.BytesValue",
	}

	wellKnownTypeNames = []string{
		".google.protobuf.FieldMask",
		".google.protobuf.Timestamp",
		".google.protobuf.Duration",
		".google.protobuf.StringValue",
		".google.protobuf.BytesValue",
		".google.protobuf.Int64Value",
		".google.protobuf.UInt64Value",
		".google.protobuf.Int32Value",
		".google.protobuf.UInt32Value",
		".google.protobuf.FloatValue",
		".google.protobuf.DoubleValue",
		".google.protobuf.BoolValue",
		".google.protobuf.Value",
		".google.protobuf.ListValue",
	}
)

type httpInfo struct {
	verb string
	url  string
	body string
}

type heuristicTarget struct {
	Format     string
	FieldNames []string
}

func getHTTPInfo(m *descriptorpb.MethodDescriptorProto) *httpInfo {
	if m == nil || m.GetOptions() == nil {
		return nil
	}

	eHTTP := proto.GetExtension(m.GetOptions(), annotations.E_Http)
	if eHTTP == nil {
		return nil
	}
	httpRule, ok := eHTTP.(*annotations.HttpRule)
	if !ok || httpRule == nil {
		return nil
	}
	info := httpInfo{body: httpRule.GetBody()}

	switch httpRule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		info.verb = "GET"
		info.url = httpRule.GetGet()
	case *annotations.HttpRule_Post:
		info.verb = "POST"
		info.url = httpRule.GetPost()
	case *annotations.HttpRule_Patch:
		info.verb = "PATCH"
		info.url = httpRule.GetPatch()
	case *annotations.HttpRule_Put:
		info.verb = "PUT"
		info.url = httpRule.GetPut()
	case *annotations.HttpRule_Delete:
		info.verb = "DELETE"
		info.url = httpRule.GetDelete()
	}

	return &info
}

func parseImplicitRequestHeaders(m *descriptorpb.MethodDescriptorProto) [][]string {
	var matches [][]string
	if m == nil || m.GetOptions() == nil {
		return nil
	}

	eHTTP := proto.GetExtension(m.GetOptions(), annotations.E_Http)
	if eHTTP == nil {
		return nil
	}
	httpRule, ok := eHTTP.(*annotations.HttpRule)
	if !ok || httpRule == nil {
		return nil
	}
	rules := []*annotations.HttpRule{httpRule}
	rules = append(rules, httpRule.GetAdditionalBindings()...)

	for _, rule := range rules {
		pattern := ""
		switch rule.GetPattern().(type) {
		case *annotations.HttpRule_Get:
			pattern = rule.GetGet()
		case *annotations.HttpRule_Post:
			pattern = rule.GetPost()
		case *annotations.HttpRule_Patch:
			pattern = rule.GetPatch()
		case *annotations.HttpRule_Put:
			pattern = rule.GetPut()
		case *annotations.HttpRule_Delete:
			pattern = rule.GetDelete()
		}

		matches = append(matches, headerParamRegexp.FindAllStringSubmatch(pattern, -1)...)
	}

	return matches
}

func dynamicRequestHeadersExist(m *descriptorpb.MethodDescriptorProto) bool {
	if m == nil || m.GetOptions() == nil {
		return false
	}
	return proto.HasExtension(m.GetOptions(), annotations.E_Routing)
}

func parseDynamicRequestHeaders(m *descriptorpb.MethodDescriptorProto) [][]string {
	var matches [][]string
	if m == nil || m.GetOptions() == nil {
		return nil
	}
	reqHeaders := proto.GetExtension(m.GetOptions(), annotations.E_Routing)
	if reqHeaders == nil {
		return nil
	}
	routingRule, ok := reqHeaders.(*annotations.RoutingRule)
	if !ok || routingRule == nil {
		return nil
	}
	for _, param := range routingRule.GetRoutingParameters() {
		pathTemplateRegex := convertPathTemplateToRegex(param.GetPathTemplate())
		fieldReq := param.Field
		headerName := getHeaderName(param.GetPathTemplate())
		if len(headerName) < 1 {
			headerName = fieldReq
		}
		paramSlice := []string{pathTemplateRegex, fieldReq, headerName}
		matches = append(matches, paramSlice)
	}
	return matches
}

func convertPathTemplateToRegex(pattern string) string {
	if pattern == "" {
		return "(.*)"
	}
	regexPattern := strings.ReplaceAll(pattern, "{", "(?P<")
	regexPattern = strings.ReplaceAll(regexPattern, "}", ")")
	if !strings.Contains(pattern, "=") || !strings.Contains(pattern, "/") {
		regexPattern = strings.ReplaceAll(regexPattern, "*", "")
		regexPattern = strings.ReplaceAll(regexPattern, "=", "")
		regexPattern = strings.ReplaceAll(regexPattern, ")", ">.*)")
		return regexPattern
	}
	regexPattern = strings.ReplaceAll(regexPattern, "/**", "(?:/.*)?")
	regexPattern = strings.ReplaceAll(regexPattern, "/*", "/[^/]+")
	regexPattern = strings.ReplaceAll(regexPattern, "=**", ">.*")
	regexPattern = strings.ReplaceAll(regexPattern, "=*", ">[^/]+")
	regexPattern = strings.ReplaceAll(regexPattern, "=", ">")
	regexPattern = strings.ReplaceAll(regexPattern, "**", ".*")
	return regexPattern
}

func getHeaderName(pattern string) string {
	curlyBraceRegex := regexp.MustCompile(`{([^}]+)\}`)
	if strings.Count(pattern, "=") > 1 || !curlyBraceRegex.MatchString(pattern) {
		return ""
	}
	curlyBraceSegment := curlyBraceRegex.FindStringSubmatch(pattern)[1]
	if !strings.Contains(curlyBraceSegment, "=") {
		return curlyBraceSegment
	}
	return strings.Split(curlyBraceSegment, "=")[0]
}

func buildAccessor(field string, rawFinal bool) string {
	if field == "" {
		return ""
	}
	var ax strings.Builder
	split := strings.Split(field, ".")
	idx := len(split)
	if rawFinal {
		idx--
	}
	for _, s := range split[:idx] {
		fmt.Fprintf(&ax, ".Get%s()", snakeToCamel(s))
	}
	if rawFinal {
		fmt.Fprintf(&ax, ".%s", snakeToCamel(split[len(split)-1]))
	}
	return ax.String()
}

func fieldGetter(field string) string {
	return buildAccessor(field, false)
}

func resourceNameField(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo, dynamicRes bool) *heuristicTarget {
	if m == nil || m.GetInputType() == "" || descInfo == nil || descInfo.MessageDescriptors == nil {
		return nil
	}
	msg := descInfo.MessageDescriptors[m.GetInputType()]
	if msg == nil {
		return nil
	}

	var candidates []string
	for _, f := range msg.GetField() {
		if proto.HasExtension(f.GetOptions(), annotations.E_ResourceReference) {
			candidates = append(candidates, f.GetName())
		}
	}

	if len(candidates) == 0 {
		if !dynamicRes {
			return nil
		}
		if m.GetOptions() == nil {
			return nil
		}
		eHTTP := proto.GetExtension(m.GetOptions(), annotations.E_Http)
		if eHTTP == nil {
			return nil
		}
		h, ok := eHTTP.(*annotations.HttpRule)
		if !ok || h == nil {
			return nil
		}
		target, err := identifyHeuristicTarget(m, h, descInfo.Vocabulary)
		if err != nil || target == nil {
			return nil
		}
		return target
	}

	selected := candidates[0]
	if len(candidates) > 1 {
		pathParams := make(map[string]bool)
		headers := parseImplicitRequestHeaders(m)
		for _, h := range headers {
			if len(h) > 1 {
				pathParams[h[1]] = true
			}
		}

		for _, c := range candidates {
			if pathParams[c] {
				selected = c
				break
			}
		}
	}

	return &heuristicTarget{
		Format:     "%v",
		FieldNames: []string{selected},
	}
}

func isRequired(field *descriptorpb.FieldDescriptorProto) bool {
	if field == nil || field.GetOptions() == nil {
		return false
	}
	eBehav := proto.GetExtension(field.GetOptions(), annotations.E_FieldBehavior)
	if eBehav == nil {
		return false
	}
	behaviors, ok := eBehav.([]annotations.FieldBehavior)
	if !ok {
		return false
	}
	return slices.Contains(behaviors, annotations.FieldBehavior_REQUIRED)
}

func lookupField(msg *descriptorpb.DescriptorProto, field string, descInfo *DescriptorInfo) *descriptorpb.FieldDescriptorProto {
	if msg == nil {
		return nil
	}
	currMsg := msg
	var desc *descriptorpb.FieldDescriptorProto
	for seg := range strings.SplitSeq(field, ".") {
		found := false
		for _, f := range currMsg.GetField() {
			if f.GetName() == seg {
				desc = f
				found = true
				if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && descInfo != nil && descInfo.MessageDescriptors != nil {
					if sub, ok := descInfo.MessageDescriptors[f.GetTypeName()]; ok {
						currMsg = sub
					}
				}
				break
			}
		}
		if !found {
			return nil
		}
	}
	return desc
}

func getLeafs(msg *descriptorpb.DescriptorProto, descInfo *DescriptorInfo, excludedFields ...*descriptorpb.FieldDescriptorProto) map[string]*descriptorpb.FieldDescriptorProto {
	pathsToLeafs := map[string]*descriptorpb.FieldDescriptorProto{}
	if msg == nil {
		return pathsToLeafs
	}

	visitedMsgs := make(map[string]bool)
	var recurse func([]*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto, int)

	handleLeaf := func(field *descriptorpb.FieldDescriptorProto, stack []*descriptorpb.FieldDescriptorProto) {
		elts := []string{}
		for _, f := range stack {
			elts = append(elts, f.GetName())
		}
		elts = append(elts, field.GetName())
		key := strings.Join(elts, ".")
		pathsToLeafs[key] = field
	}

	handleMsg := func(field *descriptorpb.FieldDescriptorProto, stack []*descriptorpb.FieldDescriptorProto, depth int) {
		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			return
		}
		if slices.Contains(excludedFields, field) {
			return
		}
		if slices.Contains(stack, field) {
			return
		}
		if depth > 8 {
			return
		}
		typeName := field.GetTypeName()
		if visitedMsgs[typeName] {
			return
		}
		visitedMsgs[typeName] = true
		defer func() { visitedMsgs[typeName] = false }()

		if descInfo != nil && descInfo.MessageDescriptors != nil {
			if subMsg, ok := descInfo.MessageDescriptors[typeName]; ok {
				recurse(append(stack, field), subMsg, depth+1)
			}
		}
	}

	recurse = func(stack []*descriptorpb.FieldDescriptorProto, m *descriptorpb.DescriptorProto, depth int) {
		if m == nil || depth > 8 {
			return
		}
		for _, field := range m.GetField() {
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && !slices.Contains(wellKnownTypeNames, field.GetTypeName()) {
				handleMsg(field, stack, depth)
			} else {
				handleLeaf(field, stack)
			}
		}
	}

	recurse([]*descriptorpb.FieldDescriptorProto{}, msg, 0)
	return pathsToLeafs
}

func queryParams(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo) map[string]*descriptorpb.FieldDescriptorProto {
	res := map[string]*descriptorpb.FieldDescriptorProto{}
	if m == nil || descInfo == nil || descInfo.MessageDescriptors == nil {
		return res
	}
	info := getHTTPInfo(m)
	if info == nil {
		info = &httpInfo{}
	}
	if info.body == "*" {
		return res
	}

	pathParams := make(map[string]bool)
	for _, p := range httpPatternVarRegex.FindAllStringSubmatch(info.url, -1) {
		param, _, _ := strings.Cut(p[1], "=")
		pathParams[param] = true
	}
	if info.body != "" {
		pathParams[info.body] = true
	}

	request := descInfo.MessageDescriptors[m.GetInputType()]
	if request == nil {
		return res
	}

	bodyField := lookupField(request, info.body, descInfo)
	var excluded []*descriptorpb.FieldDescriptorProto
	if bodyField != nil {
		excluded = append(excluded, bodyField)
	}

	pathToLeaf := getLeafs(request, descInfo, excluded...)
	for path, leaf := range pathToLeaf {
		if !pathParams[path] {
			res[path] = leaf
		}
	}

	return res
}

func generateQueryStringCode(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo, retErr string, restNumericEnum bool) string {
	qp := queryParams(m, descInfo)
	fields := make([]string, 0, len(qp))
	for p := range qp {
		fields = append(fields, p)
	}
	sort.Strings(fields)

	if !restNumericEnum && len(fields) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\tparams := url.Values{}\n")
	if restNumericEnum {
		sb.WriteString("\tparams.Add(\"$alt\", \"json;enum-encoding=int\")\n")
	}

	for _, path := range fields {
		field := qp[path]
		required := isRequired(field)
		accessor := fieldGetter(path)
		singularPrimitive := field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE &&
			field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_BYTES &&
			field.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		key := lowerFirst(snakeToCamel(path))

		var paramAdd string
		if slices.Contains(wellKnownTypeNames, field.GetTypeName()) {
			var b strings.Builder
			fmt.Fprintf(&b, "\tfield, err := protojson.Marshal(req%s)\n", accessor)
			b.WriteString("\tif err != nil {\n")
			fmt.Fprintf(&b, "\t\t%s\n", retErr)
			b.WriteString("\t}\n")
			if slices.Contains(wellKnownStringTypes, field.GetTypeName()) {
				fmt.Fprintf(&b, "\tparams.Add(%q, string(field[1:len(field)-1]))\n", key)
			} else {
				fmt.Fprintf(&b, "\tparams.Add(%q, string(field))\n", key)
			}
			paramAdd = b.String()
		} else {
			paramAdd = fmt.Sprintf("\tparams.Add(%q, fmt.Sprintf(\"%%v\", req%s))\n", key, accessor)
		}

		if required && singularPrimitive {
			sb.WriteString(paramAdd)
			continue
		}

		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			fmt.Fprintf(&sb, "\tif items := req%s; len(items) > 0 {\n", accessor)
			sb.WriteString("\t\tfor _, item := range items {\n")
			fmt.Fprintf(&sb, "\t\t\tparams.Add(%q, fmt.Sprintf(\"%%v\", item))\n", key)
			sb.WriteString("\t\t}\n")
			sb.WriteString("\t}\n")
		} else if field.GetProto3Optional() {
			toks := strings.Split(path, ".")
			toks = toks[:len(toks)-1]
			parentField := fieldGetter(strings.Join(toks, "."))
			directLeafField := buildAccessor(path, true)
			fmt.Fprintf(&sb, "\tif req%s != nil && req%s != nil {\n", parentField, directLeafField)
			for line := range strings.SplitSeq(strings.TrimSuffix(paramAdd, "\n"), "\n") {
				sb.WriteString("\t" + line + "\n")
			}
			sb.WriteString("\t}\n")
		} else {
			switch field.GetType() {
			case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, descriptorpb.FieldDescriptorProto_TYPE_BYTES:
				fmt.Fprintf(&sb, "\tif req%s != nil {\n", accessor)
			case descriptorpb.FieldDescriptorProto_TYPE_STRING:
				fmt.Fprintf(&sb, "\tif req%s != \"\" {\n", accessor)
			case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
				fmt.Fprintf(&sb, "\tif req%s {\n", accessor)
			default:
				fmt.Fprintf(&sb, "\tif req%s != 0 {\n", accessor)
			}
			for line := range strings.SplitSeq(strings.TrimSuffix(paramAdd, "\n"), "\n") {
				sb.WriteString("\t" + line + "\n")
			}
			sb.WriteString("\t}\n")
		}
	}

	sb.WriteString("\n\tbaseUrl.RawQuery = params.Encode()\n")
	return sb.String()
}

func generateBaseURLCode(urlStr string, retErr string) string {
	if urlStr != "" && !strings.HasPrefix(urlStr, "/") {
		urlStr = "/" + urlStr
	}
	fmtStr := urlStr
	fmtStr = httpPatternVarRegex.ReplaceAllStringFunc(fmtStr, func(s string) string { return "%v" })

	var sb strings.Builder
	sb.WriteString("\tbaseUrl, err := url.Parse(c.endpoint)\n")
	sb.WriteString("\tif err != nil {\n")
	fmt.Fprintf(&sb, "\t\t%s\n", retErr)
	sb.WriteString("\t}\n")

	tokens := []string{fmt.Sprintf("%q", fmtStr)}
	for _, path := range httpPatternVarRegex.FindAllStringSubmatch(urlStr, -1) {
		param, _, _ := strings.Cut(path[1], "=")
		tokens = append(tokens, fmt.Sprintf("req%s", fieldGetter(param)))
	}
	fmt.Fprintf(&sb, "\tbaseUrl.Path += fmt.Sprintf(%s)\n", strings.Join(tokens, ", "))
	return sb.String()
}

func getOperationPathOverride(svcConfig *serviceconfig.Service, protoPkg string) string {
	if svcConfig != nil && svcConfig.GetHttp() != nil {
		for _, rule := range svcConfig.GetHttp().GetRules() {
			if rule.GetSelector() == "google.longrunning.Operations.GetOperation" {
				url := rule.GetGet()
				return httpPatternVarRegex.ReplaceAllStringFunc(url, func(s string) string { return "%s" })
			}
		}
	}
	ver := "v1"
	if parts := strings.Split(protoPkg, "."); len(parts) > 0 {
		last := parts[len(parts)-1]
		if strings.HasPrefix(last, "v") {
			ver = last
		}
	}
	return "/" + ver + "/%s"
}

func generateGRPCMethod(m *api.Method, sAnn *ServiceAnnotation, mAnn *MethodAnnotation, descInfo *DescriptorInfo) string {
	var sb strings.Builder

	mProto := lookupMethodDescriptor(m, descInfo)
	grpcStub := resolveGRPCStub(m, sAnn)

	goMethodName := m.Name
	if mAnn != nil && mAnn.GoMethodName != "" {
		goMethodName = mAnn.GoMethodName
	}

	if mAnn.IsBidiStream || mAnn.IsClientStream {
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, opts ...gax.CallOption) (%s, error) {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.StreamClientType)
		sb.WriteString("\tctx = gax.InsertMetadataIntoOutgoingContext(ctx, c.xGoogHeaders...)\n")
		appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, false)
		fmt.Fprintf(&sb, "\tvar resp %s\n", mAnn.StreamClientType)
		fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
			m.Name, m.Name, m.Name)
		sb.WriteString("\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\tc.logger.DebugContext(ctx, \"api streaming client request\", \"serviceName\", serviceName, \"rpcName\", %q)\n", m.Name)
		fmt.Fprintf(&sb, "\t\tresp, err = %s.%s(ctx, settings.GRPC...)\n", grpcStub, m.Name)
		fmt.Fprintf(&sb, "\t\tc.logger.DebugContext(ctx, \"api streaming client response\", \"serviceName\", serviceName, \"rpcName\", %q)\n", m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\tif err != nil {\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\treturn resp, nil\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	if mAnn.IsServerStream {
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (%s, error) {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.RequestType, mAnn.StreamClientType)
		appendRoutingHeadersGRPC(&sb, m, mProto, sAnn)
		appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, false)
		fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
			m.Name, m.Name, m.Name)
		fmt.Fprintf(&sb, "\tvar resp %s\n", mAnn.StreamClientType)
		sb.WriteString("\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\tc.logger.DebugContext(ctx, \"api streaming client request\", \"serviceName\", serviceName, \"rpcName\", %q)\n", m.Name)
		fmt.Fprintf(&sb, "\t\tresp, err = %s.%s(ctx, req, settings.GRPC...)\n", grpcStub, m.Name)
		fmt.Fprintf(&sb, "\t\tc.logger.DebugContext(ctx, \"api streaming client response\", \"serviceName\", serviceName, \"rpcName\", %q)\n", m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\tif err != nil {\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\treturn resp, nil\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	// Signature
	switch {
	case mAnn.IsEmpty:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) error {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.RequestType)
	case mAnn.IsPaged:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) *%s {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.RequestType, mAnn.IteratorType)
	case mAnn.IsLRO:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.RequestType, mAnn.OperationType)
	default:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.GRPCClientName, goMethodName, mAnn.RequestType, mAnn.ResponseType)
	}

	// Routing headers
	appendRoutingHeadersGRPC(&sb, m, mProto, sAnn)

	// OpenTelemetry Telemetry Context
	appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, false)

	if !mAnn.IsPaged && !mAnn.IsServerStream {
		for _, apf := range mAnn.AutoPopulatedFields {
			if apf.IsOptional {
				fmt.Fprintf(&sb, "\tif req != nil && req.Get%s() == \"\" {\n\t\treq.%s = proto.String(uuid.NewString())\n\t}\n", apf.FieldName, apf.FieldName)
			} else {
				fmt.Fprintf(&sb, "\tif req != nil && req.Get%s() == \"\" {\n\t\treq.%s = uuid.NewString()\n\t}\n", apf.FieldName, apf.FieldName)
			}
		}
	}

	// Call options
	fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
		m.Name, m.Name, m.Name)

	// Execution

	switch {
	case mAnn.IsEmpty:
		sb.WriteString("\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\t_, err = executeRPC(ctx, %s.%s, req, settings.GRPC, c.logger, %q)\n",
			grpcStub, m.Name, m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\treturn err\n")
		sb.WriteString("}\n")

	case mAnn.IsPaged:
		fmt.Fprintf(&sb, "\tit := &%s{}\n", mAnn.IteratorType)
		sb.WriteString("\treq = proto.CloneOf(req)\n")
		elemType := mAnn.ElemType
		if elemType == "" {
			elemType = "*" + mAnn.ResponseType
		}
		itemsField := mAnn.ItemsField
		if itemsField == "" {
			itemsField = mAnn.IteratorType
		}
		fmt.Fprintf(&sb, "\tit.InternalFetch = func(pageSize int, pageToken string) ([]%s, string, error) {\n", elemType)
		fmt.Fprintf(&sb, "\t\tresp := &%s{}\n", mAnn.ResponseType)
		sb.WriteString("\t\tif pageToken != \"\" {\n")
		if mAnn.PageTokenOptional {
			sb.WriteString("\t\t\treq.PageToken = proto.String(pageToken)\n")
		} else {
			sb.WriteString("\t\t\treq.PageToken = pageToken\n")
		}
		sb.WriteString("\t\t}\n")
		pageSizeFieldName := mAnn.PageSizeFieldName
		if pageSizeFieldName == "" {
			pageSizeFieldName = "PageSize"
		}
		if mAnn.PageSizeIsWrapper {
			if mAnn.PageSizeWrapperType == "UInt32Value" {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.UInt32Value{Value: uint32(math.MaxInt32)}\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.UInt32Value{Value: uint32(pageSize)}\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.Int32Value{Value: math.MaxInt32}\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.Int32Value{Value: int32(pageSize)}\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		} else if mAnn.PageSizeIsUint32 {
			if mAnn.PageSizeIsOptional {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Uint32(uint32(math.MaxInt32))\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Uint32(uint32(pageSize))\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = uint32(math.MaxInt32)\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = uint32(pageSize)\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		} else {
			if mAnn.PageSizeIsOptional {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Int32(int32(math.MaxInt32))\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Int32(int32(pageSize))\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = math.MaxInt32\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = int32(pageSize)\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		}
		sb.WriteString("\t\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\t\tresp, err = executeRPC(ctx, %s.%s, req, settings.GRPC, c.logger, %q)\n",
			grpcStub, m.Name, m.Name)
		sb.WriteString("\t\t\treturn err\n")
		sb.WriteString("\t\t}, opts...)\n")
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn nil, \"\", err\n")
		sb.WriteString("\t\t}\n\n")
		sb.WriteString("\t\tit.Response = resp\n")
		fmt.Fprintf(&sb, "\t\treturn resp.Get%s(), resp.GetNextPageToken(), nil\n", itemsField)
		sb.WriteString("\t}\n")
		sb.WriteString("\tfetch := func(pageSize int, pageToken string) (string, error) {\n")
		sb.WriteString("\t\titems, nextPageToken, err := it.InternalFetch(pageSize, pageToken)\n")
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn \"\", err\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\tit.items = append(it.items, items...)\n")
		sb.WriteString("\t\treturn nextPageToken, nil\n")
		sb.WriteString("\t}\n\n")
		sb.WriteString("\tit.pageInfo, it.nextFunc = iterator.NewPageInfo(fetch, it.bufLen, it.takeBuf)\n")
		if mAnn.PageSizeIsWrapper {
			fmt.Fprintf(&sb, "\tif psVal := req.Get%s(); psVal != nil {\n", pageSizeFieldName)
			sb.WriteString("\t\tit.pageInfo.MaxSize = int(psVal.GetValue())\n")
			sb.WriteString("\t}\n")
		} else {
			fmt.Fprintf(&sb, "\tit.pageInfo.MaxSize = int(req.Get%s())\n", pageSizeFieldName)
		}
		sb.WriteString("\tit.pageInfo.Token = req.GetPageToken()\n\n")
		sb.WriteString("\treturn it\n")
		sb.WriteString("}\n")

	case mAnn.IsLRO:
		sb.WriteString("\tvar resp *longrunningpb.Operation\n")
		sb.WriteString("\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\tresp, err = executeRPC(ctx, %s.%s, req, settings.GRPC, c.logger, %q)\n",
			grpcStub, m.Name, m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\tif err != nil {\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		fmt.Fprintf(&sb, "\tlro := longrunning.InternalNewOperationWithMetadata(*c.LROClient, resp, \"*%s.%s\")\n",
			sAnn.PackageName, mAnn.OperationType)
		sb.WriteString("\tif gax.IsFeatureEnabled(\"TRACING\") {\n")
		sb.WriteString("\t\tlro.SetParentSpanContext(trace.SpanContextFromContext(ctx))\n")
		sb.WriteString("\t}\n")
		fmt.Fprintf(&sb, "\treturn &%s{\n\t\tlro: lro,\n\t}, nil\n", mAnn.OperationType)
		sb.WriteString("}\n")

	default:
		fmt.Fprintf(&sb, "\tvar resp *%s\n", mAnn.ResponseType)
		sb.WriteString("\terr := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tvar err error\n")
		fmt.Fprintf(&sb, "\t\tresp, err = executeRPC(ctx, %s.%s, req, settings.GRPC, c.logger, %q)\n",
			grpcStub, m.Name, m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\tif err != nil {\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\treturn resp, nil\n")
		sb.WriteString("}\n")
	}

	return sb.String()
}

func generateRESTMethod(m *api.Method, sAnn *ServiceAnnotation, mAnn *MethodAnnotation, descInfo *DescriptorInfo) string {
	if !sAnn.HasREST {
		return ""
	}
	mProto := lookupMethodDescriptor(m, descInfo)
	httpInf := getHTTPInfo(mProto)
	if httpInf == nil && (m.PathInfo == nil || len(m.PathInfo.Bindings) == 0) {
		if mAnn.IsServerStream {
			return ""
		}
	}

	var sb strings.Builder

	var verb, urlStr, bodyField string
	if httpInf != nil {
		verb = httpInf.verb
		urlStr = httpInf.url
		bodyField = httpInf.body
	} else if m.PathInfo != nil && len(m.PathInfo.Bindings) > 0 {
		b := m.PathInfo.Bindings[0]
		verb = strings.ToUpper(b.Verb)
		if b.PathTemplate != nil {
			urlStr = b.PathTemplate.FlatPath()
		}
		bodyField = m.PathInfo.BodyFieldPath
	}

	retErr := "return nil, err"
	if mAnn.IsEmpty {
		retErr = "return err"
	} else if mAnn.IsPaged {
		retErr = "return nil, \"\", err"
	}

	restNumericEnum := false
	if sAnn.Model != nil && sAnn.Model.RESTNumericEnums {
		restNumericEnum = true
	}

	// Doc comment
	if mAnn.Doc != "" {
		sb.WriteString(mAnn.Doc + "\n")
	}

	goMethodName := m.Name
	if mAnn != nil && mAnn.GoMethodName != "" {
		goMethodName = mAnn.GoMethodName
	}

	if mAnn.IsBidiStream || mAnn.IsClientStream {
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, opts ...gax.CallOption) (%s, error) {\n",
			sAnn.RESTClientName, goMethodName, mAnn.StreamClientType)
		fmt.Fprintf(&sb, "\treturn nil, errors.New(%q)\n", fmt.Sprintf("%s not yet supported for REST clients", m.Name))
		sb.WriteString("}\n")
		return sb.String()
	}

	// Signature
	switch {
	case mAnn.IsEmpty:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) error {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType)
	case mAnn.IsPaged:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) *%s {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType, mAnn.IteratorType)
	case mAnn.IsLRO:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType, mAnn.OperationType)
	case mAnn.IsCustomOp:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*Operation, error) {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType)
	case mAnn.IsServerStream:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (%s, error) {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType, mAnn.StreamClientType)
	default:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.RESTClientName, goMethodName, mAnn.RequestType, mAnn.ResponseType)
	}

	if !mAnn.IsPaged && !mAnn.IsServerStream {
		for _, apf := range mAnn.AutoPopulatedFields {
			if apf.IsOptional {
				fmt.Fprintf(&sb, "\tif req != nil && req.Get%s() == \"\" {\n\t\treq.%s = proto.String(uuid.NewString())\n\t}\n", apf.FieldName, apf.FieldName)
			} else {
				fmt.Fprintf(&sb, "\tif req != nil && req.Get%s() == \"\" {\n\t\treq.%s = uuid.NewString()\n\t}\n", apf.FieldName, apf.FieldName)
			}
		}
	}

	// Paged REST method structure
	if mAnn.IsPaged {
		fmt.Fprintf(&sb, "\tit := &%s{}\n", mAnn.IteratorType)
		sb.WriteString("\treq = proto.CloneOf(req)\n")
		hasBody := bodyField != "" && verb != "GET" && verb != "DELETE"
		if hasBody {
			if sAnn.Model != nil && sAnn.Model.DIREGAPIC {
				sb.WriteString("\tm := protojson.MarshalOptions{AllowPartial: true}\n")
			} else {
				sb.WriteString("\tm := protojson.MarshalOptions{AllowPartial: true, UseEnumNumbers: true}\n")
			}
		}
		sb.WriteString("\tunm := protojson.UnmarshalOptions{AllowPartial: true, DiscardUnknown: true}\n")
		elemType := mAnn.ElemType
		if elemType == "" {
			elemType = "*" + mAnn.ResponseType
		}
		itemsField := mAnn.ItemsField
		if itemsField == "" {
			itemsField = mAnn.IteratorType
		}
		fmt.Fprintf(&sb, "\tit.InternalFetch = func(pageSize int, pageToken string) ([]%s, string, error) {\n", elemType)
		fmt.Fprintf(&sb, "\t\tresp := &%s{}\n", mAnn.ResponseType)
		if mAnn.PageTokenOptional {
			sb.WriteString("\t\tif pageToken != \"\" {\n")
			sb.WriteString("\t\t\treq.PageToken = proto.String(pageToken)\n")
			sb.WriteString("\t\t}\n")
		} else {
			sb.WriteString("\t\tif pageToken != \"\" {\n")
			sb.WriteString("\t\t\treq.PageToken = pageToken\n")
			sb.WriteString("\t\t}\n")
		}
		pageSizeFieldName := mAnn.PageSizeFieldName
		if pageSizeFieldName == "" {
			pageSizeFieldName = "PageSize"
		}
		if mAnn.PageSizeIsWrapper {
			if mAnn.PageSizeWrapperType == "UInt32Value" {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.UInt32Value{Value: uint32(math.MaxInt32)}\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.UInt32Value{Value: uint32(pageSize)}\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.Int32Value{Value: math.MaxInt32}\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = &wrapperspb.Int32Value{Value: int32(pageSize)}\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		} else if mAnn.PageSizeIsUint32 {
			if mAnn.PageSizeIsOptional {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Uint32(uint32(math.MaxInt32))\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Uint32(uint32(pageSize))\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = uint32(math.MaxInt32)\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = uint32(pageSize)\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		} else {
			if mAnn.PageSizeIsOptional {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Int32(int32(math.MaxInt32))\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = proto.Int32(int32(pageSize))\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			} else {
				sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = math.MaxInt32\n", pageSizeFieldName)
				sb.WriteString("\t\t} else if pageSize != 0 {\n")
				fmt.Fprintf(&sb, "\t\t\treq.%s = int32(pageSize)\n", pageSizeFieldName)
				sb.WriteString("\t\t}\n")
			}
		}

		bodyReader := "nil"
		logBody := "nil"
		if hasBody {
			sb.WriteString("\t\tjsonReq, err := m.Marshal(req)\n")
			sb.WriteString("\t\tif err != nil {\n")
			sb.WriteString("\t\t\treturn nil, \"\", err\n")
			sb.WriteString("\t\t}\n\n")
			bodyReader = "bytes.NewReader(jsonReq)"
			logBody = "jsonReq"
		}

		// URL and Query
		baseURLCode := generateBaseURLCode(urlStr, "\treturn nil, \"\", err")
		sb.WriteString(indentCode(baseURLCode, 1))
		sb.WriteString("\n")

		queryCode := generateQueryStringCode(mProto, descInfo, "\treturn nil, \"\", err", restNumericEnum)
		if queryCode != "" {
			sb.WriteString(indentCode(queryCode, 1))
			sb.WriteString("\n")
		}

		sb.WriteString("\t\t// Build HTTP headers from client and context metadata.\n")
		sb.WriteString("\t\thds := append(c.xGoogHeaders, \"Content-Type\", \"application/json\")\n")
		sb.WriteString("\t\theaders := gax.BuildHeaders(ctx, hds...)\n")
		sb.WriteString("\t\te := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\t\tif settings.Path != \"\" {\n")
		sb.WriteString("\t\t\t\tbaseUrl.Path = settings.Path\n")
		sb.WriteString("\t\t\t}\n")
		fmt.Fprintf(&sb, "\t\t\thttpReq, err := http.NewRequest(%q, baseUrl.String(), %s)\n", verb, bodyReader)
		sb.WriteString("\t\t\tif err != nil {\n")
		sb.WriteString("\t\t\t\treturn err\n")
		sb.WriteString("\t\t\t}\n")
		sb.WriteString("\t\t\thttpReq.Header = headers\n\n")
		fmt.Fprintf(&sb, "\t\t\tbuf, err := executeHTTPRequest(ctx, c.httpClient, httpReq, c.logger, %s, %q)\n", logBody, m.Name)
		sb.WriteString("\t\t\tif err != nil {\n")
		sb.WriteString("\t\t\t\treturn err\n")
		sb.WriteString("\t\t\t}\n")
		sb.WriteString("\t\t\tif err := unm.Unmarshal(buf, resp); err != nil {\n")
		sb.WriteString("\t\t\t\treturn err\n")
		sb.WriteString("\t\t\t}\n\n")
		sb.WriteString("\t\t\treturn nil\n")
		sb.WriteString("\t\t}, opts...)\n")
		sb.WriteString("\t\tif e != nil {\n")
		sb.WriteString("\t\t\treturn nil, \"\", e\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\tit.Response = resp\n")
		if mAnn.IsMapPagination {
			sb.WriteString("\n")
			fmt.Fprintf(&sb, "\t\telems := make([]%s, 0, len(resp.Get%s()))\n", elemType, itemsField)
			fmt.Fprintf(&sb, "\t\tfor k, v := range resp.Get%s() {\n", itemsField)
			fmt.Fprintf(&sb, "\t\t\telems = append(elems, %s{k, v})\n", elemType)
			sb.WriteString("\t\t}\n")
			sb.WriteString("\t\tsort.Slice(elems, func(i, j int) bool { return elems[i].Key < elems[j].Key })\n\n")
			sb.WriteString("\t\treturn elems, resp.GetNextPageToken(), nil\n")
		} else {
			fmt.Fprintf(&sb, "\t\treturn resp.Get%s(), resp.GetNextPageToken(), nil\n", itemsField)
		}
		sb.WriteString("\t}\n\n")

		sb.WriteString("\tfetch := func(pageSize int, pageToken string) (string, error) {\n")
		sb.WriteString("\t\titems, nextPageToken, err := it.InternalFetch(pageSize, pageToken)\n")
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn \"\", err\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\tit.items = append(it.items, items...)\n")
		sb.WriteString("\t\treturn nextPageToken, nil\n")
		sb.WriteString("\t}\n\n")

		sb.WriteString("\tit.pageInfo, it.nextFunc = iterator.NewPageInfo(fetch, it.bufLen, it.takeBuf)\n")
		if mAnn.PageSizeIsWrapper {
			fmt.Fprintf(&sb, "\tif psVal := req.Get%s(); psVal != nil {\n", pageSizeFieldName)
			sb.WriteString("\t\tit.pageInfo.MaxSize = int(psVal.GetValue())\n")
			sb.WriteString("\t}\n")
		} else {
			fmt.Fprintf(&sb, "\tit.pageInfo.MaxSize = int(req.Get%s())\n", pageSizeFieldName)
		}
		sb.WriteString("\tit.pageInfo.Token = req.GetPageToken()\n\n")
		sb.WriteString("\treturn it\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	// Body serialization
	bodyReader := "nil"
	logBody := "nil"
	if bodyField != "" && verb != "GET" && verb != "DELETE" {
		if sAnn.Model != nil && sAnn.Model.DIREGAPIC {
			sb.WriteString("\tm := protojson.MarshalOptions{AllowPartial: true}\n")
		} else {
			sb.WriteString("\tm := protojson.MarshalOptions{AllowPartial: true, UseEnumNumbers: true}\n")
		}
		requestObj := "req"
		if bodyField != "*" {
			requestObj = "body"
			fmt.Fprintf(&sb, "\tbody := req%s\n", fieldGetter(bodyField))
		}
		fmt.Fprintf(&sb, "\tjsonReq, err := m.Marshal(%s)\n", requestObj)
		sb.WriteString("\tif err != nil {\n")
		fmt.Fprintf(&sb, "\t\t%s\n", retErr)
		sb.WriteString("\t}\n\n")

		bodyReader = "bytes.NewReader(jsonReq)"
		logBody = "jsonReq"
	}

	// Base URL
	sb.WriteString(generateBaseURLCode(urlStr, retErr))
	sb.WriteString("\n")

	// Query params
	queryCode := generateQueryStringCode(mProto, descInfo, retErr, restNumericEnum)
	if queryCode != "" {
		sb.WriteString(queryCode)
		sb.WriteString("\n")
	}

	// Headers
	sb.WriteString("\t// Build HTTP headers from client and context metadata.\n")
	appendRoutingHeadersREST(&sb, m, mProto, sAnn)

	// Telemetry
	appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, true)

	// Call options (only for Unary and CustomOp)
	if mAnn.IsUnary || mAnn.IsCustomOp {
		fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
			m.Name, m.Name, m.Name)
	}

	// Execution
	if mAnn.IsServerStream {
		streamClient := fmt.Sprintf("%sRESTStreamClient", lowerFirst(m.Name))
		fmt.Fprintf(&sb, "\tvar streamClient *%s\n", streamClient)
		sb.WriteString("\te := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tif settings.Path != \"\" {\n")
		sb.WriteString("\t\t\tbaseUrl.Path = settings.Path\n")
		sb.WriteString("\t\t}\n")
		fmt.Fprintf(&sb, "\t\thttpReq, err := http.NewRequest(%q, baseUrl.String(), %s)\n", verb, bodyReader)
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn err\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\thttpReq = httpReq.WithContext(ctx)\n")
		sb.WriteString("\t\thttpReq.Header = headers\n\n")
		fmt.Fprintf(&sb, "\t\thttpRsp, err := executeStreamingHTTPRequest(ctx, c.httpClient, httpReq, c.logger, %s, %q)\n", logBody, m.Name)
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn err\n")
		sb.WriteString("\t\t}\n\n")
		fmt.Fprintf(&sb, "\t\tstreamClient = &%s{\n", streamClient)
		sb.WriteString("\t\t\tctx:    ctx,\n")
		sb.WriteString("\t\t\tmd:     metadata.MD(httpRsp.Header),\n")
		fmt.Fprintf(&sb, "\t\t\tstream: gax.NewProtoJSONStreamReader(httpRsp.Body, (&%s{}).ProtoReflect().Type()),\n", mAnn.ResponseType)
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\treturn nil\n")
		sb.WriteString("\t}, opts...)\n\n")
		sb.WriteString("\treturn streamClient, e\n")
		sb.WriteString("}\n\n")

		fmt.Fprintf(&sb, "// %s is the stream client used to consume the server stream created by\n", streamClient)
		fmt.Fprintf(&sb, "// the REST implementation of %s.\n", m.Name)
		fmt.Fprintf(&sb, "type %s struct {\n", streamClient)
		sb.WriteString("\tctx    context.Context\n")
		sb.WriteString("\tmd     metadata.MD\n")
		sb.WriteString("\tstream *gax.ProtoJSONStream\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) Recv() (*%s, error) {\n", streamClient, mAnn.ResponseType)
		sb.WriteString("\tif err := c.ctx.Err(); err != nil {\n")
		sb.WriteString("\t\tdefer c.stream.Close()\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\tmsg, err := c.stream.Recv()\n")
		sb.WriteString("\tif err != nil {\n")
		sb.WriteString("\t\tdefer c.stream.Close()\n")
		sb.WriteString("\t\treturn nil, err\n")
		sb.WriteString("\t}\n")
		fmt.Fprintf(&sb, "\tres := msg.(*%s)\n", mAnn.ResponseType)
		sb.WriteString("\treturn res, nil\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) Header() (metadata.MD, error) {\n", streamClient)
		sb.WriteString("\treturn c.md, nil\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) Trailer() metadata.MD {\n", streamClient)
		sb.WriteString("\treturn c.md\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) CloseSend() error {\n", streamClient)
		sb.WriteString("\t// This is a no-op to fulfill the interface.\n")
		sb.WriteString("\treturn errors.New(\"this method is not implemented for a server-stream\")\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) Context() context.Context {\n", streamClient)
		sb.WriteString("\treturn c.ctx\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) SendMsg(m interface{}) error {\n", streamClient)
		sb.WriteString("\t// This is a no-op to fulfill the interface.\n")
		sb.WriteString("\treturn errors.New(\"this method is not implemented for a server-stream\")\n")
		sb.WriteString("}\n\n")
		fmt.Fprintf(&sb, "func (c *%s) RecvMsg(m interface{}) error {\n", streamClient)
		sb.WriteString("\t// This is a no-op to fulfill the interface.\n")
		sb.WriteString("\treturn errors.New(\"this method is not implemented, use Recv\")\n")
		sb.WriteString("}\n")

		return sb.String()
	}

	isHTTPBody := m.OutputTypeID == ".google.api.HttpBody" || m.OutputTypeID == "google.api.HttpBody" || mAnn.ResponseType == "httpbodypb.HttpBody"
	if isHTTPBody {
		fmt.Fprintf(&sb, "\tresp := &%s{}\n", mAnn.ResponseType)
		sb.WriteString("\te := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\tif settings.Path != \"\" {\n")
		sb.WriteString("\t\t\tbaseUrl.Path = settings.Path\n")
		sb.WriteString("\t\t}\n")
		fmt.Fprintf(&sb, "\t\thttpReq, err := http.NewRequest(%q, baseUrl.String(), %s)\n", verb, bodyReader)
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn err\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\thttpReq = httpReq.WithContext(ctx)\n")
		sb.WriteString("\t\thttpReq.Header = headers\n\n")

		fmt.Fprintf(&sb, "\t\tbuf, httpRsp, err := executeHTTPRequestWithResponse(ctx, c.httpClient, httpReq, c.logger, %s, %q)\n", logBody, m.Name)
		sb.WriteString("\t\tif err != nil {\n")
		sb.WriteString("\t\t\treturn err\n")
		sb.WriteString("\t\t}\n\n")

		sb.WriteString("\t\tresp.Data = buf\n")
		sb.WriteString("\t\tif headers := httpRsp.Header; len(headers[\"Content-Type\"]) > 0 {\n")
		sb.WriteString("\t\t\tresp.ContentType = headers[\"Content-Type\"][0]\n")
		sb.WriteString("\t\t}\n\n")

		sb.WriteString("\t\treturn nil\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("\tif e != nil {\n")
		sb.WriteString("\t\treturn nil, e\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\treturn resp, nil\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	// Execution
	if !mAnn.IsEmpty {
		respType := mAnn.ResponseType
		if mAnn.IsLRO {
			respType = "longrunningpb.Operation"
		} else if mAnn.IsCustomOp && sAnn.Model != nil && sAnn.Model.CustomOp != nil {
			respType = strings.TrimPrefix(sAnn.Model.CustomOp.ProtoType, "*")
		}
		sb.WriteString("\tunm := protojson.UnmarshalOptions{AllowPartial: true, DiscardUnknown: true}\n")
		fmt.Fprintf(&sb, "\tresp := &%s{}\n", respType)
	}

	if mAnn.IsEmpty {
		sb.WriteString("\treturn gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
	} else {
		sb.WriteString("\te := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
	}
	sb.WriteString("\t\tif settings.Path != \"\" {\n")
	sb.WriteString("\t\t\tbaseUrl.Path = settings.Path\n")
	sb.WriteString("\t\t}\n")
	fmt.Fprintf(&sb, "\t\thttpReq, err := http.NewRequest(%q, baseUrl.String(), %s)\n", verb, bodyReader)
	sb.WriteString("\t\tif err != nil {\n")
	sb.WriteString("\t\t\treturn err\n")
	sb.WriteString("\t\t}\n")
	sb.WriteString("\t\thttpReq = httpReq.WithContext(ctx)\n")
	sb.WriteString("\t\thttpReq.Header = headers\n\n")

	if mAnn.IsEmpty {
		fmt.Fprintf(&sb, "\t\t_, err = executeHTTPRequest(ctx, c.httpClient, httpReq, c.logger, %s, %q)\n", logBody, m.Name)
		sb.WriteString("\t\treturn err\n")
		sb.WriteString("\t}, opts...)\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	fmt.Fprintf(&sb, "\t\tbuf, err := executeHTTPRequest(ctx, c.httpClient, httpReq, c.logger, %s, %q)\n", logBody, m.Name)
	sb.WriteString("\t\tif err != nil {\n")
	sb.WriteString("\t\t\treturn err\n")
	sb.WriteString("\t\t}\n")
	if !mAnn.IsLRO {
		sb.WriteString("\n")
	}
	sb.WriteString("\t\tif err := unm.Unmarshal(buf, resp); err != nil {\n")
	sb.WriteString("\t\t\treturn err\n")
	sb.WriteString("\t\t}\n\n")
	sb.WriteString("\t\treturn nil\n")
	sb.WriteString("\t}, opts...)\n")
	sb.WriteString("\tif e != nil {\n")
	sb.WriteString("\t\treturn nil, e\n")
	sb.WriteString("\t}\n")

	if mAnn.IsLRO {
		fmt.Fprintf(&sb, "\n\toverride := fmt.Sprintf(%q, resp.GetName())\n", mAnn.OperationPathOverride)
		fmt.Fprintf(&sb, "\tlro := longrunning.InternalNewOperationWithMetadata(*c.LROClient, resp, \"*%s.%s\")\n",
			sAnn.PackageName, mAnn.OperationType)
		sb.WriteString("\tif gax.IsFeatureEnabled(\"TRACING\") {\n")
		sb.WriteString("\t\tlro.SetParentSpanContext(trace.SpanContextFromContext(ctx))\n")
		sb.WriteString("\t}\n")
		fmt.Fprintf(&sb, "\treturn &%s{\n\t\tlro:      lro,\n\t\tpollPath: override,\n\t}, nil\n", mAnn.OperationType)
	} else if mAnn.IsCustomOp {
		sb.WriteString("\top := &Operation{\n")
		fmt.Fprintf(&sb, "\t\t&%s{\n", mAnn.CustomOpHandle)
		sb.WriteString("\t\t\tc:       c.operationClient,\n")
		sb.WriteString("\t\t\tproto:   resp,\n")
		for _, p := range mAnn.CustomOpParams {
			fmt.Fprintf(&sb, "\t\t\t%s: %s,\n", p.Name, p.Getter)
		}
		sb.WriteString("\t\t},\n")
		sb.WriteString("\t}\n")
		sb.WriteString("\treturn op, nil\n")
	} else {
		sb.WriteString("\treturn resp, nil\n")
	}
	sb.WriteString("}\n")

	return sb.String()
}

func indentCode(code string, levels int) string {
	prefix := strings.Repeat("\t", levels)
	lines := strings.Split(code, "\n")
	var out []string
	for _, l := range lines {
		if l == "" {
			out = append(out, "")
		} else {
			out = append(out, prefix+l)
		}
	}
	return strings.Join(out, "\n")
}

func lookupMethodDescriptor(m *api.Method, descInfo *DescriptorInfo) *descriptorpb.MethodDescriptorProto {
	if descInfo == nil || descInfo.MethodDescriptors == nil {
		return nil
	}
	key := m.SourceServiceID + "." + m.Name
	if mProto, ok := descInfo.MethodDescriptors[key]; ok {
		return mProto
	}
	if mProto, ok := descInfo.MethodDescriptors[strings.TrimPrefix(key, ".")]; ok {
		return mProto
	}
	return nil
}

func resolveGRPCStub(m *api.Method, sAnn *ServiceAnnotation) string {
	switch m.SourceServiceID {
	case ".google.longrunning.Operations":
		return "c.operationsClient"
	case ".google.cloud.location.Locations":
		return "c.locationsClient"
	case ".google.iam.v1.IAMPolicy":
		return "c.iamPolicyClient"
	default:
		return "c." + sAnn.GRPCClientField
	}
}

func formatHeaderAccessor(field string, mProto *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo) string {
	accessor := fmt.Sprintf("req%s", fieldGetter(field))
	if mProto == nil || descInfo == nil || descInfo.MessageDescriptors == nil {
		return fmt.Sprintf("url.QueryEscape(%s)", accessor)
	}
	inType := mProto.GetInputType()
	msg := descInfo.MessageDescriptors[inType]
	if msg == nil {
		msg = descInfo.MessageDescriptors[strings.TrimPrefix(inType, ".")]
	}
	if msg == nil {
		return fmt.Sprintf("url.QueryEscape(%s)", accessor)
	}
	parts := strings.Split(field, ".")
	var currMsg *descriptorpb.DescriptorProto = msg
	var lastField *descriptorpb.FieldDescriptorProto
	for _, part := range parts {
		if currMsg == nil {
			lastField = nil
			break
		}
		var found *descriptorpb.FieldDescriptorProto
		for _, f := range currMsg.GetField() {
			if f.GetName() == part {
				found = f
				break
			}
		}
		if found == nil {
			lastField = nil
			break
		}
		lastField = found
		if found.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
			nextType := found.GetTypeName()
			currMsg = descInfo.MessageDescriptors[nextType]
			if currMsg == nil {
				currMsg = descInfo.MessageDescriptors[strings.TrimPrefix(nextType, ".")]
			}
		} else {
			currMsg = nil
		}
	}
	if lastField != nil {
		switch lastField.GetType() {
		case descriptorpb.FieldDescriptorProto_TYPE_STRING:
			return fmt.Sprintf("url.QueryEscape(%s)", accessor)
		case descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, descriptorpb.FieldDescriptorProto_TYPE_FLOAT:
			return fmt.Sprintf("url.QueryEscape(fmt.Sprintf(\"%%g\", %s))", accessor)
		default:
			return accessor
		}
	}
	return fmt.Sprintf("url.QueryEscape(%s)", accessor)
}

func appendRoutingHeadersGRPC(sb *strings.Builder, m *api.Method, mProto *descriptorpb.MethodDescriptorProto, sAnn *ServiceAnnotation) {
	if mProto != nil && dynamicRequestHeadersExist(mProto) {
		headers := parseDynamicRequestHeaders(mProto)
		sb.WriteString("\troutingHeaders := \"\"\n")
		sb.WriteString("\troutingHeadersMap := make(map[string]string)\n")
		for _, h := range headers {
			namedCaptureRegex := h[0]
			field := h[1]
			headerName := h[2]
			accessor := fmt.Sprintf("req%s", fieldGetter(field))
			regexHelper := fmt.Sprintf("url.QueryEscape(reg.FindStringSubmatch(%s)[1])", accessor)
			fmt.Fprintf(sb, "\tif reg := regexp.MustCompile(%q); reg.MatchString(%s) && len(%s) > 0 {\n",
				namedCaptureRegex, accessor, regexHelper)
			fmt.Fprintf(sb, "\t\troutingHeadersMap[%q] = %s\n", headerName, regexHelper)
			sb.WriteString("\t}\n")
		}
		if sAnn != nil && sAnn.Model != nil && sAnn.Model.OrderedRoutingHeaders {
			for _, h := range headers {
				headerName := h[2]
				fmt.Fprintf(sb, "\tif headerValue, ok := routingHeadersMap[%q]; ok {\n", headerName)
				fmt.Fprintf(sb, "\t\troutingHeaders = fmt.Sprintf(\"%%s%%s=%%s&\", routingHeaders, %q, headerValue)\n", headerName)
				fmt.Fprintf(sb, "\t\tdelete(routingHeadersMap, %q)\n", headerName)
				sb.WriteString("\t}\n")
			}
		} else {
			sb.WriteString("\tfor headerName, headerValue := range routingHeadersMap {\n")
			sb.WriteString("\t\troutingHeaders = fmt.Sprintf(\"%s%s=%s&\", routingHeaders, headerName, headerValue)\n")
			sb.WriteString("\t}\n")
		}
		sb.WriteString("\troutingHeaders = strings.TrimSuffix(routingHeaders, \"&\")\n")
		sb.WriteString("\thds := []string{\"x-goog-request-params\", routingHeaders}\n\n")
		sb.WriteString("\thds = append(c.xGoogHeaders, hds...)\n")
		sb.WriteString("\tctx = gax.InsertMetadataIntoOutgoingContext(ctx, hds...)\n")
		return
	}

	var headers [][]string
	if mProto != nil {
		headers = parseImplicitRequestHeaders(mProto)
	} else {
		// Fallback for known mixins
		if m.SourceServiceID == ".google.cloud.location.Locations" && m.Name == "GetLocation" {
			headers = [][]string{{"{name=...}", "name"}}
		} else if m.SourceServiceID == ".google.longrunning.Operations" && (m.Name == "GetOperation" || m.Name == "WaitOperation" || m.Name == "DeleteOperation") {
			headers = [][]string{{"{name=...}", "name"}}
		} else if m.SourceServiceID == ".google.iam.v1.IAMPolicy" {
			headers = [][]string{{"{resource=...}", "resource"}}
		}
	}

	if len(headers) > 0 {
		seen := make(map[string]bool)
		var formats, values strings.Builder
		for _, h := range headers {
			field := h[1]
			if seen[field] {
				continue
			}
			seen[field] = true
			var descInfo *DescriptorInfo
			if sAnn != nil {
				descInfo = sAnn.DescInfo
			}
			val := formatHeaderAccessor(field, mProto, descInfo)
			fmt.Fprintf(&values, " %q, %s,", field, val)
			formats.WriteString("%s=%v&")
		}
		f := formats.String()[:formats.Len()-1]
		v := values.String()[:values.Len()-1]
		fmt.Fprintf(sb, "\thds := []string{\"x-goog-request-params\", fmt.Sprintf(%q,%s)}\n\n", f, v)
		sb.WriteString("\thds = append(c.xGoogHeaders, hds...)\n")
		sb.WriteString("\tctx = gax.InsertMetadataIntoOutgoingContext(ctx, hds...)\n")
		return
	}

	sb.WriteString("\tctx = gax.InsertMetadataIntoOutgoingContext(ctx, c.xGoogHeaders...)\n")
}

func appendRoutingHeadersREST(sb *strings.Builder, m *api.Method, mProto *descriptorpb.MethodDescriptorProto, sAnn *ServiceAnnotation) {
	if mProto != nil && dynamicRequestHeadersExist(mProto) {
		headers := parseDynamicRequestHeaders(mProto)
		sb.WriteString("\troutingHeaders := \"\"\n")
		sb.WriteString("\troutingHeadersMap := make(map[string]string)\n")
		for _, h := range headers {
			namedCaptureRegex := h[0]
			field := h[1]
			headerName := h[2]
			accessor := fmt.Sprintf("req%s", fieldGetter(field))
			regexHelper := fmt.Sprintf("url.QueryEscape(reg.FindStringSubmatch(%s)[1])", accessor)
			fmt.Fprintf(sb, "\tif reg := regexp.MustCompile(%q); reg.MatchString(%s) && len(%s) > 0 {\n",
				namedCaptureRegex, accessor, regexHelper)
			fmt.Fprintf(sb, "\t\troutingHeadersMap[%q] = %s\n", headerName, regexHelper)
			sb.WriteString("\t}\n")
		}
		if sAnn != nil && sAnn.Model != nil && sAnn.Model.OrderedRoutingHeaders {
			for _, h := range headers {
				headerName := h[2]
				fmt.Fprintf(sb, "\tif headerValue, ok := routingHeadersMap[%q]; ok {\n", headerName)
				fmt.Fprintf(sb, "\t\troutingHeaders = fmt.Sprintf(\"%%s%%s=%%s&\", routingHeaders, %q, headerValue)\n", headerName)
				fmt.Fprintf(sb, "\t\tdelete(routingHeadersMap, %q)\n", headerName)
				sb.WriteString("\t}\n")
			}
		} else {
			sb.WriteString("\tfor headerName, headerValue := range routingHeadersMap {\n")
			sb.WriteString("\t\troutingHeaders = fmt.Sprintf(\"%s%s=%s&\", routingHeaders, headerName, headerValue)\n")
			sb.WriteString("\t}\n")
		}
		sb.WriteString("\troutingHeaders = strings.TrimSuffix(routingHeaders, \"&\")\n")
		sb.WriteString("\thds := []string{\"x-goog-request-params\", routingHeaders}\n\n")
		sb.WriteString("\thds = append(c.xGoogHeaders, hds...)\n")
		sb.WriteString("\thds = append(hds, \"Content-Type\", \"application/json\")\n")
		sb.WriteString("\theaders := gax.BuildHeaders(ctx, hds...)\n")
		return
	}

	var headers [][]string
	if mProto != nil {
		headers = parseImplicitRequestHeaders(mProto)
	} else {
		if m.SourceServiceID == ".google.cloud.location.Locations" && m.Name == "GetLocation" {
			headers = [][]string{{"{name=...}", "name"}}
		} else if m.SourceServiceID == ".google.longrunning.Operations" && (m.Name == "GetOperation" || m.Name == "WaitOperation" || m.Name == "DeleteOperation") {
			headers = [][]string{{"{name=...}", "name"}}
		} else if m.SourceServiceID == ".google.iam.v1.IAMPolicy" {
			headers = [][]string{{"{resource=...}", "resource"}}
		}
	}

	if len(headers) > 0 {
		seen := make(map[string]bool)
		var formats, values strings.Builder
		for _, h := range headers {
			field := h[1]
			if seen[field] {
				continue
			}
			seen[field] = true
			var descInfo *DescriptorInfo
			if sAnn != nil {
				descInfo = sAnn.DescInfo
			}
			val := formatHeaderAccessor(field, mProto, descInfo)
			fmt.Fprintf(&values, " %q, %s,", field, val)
			formats.WriteString("%s=%v&")
		}
		f := formats.String()[:formats.Len()-1]
		v := values.String()[:values.Len()-1]
		fmt.Fprintf(sb, "\thds := []string{\"x-goog-request-params\", fmt.Sprintf(%q,%s)}\n\n", f, v)
		sb.WriteString("\thds = append(c.xGoogHeaders, hds...)\n")
		sb.WriteString("\thds = append(hds, \"Content-Type\", \"application/json\")\n")
		sb.WriteString("\theaders := gax.BuildHeaders(ctx, hds...)\n")
		return
	}

	sb.WriteString("\thds := append(c.xGoogHeaders, \"Content-Type\", \"application/json\")\n")
	sb.WriteString("\theaders := gax.BuildHeaders(ctx, hds...)\n")
}

func appendTelemetryContext(sb *strings.Builder, m *api.Method, mProto *descriptorpb.MethodDescriptorProto, sAnn *ServiceAnnotation, descInfo *DescriptorInfo, isREST bool) {
	hasHeaders := false
	if mProto != nil {
		if dynamicRequestHeadersExist(mProto) || len(parseImplicitRequestHeaders(mProto)) > 0 {
			hasHeaders = true
		}
	} else {
		if m.SourceServiceID == ".google.cloud.location.Locations" && m.Name == "GetLocation" {
			hasHeaders = true
		} else if m.SourceServiceID == ".google.longrunning.Operations" && (m.Name == "GetOperation" || m.Name == "WaitOperation" || m.Name == "DeleteOperation") {
			hasHeaders = true
		} else if m.SourceServiceID == ".google.iam.v1.IAMPolicy" {
			hasHeaders = true
		}
	}

	if hasHeaders && !m.ClientSideStreaming {
		dynamicRes := false
		if sAnn != nil && sAnn.Model != nil && sAnn.Model.DynamicResourceHeuristics {
			dynamicRes = true
		}
		resTarget := resourceNameField(mProto, descInfo, dynamicRes)
		if resTarget != nil && len(resTarget.FieldNames) > 0 {
			var getters []string
			for _, f := range resTarget.FieldNames {
				getters = append(getters, fmt.Sprintf("req%s", fieldGetter(f)))
			}
			gettersStr := strings.Join(getters, ", ")
			host := sAnn.URLDomain
			if descInfo != nil && descInfo.ServiceDescriptors != nil && m.SourceServiceID != "" {
				sProto := descInfo.ServiceDescriptors[m.SourceServiceID]
				if sProto == nil {
					sProto = descInfo.ServiceDescriptors[strings.TrimPrefix(m.SourceServiceID, ".")]
				}
				if sProto != nil && proto.HasExtension(sProto.GetOptions(), annotations.E_DefaultHost) {
					extHost := proto.GetExtension(sProto.GetOptions(), annotations.E_DefaultHost).(string)
					if extHost != "" {
						host = extHost
					}
				}
			}
			sb.WriteString("\tif gax.IsFeatureEnabled(\"TRACING\") || gax.IsFeatureEnabled(\"LOGGING\") {\n")
			if host != "" {
				fmt.Fprintf(sb, "\t\tctx = callctx.WithTelemetryContext(ctx, \"resource_name\", fmt.Sprintf(\"//%s/%s\", %s))\n",
					host, resTarget.Format, gettersStr)
			} else {
				fmt.Fprintf(sb, "\t\tctx = callctx.WithTelemetryContext(ctx, \"resource_name\", fmt.Sprintf(\"%s\", %s))\n",
					resTarget.Format, gettersStr)
			}
			sb.WriteString("\t}\n")
		}
	}

	rpcMethod := strings.TrimPrefix(m.SourceServiceID, ".") + "/" + m.Name
	sb.WriteString("\tif gax.IsFeatureEnabled(\"METRICS\") || gax.IsFeatureEnabled(\"TRACING\") || gax.IsFeatureEnabled(\"LOGGING\") {\n")
	fmt.Fprintf(sb, "\t\tctx = callctx.WithTelemetryContext(ctx, \"rpc_method\", %q)\n", rpcMethod)
	if isREST {
		httpInf := getHTTPInfo(mProto)
		urlTemplate := ""
		if httpInf != nil {
			urlTemplate = httpInf.url
		} else if m.PathInfo != nil && len(m.PathInfo.Bindings) > 0 && m.PathInfo.Bindings[0].PathTemplate != nil {
			urlTemplate = m.PathInfo.Bindings[0].PathTemplate.FlatPath()
		}
		if urlTemplate != "" {
			if !strings.HasPrefix(urlTemplate, "/") {
				urlTemplate = "/" + urlTemplate
			}
			fmt.Fprintf(sb, "\t\tctx = callctx.WithTelemetryContext(ctx, \"url_template\", %q)\n", urlTemplate)
		}
	}
	sb.WriteString("\t}\n")
}

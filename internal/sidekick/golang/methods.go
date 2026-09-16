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
		".google.protobuf.Int64Value",
		".google.protobuf.UInt64Value",
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

func resourceNameField(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo) *heuristicTarget {
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
		return nil
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

	var recurse func([]*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto)

	handleLeaf := func(field *descriptorpb.FieldDescriptorProto, stack []*descriptorpb.FieldDescriptorProto) {
		elts := []string{}
		for _, f := range stack {
			elts = append(elts, f.GetName())
		}
		elts = append(elts, field.GetName())
		key := strings.Join(elts, ".")
		pathsToLeafs[key] = field
	}

	handleMsg := func(field *descriptorpb.FieldDescriptorProto, stack []*descriptorpb.FieldDescriptorProto) {
		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			return
		}
		if slices.Contains(excludedFields, field) {
			return
		}
		if slices.Contains(stack, field) {
			return
		}
		if descInfo != nil && descInfo.MessageDescriptors != nil {
			if subMsg, ok := descInfo.MessageDescriptors[field.GetTypeName()]; ok {
				recurse(append(stack, field), subMsg)
			}
		}
	}

	recurse = func(stack []*descriptorpb.FieldDescriptorProto, m *descriptorpb.DescriptorProto) {
		if m == nil {
			return
		}
		for _, field := range m.GetField() {
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && !slices.Contains(wellKnownTypeNames, field.GetTypeName()) {
				handleMsg(field, stack)
			} else {
				handleLeaf(field, stack)
			}
		}
	}

	recurse([]*descriptorpb.FieldDescriptorProto{}, msg)
	return pathsToLeafs
}

func queryParams(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo) map[string]*descriptorpb.FieldDescriptorProto {
	res := map[string]*descriptorpb.FieldDescriptorProto{}
	if m == nil || descInfo == nil || descInfo.MessageDescriptors == nil {
		return res
	}
	info := getHTTPInfo(m)
	if info == nil || info.body == "*" {
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

func generateQueryStringCode(m *descriptorpb.MethodDescriptorProto, descInfo *DescriptorInfo, retErr string) string {
	qp := queryParams(m, descInfo)
	fields := make([]string, 0, len(qp))
	for p := range qp {
		fields = append(fields, p)
	}
	sort.Strings(fields)

	var sb strings.Builder
	sb.WriteString("\tparams := url.Values{}\n")
	sb.WriteString("\tparams.Add(\"$alt\", \"json;enum-encoding=int\")\n")

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
			fmt.Fprintf(&sb, "\tif req%s != nil {\n", accessor)
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
	if !strings.HasPrefix(urlStr, "/") {
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

	// Signature
	switch {
	case mAnn.IsEmpty:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) error {\n",
			sAnn.GRPCClientName, m.Name, mAnn.RequestType)
	case mAnn.IsPaged:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) *%s {\n",
			sAnn.GRPCClientName, m.Name, mAnn.RequestType, mAnn.IteratorType)
	case mAnn.IsLRO:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.GRPCClientName, m.Name, mAnn.RequestType, mAnn.OperationType)
	default:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.GRPCClientName, m.Name, mAnn.RequestType, mAnn.ResponseType)
	}

	// Routing headers
	appendRoutingHeadersGRPC(&sb, m, mProto)

	// OpenTelemetry Telemetry Context
	appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, false)

	// Call options
	fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
		m.Name, m.Name, m.Name)

	// Execution
	grpcStub := resolveGRPCStub(m, sAnn)

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
		sb.WriteString("\t\t\treq.PageToken = pageToken\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
		sb.WriteString("\t\t\treq.PageSize = math.MaxInt32\n")
		sb.WriteString("\t\t} else if pageSize != 0 {\n")
		sb.WriteString("\t\t\treq.PageSize = int32(pageSize)\n")
		sb.WriteString("\t\t}\n")
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
		sb.WriteString("\tit.pageInfo.MaxSize = int(req.GetPageSize())\n")
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
	var sb strings.Builder

	mProto := lookupMethodDescriptor(m, descInfo)
	httpInf := getHTTPInfo(mProto)

	verb := "GET"
	urlStr := ""
	bodyField := ""
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

	// Doc comment
	if mAnn.Doc != "" {
		sb.WriteString(mAnn.Doc + "\n")
	}

	// Signature
	switch {
	case mAnn.IsEmpty:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) error {\n",
			sAnn.RESTClientName, m.Name, mAnn.RequestType)
	case mAnn.IsPaged:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) *%s {\n",
			sAnn.RESTClientName, m.Name, mAnn.RequestType, mAnn.IteratorType)
	case mAnn.IsLRO:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.RESTClientName, m.Name, mAnn.RequestType, mAnn.OperationType)
	default:
		fmt.Fprintf(&sb, "func (c *%s) %s(ctx context.Context, req *%s, opts ...gax.CallOption) (*%s, error) {\n",
			sAnn.RESTClientName, m.Name, mAnn.RequestType, mAnn.ResponseType)
	}

	// Paged REST method structure
	if mAnn.IsPaged {
		fmt.Fprintf(&sb, "\tit := &%s{}\n", mAnn.IteratorType)
		sb.WriteString("\treq = proto.CloneOf(req)\n")
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
		sb.WriteString("\t\tif pageToken != \"\" {\n")
		sb.WriteString("\t\t\treq.PageToken = pageToken\n")
		sb.WriteString("\t\t}\n")
		sb.WriteString("\t\tif pageSize > math.MaxInt32 {\n")
		sb.WriteString("\t\t\treq.PageSize = math.MaxInt32\n")
		sb.WriteString("\t\t} else if pageSize != 0 {\n")
		sb.WriteString("\t\t\treq.PageSize = int32(pageSize)\n")
		sb.WriteString("\t\t}\n")

		// URL and Query
		baseURLCode := generateBaseURLCode(urlStr, "\treturn nil, \"\", err")
		sb.WriteString(indentCode(baseURLCode, 1))
		sb.WriteString("\n")

		queryCode := generateQueryStringCode(mProto, descInfo, "\treturn nil, \"\", err")
		sb.WriteString(indentCode(queryCode, 1))
		sb.WriteString("\n")

		sb.WriteString("\t\t// Build HTTP headers from client and context metadata.\n")
		sb.WriteString("\t\thds := append(c.xGoogHeaders, \"Content-Type\", \"application/json\")\n")
		sb.WriteString("\t\theaders := gax.BuildHeaders(ctx, hds...)\n")
		sb.WriteString("\t\te := gax.Invoke(ctx, func(ctx context.Context, settings gax.CallSettings) error {\n")
		sb.WriteString("\t\t\tif settings.Path != \"\" {\n")
		sb.WriteString("\t\t\t\tbaseUrl.Path = settings.Path\n")
		sb.WriteString("\t\t\t}\n")
		fmt.Fprintf(&sb, "\t\t\thttpReq, err := http.NewRequest(%q, baseUrl.String(), nil)\n", verb)
		sb.WriteString("\t\t\tif err != nil {\n")
		sb.WriteString("\t\t\t\treturn err\n")
		sb.WriteString("\t\t\t}\n")
		sb.WriteString("\t\t\thttpReq.Header = headers\n\n")
		fmt.Fprintf(&sb, "\t\t\tbuf, err := executeHTTPRequest(ctx, c.httpClient, httpReq, c.logger, nil, %q)\n", m.Name)
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
		fmt.Fprintf(&sb, "\t\treturn resp.Get%s(), resp.GetNextPageToken(), nil\n", itemsField)
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
		sb.WriteString("\tit.pageInfo.MaxSize = int(req.GetPageSize())\n")
		sb.WriteString("\tit.pageInfo.Token = req.GetPageToken()\n\n")
		sb.WriteString("\treturn it\n")
		sb.WriteString("}\n")
		return sb.String()
	}

	// Body serialization
	bodyReader := "nil"
	logBody := "nil"
	if bodyField != "" && verb != "GET" && verb != "DELETE" {
		sb.WriteString("\tm := protojson.MarshalOptions{AllowPartial: true, UseEnumNumbers: true}\n")
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
	sb.WriteString(generateQueryStringCode(mProto, descInfo, retErr))
	sb.WriteString("\n")

	// Headers
	sb.WriteString("\t// Build HTTP headers from client and context metadata.\n")
	appendRoutingHeadersREST(&sb, m, mProto)

	// Telemetry
	appendTelemetryContext(&sb, m, mProto, sAnn, descInfo, true)

	// Call options (only for Unary)
	if mAnn.IsUnary {
		fmt.Fprintf(&sb, "\topts = append((*c.CallOptions).%s[0:len((*c.CallOptions).%s):len((*c.CallOptions).%s)], opts...)\n",
			m.Name, m.Name, m.Name)
	}

	// Execution
	if !mAnn.IsEmpty {
		respType := mAnn.ResponseType
		if mAnn.IsLRO {
			respType = "longrunningpb.Operation"
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

func appendRoutingHeadersGRPC(sb *strings.Builder, m *api.Method, mProto *descriptorpb.MethodDescriptorProto) {
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
		sb.WriteString("\tfor headerName, headerValue := range routingHeadersMap {\n")
		sb.WriteString("\t\troutingHeaders = fmt.Sprintf(\"%s%s=%s&\", routingHeaders, headerName, headerValue)\n")
		sb.WriteString("\t}\n")
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
			accessor := fmt.Sprintf("req%s", fieldGetter(field))
			fmt.Fprintf(&values, " %q, url.QueryEscape(%s),", field, accessor)
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

func appendRoutingHeadersREST(sb *strings.Builder, m *api.Method, mProto *descriptorpb.MethodDescriptorProto) {
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
		sb.WriteString("\tfor headerName, headerValue := range routingHeadersMap {\n")
		sb.WriteString("\t\troutingHeaders = fmt.Sprintf(\"%s%s=%s&\", routingHeaders, headerName, headerValue)\n")
		sb.WriteString("\t}\n")
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
			accessor := fmt.Sprintf("req%s", fieldGetter(field))
			fmt.Fprintf(&values, " %q, url.QueryEscape(%s),", field, accessor)
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
	resTarget := resourceNameField(mProto, descInfo)
	if resTarget != nil && len(resTarget.FieldNames) > 0 {
		f := resTarget.FieldNames[0]
		getter := fmt.Sprintf("req%s", fieldGetter(f))
		host := sAnn.URLDomain
		sb.WriteString("\tif gax.IsFeatureEnabled(\"TRACING\") || gax.IsFeatureEnabled(\"LOGGING\") {\n")
		fmt.Fprintf(sb, "\t\tctx = callctx.WithTelemetryContext(ctx, \"resource_name\", fmt.Sprintf(\"//%s/%%v\", %s))\n",
			host, getter)
		sb.WriteString("\t}\n")
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

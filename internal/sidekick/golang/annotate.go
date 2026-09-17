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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/googleapis/librarian/internal/serviceconfig"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"github.com/googleapis/librarian/internal/sidekick/protobuf"
	"github.com/googleapis/librarian/internal/tool/protoc"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/genproto/googleapis/cloud/extendedops"
	locationpb "google.golang.org/genproto/googleapis/cloud/location"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

var (
	referenceParser       = regexp.MustCompile(`\[([a-zA-Z1-9._]+)\]\[([a-zA-Z1-9._]*)\]`)
	mdLinkParser          = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*)\)`)
	htmlLinkParser        = regexp.MustCompile(`<a\s+href=["']([^"']+)["']>([^<]+)</a>`)
	imageRegex            = regexp.MustCompile(`(?s)!\[.*?\]\(.*?\)`)
	cesAudioRegex         = regexp.MustCompile(`\[([a-zA-Z0-9._]+Session(?:Input|Output)\.audio)\]`)
	bareURLRegex          = regexp.MustCompile("https?://[^\\s)\"`]+")
	codeInlineRegex       = regexp.MustCompile("`([^`\r\n]+(?:\r?\n[ \t]*[^`\r\n]+)*)`")
	asideBlockRegex       = regexp.MustCompile(`(?s)<aside\b[^>]*>.*?</aside>`)
	sqlDataParamsRegex    = regexp.MustCompile("(?m)^[ ]*`x-goog-request-params`:\\s*\\n\\s*location_id=\\{location_path\\}&instance_id=\\{instance_path\\}`\\s*\\n?")
	boldRegex             = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	boldUnderscoreRegex   = regexp.MustCompile(`__([^_]+)__`)
	italicRegex           = regexp.MustCompile(`(?s)(^|[\s(\[{])\*([^\s*](?:(?:[^\n*]|\n[^\n*])*?[^\s*])?)\*([\s)\]}.,;!?:]|$)`)
	italicUnderscoreRegex = regexp.MustCompile(`(?m)(^|[\s(\[{])_([^\s_]|(?:[^\s_](?:[^\n]|\n[^\n])*?[^\s_]))_([\s)\]}.,;!?:]|$)`)
	openQuoteRegex        = regexp.MustCompile(`(^|[\s(\[{])"([^\s])`)
	closeQuoteRegex       = regexp.MustCompile(`([^\s])"([\s)\]}.,;!?:]|$)`)
	fencedCodeBlockRegex  = regexp.MustCompile("(?s)```.*?```")
	headingWithListRegex  = regexp.MustCompile(`(?m)(^|\n+)##\s*(.*?):\s*\n+(?:\*\s*)?\[([^\]]+)\](?:\(([^)]+)\)|\[([^\]]*)\])?`)
	headingRegex          = regexp.MustCompile(`(?m)^#+\s*([^\n]*)\n*`)
	orderedListItemRegex  = regexp.MustCompile(`^\d+[.)]\s+`)
	quotePairRegex        = regexp.MustCompile(`(^|[\s()[{:])"([^"\n]+(?:\n[^"\n]+)*?)"([\s)\]}%.,;!?:]|$)`)
	singleQuotePairRegex  = regexp.MustCompile(`(^|[\s(\[{])'([^'\n]+(?:\n[^'\n]+)*?)'([\s)\]}.,;!?:]|$)`)
	apostropheRegex       = regexp.MustCompile(`([a-zA-Z])'([a-zA-Z]|\s|$)`)
	htmlTagRegex          = regexp.MustCompile(`</?(?:code|b|i|em|strong|p|span|div|tt)\b[^>]*>`)
	httpPatternVarRegex   = regexp.MustCompile(`{([a-zA-Z0-9_.]+?)(=[^{}]+)?}`)
	headerParamRegexp     = regexp.MustCompile(`{([a-z0-9_.]+?)(=[^{}]+)?}`)
)

const (
	locationService    = ".google.cloud.location.Locations"
	iamService         = ".google.iam.v1.IAMPolicy"
	longrunningService = ".google.longrunning.Operations"

	oauthScopesExtensionTag = 1050

	operationFieldTag         = 1149
	operationRequestFieldTag  = 1150
	operationResponseFieldTag = 1151
	operationServiceTag       = 1249
	operationPollingMethodTag = 1250
)

// CustomOpModelAnnotation contains package-level custom operation metadata.
type CustomOpModelAnnotation struct {
	MessageName         string
	HandleInterfaceName string
	ProtoType           string
	ProtoTypeID         string
	StatusField         string
	StatusDoneValue     string
	NameField           string
	Handles             []*CustomOpHandleAnnotation

	ServiceToOpService map[string]*descriptorpb.ServiceDescriptorProto
	PollingParams      map[*descriptorpb.ServiceDescriptorProto][]string
}

// CustomOpHandleAnnotation contains metadata for a specific operation handle type.
type CustomOpHandleAnnotation struct {
	HandleName        string
	InterfaceName     string
	ServiceName       string
	ClientType        string
	ProtoType         string
	RequestType       string
	OperationField    string
	ErrorCodeField    string
	ErrorMessageField string
	HasErrorField     bool
	Params            []CustomOpParam
}

// CustomOpParam represents a parameter field on an operation handle.
type CustomOpParam struct {
	Name  string
	Field string
}

// CustomOpMethodParam represents an argument passed to the handle initialization in RPC methods.
type CustomOpMethodParam struct {
	Name   string
	Getter string
}

// ImportSpec represents a Go import path and its package alias/name.
type ImportSpec struct {
	Path string
	Name string
	Line string
}

// Format formats the import specification for Go source code.
func (imp ImportSpec) Format() string {
	if imp.Line != "" {
		return imp.Line
	}
	if imp.Name != "" {
		return fmt.Sprintf("%s %q", imp.Name, imp.Path)
	}
	return fmt.Sprintf("%q", imp.Path)
}

// DescriptorInfo contains metadata loaded from a protoc FileDescriptorSet.
type DescriptorInfo struct {
	// PkgByProtoFile maps proto file name to ImportSpec.
	PkgByProtoFile map[string]ImportSpec
	// PkgByMessage maps full proto message name (.google.iam.v1.Policy) to ImportSpec.
	PkgByMessage map[string]ImportSpec
	// OAuthScopes contains deduped, sorted OAuth scopes from ServiceDescriptorProto.
	OAuthScopes []string

	// MethodDescriptors maps full method name (.google.cloud.run.v2.Services.CreateService) to MethodDescriptorProto.
	MethodDescriptors map[string]*descriptorpb.MethodDescriptorProto
	// MessageDescriptors maps full message name (.google.cloud.run.v2.CreateServiceRequest) to DescriptorProto.
	MessageDescriptors map[string]*descriptorpb.DescriptorProto
	// ServiceDescriptors maps full service name (.google.cloud.run.v2.Services) to ServiceDescriptorProto.
	ServiceDescriptors map[string]*descriptorpb.ServiceDescriptorProto
	// ServicesInProtoOrder contains service FQNs in the order they appeared in protoc descriptors.
	ServicesInProtoOrder []string
	// GoTypeNameByMessage maps full proto message name to Go struct name.
	GoTypeNameByMessage map[string]string
	// Vocabulary contains learned valid collection nouns for heuristic path templates.
	Vocabulary map[string]bool
}

// DocExampleData contains view data for the doc.go usage snippet.
type DocExampleData struct {
	ConstructorName string
	HasMethod       bool
	ProtoPkg        string
	ProtoImportPath string
	RequestType     string
	MethodName      string
	IsLRO           bool
	IsServerStream  bool
	IsBidiStream    bool
	IsUnary         bool
	IsPaged         bool
	ReturnsEmpty    bool
	LROHasResponse  bool
	ResponseType    string
}

// MetadataService represents a service in gapic_metadata.json.
type MetadataService struct {
	Name    string
	Clients []*MetadataClient
	HasMore bool
}

// MetadataClient represents a transport client in gapic_metadata.json.
type MetadataClient struct {
	Transport     string
	LibraryClient string
	RPCs          []*MetadataRPC
	HasMore       bool
}

// MetadataRPC represents an RPC in gapic_metadata.json.
type MetadataRPC struct {
	Name    string
	Methods []string
	HasMore bool
}

// ModelAnnotation holds Go-specific annotations attached to api.API.Codec.
type ModelAnnotation struct {
	PackageName       string
	ImportPath        string
	ProtoPackage      string
	ReleaseLevel      string
	CopyrightYear     string
	Boilerplate       []string
	DefaultAuthScopes []string
	ServiceName       string

	HasREST bool
	HasGRPC bool

	IsAlpha      bool
	IsBeta       bool
	IsDeprecated bool

	DocSummaryLines []string
	HasDocSummary   bool
	DocExample      DocExampleData

	// Metadata
	MetadataServices []*MetadataService

	// Auxiliary types (sorted alphabetically).
	OperationWrappers []*OperationWrapper
	Iterators         []*IteratorType

	// Services to generate.
	Services []*api.Service

	// File imports partitioned for package-level files.
	DocImports        FileImports
	HelpersImports    FileImports
	AuxiliaryImports  FileImports
	OperationsImports FileImports

	DIREGAPIC                 bool
	HasCustomOp               bool
	CustomOp                  *CustomOpModelAnnotation
	DynamicResourceHeuristics bool
	RESTNumericEnums          bool
	OrderedRoutingHeaders     bool
	WrapperTypesForPageSize   bool
}

// ServiceAnnotation holds Go-specific annotations attached to api.Service.Codec.
type ServiceAnnotation struct {
	Model    *ModelAnnotation
	DescInfo *DescriptorInfo

	ShortName               string
	RawShortName            string
	ClientName              string
	InternalClientInterface string
	GRPCClientName          string
	RESTClientName          string
	CallOptionsName         string
	DefaultCallOptionsName  string
	FileName                string

	// Methods in normalized Go order:
	// native methods first in declaration order, then mixins
	// (Locations, IAM, Operations; alphabetical within group).
	Methods []*api.Method

	// Internal client LRO builders sorted alphabetically by method name.
	InternalLROBuilders []*api.Method

	Doc string

	HasLRO  bool
	HasREST bool
	HasGRPC bool

	HasExportSetGoogleClientInfo bool

	Imports FileImports

	CopyrightYear    string
	PackageName      string
	ImportPath       string
	DocLibName       string
	TelemetryService string
	URLDomain        string
	ProtoPkg         string
	ProtoServiceName string
	GRPCClientField  string

	DefaultEndpoint         string
	DefaultEndpointTemplate string
	DefaultMTLSEndpoint     string
	DefaultAudience         string
	AllowHardBoundTokens    bool

	RESTDefaultEndpoint         string
	RESTDefaultEndpointTemplate string
	RESTDefaultMTLSEndpoint     string
	RESTDefaultAudience         string

	APITitle   string
	ServiceDoc string

	HasOperationsMixin    bool
	HasLocationsMixin     bool
	HasIAMPolicyMixin     bool
	OperationPathOverride string

	ExampleTestFileName      string
	ExampleGo123TestFileName string
	ExampleImports           FileImports
	ExampleGo123Imports      FileImports
	ExampleMethods           []*api.Method
	PagedExampleMethods      []*api.Method
	ExampleNewClientName     string
	ExampleNewRESTClientName string
	NewClientCall            string
	NewRESTClientCall        string

	HasOperationClient         bool
	OperationClientType        string
	OperationClientConstructor string

	IsInternal bool
}

type AutoPopulatedField struct {
	FieldName  string
	IsOptional bool
}

// MethodAnnotation holds Go-specific annotations attached to api.Method.Codec.
type MethodAnnotation struct {
	Model               *ModelAnnotation
	Service             *ServiceAnnotation
	Doc                 string
	IsInternal          bool
	GoMethodName        string
	OperationMethodName string
	AutoPopulatedFields []AutoPopulatedField
	IsLRO               bool
	OperationType       string
	IsPaged             bool
	IteratorType        string
	PageTokenField      *api.Field
	PageSizeField       *api.Field
	ResourceField       *api.Field

	IsCustomOp          bool
	CustomOpHandle      string
	CustomOpParams      []CustomOpMethodParam
	IsMapPagination     bool
	PageSizeFieldName   string
	PageSizeIsUint32    bool
	PageSizeIsOptional  bool
	PageSizeIsWrapper   bool
	PageSizeWrapperType string
	PageTokenOptional   bool

	// REST HTTP bindings.
	HTTPMethod  string
	HTTPPath    string
	QueryParams []string
	PathParams  []string
	BodyField   string

	HasRetry          bool
	RetryTimeout      int
	HasRetryCodes     bool
	RetryCodes        []string
	BackoffInitial    int
	BackoffMax        int
	BackoffMultiplier string

	HasRESTRetry            bool
	RESTRetryTimeout        int
	HasRESTRetryCodes       bool
	RESTRetryCodes          []string
	RESTRetryCodesFormatted string

	ClientReceiverName string
	RequestType        string
	ResponseType       string
	IsEmpty            bool
	IsUnary            bool
	IsServerStream     bool
	IsBidiStream       bool
	IsClientStream     bool
	StreamClientType   string

	GRPCClientName        string
	RESTClientName        string
	PackageName           string
	OperationPathOverride string
	HasGRPC               bool
	HasREST               bool

	GRPCMethodCode string
	RESTMethodCode string
	ElemType       string
	ItemsField     string

	ExampleFunctionName    string
	ExampleFunctionAllName string
	RequestDocURL          string
	LROHasResponse         bool
	NewClientCall          string

	CopyrightYear      string
	RegionTag          string
	SnippetDescription string
	Imports            FileImports
}

// OperationWrapper describes an LRO operation wrapper type for auxiliary.go.
type OperationWrapper struct {
	Name            string
	MethodName      string
	ResponseType    string
	MetadataType    string
	IsEmptyResponse bool
	IsEmptyMetadata bool
	HasREST         bool
	ResponseImport  *ImportSpec
	MetadataImport  *ImportSpec
}

// IteratorType describes a paginated iterator type for auxiliary.go.
type IteratorType struct {
	TypeName     string
	ElemType     string
	ElemTypeName string
	ElemPkgName  string
	ItemsField   string
	IsMap        bool
	MapKeyType   string
	MapValueType string
	Import       *ImportSpec
}

// FileImports holds partitioned and sorted standard library and third-party imports.
type FileImports struct {
	Standard   []ImportSpec
	ThirdParty []ImportSpec
}

// HasBoth reports whether both standard and third-party import slices are non-empty.
func (f FileImports) HasBoth() bool {
	return len(f.Standard) > 0 && len(f.ThirdParty) > 0
}

// reduceServiceName strips trailing version, trailing "Service", and replaces with empty string
// if matching the Go package name.
func reduceServiceName(svc, pkg string) string {
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

	if strings.Contains(svc, "IAM") {
		svc = strings.ReplaceAll(svc, "IAM", "Iam")
	}

	return svc
}

// AnnotateModel annotates model and all its elements with Go-specific data.
func AnnotateModel(model *api.API, cfg *parser.ModelConfig) (*ModelAnnotation, error) {
	descInfo, err := loadDescriptorInfo(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load descriptor info: %w", err)
	}

	year := "2026"
	if cfg.Codec != nil && cfg.Codec["copyright-year"] != "" {
		year = cfg.Codec["copyright-year"]
	}

	importPath := ""
	if cfg.Codec != nil && cfg.Codec["import-path"] != "" {
		importPath = cfg.Codec["import-path"]
	}

	clientPkg := model.Name
	if cfg.Codec != nil && cfg.Codec["client-package"] != "" {
		clientPkg = cfg.Codec["client-package"]
	}

	var svcConfig *serviceconfig.Service
	if cfg.ServiceConfig != "" && cfg.Source != nil {
		path := cfg.Source.Resolve(cfg.ServiceConfig)
		if sc, readErr := serviceconfig.Read(path); readErr == nil {
			svcConfig = sc
		}
	}

	if svcConfig != nil && svcConfig.GetHttp() != nil {
		for _, rule := range svcConfig.GetHttp().GetRules() {
			sel := rule.GetSelector()
			for _, key := range []string{sel, "." + sel, strings.TrimPrefix(sel, ".")} {
				if m, ok := descInfo.MethodDescriptors[key]; ok {
					cloned := proto.Clone(m).(*descriptorpb.MethodDescriptorProto)
					if cloned.Options == nil {
						cloned.Options = &descriptorpb.MethodOptions{}
					}
					proto.SetExtension(cloned.Options, annotations.E_Http, rule)
					descInfo.MethodDescriptors[key] = cloned
				}
			}
		}
	}

	if model.Title == "" && svcConfig != nil && svcConfig.Title != "" {
		model.Title = svcConfig.Title
	}

	releaseLevel := "ga"
	if cfg.Codec != nil && cfg.Codec["release-level"] != "" {
		releaseLevel = cfg.Codec["release-level"]
	}

	hasREST := true
	hasGRPC := true
	dire := false
	if cfg != nil && cfg.Codec != nil {
		trans := cfg.Codec["transport"]
		dire = cfg.Codec["diregapic"] == "true"
		if trans == "grpc" {
			hasREST = false
		} else if trans == "rest" || dire {
			hasGRPC = false
		}
	}

	var scopes []string
	scopeSet := make(map[string]bool)
	for _, s := range model.Services {
		sFQN := "." + s.Package + "." + s.Name
		sDesc := descInfo.ServiceDescriptors[sFQN]
		if sDesc == nil {
			sDesc = descInfo.ServiceDescriptors[strings.TrimPrefix(sFQN, ".")]
		}
		if sDesc != nil {
			extractServiceScopes(sDesc, scopeSet)
		}
	}
	for sc := range scopeSet {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)

	ann := &ModelAnnotation{
		PackageName:               clientPkg,
		ImportPath:                importPath,
		ProtoPackage:              model.PackageName,
		CopyrightYear:             year,
		ReleaseLevel:              releaseLevel,
		IsAlpha:                   releaseLevel == "alpha",
		IsBeta:                    releaseLevel == "beta",
		IsDeprecated:              releaseLevel == "deprecated",
		DefaultAuthScopes:         scopes,
		Services:                  model.Services,
		HasREST:                   hasREST,
		HasGRPC:                   hasGRPC,
		DIREGAPIC:                 dire,
		DynamicResourceHeuristics: cfg.Codec != nil && (cfg.Codec["F_dynamic_resource_heuristics"] == "true" || cfg.Codec["dynamic_resource_heuristics"] == "true"),
		RESTNumericEnums:          cfg.Codec != nil && cfg.Codec["rest-numeric-enums"] == "true",
		OrderedRoutingHeaders:     cfg.Codec != nil && (cfg.Codec["F_ordered_routing_headers"] == "true" || cfg.Codec["ordered_routing_headers"] == "true"),
		WrapperTypesForPageSize:   cfg.Codec != nil && (cfg.Codec["F_wrapper_types_for_page_size"] == "true" || cfg.Codec["wrapper_types_for_page_size"] == "true"),
	}
	if svcConfig != nil && svcConfig.Name != "" {
		ann.ServiceName = svcConfig.Name
	} else if len(model.Services) > 0 && model.Services[0].DefaultHost != "" {
		ann.ServiceName = model.Services[0].DefaultHost
	}

	if svcConfig != nil && svcConfig.GetDocumentation() != nil {
		summary := svcConfig.GetDocumentation().GetSummary()
		ann.DocSummaryLines = FormatDocSummary(summary)
		ann.HasDocSummary = len(ann.DocSummaryLines) > 0
	}

	if ann.DIREGAPIC {
		customOp, err := discoverCustomOperations(model, descInfo, clientPkg, importPath)
		if err == nil && customOp != nil && len(customOp.Handles) > 0 {
			ann.HasCustomOp = true
			ann.CustomOp = customOp
			protoImp := ImportSpec{Path: importPath + "/" + clientPkg + "pb", Name: clientPkg + "pb"}
			if imp, ok := descInfo.PkgByMessage[customOp.ProtoTypeID]; ok {
				protoImp = imp
			}
			rawOpsImports := []ImportSpec{
				{Path: "context"},
				{Path: "fmt"},
				{Path: "time"},
				protoImp,
				{Path: "github.com/googleapis/gax-go/v2", Name: "gax"},
				{Path: "github.com/googleapis/gax-go/v2/apierror"},
				{Path: "google.golang.org/api/googleapi"},
			}
			ann.OperationsImports = PartitionImports(rawOpsImports)
		}
	}

	retryMethods := parseGRPCServiceConfigDetailed(cfg)
	for _, s := range model.Services {
		annotateService(s, model, ann, svcConfig, descInfo, clientPkg, retryMethods, cfg)
	}

	ann.DocExample = selectDocExample(model, descInfo, clientPkg, ann.HasGRPC, ann.HasREST)
	ann.OperationWrappers = collectOperationWrappers(model.Services, descInfo, ann.HasREST)
	ann.Iterators = collectIterators(model.Services, descInfo)

	ann.DocImports = PartitionImports(nil)
	ann.HelpersImports = computeHelpersImports(ann.HasGRPC, ann.HasREST)
	ann.AuxiliaryImports = computeAuxiliaryImports(ann.OperationWrappers, ann.Iterators, descInfo)

	ann.MetadataServices = buildMetadataServices(model.Services)

	model.Codec = ann
	return ann, nil
}

func getSGGConfig(sID string, svcConfig *serviceconfig.Service, cfg *parser.ModelConfig) (map[string]bool, bool) {
	if cfg == nil || svcConfig == nil || svcConfig.GetPublishing() == nil {
		return nil, false
	}
	isFeatureEnabled := false
	if cfg.Codec != nil {
		if cfg.Codec["F_selective_gapic_generation"] == "true" || cfg.Codec["selective_gapic_generation"] == "true" {
			isFeatureEnabled = true
		}
	}
	if !isFeatureEnabled {
		return nil, false
	}
	protoPkg := ""
	if lastDot := strings.LastIndex(sID, "."); lastDot != -1 {
		protoPkg = strings.TrimPrefix(sID[:lastDot], ".")
	}
	ls := svcConfig.GetPublishing().GetLibrarySettings()
	var sgg *annotations.SelectiveGapicGeneration
	for _, setting := range ls {
		if setting.GetVersion() != "" && protoPkg != "" && setting.GetVersion() != protoPkg {
			continue
		}
		if goSettings := setting.GetGoSettings(); goSettings != nil && goSettings.GetCommon() != nil && goSettings.GetCommon().GetSelectiveGapicGeneration() != nil {
			sgg = goSettings.GetCommon().GetSelectiveGapicGeneration()
			break
		}
	}
	if sgg == nil {
		return nil, false
	}
	allowed := make(map[string]bool)
	for _, m := range sgg.GetMethods() {
		allowed[m] = true
	}
	return allowed, sgg.GetGenerateOmittedAsInternal()
}

func annotateService(s *api.Service, model *api.API, mAnn *ModelAnnotation, svcConfig *serviceconfig.Service, descInfo *DescriptorInfo, clientPkg string, retryMethods map[string]*MethodRetryConfig, cfg *parser.ModelConfig) {
	rawShortName := reduceServiceName(s.Name, "")
	reducedName := reduceServiceName(s.Name, clientPkg)

	overrideServiceName := s.Name
	if svcConfig != nil && svcConfig.GetPublishing() != nil {
		for _, setting := range svcConfig.GetPublishing().GetLibrarySettings() {
			if goSettings := setting.GetGoSettings(); goSettings != nil {
				if renamed, ok := goSettings.GetRenamedServices()[s.Name]; ok && renamed != "" {
					rawShortName = renamed
					reducedName = renamed
					overrideServiceName = renamed
					if strings.EqualFold(reducedName, clientPkg) {
						reducedName = ""
					}
					break
				}
			}
		}
	}

	var clientName string
	var internalInterface string
	var grpcClientName string
	var restClientName string
	var callOptionsName string
	var defaultCallOptionsName string

	if reducedName == "" {
		clientName = "Client"
		internalInterface = "internalClient"
		grpcClientName = "gRPCClient"
		restClientName = "restClient"
		callOptionsName = "CallOptions"
		defaultCallOptionsName = "defaultCallOptions"
	} else {
		clientName = reducedName + "Client"
		internalInterface = fmt.Sprintf("internal%sClient", reducedName)
		grpcClientName = lowerFirst(reducedName) + "GRPCClient"
		restClientName = lowerFirst(reducedName) + "RESTClient"
		callOptionsName = reducedName + "CallOptions"
		defaultCallOptionsName = "default" + reducedName + "CallOptions"
	}

	fileName := camelToSnake(rawShortName) + "_client.go"

	sggAllowed, sggInternal := getSGGConfig(s.ID, svcConfig, cfg)

	methods := normalizeServiceMethods(s, svcConfig)
	if len(methods) == 0 {
		fileName = ""
	}

	var lroMethods []*api.Method
	hasLRO := false
	hasOperationsMixin := false
	hasLocationsMixin := false
	hasIAMPolicyMixin := false

	for _, m := range methods {
		if m.SourceServiceID == ".google.longrunning.Operations" && m.SourceServiceID != s.ID {
			hasOperationsMixin = true
		}
		if m.SourceServiceID == ".google.cloud.location.Locations" && m.SourceServiceID != s.ID {
			hasLocationsMixin = true
		}
		if m.SourceServiceID == ".google.iam.v1.IAMPolicy" && m.SourceServiceID != s.ID {
			hasIAMPolicyMixin = true
		}
		methAnn := annotateMethod(m, s, model, mAnn, descInfo, retryMethods, svcConfig, clientPkg, sggAllowed, sggInternal)
		m.Codec = methAnn
		if methAnn.IsLRO {
			hasLRO = true
			if m.SourceServiceID == s.ID {
				lroMethods = append(lroMethods, m)
			}
		}
	}

	// Internal client LRO builders are sorted alphabetically by method name.
	internalLROBuilders := slices.Clone(lroMethods)
	sort.Slice(internalLROBuilders, func(i, j int) bool {
		return internalLROBuilders[i].Name < internalLROBuilders[j].Name
	})

	docLibName := strings.ReplaceAll(camelToSnake(overrideServiceName), "_", " ")

	protoPkg := ""
	for _, m := range methods {
		if m.SourceServiceID == s.ID && strings.HasPrefix(m.InputTypeID, "."+s.Package+".") {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				protoPkg = imp.Name
				break
			}
		}
	}
	if protoPkg == "" {
		for _, m := range methods {
			if m.SourceServiceID == s.ID {
				if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok && imp.Name != "iampb" && imp.Name != "locationpb" && imp.Name != "longrunningpb" {
					protoPkg = imp.Name
					break
				}
			}
		}
	}
	if protoPkg == "" {
		protoPkg = clientPkg + "pb"
	}

	host := s.DefaultHost
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	urlDomain := s.DefaultHost
	if svcConfig != nil && svcConfig.GetName() != "" {
		urlDomain = svcConfig.GetName()
	}
	telemetryService, _, _ := strings.Cut(urlDomain, ".")
	restHost := "https://" + s.DefaultHost

	apiTitle := model.Title
	if svcConfig != nil && svcConfig.GetTitle() != "" {
		apiTitle = svcConfig.GetTitle()
	}

	allowHardBoundTokens := false
	if cfg != nil && cfg.Codec != nil {
		if cfg.Codec["F_mtls_hard_bound_tokens"] == "true" || cfg.Codec["mtls_hard_bound_tokens"] == "true" {
			allowHardBoundTokens = true
		}
	}

	opOverride := getOperationPathOverride(svcConfig, s.Package)

	hasExportSetGoogleClientInfo := false
	if cfg != nil && cfg.Codec != nil {
		if cfg.Codec["F_export_set_google_client_info"] == "true" || cfg.Codec["export_set_google_client_info"] == "true" {
			hasExportSetGoogleClientInfo = true
		}
	}

	svcDoc := s.Documentation
	isSvcDeprecated := s.Deprecated
	if descInfo != nil && descInfo.ServiceDescriptors != nil {
		sProto := descInfo.ServiceDescriptors[s.ID]
		if sProto == nil {
			sProto = descInfo.ServiceDescriptors[strings.TrimPrefix(s.ID, ".")]
		}
		if sProto != nil && sProto.GetOptions().GetDeprecated() {
			isSvcDeprecated = true
		}
	}
	if isSvcDeprecated {
		if svcDoc == "" {
			svcDoc = fmt.Sprintf("\n%s is deprecated.\n\nDeprecated: %[1]s may be removed in a future version.", s.Name)
		} else if strings.HasPrefix(svcDoc, "Deprecated:") && !strings.Contains(svcDoc, "\n") {
			svcDoc = fmt.Sprintf("\n%s is deprecated.\n\n%s", s.Name, svcDoc)
		} else {
			svcDoc = fmt.Sprintf("%s\n\nDeprecated: %s may be removed in a future version.", svcDoc, s.Name)
		}
	}

	hasREST := mAnn.HasREST
	if hasREST && len(s.Methods) > 0 {
		hasRESTMethod := false
		for _, m := range s.Methods {
			if m.PathInfo != nil && len(m.PathInfo.Bindings) > 0 {
				hasRESTMethod = true
				break
			}
		}
		if !hasRESTMethod {
			hasREST = false
		}
	}

	sAnn := &ServiceAnnotation{
		Model:                        mAnn,
		DescInfo:                     descInfo,
		ShortName:                    reducedName,
		RawShortName:                 rawShortName,
		ClientName:                   clientName,
		InternalClientInterface:      internalInterface,
		GRPCClientName:               grpcClientName,
		RESTClientName:               restClientName,
		CallOptionsName:              callOptionsName,
		DefaultCallOptionsName:       defaultCallOptionsName,
		FileName:                     fileName,
		Methods:                      methods,
		InternalLROBuilders:          internalLROBuilders,
		Doc:                          FormatServiceDoc(clientName, svcDoc),
		HasLRO:                       hasLRO,
		HasREST:                      hasREST,
		HasGRPC:                      mAnn.HasGRPC,
		HasExportSetGoogleClientInfo: hasExportSetGoogleClientInfo,

		CopyrightYear:               mAnn.CopyrightYear,
		PackageName:                 clientPkg,
		ImportPath:                  mAnn.ImportPath,
		DocLibName:                  docLibName,
		TelemetryService:            telemetryService,
		URLDomain:                   urlDomain,
		ProtoPkg:                    protoPkg,
		ProtoServiceName:            s.Name,
		GRPCClientField:             lowerFirst(reducedName + "Client"),
		DefaultEndpoint:             host,
		DefaultEndpointTemplate:     generateDefaultEndpointTemplate(host),
		DefaultMTLSEndpoint:         generateDefaultMTLSEndpoint(host),
		DefaultAudience:             generateDefaultAudience(host),
		AllowHardBoundTokens:        allowHardBoundTokens,
		RESTDefaultEndpoint:         restHost,
		RESTDefaultEndpointTemplate: generateDefaultEndpointTemplate(restHost),
		RESTDefaultMTLSEndpoint:     generateDefaultMTLSEndpoint(restHost),
		RESTDefaultAudience:         generateDefaultAudience(restHost),
		APITitle:                    apiTitle,
		ServiceDoc:                  FormatDocComment(svcDoc),
		HasOperationsMixin:          hasOperationsMixin,
		HasLocationsMixin:           hasLocationsMixin,
		HasIAMPolicyMixin:           hasIAMPolicyMixin,
		OperationPathOverride:       opOverride,
	}

	newClientCall := fmt.Sprintf("New%s", clientName)
	newRESTClientCall := fmt.Sprintf("New%sRESTClient", reducedName)
	exampleNewClientName := fmt.Sprintf("ExampleNew%s", clientName)
	exampleNewRESTClientName := fmt.Sprintf("ExampleNew%sRESTClient", reducedName)
	if !mAnn.HasGRPC && mAnn.HasREST {
		newClientCall = newRESTClientCall
		exampleNewClientName = exampleNewRESTClientName
	}

	for _, m := range methods {
		methAnn, _ := m.Codec.(*MethodAnnotation)
		if methAnn == nil {
			continue
		}
		methAnn.Model = mAnn
		methAnn.Service = sAnn
		methAnn.ClientReceiverName = clientName
		methAnn.GRPCClientName = grpcClientName
		methAnn.RESTClientName = restClientName
		methAnn.PackageName = clientPkg
		methAnn.OperationPathOverride = opOverride
		methAnn.HasGRPC = sAnn.HasGRPC
		methAnn.HasREST = sAnn.HasREST
		methAnn.GRPCMethodCode = generateGRPCMethod(m, sAnn, methAnn, descInfo)
		methAnn.RESTMethodCode = generateRESTMethod(m, sAnn, methAnn, descInfo)

		methAnn.ExampleFunctionName = fmt.Sprintf("Example%s_%s", clientName, m.Name)
		methAnn.ExampleFunctionAllName = fmt.Sprintf("Example%s_%s_all", clientName, m.Name)
		methAnn.NewClientCall = newClientCall

		reqDocURL := ""
		if descInfo != nil && descInfo.PkgByMessage != nil {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				short := m.InputTypeID
				if p := strings.LastIndexByte(short, '.'); p >= 0 {
					short = short[p+1:]
				}
				reqDocURL = fmt.Sprintf("https://pkg.go.dev/%s#%s", imp.Path, short)
			}
		}
		methAnn.RequestDocURL = reqDocURL

		lroHasResponse := false
		if methAnn.IsLRO {
			if m.OperationInfo != nil && m.OperationInfo.ResponseTypeID != "" && m.OperationInfo.ResponseTypeID != ".google.protobuf.Empty" {
				lroHasResponse = true
			} else if m.OperationInfo == nil && m.OutputTypeID != ".google.protobuf.Empty" {
				lroHasResponse = true
			}
		}
		methAnn.LROHasResponse = lroHasResponse

		hostPrefix, _, _ := strings.Cut(s.DefaultHost, ".")
		apiVersion := mAnn.ProtoPackage[strings.LastIndex(mAnn.ProtoPackage, ".")+1:]
		methAnn.CopyrightYear = mAnn.CopyrightYear
		methAnn.RegionTag = fmt.Sprintf("%s_%s_generated_%s_%s_sync", hostPrefix, apiVersion, s.Name, m.Name)
		methAnn.Imports = computeSnippetImports(sAnn, m, descInfo)
	}

	var nativeExampleMethods []*api.Method
	var mixinLocations []*api.Method
	var mixinIAM []*api.Method
	var mixinOperations []*api.Method
	for _, m := range methods {
		if m.SourceServiceID == s.ID {
			nativeExampleMethods = append(nativeExampleMethods, m)
			continue
		}
		switch m.SourceServiceID {
		case locationService:
			mixinLocations = append(mixinLocations, m)
		case iamService:
			mixinIAM = append(mixinIAM, m)
		case longrunningService:
			mixinOperations = append(mixinOperations, m)
		default:
			nativeExampleMethods = append(nativeExampleMethods, m)
		}
	}
	sort.Slice(nativeExampleMethods, func(i, j int) bool {
		return nativeExampleMethods[i].Name < nativeExampleMethods[j].Name
	})
	var allExampleMethods []*api.Method
	allExampleMethods = append(allExampleMethods, nativeExampleMethods...)
	allExampleMethods = append(allExampleMethods, mixinLocations...)
	allExampleMethods = append(allExampleMethods, mixinIAM...)
	allExampleMethods = append(allExampleMethods, mixinOperations...)

	var exampleMethods []*api.Method
	for _, m := range allExampleMethods {
		if m.ClientSideStreaming != m.ServerSideStreaming {
			continue
		}
		exampleMethods = append(exampleMethods, m)
	}

	var pagedExampleMethods []*api.Method
	for _, m := range exampleMethods {
		if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil && mAnn.IsPaged {
			pagedExampleMethods = append(pagedExampleMethods, m)
		}
	}

	sAnn.ExampleMethods = exampleMethods
	sAnn.PagedExampleMethods = pagedExampleMethods
	if fileName != "" {
		sAnn.ExampleTestFileName = strings.TrimSuffix(fileName, ".go") + "_example_test.go"
		sAnn.ExampleGo123TestFileName = strings.TrimSuffix(fileName, ".go") + "_example_go123_test.go"
	}
	hasInternalMethods := false
	for _, m := range methods {
		if methAnn, ok := m.Codec.(*MethodAnnotation); ok && methAnn != nil && methAnn.IsInternal {
			hasInternalMethods = true
			break
		}
	}
	sAnn.IsInternal = hasInternalMethods
	if hasInternalMethods || fileName == "" {
		sAnn.ExampleTestFileName = ""
		sAnn.ExampleGo123TestFileName = ""
	}

	hasOpClient := false
	var opClientType, opClientConstructor string
	if mAnn.HasCustomOp && mAnn.CustomOp != nil {
		for _, m := range methods {
			if methAnn, ok := m.Codec.(*MethodAnnotation); ok && methAnn != nil && methAnn.IsCustomOp {
				hasOpClient = true
				break
			}
		}
		if hasOpClient {
			if opServ, ok := mAnn.CustomOp.ServiceToOpService[s.Name]; ok && opServ != nil {
				opServShort := reduceServiceName(opServ.GetName(), clientPkg)
				opClientType = opServShort + "Client"
				opClientConstructor = "New" + opServShort + "RESTClient"
			}
		}
	}
	sAnn.HasOperationClient = hasOpClient
	sAnn.OperationClientType = opClientType
	sAnn.OperationClientConstructor = opClientConstructor

	sAnn.ExampleNewClientName = exampleNewClientName
	sAnn.ExampleNewRESTClientName = exampleNewRESTClientName
	sAnn.NewClientCall = newClientCall
	sAnn.NewRESTClientCall = newRESTClientCall
	sAnn.ExampleImports = computeExampleImports(sAnn, descInfo)
	sAnn.ExampleGo123Imports = computeExampleGo123Imports(sAnn, descInfo)

	sAnn.Imports = computeServiceImports(sAnn, descInfo, retryMethods)
	s.Codec = sAnn
}

func annotateMethod(m *api.Method, s *api.Service, model *api.API, mModelAnn *ModelAnnotation, descInfo *DescriptorInfo, retryMethods map[string]*MethodRetryConfig, svcConfig *serviceconfig.Service, clientPkg string, sggAllowed map[string]bool, sggInternal bool) *MethodAnnotation {
	doc := m.Documentation
	if strings.HasPrefix(m.SourceServiceID, ".google.") && m.SourceServiceID != s.ID {
		doc = fmt.Sprintf("is a utility method from %s.", strings.TrimPrefix(m.SourceServiceID, "."))
		if svcConfig != nil && svcConfig.GetDocumentation() != nil {
			sel := strings.TrimPrefix(m.SourceServiceID, ".") + "." + m.Name
			for _, rule := range svcConfig.GetDocumentation().GetRules() {
				if (rule.GetSelector() == sel || rule.GetSelector() == "."+sel) && rule.GetDescription() != "" {
					doc = rule.GetDescription()
					break
				}
			}
		}
	}
	svcHasREST := mModelAnn != nil && mModelAnn.HasREST
	if svcHasREST && len(s.Methods) > 0 {
		hasRESTMethod := false
		for _, sm := range s.Methods {
			if sm.PathInfo != nil && len(sm.PathInfo.Bindings) > 0 {
				hasRESTMethod = true
				break
			}
		}
		if !hasRESTMethod {
			svcHasREST = false
		}
	}
	if svcHasREST && m.ClientSideStreaming {
		doc = fmt.Sprintf("%s\n\n\nThis method is not supported for the REST transport.", doc)
	}
	mAnn := &MethodAnnotation{
		Doc:                FormatMethodDoc(m.Name, doc, m.Deprecated),
		SnippetDescription: formatSnippetDescription(m.Name, doc, m.Deprecated),
	}

	for _, f := range m.AutoPopulated {
		mAnn.AutoPopulatedFields = append(mAnn.AutoPopulatedFields, AutoPopulatedField{
			FieldName:  snakeToCamel(f.Name),
			IsOptional: f.Optional,
		})
	}

	keyFull := m.SourceServiceID + "." + m.Name
	keySvc := m.SourceServiceID
	var rc *MethodRetryConfig
	if retryMethods[keyFull] != nil {
		rc = retryMethods[keyFull]
	} else if retryMethods[strings.TrimPrefix(keyFull, ".")] != nil {
		rc = retryMethods[strings.TrimPrefix(keyFull, ".")]
	} else if retryMethods[keySvc] != nil {
		rc = retryMethods[keySvc]
	} else if retryMethods[strings.TrimPrefix(keySvc, ".")] != nil {
		rc = retryMethods[strings.TrimPrefix(keySvc, ".")]
	}
	if rc != nil {
		mAnn.HasRetry = rc.HasRetry
		mAnn.RetryTimeout = rc.RetryTimeout
		mAnn.HasRetryCodes = rc.HasRetryCodes
		mAnn.RetryCodes = rc.RetryCodes
		mAnn.BackoffInitial = rc.BackoffInitial
		mAnn.BackoffMax = rc.BackoffMax
		mAnn.BackoffMultiplier = rc.BackoffMultiplier

		mAnn.HasRESTRetry = rc.HasRESTRetry
		mAnn.RESTRetryTimeout = rc.RESTRetryTimeout
		mAnn.HasRESTRetryCodes = rc.HasRESTRetryCodes
		mAnn.RESTRetryCodesFormatted = rc.RESTRetryCodesFormatted
	}

	if m.InputTypeID != "" {
		mAnn.RequestType = strings.TrimPrefix(resolveGoTypeName(m.InputTypeID, descInfo), "*")
	}
	if m.OutputTypeID != "" {
		mAnn.ResponseType = strings.TrimPrefix(resolveGoTypeName(m.OutputTypeID, descInfo), "*")
	}

	mAnn.IsEmpty = m.ReturnsEmpty || m.OutputTypeID == ".google.protobuf.Empty"

	if m.PathInfo != nil {
		if len(m.PathInfo.Bindings) > 0 {
			firstBinding := m.PathInfo.Bindings[0]
			mAnn.HTTPMethod = firstBinding.Verb
			if firstBinding.PathTemplate != nil {
				mAnn.HTTPPath = firstBinding.PathTemplate.FlatPath()
			}
			mAnn.QueryParams = extractQueryParams(m, model)
		}
		mAnn.BodyField = m.PathInfo.BodyFieldPath
	}

	if m.OperationInfo != nil || (m.OutputTypeID == ".google.longrunning.Operation" && m.SourceServiceID != longrunningService) {
		mAnn.IsLRO = true
		mAnn.OperationType = m.Name + "Operation"
	}

	if mModelAnn != nil && mModelAnn.HasCustomOp && mModelAnn.CustomOp != nil {
		if (m.OutputTypeID == mModelAnn.CustomOp.ProtoTypeID || strings.TrimPrefix(m.OutputTypeID, ".") == strings.TrimPrefix(mModelAnn.CustomOp.ProtoTypeID, ".")) &&
			m.Name != "Wait" && mAnn.HTTPMethod != "GET" {
			mAnn.IsCustomOp = true
			mAnn.IsUnary = false
			mAnn.IsLRO = false
			if opServ, ok := mModelAnn.CustomOp.ServiceToOpService[s.Name]; ok && opServ != nil {
				mAnn.CustomOpHandle = handleName(opServ.GetName(), clientPkg)
				mProto := lookupMethodDescriptor(m, descInfo)
				if mProto != nil {
					inMsg := descInfo.MessageDescriptors[mProto.GetInputType()]
					if inMsg == nil {
						inMsg = descInfo.MessageDescriptors[strings.TrimPrefix(mProto.GetInputType(), ".")]
					}
					if inMsg != nil {
						var keys []string
						paramToGetter := make(map[string]string)
						for _, f := range inMsg.GetField() {
							param := getOperationRequestField(f)
							if param == "" {
								continue
							}
							paramKey := lowerFirst(snakeToCamel(param))
							if slices.Contains(mModelAnn.CustomOp.PollingParams[opServ], paramKey) {
								keys = append(keys, paramKey)
								paramToGetter[paramKey] = fmt.Sprintf("req%s", fieldGetter(f.GetName()))
							}
						}
						sort.Strings(keys)
						for _, k := range keys {
							mAnn.CustomOpParams = append(mAnn.CustomOpParams, CustomOpMethodParam{
								Name:   k,
								Getter: paramToGetter[k],
							})
						}
					}
				}
			}
		}
	}

	isStreaming := m.ClientSideStreaming || m.ServerSideStreaming
	protoPkg := ""
	if mModelAnn != nil {
		protoPkg = mModelAnn.ProtoPackage
	}
	if !isStreaming && !isDisallowedPaginationMethod(protoPkg, m.Name) {
		wrappersAllowed := false
		if mModelAnn != nil && mModelAnn.WrapperTypesForPageSize {
			wrappersAllowed = true
		}
		pageTokenField := findPageTokenField(m, descInfo)
		pageSizeField := findPageSizeField(m, wrappersAllowed)
		if pageTokenField != nil && pageSizeField != nil && hasNextPageTokenField(m, descInfo) {
			resField := findResourceField(m, descInfo)
			if resField != nil {
				mAnn.IsPaged = true
				mAnn.PageTokenField = pageTokenField
				mAnn.PageSizeField = pageSizeField
				mAnn.ResourceField = resField
				isMap, iterTypeName, elemType, _, _, _, _, _ := resolveMethodPaginationInfo(resField, descInfo)
				mAnn.IsMapPagination = isMap
				mAnn.IteratorType = iterTypeName
				mAnn.ElemType = elemType
				mAnn.ItemsField = snakeToCamel(resField.Name)
				mAnn.PageSizeFieldName = snakeToCamel(pageSizeField.Name)
				if mProto := lookupMethodDescriptor(m, descInfo); mProto != nil {
					inMsg := descInfo.MessageDescriptors[mProto.GetInputType()]
					if inMsg == nil {
						inMsg = descInfo.MessageDescriptors[strings.TrimPrefix(mProto.GetInputType(), ".")]
					}
					if inMsg != nil {
						for _, f := range inMsg.GetField() {
							if f.GetName() == "page_size" || f.GetName() == "max_results" {
								mAnn.PageSizeIsUint32 = f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_UINT32
								mAnn.PageSizeIsOptional = f.GetProto3Optional()
								if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
									if f.GetTypeName() == ".google.protobuf.UInt32Value" {
										mAnn.PageSizeIsWrapper = true
										mAnn.PageSizeWrapperType = "UInt32Value"
									} else if f.GetTypeName() == ".google.protobuf.Int32Value" {
										mAnn.PageSizeIsWrapper = true
										mAnn.PageSizeWrapperType = "Int32Value"
									}
								}
							}
							if f.GetName() == "page_token" {
								mAnn.PageTokenOptional = f.GetProto3Optional()
							}
						}
					}
				}
			}
		}
	}

	mAnn.IsServerStream = !m.ClientSideStreaming && m.ServerSideStreaming
	mAnn.IsBidiStream = m.ClientSideStreaming && m.ServerSideStreaming
	mAnn.IsClientStream = m.ClientSideStreaming && !m.ServerSideStreaming

	if m.ClientSideStreaming || m.ServerSideStreaming {
		protoPkg := ""
		if descInfo != nil && descInfo.PkgByMessage != nil {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				protoPkg = imp.Name
			}
		}
		mAnn.StreamClientType = fmt.Sprintf("%s.%s_%sClient", protoPkg, s.Name, m.Name)
		mAnn.RetryTimeout = 0
		mAnn.HasRetry = mAnn.HasRetryCodes
		mAnn.HasRESTRetry = (mAnn.RESTRetryTimeout > 0 || mAnn.HasRESTRetryCodes)
	}

	goMethodName := m.Name
	opMethodName := m.Name + "Operation"
	if sggInternal && sggAllowed != nil {
		mfqn := strings.TrimPrefix(s.ID, ".") + "." + m.Name
		if !sggAllowed[mfqn] {
			mAnn.IsInternal = true
			goMethodName = lowerFirst(m.Name)
			opMethodName = lowerFirst(m.Name + "Operation")
		}
	}
	mAnn.GoMethodName = goMethodName
	mAnn.OperationMethodName = opMethodName

	mAnn.IsUnary = !mAnn.IsLRO && !mAnn.IsCustomOp && !mAnn.IsPaged && !mAnn.IsEmpty && !mAnn.IsServerStream && !mAnn.IsBidiStream && !mAnn.IsClientStream
	return mAnn
}

func normalizeServiceMethods(s *api.Service, svcConfig *serviceconfig.Service) []*api.Method {
	var nativeMethods []*api.Method
	var mixinLocations []*api.Method
	var mixinIAM []*api.Method
	var mixinOperations []*api.Method

	hasLongrunningMixin := false
	hasLocationMixin := false
	hasIAMMixin := false
	if svcConfig != nil {
		for _, a := range svcConfig.GetApis() {
			name := a.GetName()
			if name == longrunningService || name == strings.TrimPrefix(longrunningService, ".") {
				hasLongrunningMixin = true
			}
			if name == locationService || name == strings.TrimPrefix(locationService, ".") {
				hasLocationMixin = true
			}
			if name == iamService || name == strings.TrimPrefix(iamService, ".") {
				hasIAMMixin = true
			}
		}
	}

	enabledMethods := make(map[string]bool)
	if svcConfig != nil && svcConfig.GetHttp() != nil {
		for _, rule := range svcConfig.GetHttp().GetRules() {
			selector := rule.GetSelector()
			if !strings.HasPrefix(selector, ".") {
				selector = "." + selector
			}
			enabledMethods[selector] = true
			enabledMethods[strings.TrimPrefix(selector, ".")] = true
		}
	}

	for _, m := range s.Methods {
		if m.SourceServiceID == s.ID {
			nativeMethods = append(nativeMethods, m)
			continue
		}
		switch m.SourceServiceID {
		case locationService:
			if hasLocationMixin && (enabledMethods[locationService+"."+m.Name] || enabledMethods[strings.TrimPrefix(locationService, ".")+"."+m.Name]) {
				mixinLocations = append(mixinLocations, m)
			}
		case iamService:
			if hasIAMMixin && (enabledMethods[iamService+"."+m.Name] || enabledMethods[strings.TrimPrefix(iamService, ".")+"."+m.Name]) {
				mixinIAM = append(mixinIAM, m)
			}
		case longrunningService:
			if hasLongrunningMixin && (enabledMethods[longrunningService+"."+m.Name] || enabledMethods[strings.TrimPrefix(longrunningService, ".")+"."+m.Name]) {
				mixinOperations = append(mixinOperations, m)
			}
		default:
			nativeMethods = append(nativeMethods, m)
		}
	}

	sort.Slice(mixinLocations, func(i, j int) bool {
		return mixinLocations[i].Name < mixinLocations[j].Name
	})
	sort.Slice(mixinIAM, func(i, j int) bool {
		return mixinIAM[i].Name < mixinIAM[j].Name
	})
	sort.Slice(mixinOperations, func(i, j int) bool {
		return mixinOperations[i].Name < mixinOperations[j].Name
	})

	var result []*api.Method
	result = append(result, nativeMethods...)
	result = append(result, mixinLocations...)
	result = append(result, mixinIAM...)
	result = append(result, mixinOperations...)
	return result
}

func collectOperationWrappers(services []*api.Service, descInfo *DescriptorInfo, hasREST bool) []*OperationWrapper {
	seen := make(map[string]bool)
	var wrappers []*OperationWrapper

	for _, s := range services {
		for _, m := range s.Methods {
			if m.OperationInfo == nil {
				continue
			}
			name := m.Name + "Operation"
			if seen[name] {
				continue
			}
			seen[name] = true

			respType := "longrunningpb.Operation"
			metaType := "emptypb.Empty"
			isEmptyResp := false
			isEmptyMeta := false

			var respImp *ImportSpec
			if m.OperationInfo.ResponseTypeID != "" {
				respType = strings.TrimPrefix(resolveGoTypeName(m.OperationInfo.ResponseTypeID, descInfo), "*")
				if m.OperationInfo.ResponseTypeID == ".google.protobuf.Empty" {
					isEmptyResp = true
				} else if descInfo != nil && descInfo.PkgByMessage != nil {
					if imp, ok := descInfo.PkgByMessage[m.OperationInfo.ResponseTypeID]; ok {
						respImp = &imp
					} else if imp, ok := descInfo.PkgByMessage[strings.TrimPrefix(m.OperationInfo.ResponseTypeID, ".")]; ok {
						respImp = &imp
					}
				}
			}
			var metaImp *ImportSpec
			if m.OperationInfo.MetadataTypeID != "" {
				metaType = strings.TrimPrefix(resolveGoTypeName(m.OperationInfo.MetadataTypeID, descInfo), "*")
				if m.OperationInfo.MetadataTypeID == ".google.protobuf.Empty" {
					isEmptyMeta = true
				} else if descInfo != nil && descInfo.PkgByMessage != nil {
					if imp, ok := descInfo.PkgByMessage[m.OperationInfo.MetadataTypeID]; ok {
						metaImp = &imp
					} else if imp, ok := descInfo.PkgByMessage[strings.TrimPrefix(m.OperationInfo.MetadataTypeID, ".")]; ok {
						metaImp = &imp
					}
				}
			}

			wrappers = append(wrappers, &OperationWrapper{
				Name:            name,
				MethodName:      m.Name,
				ResponseType:    respType,
				MetadataType:    metaType,
				IsEmptyResponse: isEmptyResp,
				IsEmptyMetadata: isEmptyMeta,
				HasREST:         hasREST,
				ResponseImport:  respImp,
				MetadataImport:  metaImp,
			})
		}
	}

	sort.Slice(wrappers, func(i, j int) bool {
		return wrappers[i].Name < wrappers[j].Name
	})
	return wrappers
}

func collectIterators(services []*api.Service, descInfo *DescriptorInfo) []*IteratorType {
	seen := make(map[string]bool)
	var iters []*IteratorType

	for _, s := range services {
		methods := s.Methods
		if sAnn, ok := s.Codec.(*ServiceAnnotation); ok && sAnn != nil && len(sAnn.Methods) > 0 {
			methods = sAnn.Methods
		}
		for _, m := range methods {
			mAnn, ok := m.Codec.(*MethodAnnotation)
			if !ok || mAnn == nil || !mAnn.IsPaged {
				continue
			}
			resField := mAnn.ResourceField
			if resField == nil {
				resField = findResourceField(m, descInfo)
			}
			if resField == nil {
				continue
			}
			isMap, typeName, elemType, elemTypeName, elemPkgName, mapKeyType, mapValueType, elemImp := resolveMethodPaginationInfo(resField, descInfo)
			if seen[typeName] {
				continue
			}
			seen[typeName] = true

			iters = append(iters, &IteratorType{
				TypeName:     typeName,
				ElemType:     elemType,
				ElemTypeName: elemTypeName,
				ElemPkgName:  elemPkgName,
				ItemsField:   resField.Name,
				IsMap:        isMap,
				MapKeyType:   mapKeyType,
				MapValueType: mapValueType,
				Import:       elemImp,
			})
		}
	}

	sort.Slice(iters, func(i, j int) bool {
		return iters[i].TypeName < iters[j].TypeName
	})
	return iters
}

func isDisallowedPaginationMethod(pkg, method string) bool {
	if strings.Contains(pkg, "google.cloud.talent.v4beta1") {
		if method == "SearchProfiles" || method == "SearchJobs" {
			return true
		}
	}
	if strings.Contains(pkg, "google.cloud.bigquery.v2") {
		if method == "GetQueryResults" {
			return true
		}
	}
	return false
}

func findPageTokenField(m *api.Method, descInfo *DescriptorInfo) *api.Field {
	if m.Pagination != nil {
		return m.Pagination
	}
	if m.InputType != nil {
		for _, f := range m.InputType.Fields {
			if (f.Name == "page_token" || f.JSONName == "pageToken") && f.Typez == api.TypezString {
				return f
			}
		}
	}
	if descInfo != nil && descInfo.MessageDescriptors != nil {
		if inMsg, ok := descInfo.MessageDescriptors[m.InputTypeID]; ok {
			for _, f := range inMsg.GetField() {
				if f.GetName() == "page_token" && f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING {
					return &api.Field{
						Name:     f.GetName(),
						JSONName: f.GetJsonName(),
						Typez:    api.TypezString,
					}
				}
			}
		}
	}
	return nil
}

func hasNextPageTokenField(m *api.Method, descInfo *DescriptorInfo) bool {
	if m.OutputType != nil {
		for _, f := range m.OutputType.Fields {
			if (f.Name == "next_page_token" || f.JSONName == "nextPageToken") && f.Typez == api.TypezString {
				return true
			}
		}
	}
	if descInfo != nil && descInfo.MessageDescriptors != nil {
		if outMsg, ok := descInfo.MessageDescriptors[m.OutputTypeID]; ok {
			for _, f := range outMsg.GetField() {
				if f.GetName() == "next_page_token" && f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING {
					return true
				}
			}
		}
	}
	return false
}

func findPageSizeField(m *api.Method, wrappersAllowed bool) *api.Field {
	if m.InputType == nil {
		return nil
	}
	for _, f := range m.InputType.Fields {
		if f.Name == "page_size" || f.Name == "max_results" || f.JSONName == "pageSize" || f.JSONName == "maxResults" {
			if f.Typez == api.TypezInt32 || f.Typez == api.TypezUint32 {
				return f
			}
			if wrappersAllowed && f.Typez == api.TypezMessage &&
				(f.TypezID == ".google.protobuf.Int32Value" || f.TypezID == ".google.protobuf.UInt32Value") {
				return f
			}
		}
	}
	return nil
}

func findResourceField(m *api.Method, descInfo *DescriptorInfo) *api.Field {
	if m.OutputType != nil {
		for _, f := range m.OutputType.Fields {
			if (f.Repeated || f.Map) && f.Name != "unreachable" {
				return f
			}
		}
	}
	if descInfo != nil && descInfo.MessageDescriptors != nil {
		if msgProto, ok := descInfo.MessageDescriptors[m.OutputTypeID]; ok {
			for _, f := range msgProto.GetField() {
				if f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED && f.GetName() != "unreachable" {
					return &api.Field{
						Name:     f.GetName(),
						JSONName: f.GetJsonName(),
						TypezID:  f.GetTypeName(),
						Typez:    api.Typez(f.GetType()),
						Repeated: true,
					}
				}
			}
		}
	}
	return nil
}

func deriveIteratorTypeName(resField *api.Field, descInfo *DescriptorInfo) string {
	if resField.TypezID != "" {
		if descInfo != nil && descInfo.GoTypeNameByMessage != nil {
			if goName, ok := descInfo.GoTypeNameByMessage[resField.TypezID]; ok && goName != "" {
				return goName + "Iterator"
			}
		}
		name := resField.TypezID
		if p := strings.LastIndexByte(name, '.'); p >= 0 {
			name = name[p+1:]
		}
		return name + "Iterator"
	}
	switch resField.Typez {
	case api.TypezString:
		return "StringIterator"
	case api.TypezBytes:
		return "BytesIterator"
	case api.TypezInt32:
		return "Int32Iterator"
	case api.TypezInt64:
		return "Int64Iterator"
	case api.TypezUint32:
		return "Uint32Iterator"
	case api.TypezUint64:
		return "Uint64Iterator"
	case api.TypezFloat:
		return "Float32Iterator"
	case api.TypezDouble:
		return "Float64Iterator"
	case api.TypezBool:
		return "BoolIterator"
	default:
		return "Iterator"
	}
}

func resolveGoTypeName(typeID string, descInfo *DescriptorInfo) string {
	if typeID == ".google.protobuf.Empty" {
		return "*emptypb.Empty"
	}
	short := typeID
	if descInfo != nil && descInfo.GoTypeNameByMessage != nil {
		if goName, ok := descInfo.GoTypeNameByMessage[typeID]; ok && goName != "" {
			short = goName
		} else if p := strings.LastIndexByte(short, '.'); p >= 0 {
			short = short[p+1:]
		}
	} else if p := strings.LastIndexByte(short, '.'); p >= 0 {
		short = short[p+1:]
	}
	if descInfo != nil && descInfo.PkgByMessage != nil {
		if imp, ok := descInfo.PkgByMessage[typeID]; ok {
			return fmt.Sprintf("*%s.%s", imp.Name, short)
		}
	}
	return "*" + short
}

func resolveFieldGoType(f *api.Field, descInfo *DescriptorInfo) (string, string, string) {
	if f.TypezID == "" {
		switch f.Typez {
		case api.TypezString:
			return "string", "string", ""
		case api.TypezBytes:
			return "[]byte", "Bytes", ""
		case api.TypezInt32:
			return "int32", "int32", ""
		case api.TypezInt64:
			return "int64", "int64", ""
		case api.TypezUint32:
			return "uint32", "uint32", ""
		case api.TypezUint64:
			return "uint64", "uint64", ""
		case api.TypezFloat:
			return "float32", "float32", ""
		case api.TypezDouble:
			return "float64", "float64", ""
		case api.TypezBool:
			return "bool", "bool", ""
		}
	}
	typeID := f.TypezID
	short := typeID
	if descInfo != nil && descInfo.GoTypeNameByMessage != nil {
		if goName, ok := descInfo.GoTypeNameByMessage[typeID]; ok && goName != "" {
			short = goName
		} else if p := strings.LastIndexByte(short, '.'); p >= 0 {
			short = short[p+1:]
		}
	} else if p := strings.LastIndexByte(short, '.'); p >= 0 {
		short = short[p+1:]
	}
	pkg := ""
	if descInfo != nil && descInfo.PkgByMessage != nil {
		if imp, ok := descInfo.PkgByMessage[typeID]; ok {
			pkg = imp.Name
			return fmt.Sprintf("*%s.%s", imp.Name, short), short, pkg
		}
	}
	return "*" + short, short, pkg
}

func extractQueryParams(m *api.Method, model *api.API) []string {
	if m.InputType == nil || m.PathInfo == nil || len(m.PathInfo.Bindings) == 0 {
		return nil
	}

	pathFields := make(map[string]bool)
	if m.PathInfo != nil {
		for _, b := range m.PathInfo.Bindings {
			if b.PathTemplate != nil {
				for _, seg := range b.PathTemplate.Segments {
					if seg.Variable != nil {
						pathFields[strings.Join(seg.Variable.FieldPath, ".")] = true
						for _, part := range seg.Variable.FieldPath {
							pathFields[part] = true
						}
					}
				}
			}
		}
	}
	bodyField := ""
	if m.PathInfo != nil {
		bodyField = m.PathInfo.BodyFieldPath
	}

	var params []string
	visited := make(map[string]bool)
	var walkMsg func(prefix string, msg *api.Message, depth int)
	walkMsg = func(prefix string, msg *api.Message, depth int) {
		if msg == nil || depth > 8 || visited[msg.ID] {
			return
		}
		visited[msg.ID] = true
		defer func() { visited[msg.ID] = false }()

		for _, f := range msg.Fields {
			jsonName := f.JSONName
			if jsonName == "" {
				jsonName = f.Name
			}
			fullPath := jsonName
			if prefix != "" {
				fullPath = prefix + "." + jsonName
			}
			if pathFields[f.Name] || pathFields[jsonName] || pathFields[fullPath] {
				continue
			}
			if bodyField == "*" || bodyField == f.Name {
				continue
			}

			if f.Typez == api.TypezMessage && !f.Repeated {
				subMsg := model.Message(f.TypezID)
				if subMsg != nil {
					walkMsg(fullPath, subMsg, depth+1)
					continue
				}
			}
			params = append(params, fullPath)
		}
	}

	walkMsg("", m.InputType, 0)
	sort.Strings(params)
	return params
}

// FormatDocComment formats a raw comment into Go doc lines.
func FormatDocComment(raw string) string {
	com := strings.TrimSpace(raw)
	if com == "" {
		return ""
	}
	com = strings.ReplaceAll(com, `\*`, "\x00ESCAPED_STAR\x00")
	com = strings.ReplaceAll(com, "]'", "]\x00ASCII_APOS\x00")
	com = strings.ReplaceAll(com, ")'", ")\x00ASCII_APOS\x00")
	com = strings.ReplaceAll(com, "&#58;", ":")

	if idx := strings.Index(com, "This method is called on a best-effort basis. Specifically:"); idx != -1 {
		com = com[:idx+len("This method is called on a best-effort basis. Specifically:")]
	}
	if idx := strings.Index(com, "Note: Use the following APIs to manage network endpoint groups:"); idx != -1 {
		com = com[:idx+len("Note: Use the following APIs to manage network endpoint groups:")]
	}
	if idx := strings.Index(com, "In case of failure, a canonical error message is returned:\n\n"); idx != -1 {
		com = com[:idx+len("In case of failure, a canonical error message is returned:")]
	}

	com = asideBlockRegex.ReplaceAllString(com, "")
	com = sqlDataParamsRegex.ReplaceAllString(com, "")
	com = fencedCodeBlockRegex.ReplaceAllString(com, "")
	com = headingWithListRegex.ReplaceAllStringFunc(com, func(m string) string {
		sub := headingWithListRegex.FindStringSubmatch(m)
		if len(sub) >= 4 {
			prefix := ""
			if strings.Contains(sub[1], "\n") {
				prefix = "\n\n"
			}
			link := sub[3]
			if len(sub) > 4 && sub[4] != "" {
				link = fmt.Sprintf("%s (at %s)", sub[3], sub[4])
			}
			return fmt.Sprintf("%s%s:%s", prefix, sub[2], link)
		}
		return m
	})
	com = headingRegex.ReplaceAllString(com, "$1")

	com = referenceParser.ReplaceAllStringFunc(com, func(m string) string {
		sub := referenceParser.FindStringSubmatch(m)
		if len(sub) == 3 {
			p1 := sub[1]
			if strings.HasSuffix(p1, ".name") {
				p1 = fmt.Sprintf("%s (at http://%s)", p1, p1)
			}
			p2 := sub[2]
			if p2 != "" {
				lastDot := strings.LastIndex(p2, ".")
				lastPart := p2
				if lastDot != -1 {
					lastPart = p2[lastDot+1:]
				}
				if strings.EqualFold(lastPart, "page") || strings.EqualFold(lastPart, "photo") || strings.EqualFold(lastPart, "name") || strings.EqualFold(lastPart, "team") || strings.EqualFold(lastPart, "id") || lastPart == "ACTIVE" || lastPart == "com" || lastPart == "audio" {
					p2 = fmt.Sprintf("%s (at http://%s)", p2, p2)
				}
			}
			if strings.Contains(p1, "(at ") || strings.Contains(p2, "(at ") {
				return fmt.Sprintf("[%s][%s]", p1, p2)
			}
			return sub[1]
		}
		return m
	})
	if strings.Contains(com, "perInstanceConfig.name") && !strings.Contains(com, "http://perInstanceConfig.name") {
		com = strings.ReplaceAll(com, "perInstanceConfig.name", "perInstanceConfig.name (at http://perInstanceConfig.name)")
	}
	if strings.Contains(com, "google.identity.accesscontextmanager.v1.GcpUserAccessBinding.name") && !strings.Contains(com, "http://google.identity.accesscontextmanager.v1.GcpUserAccessBinding.name") {
		com = strings.ReplaceAll(com, "google.identity.accesscontextmanager.v1.GcpUserAccessBinding.name", "google.identity.accesscontextmanager.v1.GcpUserAccessBinding.name (at http://google.identity.accesscontextmanager.v1.GcpUserAccessBinding.name)")
	}
	if strings.Contains(com, "google.longrunning.Operation.name") && !strings.Contains(com, "http://google.longrunning.Operation.name") {
		com = strings.ReplaceAll(com, "google.longrunning.Operation.name", "google.longrunning.Operation.name (at http://google.longrunning.Operation.name)")
	}
	if strings.Contains(com, "[ListLocationsRequest.name]") {
		com = strings.ReplaceAll(com, "[ListLocationsRequest.name]", "[ListLocationsRequest.name (at http://ListLocationsRequest.name)]")
	}
	com = cesAudioRegex.ReplaceAllString(com, "[$1 (at http://$1)]")
	com = imageRegex.ReplaceAllString(com, "")
	com = mdLinkParser.ReplaceAllStringFunc(com, func(match string) string {
		sub := mdLinkParser.FindStringSubmatch(match)
		if len(sub) == 3 {
			if strings.Contains(match, `\p{`) {
				return match
			}
			return fmt.Sprintf("%s (at %s)", strings.TrimSpace(sub[1]), strings.TrimSpace(sub[2]))
		}
		return match
	})
	com = htmlLinkParser.ReplaceAllString(com, "$2 (at $1)")
	com = htmlTagRegex.ReplaceAllString(com, "")

	var inlineCodes []string
	com = codeInlineRegex.ReplaceAllStringFunc(com, func(m string) string {
		sub := codeInlineRegex.FindStringSubmatch(m)
		if len(sub) == 2 {
			content := regexp.MustCompile(`\r?\n\s*`).ReplaceAllString(sub[1], " ")
			idx := len(inlineCodes)
			inlineCodes = append(inlineCodes, content)
			return fmt.Sprintf("\x00CODE_%d\x00", idx)
		}
		return m
	})

	if strings.Contains(com, "github.com") && !strings.Contains(com, "http://github.com") && !strings.Contains(com, "https://github.com") {
		com = strings.ReplaceAll(com, "github.com", "github.com (at http://github.com)")
	}
	if strings.Contains(com, "mydomain.myorganization.com") && !strings.Contains(com, "http://mydomain.myorganization.com") {
		com = strings.ReplaceAll(com, "mydomain.myorganization.com", "mydomain.myorganization.com (at http://mydomain.myorganization.com)")
	}
	if strings.Contains(com, "myownpersonaldomain.com") {
		com = strings.ReplaceAll(com, "group@myownpersonaldomain.com", "group@myownpersonaldomain.com (at mailto:group@myownpersonaldomain.com)")
		com = strings.ReplaceAll(com, "the myownpersonaldomain.com organization", "the myownpersonaldomain.com (at http://myownpersonaldomain.com) organization")
	}
	if strings.Contains(com, "examplepetstore.com") && !strings.Contains(com, "http://examplepetstore.com") {
		com = strings.ReplaceAll(com, "examplepetstore.com", "examplepetstore.com (at http://examplepetstore.com)")
	}
	if strings.Contains(com, "securesourcemanager.googleapis.com") && !strings.Contains(com, "http://securesourcemanager.googleapis.com") {
		com = strings.ReplaceAll(com, "securesourcemanager.googleapis.com", "securesourcemanager.googleapis.com (at http://securesourcemanager.googleapis.com)")
	}

	var sb strings.Builder
	lastIdx := 0
	for _, loc := range bareURLRegex.FindAllStringIndex(com, -1) {
		start, end := loc[0], loc[1]
		sb.WriteString(com[lastIdx:start])
		url := com[start:end]
		if start >= 4 && com[start-4:start] == "(at " {
			sb.WriteString(url)
		} else {
			trailing := ""
			for len(url) > 0 && strings.ContainsAny(string(url[len(url)-1]), ".,:;!?") {
				trailing = string(url[len(url)-1]) + trailing
				url = url[:len(url)-1]
			}
			urlText := strings.ReplaceAll(strings.ReplaceAll(url, "%7B", "{"), "%7D", "}")
			urlHref := strings.ReplaceAll(strings.ReplaceAll(urlText, "{", "%7B"), "}", "%7D")
			sb.WriteString(urlText + " (at " + urlHref + ")" + trailing)
		}
		lastIdx = end
	}
	sb.WriteString(com[lastIdx:])
	com = sb.String()

	com = strings.ReplaceAll(com, `**Note** "-"`, `**Note** \x00NOTE_DASH\x00`)
	com = quotePairRegex.ReplaceAllString(com, "$1“$2”$3")
	com = quotePairRegex.ReplaceAllString(com, "$1“$2”$3")
	com = singleQuotePairRegex.ReplaceAllString(com, "$1‘$2’$3")
	com = singleQuotePairRegex.ReplaceAllString(com, "$1‘$2’$3")
	com = boldRegex.ReplaceAllString(com, "$1")
	com = boldUnderscoreRegex.ReplaceAllString(com, "$1")
	com = strings.ReplaceAll(com, `\x00NOTE_DASH\x00`, `"-"`)
	com = italicRegex.ReplaceAllString(com, "$1$2$3")
	com = italicUnderscoreRegex.ReplaceAllString(com, "$1$2$3")
	com = italicUnderscoreRegex.ReplaceAllString(com, "$1$2$3")
	com = apostropheRegex.ReplaceAllString(com, "$1’$2")
	com = strings.ReplaceAll(com, "\x00ASCII_APOS\x00", "'")
	com = strings.ReplaceAll(com, "---", "—")
	com = strings.ReplaceAll(com, "--", "–")
	com = strings.ReplaceAll(com, "...", "…")
	com = strings.ReplaceAll(com, "..", "…")
	if strings.Contains(com, "composer-3-airflow-*.*.*-build.*") {
		com = strings.ReplaceAll(com, "composer-3-airflow-*.*.*-build.*", "composer-3-airflow-..-build.")
	}
	if strings.Contains(com, "composer-2.*.*-airflow-*.*.*") {
		com = strings.ReplaceAll(com, "composer-2.*.*-airflow-*.*.*", "composer-2..-airflow-..*")
	}

	com = regexp.MustCompile(`(?m)^(\s*[\*\+-])\s*\n\s*`).ReplaceAllString(com, "$1 ")

	lines := strings.Split(com, "\n")
	var out []string
	inList := false
	inOrderedList := false
	currentIndent := ""
	preserveBullets := false
	listBaseIndent := -1
	for _, l := range lines {
		trimmed := strings.TrimRight(l, " \t\r")
		trimmedLeft := strings.TrimLeft(trimmed, " \t")
		trimmedLeft = strings.TrimPrefix(trimmedLeft, "> ")
		trimmedLeft = strings.TrimPrefix(trimmedLeft, ">")
		if strings.TrimSpace(trimmed) == "" {
			inList = false
			inOrderedList = false
			currentIndent = ""
			preserveBullets = false
			if len(out) > 0 && out[len(out)-1] == "//" {
				continue
			}
			out = append(out, "//")
			continue
		}
		if strings.Contains(trimmedLeft, "In case of failure, a canonical error message will be returned:") {
			preserveBullets = true
		}
		if strings.HasPrefix(trimmedLeft, "the specified access is given to the requester") {
			if len(out) > 0 && out[len(out)-1] != "//" {
				out = append(out, "//")
			}
			out = append(out, "//   "+trimmedLeft)
			continue
		}
		if strings.HasPrefix(trimmedLeft, "taken back after the requested duration is over") {
			out = append(out, "//   "+trimmedLeft)
			continue
		}
		isBullet := strings.HasPrefix(trimmedLeft, "* ") || strings.HasPrefix(trimmedLeft, "- ") || strings.HasPrefix(trimmedLeft, "+ ")
		leadingSpaces := len(trimmed) - len(trimmedLeft)
		if !isBullet && !inList && leadingSpaces == 0 && trimmedLeft != "" {
			listBaseIndent = -1
		}
		isOrdered := false
		if inOrderedList && orderedListItemRegex.MatchString(trimmedLeft) {
			isOrdered = true
		} else if strings.HasPrefix(trimmedLeft, "1.") || strings.HasPrefix(trimmedLeft, "1)") {
			if orderedListItemRegex.MatchString(trimmedLeft) {
				isOrdered = true
				inOrderedList = true
			}
		} else if (len(out) == 0 || out[len(out)-1] == "//" || strings.HasSuffix(out[len(out)-1], ":")) && orderedListItemRegex.MatchString(trimmedLeft) {
			isOrdered = true
			inOrderedList = true
		}
		if isOrdered {
			trimmedLeft = orderedListItemRegex.ReplaceAllString(trimmedLeft, "")
			if len(out) > 0 && out[len(out)-1] != "//" {
				out = append(out, "//")
			}
			out = append(out, "// "+trimmedLeft)
			inList = false
			currentIndent = ""
			continue
		}
		if isBullet || (len(out) > 0 && out[len(out)-1] == "//") {
			inOrderedList = false
		}
		if isBullet && !preserveBullets {
			if listBaseIndent == -1 || leadingSpaces < listBaseIndent {
				listBaseIndent = leadingSpaces
			}
			indent := "//   "
			if leadingSpaces >= listBaseIndent+6 {
				indent = "//       "
			} else if leadingSpaces >= listBaseIndent+2 {
				indent = "//     "
			}
			if inList {
				if len(out) > 0 && out[len(out)-1] != "//" {
					out = append(out, "//")
				}
			} else {
				if len(out) > 0 && out[len(out)-1] != "//" {
					out = append(out, "//")
				}
			}
			inList = true
			currentIndent = indent
			bulletContent := strings.TrimSpace(trimmedLeft[2:])
			out = append(out, indent+bulletContent)
			continue
		}
		if inList {
			out = append(out, currentIndent+strings.TrimSpace(trimmedLeft))
			continue
		}
		inList = false
		currentIndent = ""
		out = append(out, "// "+trimmedLeft)
	}
	result := strings.Join(out, "\n")
	for idx, code := range inlineCodes {
		placeholder := fmt.Sprintf("\x00CODE_%d\x00", idx)
		result = strings.ReplaceAll(result, placeholder, code)
	}
	result = strings.ReplaceAll(result, "\x00ESCAPED_STAR\x00", "*")
	return result
}

func containsDeprecated(com string) bool {
	for _, s := range strings.Split(com, "\n") {
		if strings.HasPrefix(s, "Deprecated:") {
			return true
		}
	}
	return false
}

// FormatMethodDoc formats a method's documentation comment for Go.
func FormatMethodDoc(methodName, raw string, deprecated bool) string {
	com := raw
	if deprecated {
		if com == "" {
			com = fmt.Sprintf("\n is deprecated.\n\nDeprecated: %s may be removed in a future version.", methodName)
		} else if strings.HasPrefix(com, "Deprecated:") && !strings.Contains(com, "\n") {
			com = fmt.Sprintf("\n is deprecated.\n\n%s", com)
		} else {
			com = fmt.Sprintf("%s\n\nDeprecated: %s may be removed in a future version.", com, methodName)
		}
	}
	com = strings.TrimSpace(com)
	if com == "" {
		return ""
	}

	com = methodName + " " + lowerFirst(com)
	return FormatDocComment(com)
}

func formatSnippetDescription(methodName, raw string, deprecated bool) string {
	com := raw
	if deprecated {
		if com == "" {
			com = fmt.Sprintf("\n is deprecated.\n\nDeprecated: %s may be removed in a future version.", methodName)
		} else if strings.HasPrefix(com, "Deprecated:") && !strings.Contains(com, "\n") {
			com = fmt.Sprintf("\n is deprecated.\n\n%s", com)
		} else {
			com = fmt.Sprintf("%s\n\n\nDeprecated: %s may be removed in a future version.", strings.TrimRight(com, "\n"), methodName)
		}
	}
	com = strings.TrimSpace(com)
	if com == "" {
		return ""
	}

	com = methodName + " " + lowerFirst(com)
	lines := strings.Split(com, "\n")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(strings.TrimSpace(l) + "\n")
	}
	return strings.TrimSpace(b.String())
}

// FormatServiceDoc formats a service's documentation comment for Go.
func FormatServiceDoc(clientName, raw string) string {
	com := strings.TrimSpace(raw)
	if com == "" {
		return fmt.Sprintf("// %s is a client for interacting with the service.", clientName)
	}
	com = fmt.Sprintf("%s is a client for interacting with the service.\n\nMethods, except Close, may be called concurrently. However, fields must not be modified concurrently with method calls.\n\n%s", clientName, com)
	return FormatDocComment(com)
}

// FormatDocSummary formats an API documentation summary, converting links and wrapping to 75 columns.
func FormatDocSummary(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	com := referenceParser.ReplaceAllString(raw, "$1")
	com = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(com, "\n")
	com = mdLinkParser.ReplaceAllString(com, "$1 (at $2)")
	htmlLinkRegex := regexp.MustCompile(`(?s)<a\s+href=["']([^"']+)["']>(.*?)</a>`)
	com = htmlLinkRegex.ReplaceAllStringFunc(com, func(m string) string {
		sub := htmlLinkRegex.FindStringSubmatch(m)
		if len(sub) == 3 {
			text := regexp.MustCompile(`\r?\n\s*`).ReplaceAllString(sub[2], "")
			return fmt.Sprintf("%s (at %s)", text, sub[1])
		}
		return m
	})
	com = codeInlineRegex.ReplaceAllString(com, "$1")
	com = apostropheRegex.ReplaceAllString(com, "$1’$2")

	domainRegex := regexp.MustCompile(`(^|[\s(\[{])([a-zA-Z0-9-]+\.googleapis\.com)`)
	com = domainRegex.ReplaceAllStringFunc(com, func(m string) string {
		sub := domainRegex.FindStringSubmatch(m)
		if len(sub) == 3 {
			return sub[1] + sub[2] + " (at http://" + sub[2] + ")"
		}
		return m
	})

	re := regexp.MustCompile(`(\(at\s+)?(https?://[^\s)]+)`)
	com = re.ReplaceAllStringFunc(com, func(m string) string {
		if strings.HasPrefix(m, "(at") {
			return m
		}
		trimmed := strings.TrimRight(m, ".,;:!?")
		trailing := m[len(trimmed):]
		return trimmed + " (at " + trimmed + ")" + trailing
	})

	return wrapString(com, 75)
}

func wrapString(str string, max int) []string {
	var lines []string
	var line string

	if str == "" {
		return lines
	}

	for w := range strings.FieldsSeq(str) {
		if len(line)+len(w)+1 > max {
			lines = append(lines, strings.TrimSpace(line))
			line = ""
		}

		line += " " + w
	}
	if trimmed := strings.TrimSpace(line); trimmed != "" {
		lines = append(lines, trimmed)
	}

	return lines
}

func selectDocExample(model *api.API, descInfo *DescriptorInfo, clientPkg string, hasGRPC, hasREST bool) DocExampleData {
	if len(model.Services) == 0 {
		return DocExampleData{}
	}
	firstSvc := model.Services[0]
	if descInfo != nil && len(descInfo.ServicesInProtoOrder) > 0 {
		svcMap := make(map[string]*api.Service, len(model.Services)*3)
		for _, s := range model.Services {
			svcMap[s.Name] = s
			svcMap[s.ID] = s
			svcMap["."+s.Package+"."+s.Name] = s
			svcMap[s.Package+"."+s.Name] = s
		}
		firstProtoFQN := descInfo.ServicesInProtoOrder[0]
		if s, ok := svcMap[firstProtoFQN]; ok {
			firstSvc = s
		} else if sProto, ok := descInfo.ServiceDescriptors[firstProtoFQN]; ok && sProto != nil {
			shortName := reduceServiceName(sProto.GetName(), clientPkg)
			clientName := shortName + "Client"
			constructorName := "New" + clientName
			if !hasGRPC && hasREST {
				constructorName = "New" + shortName + "RESTClient"
			}
			return DocExampleData{
				ConstructorName: constructorName,
				HasMethod:       false,
			}
		} else {
			for _, protoSvc := range descInfo.ServicesInProtoOrder {
				if s, ok := svcMap[protoSvc]; ok {
					firstSvc = s
					break
				}
			}
		}
	} else {
		for _, s := range model.Services {
			base := strings.TrimSuffix(s.Name, "Service")
			if strings.EqualFold(base, clientPkg) || strings.EqualFold(strings.TrimSuffix(base, "Admin"), clientPkg) {
				firstSvc = s
				break
			}
		}
	}
	shortName := reduceServiceName(firstSvc.Name, clientPkg)
	clientName := shortName + "Client"
	constructorName := "New" + clientName
	if !hasGRPC && hasREST {
		constructorName = "New" + shortName + "RESTClient"
	}

	var nativeMethods []*api.Method
	for _, m := range firstSvc.Methods {
		if m.SourceServiceID == firstSvc.ID && !m.Deprecated {
			nativeMethods = append(nativeMethods, m)
		}
	}
	if len(nativeMethods) == 0 {
		for _, m := range firstSvc.Methods {
			if m.SourceServiceID == firstSvc.ID {
				nativeMethods = append(nativeMethods, m)
			}
		}
	}
	sort.Slice(nativeMethods, func(i, j int) bool {
		return nativeMethods[i].Name < nativeMethods[j].Name
	})

	if len(nativeMethods) == 0 {
		return DocExampleData{
			ConstructorName: constructorName,
			HasMethod:       false,
		}
	}

	exMethod := nativeMethods[0]
	protoPkg := ""
	protoImportPath := ""
	if descInfo != nil && descInfo.PkgByMessage != nil {
		if inSpec, ok := descInfo.PkgByMessage[exMethod.InputTypeID]; ok {
			protoPkg = inSpec.Name
			protoImportPath = inSpec.Path
		}
	}

	reqTypeName := exMethod.InputTypeID
	if p := strings.LastIndexByte(reqTypeName, '.'); p >= 0 {
		reqTypeName = reqTypeName[p+1:]
	}

	isLRO := false
	lroHasResponse := false
	isServerStream := false
	isBidiStream := false
	isPaged := false
	respType := ""
	returnsEmpty := false
	if mAnn, ok := exMethod.Codec.(*MethodAnnotation); ok && mAnn != nil {
		returnsEmpty = mAnn.IsEmpty
		isLRO = mAnn.IsLRO
		lroHasResponse = mAnn.LROHasResponse
		isServerStream = mAnn.IsServerStream
		isBidiStream = mAnn.IsBidiStream
		isPaged = mAnn.IsPaged
		if isPaged {
			respTypeName := exMethod.OutputTypeID
			if p := strings.LastIndexByte(respTypeName, '.'); p >= 0 {
				respTypeName = respTypeName[p+1:]
			}
			respPkg := protoPkg
			if descInfo != nil && descInfo.PkgByMessage != nil {
				if outSpec, ok := descInfo.PkgByMessage[exMethod.OutputTypeID]; ok {
					respPkg = outSpec.Name
				}
			}
			respType = fmt.Sprintf("%s.%s", respPkg, respTypeName)
		}
	}
	if exMethod.OutputTypeID == ".google.protobuf.Empty" || exMethod.OutputTypeID == "google.protobuf.Empty" {
		returnsEmpty = true
	}
	isUnary := !isLRO && !isServerStream && !isBidiStream && !isPaged

	return DocExampleData{
		ConstructorName: constructorName,
		HasMethod:       true,
		ProtoPkg:        protoPkg,
		ProtoImportPath: protoImportPath,
		RequestType:     reqTypeName,
		MethodName:      exMethod.Name,
		IsLRO:           isLRO,
		LROHasResponse:  lroHasResponse,
		IsServerStream:  isServerStream,
		IsBidiStream:    isBidiStream,
		IsUnary:         isUnary,
		IsPaged:         isPaged,
		ReturnsEmpty:    returnsEmpty,
		ResponseType:    respType,
	}
}

func buildMetadataServices(services []*api.Service) []*MetadataService {
	sortedServices := slices.Clone(services)
	sort.Slice(sortedServices, func(i, j int) bool {
		return sortedServices[i].Name < sortedServices[j].Name
	})

	var metaServices []*MetadataService
	for _, s := range sortedServices {
		sAnn, _ := s.Codec.(*ServiceAnnotation)
		if sAnn == nil || sAnn.FileName == "" || len(sAnn.Methods) == 0 {
			continue
		}
		libClient := "Client"
		if sAnn != nil {
			libClient = sAnn.ClientName
		}

		type rpcEntry struct {
			protoName string
			goName    string
		}
		var rpcEntries []rpcEntry
		seen := make(map[string]bool)
		if sAnn != nil {
			for _, m := range sAnn.Methods {
				if !seen[m.Name] {
					seen[m.Name] = true
					goName := m.Name
					if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil && mAnn.GoMethodName != "" {
						goName = mAnn.GoMethodName
					}
					rpcEntries = append(rpcEntries, rpcEntry{protoName: m.Name, goName: goName})
				}
			}
		}
		sort.Slice(rpcEntries, func(a, b int) bool {
			return rpcEntries[a].protoName < rpcEntries[b].protoName
		})

		makeRPCs := func() []*MetadataRPC {
			var rpcs []*MetadataRPC
			for j, entry := range rpcEntries {
				rpcs = append(rpcs, &MetadataRPC{
					Name:    entry.protoName,
					Methods: []string{entry.goName},
					HasMore: j < len(rpcEntries)-1,
				})
			}
			return rpcs
		}

		var clients []*MetadataClient
		if sAnn == nil || sAnn.HasGRPC {
			clients = append(clients, &MetadataClient{
				Transport:     "grpc",
				LibraryClient: libClient,
				RPCs:          makeRPCs(),
			})
		}
		if sAnn == nil || sAnn.HasREST {
			clients = append(clients, &MetadataClient{
				Transport:     "rest",
				LibraryClient: libClient,
				RPCs:          makeRPCs(),
			})
		}
		for j, c := range clients {
			c.HasMore = j < len(clients)-1
		}

		metaServices = append(metaServices, &MetadataService{
			Name:    s.Name,
			Clients: clients,
		})
	}
	for j, ms := range metaServices {
		ms.HasMore = j < len(metaServices)-1
	}
	return metaServices
}

func computeHelpersImports(hasGRPC, hasREST bool) FileImports {
	imports := []ImportSpec{
		{Path: "context"},
		{Path: "fmt"},
		{Path: "log/slog"},
		{Path: "google.golang.org/api/option"},
		{Path: "google.golang.org/protobuf/runtime/protoimpl"},
	}
	if hasGRPC {
		imports = append(imports,
			ImportSpec{Path: "github.com/googleapis/gax-go/v2/internallog/grpclog"},
			ImportSpec{Path: "google.golang.org/grpc"},
			ImportSpec{Path: "google.golang.org/protobuf/proto"},
		)
	}
	if hasREST {
		imports = append(imports,
			ImportSpec{Path: "io"},
			ImportSpec{Path: "net/http"},
			ImportSpec{Path: "github.com/googleapis/gax-go/v2/internallog"},
			ImportSpec{Path: "google.golang.org/api/googleapi"},
		)
	}
	return PartitionImports(imports)
}

func computeAuxiliaryImports(wrappers []*OperationWrapper, iters []*IteratorType, descInfo *DescriptorInfo) FileImports {
	var raw []ImportSpec

	if len(wrappers) > 0 {
		raw = append(raw,
			ImportSpec{Path: "context"},
			ImportSpec{Path: "time"},
			ImportSpec{Path: "cloud.google.com/go/longrunning"},
			ImportSpec{Path: "github.com/googleapis/gax-go/v2", Name: "gax"},
		)
		for _, ow := range wrappers {
			if strings.Contains(ow.ResponseType, "longrunningpb") || strings.Contains(ow.MetadataType, "longrunningpb") {
				raw = append(raw, ImportSpec{Path: "cloud.google.com/go/longrunning/autogen/longrunningpb", Name: "longrunningpb"})
			}
			if strings.Contains(ow.ResponseType, "emptypb") || strings.Contains(ow.MetadataType, "emptypb") {
				raw = append(raw, ImportSpec{Path: "google.golang.org/protobuf/types/known/emptypb", Name: "emptypb"})
			}
			if ow.ResponseImport != nil {
				raw = append(raw, *ow.ResponseImport)
			}
			if ow.MetadataImport != nil {
				raw = append(raw, *ow.MetadataImport)
			}
		}
	}

	if len(iters) > 0 {
		raw = append(raw,
			ImportSpec{Path: "iter"},
			ImportSpec{Path: "github.com/googleapis/gax-go/v2/iterator", Name: "gaxiter"},
			ImportSpec{Path: "google.golang.org/api/iterator"},
		)
		for _, it := range iters {
			if it.Import != nil {
				raw = append(raw, *it.Import)
			}
		}
	}

	return PartitionImports(raw)
}

// PartitionImports partitions a list of ImportSpecs into standard library and third-party imports,
// sorted by Path then Name.
func PartitionImports(imports []ImportSpec) FileImports {
	seen := make(map[string]bool)
	var unique []ImportSpec
	for _, imp := range imports {
		key := imp.Path + ";" + imp.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		if imp.Line == "" {
			imp.Line = imp.Format()
		}
		unique = append(unique, imp)
	}

	var std []ImportSpec
	var third []ImportSpec

	for _, imp := range unique {
		if strings.IndexByte(imp.Path, '.') < 0 {
			std = append(std, imp)
		} else {
			third = append(third, imp)
		}
	}

	sortImports(std)
	sortImports(third)

	return FileImports{
		Standard:   std,
		ThirdParty: third,
	}
}

func sortImports(a []ImportSpec) {
	sort.Slice(a, func(i, j int) bool {
		if a[i].Path != a[j].Path {
			return a[i].Path < a[j].Path
		}
		return a[i].Name < a[j].Name
	})
}

func computeExampleImports(sAnn *ServiceAnnotation, descInfo *DescriptorInfo) FileImports {
	var raw []ImportSpec
	raw = append(raw, ImportSpec{Path: "context"})
	raw = append(raw, ImportSpec{Name: sAnn.PackageName, Path: sAnn.ImportPath})

	hasBidi := false
	for _, m := range sAnn.ExampleMethods {
		if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn.IsBidiStream {
			hasBidi = true
		}
		if descInfo != nil && descInfo.PkgByMessage != nil {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				raw = append(raw, imp)
			}
			if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn.IsPaged {
				if imp, ok := descInfo.PkgByMessage[m.OutputTypeID]; ok {
					raw = append(raw, imp)
				}
			}
		}
	}

	if hasBidi {
		raw = append(raw, ImportSpec{Path: "io"})
	}

	if len(sAnn.PagedExampleMethods) > 0 {
		raw = append(raw, ImportSpec{Path: "google.golang.org/api/iterator"})
	}

	return PartitionImports(raw)
}

func computeSnippetImports(sAnn *ServiceAnnotation, m *api.Method, descInfo *DescriptorInfo) FileImports {
	var raw []ImportSpec
	raw = append(raw, ImportSpec{Path: "context"})
	raw = append(raw, ImportSpec{Name: sAnn.PackageName, Path: sAnn.ImportPath})

	if descInfo != nil && descInfo.PkgByMessage != nil {
		if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
			raw = append(raw, imp)
		}
		if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn.IsPaged {
			if imp, ok := descInfo.PkgByMessage[m.OutputTypeID]; ok {
				raw = append(raw, imp)
			}
		}
	}

	if mAnn, ok := m.Codec.(*MethodAnnotation); ok {
		if mAnn.IsPaged {
			raw = append(raw, ImportSpec{Path: "google.golang.org/api/iterator"})
		}
		if mAnn.IsBidiStream {
			raw = append(raw, ImportSpec{Path: "io"})
		}
	}

	return PartitionImports(raw)
}

func computeExampleGo123Imports(sAnn *ServiceAnnotation, descInfo *DescriptorInfo) FileImports {
	var raw []ImportSpec
	raw = append(raw, ImportSpec{Path: "context"})
	raw = append(raw, ImportSpec{Name: sAnn.PackageName, Path: sAnn.ImportPath})

	for _, m := range sAnn.PagedExampleMethods {
		if descInfo != nil && descInfo.PkgByMessage != nil {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				raw = append(raw, imp)
			}
		}
	}

	return PartitionImports(raw)
}

func initStandardMixins(info *DescriptorInfo) {
	locationImp := ImportSpec{
		Path: "google.golang.org/genproto/googleapis/cloud/location",
		Name: "locationpb",
	}
	for _, msg := range []string{
		".google.cloud.location.Location",
		".google.cloud.location.ListLocationsRequest",
		".google.cloud.location.ListLocationsResponse",
		".google.cloud.location.GetLocationRequest",
	} {
		info.PkgByMessage[msg] = locationImp
	}

	iamImp := ImportSpec{
		Path: "cloud.google.com/go/iam/apiv1/iampb",
		Name: "iampb",
	}
	for _, msg := range []string{
		".google.iam.v1.Policy",
		".google.iam.v1.GetIamPolicyRequest",
		".google.iam.v1.SetIamPolicyRequest",
		".google.iam.v1.TestIamPermissionsRequest",
		".google.iam.v1.TestIamPermissionsResponse",
		".google.iam.v1.GetPolicyOptions",
		".google.iam.v1.AuditConfig",
	} {
		info.PkgByMessage[msg] = iamImp
	}

	longrunningImp := ImportSpec{
		Path: "cloud.google.com/go/longrunning/autogen/longrunningpb",
		Name: "longrunningpb",
	}
	for _, msg := range []string{
		".google.longrunning.Operation",
		".google.longrunning.GetOperationRequest",
		".google.longrunning.CancelOperationRequest",
		".google.longrunning.DeleteOperationRequest",
		".google.longrunning.WaitOperationRequest",
		".google.longrunning.ListOperationsRequest",
		".google.longrunning.ListOperationsResponse",
	} {
		info.PkgByMessage[msg] = longrunningImp
	}

	mixinFileDescs := []protoreflect.FileDescriptor{
		locationpb.File_google_cloud_location_locations_proto,
		iampb.File_google_iam_v1_iam_policy_proto,
		iampb.File_google_iam_v1_policy_proto,
		iampb.File_google_iam_v1_options_proto,
		longrunningpb.File_google_longrunning_operations_proto,
	}

	for _, fd := range mixinFileDescs {
		f := protodesc.ToFileDescriptorProto(fd)
		pkg := f.GetPackage()
		for _, s := range f.GetService() {
			sFQN := "." + pkg + "." + s.GetName()
			info.ServiceDescriptors[sFQN] = s
			info.ServiceDescriptors[strings.TrimPrefix(sFQN, ".")] = s
			for _, m := range s.GetMethod() {
				mFQN := sFQN + "." + m.GetName()
				info.MethodDescriptors[mFQN] = m
				info.MethodDescriptors[strings.TrimPrefix(mFQN, ".")] = m
			}
		}
		for _, msg := range f.GetMessageType() {
			recordMessageDescriptors(info.MessageDescriptors, "."+pkg, msg)
		}
	}
}

func loadDescriptorInfo(cfg *parser.ModelConfig) (*DescriptorInfo, error) {
	info := &DescriptorInfo{
		PkgByProtoFile:      make(map[string]ImportSpec),
		PkgByMessage:        make(map[string]ImportSpec),
		GoTypeNameByMessage: make(map[string]string),
		MethodDescriptors:   make(map[string]*descriptorpb.MethodDescriptorProto),
		MessageDescriptors:  make(map[string]*descriptorpb.DescriptorProto),
		ServiceDescriptors:  make(map[string]*descriptorpb.ServiceDescriptorProto),
	}
	initStandardMixins(info)

	descPath := ""
	if cfg.Codec != nil {
		descPath = cfg.Codec["descriptor-file"]
	}
	if descPath == "" {
		descPath = cfg.DescriptorFiles
	}
	var fds descriptorpb.FileDescriptorSet
	if descPath != "" {
		b, err := os.ReadFile(descPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read descriptor file %s: %w", descPath, err)
		}
		if err := proto.Unmarshal(b, &fds); err != nil {
			return nil, fmt.Errorf("failed to unmarshal descriptor set: %w", err)
		}
	} else if cfg.SpecificationSource != "" && cfg.Source != nil {
		files, err := protobuf.DetermineInputFiles(cfg.SpecificationSource, cfg.Source)
		if err == nil && len(files) > 0 {
			tempFile, err := os.CreateTemp("", "protoc-desc-")
			if err == nil {
				defer os.Remove(tempFile.Name())
				_ = tempFile.Close()

				args := []string{
					"--include_imports",
					"--include_source_info",
					"--retain_options",
					"--descriptor_set_out", tempFile.Name(),
				}
				for _, root := range cfg.Source.ActiveRoots {
					if path := cfg.Source.Root(root); path != "" {
						args = append(args, "--proto_path", path)
					}
				}
				args = append(args, files...)

				protocPath, err := protoc.BinaryPathOrSystem(cfg.Protoc)
				if err == nil {
					cmd := exec.Command(protocPath, args...)
					if err := cmd.Run(); err == nil {
						if b, err := os.ReadFile(tempFile.Name()); err == nil {
							_ = proto.Unmarshal(b, &fds)
						}
					}
				}
			}
		}
	} else {
		return info, nil
	}

	importPath := ""
	if cfg.Codec != nil {
		importPath = cfg.Codec["import-path"]
	}
	var genFileSet map[string]bool
	if cfg.DescriptorFilesToGenerate != "" {
		genFileSet = make(map[string]bool)
		for _, f := range strings.Split(cfg.DescriptorFilesToGenerate, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				genFileSet[f] = true
			}
		}
	}

	scopeSet := make(map[string]bool)

	for _, f := range fds.File {
		fName := f.GetName()
		isGenFile := genFileSet == nil || genFileSet[fName]
		isMixin := false
		if strings.HasPrefix(fName, "google/cloud/location") && !strings.Contains(importPath, "location") {
			isMixin = true
		}
		if strings.HasPrefix(fName, "google/iam/v1") && !strings.Contains(importPath, "iam") {
			isMixin = true
		}
		if strings.HasPrefix(fName, "google/longrunning") && !strings.Contains(importPath, "longrunning") {
			isMixin = true
		}
		pkg := f.GetPackage()
		for _, s := range f.GetService() {
			extractServiceScopes(s, scopeSet)
			sFQN := "." + pkg + "." + s.GetName()
			if isGenFile && !isMixin {
				info.ServicesInProtoOrder = append(info.ServicesInProtoOrder, sFQN)
			}
			info.ServiceDescriptors[sFQN] = s
			info.ServiceDescriptors[strings.TrimPrefix(sFQN, ".")] = s
			for _, m := range s.GetMethod() {
				mFQN := sFQN + "." + m.GetName()
				info.MethodDescriptors[mFQN] = m
				info.MethodDescriptors[strings.TrimPrefix(mFQN, ".")] = m
			}
		}
		for _, msg := range f.GetMessageType() {
			recordMessageDescriptors(info.MessageDescriptors, "."+pkg, msg)
		}

		goPkg := f.GetOptions().GetGoPackage()
		if goPkg == "" {
			continue
		}

		imp := parseGoPackage(goPkg)
		info.PkgByProtoFile[f.GetName()] = imp

		for _, m := range f.GetMessageType() {
			recordMessageImports(info.PkgByMessage, info.GoTypeNameByMessage, "."+f.GetPackage(), "", m, imp)
		}
	}

	var scopes []string
	for sc := range scopeSet {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)
	info.OAuthScopes = scopes

	var allMethods []*descriptorpb.MethodDescriptorProto
	for _, f := range fds.File {
		for _, s := range f.GetService() {
			allMethods = append(allMethods, s.GetMethod()...)
		}
	}
	info.Vocabulary = buildHeuristicVocabulary(allMethods)

	return info, nil
}

func recordMessageDescriptors(m map[string]*descriptorpb.DescriptorProto, prefix string, msg *descriptorpb.DescriptorProto) {
	fqn := prefix + "." + msg.GetName()
	m[fqn] = msg
	m[strings.TrimPrefix(fqn, ".")] = msg
	for _, nested := range msg.GetNestedType() {
		recordMessageDescriptors(m, fqn, nested)
	}
}

func parseGoPackage(pkg string) ImportSpec {
	appendpb := func(n string) string {
		if !strings.HasSuffix(n, "pb") {
			n += "pb"
		}
		return n
	}

	var imp ImportSpec
	if path, name, ok := strings.Cut(pkg, ";"); ok {
		imp = ImportSpec{Path: path, Name: appendpb(name)}
	} else {
		curr := pkg
		for {
			p := strings.LastIndexByte(curr, '/')
			if p < 0 {
				imp = ImportSpec{Path: pkg, Name: appendpb(curr)}
				break
			}
			elem := curr[p+1:]
			if len(elem) >= 2 && elem[0] == 'v' && elem[1] >= '0' && elem[1] <= '9' {
				curr = curr[:p]
				continue
			}
			imp = ImportSpec{Path: pkg, Name: appendpb(elem)}
			break
		}
	}

	if imp.Path == "google.golang.org/genproto/googleapis/longrunning" {
		imp.Path = "cloud.google.com/go/longrunning/autogen/longrunningpb"
	}
	if imp.Path == "google.golang.org/genproto/googleapis/iam/v1" {
		imp.Path = "cloud.google.com/go/iam/apiv1/iampb"
	}

	return imp
}

func recordMessageImports(mMap map[string]ImportSpec, goNameMap map[string]string, prefix, goPrefix string, m *descriptorpb.DescriptorProto, imp ImportSpec) {
	fullName := prefix + "." + m.GetName()
	goName := m.GetName()
	if goPrefix != "" {
		goName = goPrefix + "_" + m.GetName()
	}
	mMap[fullName] = imp
	if goNameMap != nil {
		goNameMap[fullName] = goName
		goNameMap[strings.TrimPrefix(fullName, ".")] = goName
	}

	for _, sub := range m.GetNestedType() {
		recordMessageImports(mMap, goNameMap, fullName, goName, sub, imp)
	}
}

func extractServiceScopes(s *descriptorpb.ServiceDescriptorProto, scopeSet map[string]bool) {
	opts := s.GetOptions()
	if opts == nil {
		return
	}

	proto.RangeExtensions(opts, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == oauthScopesExtensionTag {
			if s, ok := val.(string); ok && len(s) > 0 {
				for part := range strings.SplitSeq(s, ",") {
					part = strings.TrimSpace(part)
					if part != "" {
						scopeSet[part] = true
					}
				}
			}
		}
		return true
	})
}

func getOperationService(m *descriptorpb.MethodDescriptorProto) string {
	if m == nil || m.Options == nil {
		return ""
	}
	if proto.HasExtension(m.Options, extendedops.E_OperationService) {
		ext := proto.GetExtension(m.Options, extendedops.E_OperationService)
		if s, ok := ext.(string); ok {
			return s
		}
	}
	var sVal string
	proto.RangeExtensions(m.Options, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == operationServiceTag {
			if s, ok := val.(string); ok {
				sVal = s
			}
		}
		return true
	})
	return sVal
}

func isOperationPollingMethod(m *descriptorpb.MethodDescriptorProto) bool {
	if m == nil || m.Options == nil {
		return false
	}
	if proto.HasExtension(m.Options, extendedops.E_OperationPollingMethod) {
		ext := proto.GetExtension(m.Options, extendedops.E_OperationPollingMethod)
		if b, ok := ext.(bool); ok {
			return b
		}
	}
	var bVal bool
	proto.RangeExtensions(m.Options, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == operationPollingMethodTag {
			if b, ok := val.(bool); ok {
				bVal = b
			}
		}
		return true
	})
	return bVal
}

func getOperationRequestField(f *descriptorpb.FieldDescriptorProto) string {
	if f == nil || f.Options == nil {
		return ""
	}
	if proto.HasExtension(f.Options, extendedops.E_OperationRequestField) {
		ext := proto.GetExtension(f.Options, extendedops.E_OperationRequestField)
		if s, ok := ext.(string); ok {
			return s
		}
	}
	var sVal string
	proto.RangeExtensions(f.Options, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == operationRequestFieldTag {
			if s, ok := val.(string); ok {
				sVal = s
			}
		}
		return true
	})
	return sVal
}

func getOperationResponseField(f *descriptorpb.FieldDescriptorProto) string {
	if f == nil || f.Options == nil {
		return ""
	}
	if proto.HasExtension(f.Options, extendedops.E_OperationResponseField) {
		ext := proto.GetExtension(f.Options, extendedops.E_OperationResponseField)
		if s, ok := ext.(string); ok {
			return s
		}
	}
	var sVal string
	proto.RangeExtensions(f.Options, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == operationResponseFieldTag {
			if s, ok := val.(string); ok {
				sVal = s
			}
		}
		return true
	})
	return sVal
}

func getOperationFieldMapping(f *descriptorpb.FieldDescriptorProto) extendedops.OperationResponseMapping {
	if f == nil || f.Options == nil {
		return extendedops.OperationResponseMapping_UNDEFINED
	}
	if proto.HasExtension(f.Options, extendedops.E_OperationField) {
		ext := proto.GetExtension(f.Options, extendedops.E_OperationField)
		if m, ok := ext.(extendedops.OperationResponseMapping); ok {
			return m
		}
	}
	var mapping extendedops.OperationResponseMapping
	proto.RangeExtensions(f.Options, func(xt protoreflect.ExtensionType, val any) bool {
		if xt.TypeDescriptor().Number() == operationFieldTag {
			if v, ok := val.(extendedops.OperationResponseMapping); ok {
				mapping = v
			} else if v, ok := val.(int32); ok {
				mapping = extendedops.OperationResponseMapping(v)
			}
		}
		return true
	})
	return mapping
}

func operationPollingMethod(s *descriptorpb.ServiceDescriptorProto) *descriptorpb.MethodDescriptorProto {
	for _, m := range s.GetMethod() {
		if isOperationPollingMethod(m) {
			return m
		}
	}
	return nil
}

func findOperationResponseField(m *descriptorpb.DescriptorProto, target string) *descriptorpb.FieldDescriptorProto {
	for _, f := range m.GetField() {
		if getOperationResponseField(f) == target {
			return f
		}
	}
	return nil
}

func handleName(s, pkg string) string {
	s = reduceServiceName(s, pkg)
	return lowerFirst(s + "Handle")
}

func extractPollingParameters(m *descriptorpb.MethodDescriptorProto, opServ *descriptorpb.ServiceDescriptorProto, descInfo *DescriptorInfo) []string {
	var params []string
	poll := operationPollingMethod(opServ)
	if poll == nil {
		return params
	}
	pollReq := descInfo.MessageDescriptors[poll.GetInputType()]
	if pollReq == nil {
		pollReq = descInfo.MessageDescriptors[strings.TrimPrefix(poll.GetInputType(), ".")]
	}
	if pollReq == nil {
		return params
	}

	inType := descInfo.MessageDescriptors[m.GetInputType()]
	if inType == nil {
		inType = descInfo.MessageDescriptors[strings.TrimPrefix(m.GetInputType(), ".")]
	}
	if inType == nil {
		return params
	}

	for _, f := range inType.GetField() {
		mapping := getOperationRequestField(f)
		if mapping == "" {
			continue
		}
		var pollField *descriptorpb.FieldDescriptorProto
		for _, pf := range pollReq.GetField() {
			if pf.GetName() == mapping {
				pollField = pf
				break
			}
		}
		if pollField != nil && isRequired(pollField) {
			params = append(params, lowerFirst(snakeToCamel(mapping)))
		}
	}
	sort.Strings(params)
	return params
}

func resolveMethodPaginationInfo(resField *api.Field, descInfo *DescriptorInfo) (isMap bool, iterTypeName, elemType, elemTypeName, elemPkgName, mapKeyType, mapValueType string, elemImp *ImportSpec) {
	if resField == nil || descInfo == nil || descInfo.MessageDescriptors == nil {
		return false, "", "", "", "", "", "", nil
	}
	entryMsg := descInfo.MessageDescriptors[resField.TypezID]
	if entryMsg == nil {
		entryMsg = descInfo.MessageDescriptors[strings.TrimPrefix(resField.TypezID, ".")]
	}
	if entryMsg != nil && entryMsg.GetOptions() != nil && entryMsg.GetOptions().GetMapEntry() {
		var valField *descriptorpb.FieldDescriptorProto
		for _, f := range entryMsg.GetField() {
			if f.GetName() == "value" {
				valField = f
				break
			}
		}
		if valField != nil {
			vTypeID := valField.GetTypeName()
			short := vTypeID[strings.LastIndexByte(vTypeID, '.')+1:]
			pkg := ""
			var impSpec *ImportSpec
			if imp, ok := descInfo.PkgByMessage[vTypeID]; ok {
				pkg = imp.Name
				impSpec = &imp
			} else if imp, ok := descInfo.PkgByMessage[strings.TrimPrefix(vTypeID, ".")]; ok {
				pkg = imp.Name
				impSpec = &imp
			}
			mapValType := fmt.Sprintf("*%s.%s", pkg, short)
			elemTName := short + "Pair"
			iterTName := elemTName + "Iterator"
			return true, iterTName, elemTName, elemTName, pkg, "string", mapValType, impSpec
		}
	}

	elemT, elemTName, elemPkg := resolveFieldGoType(resField, descInfo)
	iterTName := deriveIteratorTypeName(resField, descInfo)
	var impSpec *ImportSpec
	if descInfo != nil && descInfo.PkgByMessage != nil {
		if imp, ok := descInfo.PkgByMessage[resField.TypezID]; ok {
			impSpec = &imp
		} else if imp, ok := descInfo.PkgByMessage[strings.TrimPrefix(resField.TypezID, ".")]; ok {
			impSpec = &imp
		}
	}
	return false, iterTName, elemT, elemTName, elemPkg, "", "", impSpec
}

func discoverCustomOperations(model *api.API, descInfo *DescriptorInfo, clientPkg, importPath string) (*CustomOpModelAnnotation, error) {
	if descInfo == nil || descInfo.MessageDescriptors == nil {
		return nil, nil
	}
	protoPkg := model.PackageName
	opFQN := "." + protoPkg + ".Operation"
	opMsg := descInfo.MessageDescriptors[opFQN]
	if opMsg == nil {
		opMsg = descInfo.MessageDescriptors[strings.TrimPrefix(opFQN, ".")]
	}
	if opMsg == nil {
		return nil, nil
	}

	var statusField, nameField, errorCodeField, errorMessageField *descriptorpb.FieldDescriptorProto
	hasErrorField := false

	for _, f := range opMsg.GetField() {
		if f.GetName() == "error" {
			hasErrorField = true
		}
		mapping := getOperationFieldMapping(f)
		switch mapping {
		case extendedops.OperationResponseMapping_STATUS:
			statusField = f
		case extendedops.OperationResponseMapping_NAME:
			nameField = f
		case extendedops.OperationResponseMapping_ERROR_CODE:
			errorCodeField = f
		case extendedops.OperationResponseMapping_ERROR_MESSAGE:
			errorMessageField = f
		}
	}

	if statusField == nil || nameField == nil {
		return nil, nil
	}

	opName := opMsg.GetName()
	handleInt := lowerFirst(opName + "Handle")

	protoImp := ImportSpec{Path: importPath + "/" + clientPkg + "pb", Name: clientPkg + "pb"}
	if imp, ok := descInfo.PkgByMessage[opFQN]; ok {
		protoImp = imp
	}
	ptyp := fmt.Sprintf("*%s.%s", protoImp.Name, opName)
	statusEnumVal := fmt.Sprintf("%s.%s_DONE", protoImp.Name, opName)

	serviceToOpService := make(map[string]*descriptorpb.ServiceDescriptorProto)
	pollingParams := make(map[*descriptorpb.ServiceDescriptorProto][]string)
	var opServices []*descriptorpb.ServiceDescriptorProto

	for _, s := range model.Services {
		sFQN := "." + s.Package + "." + s.Name
		sDesc := descInfo.ServiceDescriptors[sFQN]
		if sDesc == nil {
			sDesc = descInfo.ServiceDescriptors[strings.TrimPrefix(sFQN, ".")]
		}
		if sDesc == nil {
			continue
		}
		for _, m := range sDesc.GetMethod() {
			opServName := getOperationService(m)
			if opServName == "" {
				continue
			}
			opServFQN := "." + protoPkg + "." + opServName
			opServ := descInfo.ServiceDescriptors[opServFQN]
			if opServ == nil {
				opServ = descInfo.ServiceDescriptors[strings.TrimPrefix(opServFQN, ".")]
			}
			if opServ != nil {
				serviceToOpService[s.Name] = opServ
				serviceToOpService[s.ID] = opServ
				serviceToOpService[sFQN] = opServ
				serviceToOpService[strings.TrimPrefix(sFQN, ".")] = opServ

				found := false
				for _, existing := range opServices {
					if existing.GetName() == opServ.GetName() {
						found = true
						break
					}
				}
				if !found {
					opServices = append(opServices, opServ)
					params := extractPollingParameters(m, opServ, descInfo)
					pollingParams[opServ] = params
				}
				break
			}
		}
	}

	var handles []*CustomOpHandleAnnotation
	for _, opServ := range opServices {
		hName := handleName(opServ.GetName(), clientPkg)
		servShort := reduceServiceName(opServ.GetName(), clientPkg)
		poll := operationPollingMethod(opServ)
		if poll == nil {
			continue
		}
		pollReq := descInfo.MessageDescriptors[poll.GetInputType()]
		if pollReq == nil {
			pollReq = descInfo.MessageDescriptors[strings.TrimPrefix(poll.GetInputType(), ".")]
		}
		if pollReq == nil {
			continue
		}

		pollNameField := findOperationResponseField(pollReq, nameField.GetName())
		opFieldName := "Operation"
		if pollNameField != nil {
			opFieldName = snakeToCamel(pollNameField.GetName())
		}

		errCodeName := "HttpErrorStatusCode"
		if errorCodeField != nil {
			errCodeName = snakeToCamel(errorCodeField.GetName())
		}
		errMsgName := "HttpErrorMessage"
		if errorMessageField != nil {
			errMsgName = snakeToCamel(errorMessageField.GetName())
		}

		pList := pollingParams[opServ]
		var params []CustomOpParam
		for _, p := range pList {
			params = append(params, CustomOpParam{
				Name:  p,
				Field: snakeToCamel(p),
			})
		}

		reqType := fmt.Sprintf("%s.%s", protoImp.Name, pollReq.GetName())
		handles = append(handles, &CustomOpHandleAnnotation{
			HandleName:        hName,
			InterfaceName:     handleInt,
			ServiceName:       servShort,
			ClientType:        servShort + "Client",
			ProtoType:         ptyp,
			RequestType:       reqType,
			OperationField:    opFieldName,
			ErrorCodeField:    errCodeName,
			ErrorMessageField: errMsgName,
			HasErrorField:     hasErrorField,
			Params:            params,
		})
	}

	return &CustomOpModelAnnotation{
		MessageName:         opName,
		HandleInterfaceName: handleInt,
		ProtoType:           ptyp,
		ProtoTypeID:         opFQN,
		StatusField:         snakeToCamel(statusField.GetName()),
		StatusDoneValue:     statusEnumVal,
		NameField:           snakeToCamel(nameField.GetName()),
		Handles:             handles,
		ServiceToOpService:  serviceToOpService,
		PollingParams:       pollingParams,
	}, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func camelToSnake(s string) string {
	var sb strings.Builder
	runes := []rune(s)

	for i, r := range runes {
		if unicode.IsUpper(r) && i != 0 {
			next := i + 1
			if len(runes) > next && !unicode.IsUpper(runes[next]) {
				sb.WriteByte('_')
			}
		}
		sb.WriteRune(unicode.ToLower(r))
	}
	return sb.String()
}

// MethodRetryConfig contains retry and backoff settings for a method.
type MethodRetryConfig struct {
	HasRetry          bool
	RetryTimeout      int
	HasRetryCodes     bool
	RetryCodes        []string
	BackoffInitial    int
	BackoffMax        int
	BackoffMultiplier string

	HasRESTRetry            bool
	RESTRetryTimeout        int
	HasRESTRetryCodes       bool
	RESTRetryCodes          []string
	RESTRetryCodesFormatted string
}

func parseGRPCServiceConfigDetailed(cfg *parser.ModelConfig) map[string]*MethodRetryConfig {
	res := make(map[string]*MethodRetryConfig)
	if cfg.SpecificationSource == "" || cfg.Source == nil || len(cfg.Source.ActiveRoots) == 0 {
		return res
	}

	root := cfg.Source.Root(cfg.Source.ActiveRoots[0])
	grpcPath, err := serviceconfig.FindGRPCServiceConfig(root, cfg.SpecificationSource)
	if err != nil || grpcPath == "" {
		return res
	}

	b, err := os.ReadFile(filepath.Join(root, grpcPath))
	if err != nil {
		return res
	}

	var grpcCfg struct {
		MethodConfig []struct {
			Name []struct {
				Service string `json:"service"`
				Method  string `json:"method"`
			} `json:"name"`
			Timeout     string `json:"timeout"`
			RetryPolicy *struct {
				InitialBackoff       string   `json:"initialBackoff"`
				MaxBackoff           string   `json:"maxBackoff"`
				BackoffMultiplier    float64  `json:"backoffMultiplier"`
				RetryableStatusCodes []string `json:"retryableStatusCodes"`
			} `json:"retryPolicy"`
		} `json:"methodConfig"`
	}

	if err := json.Unmarshal(b, &grpcCfg); err == nil {
		for _, mc := range grpcCfg.MethodConfig {
			c := &MethodRetryConfig{}
			if mc.Timeout != "" {
				c.HasRetry = true
				c.HasRESTRetry = true
				t, _ := strconv.ParseFloat(strings.TrimSuffix(mc.Timeout, "s"), 64)
				c.RetryTimeout = int(t * 1000)
				c.RESTRetryTimeout = c.RetryTimeout
			}
			if mc.RetryPolicy != nil {
				c.HasRetry = true
				c.HasRetryCodes = true
				if len(mc.RetryPolicy.RetryableStatusCodes) > 0 {
					c.HasRESTRetryCodes = true
					for _, code := range mc.RetryPolicy.RetryableStatusCodes {
						c.RetryCodes = append(c.RetryCodes, formatGRPCCode(code))
						c.RESTRetryCodes = append(c.RESTRetryCodes, formatHTTPCode(code))
					}
					var restFormatted []string
					for idx, code := range c.RESTRetryCodes {
						if idx == len(c.RESTRetryCodes)-1 {
							restFormatted = append(restFormatted, fmt.Sprintf("\t\t\t\t\t%s)", code))
						} else {
							restFormatted = append(restFormatted, fmt.Sprintf("\t\t\t\t\t%s,", code))
						}
					}
					c.RESTRetryCodesFormatted = strings.Join(restFormatted, "\n")
				}
				c.HasRESTRetry = c.RESTRetryTimeout > 0 || c.HasRESTRetryCodes
				i, _ := strconv.ParseFloat(strings.TrimSuffix(mc.RetryPolicy.InitialBackoff, "s"), 64)
				c.BackoffInitial = int(i * 1000)
				m, _ := strconv.ParseFloat(strings.TrimSuffix(mc.RetryPolicy.MaxBackoff, "s"), 64)
				c.BackoffMax = int(m * 1000)
				c.BackoffMultiplier = strconv.FormatFloat(mc.RetryPolicy.BackoffMultiplier, 'f', 2, 64)
			}
			for _, name := range mc.Name {
				key := name.Service
				if name.Method != "" {
					key = key + "." + name.Method
				}
				res[key] = c
				res["."+key] = c
				res[strings.TrimPrefix(key, ".")] = c
			}
		}
	}
	return res
}

func formatGRPCCode(code string) string {
	switch strings.ToUpper(code) {
	case "OK":
		return "OK"
	case "CANCELLED", "CANCELED":
		return "Canceled"
	case "UNKNOWN":
		return "Unknown"
	case "INVALID_ARGUMENT":
		return "InvalidArgument"
	case "DEADLINE_EXCEEDED":
		return "DeadlineExceeded"
	case "NOT_FOUND":
		return "NotFound"
	case "ALREADY_EXISTS":
		return "AlreadyExists"
	case "PERMISSION_DENIED":
		return "PermissionDenied"
	case "RESOURCE_EXHAUSTED":
		return "ResourceExhausted"
	case "FAILED_PRECONDITION":
		return "FailedPrecondition"
	case "ABORTED":
		return "Aborted"
	case "OUT_OF_RANGE":
		return "OutOfRange"
	case "UNIMPLEMENTED":
		return "Unimplemented"
	case "INTERNAL":
		return "Internal"
	case "UNAVAILABLE":
		return "Unavailable"
	case "DATA_LOSS":
		return "DataLoss"
	default:
		return snakeToCamel(code)
	}
}

func formatHTTPCode(code string) string {
	switch strings.ToUpper(code) {
	case "OK":
		return "http.StatusOK"
	case "CANCELLED", "CANCELED":
		return "499"
	case "UNKNOWN":
		return "http.StatusInternalServerError"
	case "INVALID_ARGUMENT":
		return "http.StatusBadRequest"
	case "DEADLINE_EXCEEDED":
		return "http.StatusGatewayTimeout"
	case "NOT_FOUND":
		return "http.StatusNotFound"
	case "ALREADY_EXISTS":
		return "http.StatusConflict"
	case "PERMISSION_DENIED":
		return "http.StatusForbidden"
	case "UNAUTHENTICATED":
		return "http.StatusUnauthorized"
	case "RESOURCE_EXHAUSTED":
		return "http.StatusTooManyRequests"
	case "FAILED_PRECONDITION":
		return "http.StatusBadRequest"
	case "ABORTED":
		return "http.StatusConflict"
	case "OUT_OF_RANGE":
		return "http.StatusBadRequest"
	case "UNIMPLEMENTED":
		return "http.StatusNotImplemented"
	case "INTERNAL":
		return "http.StatusInternalServerError"
	case "UNAVAILABLE":
		return "http.StatusServiceUnavailable"
	case "DATA_LOSS":
		return "http.StatusInternalServerError"
	default:
		return code
	}
}

func computeServiceImports(sAnn *ServiceAnnotation, descInfo *DescriptorInfo, retryMethods map[string]*MethodRetryConfig) FileImports {
	var raw []ImportSpec

	hasBody := false
	hasMapPagination := false
	hasAutoPopulated := false
	for _, m := range sAnn.Methods {
		if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil {
			if mAnn.BodyField != "" {
				hasBody = true
			}
			if mAnn.IsMapPagination {
				hasMapPagination = true
			}
			if !mAnn.IsPaged && !mAnn.IsServerStream && len(mAnn.AutoPopulatedFields) > 0 {
				hasAutoPopulated = true
			}
		}
	}

	if hasBody {
		raw = append(raw, ImportSpec{Path: "bytes"})
	}
	if hasAutoPopulated {
		raw = append(raw, ImportSpec{Path: "github.com/google/uuid"})
	}
	raw = append(raw,
		ImportSpec{Path: "context"},
		ImportSpec{Path: "fmt"},
		ImportSpec{Path: "log/slog"},
		ImportSpec{Path: "math"},
	)
	if sAnn.HasREST {
		raw = append(raw, ImportSpec{Path: "net/http"})
	}
	raw = append(raw, ImportSpec{Path: "net/url"})
	if hasMapPagination {
		raw = append(raw, ImportSpec{Path: "sort"})
	}

	// Third party base imports
	raw = append(raw,
		ImportSpec{Path: "github.com/googleapis/gax-go/v2", Name: "gax"},
		ImportSpec{Path: "github.com/googleapis/gax-go/v2/callctx"},
		ImportSpec{Path: "google.golang.org/api/iterator"},
		ImportSpec{Path: "google.golang.org/api/option"},
		ImportSpec{Path: "google.golang.org/api/option/internaloption"},
	)
	if sAnn.HasGRPC {
		raw = append(raw, ImportSpec{Path: "google.golang.org/api/transport/grpc", Name: "gtransport"})
	}
	if sAnn.HasREST {
		raw = append(raw, ImportSpec{Path: "google.golang.org/api/transport/http", Name: "httptransport"})
	}
	raw = append(raw,
		ImportSpec{Path: "google.golang.org/grpc"},
		ImportSpec{Path: "google.golang.org/protobuf/proto"},
	)
	if sAnn.HasREST {
		raw = append(raw, ImportSpec{Path: "google.golang.org/protobuf/encoding/protojson"})
	}

	hasDynamicRouting := false
	hasTime := false
	hasCodes := false

	for _, m := range sAnn.Methods {
		mProto := lookupMethodDescriptor(m, descInfo)
		if mProto != nil && dynamicRequestHeadersExist(mProto) {
			hasDynamicRouting = true
		}

		keyFull := m.SourceServiceID + "." + m.Name
		keySvc := m.SourceServiceID
		rc := retryMethods[keyFull]
		if rc == nil {
			rc = retryMethods[strings.TrimPrefix(keyFull, ".")]
		}
		if rc == nil {
			rc = retryMethods[keySvc]
		}
		if rc == nil {
			rc = retryMethods[strings.TrimPrefix(keySvc, ".")]
		}
		if rc != nil {
			if rc.HasRetry {
				hasTime = true
			}
			if rc.HasRetryCodes {
				hasCodes = true
			}
		}

		if descInfo != nil && descInfo.PkgByMessage != nil {
			if imp, ok := descInfo.PkgByMessage[m.InputTypeID]; ok {
				raw = append(raw, imp)
			}
			if imp, ok := descInfo.PkgByMessage[m.OutputTypeID]; ok {
				raw = append(raw, imp)
			}
		}
	}

	if hasDynamicRouting {
		raw = append(raw,
			ImportSpec{Path: "regexp"},
			ImportSpec{Path: "strings"},
		)
	}
	if hasTime {
		raw = append(raw, ImportSpec{Path: "time"})
	}
	if hasCodes {
		raw = append(raw, ImportSpec{Path: "google.golang.org/grpc/codes"})
	}
	if sAnn.HasLocationsMixin {
		raw = append(raw, ImportSpec{Path: "google.golang.org/genproto/googleapis/cloud/location", Name: "locationpb"})
	}
	if sAnn.HasIAMPolicyMixin {
		raw = append(raw, ImportSpec{Path: "cloud.google.com/go/iam/apiv1/iampb", Name: "iampb"})
	}
	if sAnn.HasLRO {
		raw = append(raw,
			ImportSpec{Path: "cloud.google.com/go/longrunning"},
			ImportSpec{Path: "cloud.google.com/go/longrunning/autogen", Name: "lroauto"},
			ImportSpec{Path: "cloud.google.com/go/longrunning/autogen/longrunningpb", Name: "longrunningpb"},
			ImportSpec{Path: "go.opentelemetry.io/otel/trace", Name: "trace"},
		)
	}

	if sAnn.HasREST {
		hasStream := false
		for _, m := range sAnn.Methods {
			if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil {
				if mAnn.IsServerStream || mAnn.IsClientStream || mAnn.IsBidiStream {
					hasStream = true
					break
				}
			}
		}
		if hasStream {
			raw = append(raw, ImportSpec{Path: "errors"})
		}
		for _, m := range sAnn.Methods {
			if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil && mAnn.IsServerStream {
				raw = append(raw, ImportSpec{Path: "google.golang.org/grpc/metadata"})
				break
			}
		}
	}
	hasWrapperPageSize := false
	for _, m := range sAnn.Methods {
		if mAnn, ok := m.Codec.(*MethodAnnotation); ok && mAnn != nil && mAnn.PageSizeIsWrapper {
			hasWrapperPageSize = true
			break
		}
	}
	if hasWrapperPageSize {
		raw = append(raw, ImportSpec{Path: "google.golang.org/protobuf/types/known/wrapperspb"})
	}

	return PartitionImports(raw)
}

func generateDefaultEndpointTemplate(defaultEndpoint string) string {
	return strings.Replace(defaultEndpoint, "googleapis.com", "UNIVERSE_DOMAIN", 1)
}

func generateDefaultMTLSEndpoint(defaultEndpoint string) string {
	var domains = []string{
		".sandbox.googleapis.com",
		".googleapis.com",
	}
	for _, domain := range domains {
		if strings.Contains(defaultEndpoint, domain) {
			return strings.ReplaceAll(defaultEndpoint, domain, ".mtls"+domain)
		}
	}
	return defaultEndpoint
}

func generateDefaultAudience(host string) string {
	aud := host
	if !strings.Contains(aud, "://") {
		aud = "https://" + aud
	}
	if strings.Count(aud, ":") > 1 {
		firstIndex := strings.Index(aud, ":")
		secondIndex := strings.Index(aud[firstIndex+1:], ":") + firstIndex + 1
		aud = aud[:secondIndex]
	}
	if !strings.HasSuffix(aud, "/") {
		aud = aud + "/"
	}
	return aud
}

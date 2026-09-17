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
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/librarian/internal/config"
	"github.com/googleapis/librarian/internal/sidekick/api"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"github.com/googleapis/librarian/internal/sources"
)

func findPinnedGoogleapis(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"../../../../harness/googleapis",
		"/usr/local/google/home/codyoss/code/sidekick-go/harness/googleapis",
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil {
			if _, statErr := os.Stat(path.Join(abs, "google/cloud/secretmanager/v1")); statErr == nil {
				return abs
			}
		}
	}
	t.Skip("pinned googleapis directory not found")
	return ""
}

func TestDocCommentFormatting(t *testing.T) {
	tests := []struct {
		name       string
		methodName string
		raw        string
		deprecated bool
		want       string
	}{
		{
			name:       "prepend method name and lowercase first word",
			methodName: "CreateSecret",
			raw:        "Creates a new secret.",
			want:       "// CreateSecret creates a new secret.",
		},
		{
			name:       "strip reference links",
			methodName: "ListSecrets",
			raw:        "Lists [Secrets][google.cloud.secretmanager.v1.Secret].",
			want:       "// ListSecrets lists Secrets.",
		},
		{
			name:       "format markdown links",
			methodName: "OpenConsole",
			raw:        "Opens the [Google Cloud Console](https://console.cloud.google.com).",
			want:       "// OpenConsole opens the Google Cloud Console (at https://console.cloud.google.com).",
		},
		{
			name:       "format html links",
			methodName: "OpenConsoleHTML",
			raw:        `Opens <a href="https://console.cloud.google.com">Google Cloud Console</a>.`,
			want:       "// OpenConsoleHTML opens Google Cloud Console (at https://console.cloud.google.com).",
		},
		{
			name:       "strip code inline backticks",
			methodName: "GetSecretVersion",
			raw:        "Gets the `latest` version.",
			want:       "// GetSecretVersion gets the latest version.",
		},
		{
			name:       "strip bold asterisks",
			methodName: "DoWork",
			raw:        "Performs **critical** work.",
			want:       "// DoWork performs critical work.",
		},
		{
			name:       "convert typographic smart quotes",
			methodName: "TestIamPermissions",
			raw:        `May "fail open" without warning.`,
			want:       "// TestIamPermissions may “fail open” without warning.",
		},
		{
			name:       "deprecated method formatting",
			methodName: "OldMethod",
			raw:        "Does something old.",
			deprecated: true,
			want:       "// OldMethod does something old.\n//\n// Deprecated: OldMethod may be removed in a future version.",
		},
		{
			name:       "preserve ASCII quotes after equals in filter syntax",
			methodName: "ListAccounts",
			raw:        `Filter by type="ACCOUNT_AGGREGATION".`,
			want:       `// ListAccounts filter by type="ACCOUNT_AGGREGATION".`,
		},
		{
			name:       "preserve existing Deprecated comment",
			methodName: "OldMethod",
			raw:        "Deprecated: use NewMethod instead.",
			deprecated: true,
			want:       "// OldMethod is deprecated.\n//\n// Deprecated: use NewMethod instead.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMethodDoc(tt.methodName, tt.raw, tt.deprecated)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("FormatMethodDoc mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFormatDocSummary(t *testing.T) {
	raw := "Deploy and manage user provided container images that scale automatically based on incoming requests. The Cloud Run Admin API v1 follows the Knative Serving API specification, while v2 is aligned with Google Cloud AIP-based API standards, as described in https://google.aip.dev/."
	want := []string{
		"Deploy and manage user provided container images that scale automatically",
		"based on incoming requests. The Cloud Run Admin API v1 follows the Knative",
		"Serving API specification, while v2 is aligned with Google Cloud AIP-based",
		"API standards, as described in https://google.aip.dev/ (at",
		"https://google.aip.dev/).",
	}

	got := FormatDocSummary(raw)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FormatDocSummary mismatch (-want +got):\n%s", diff)
	}
}

func TestPartitionImports(t *testing.T) {
	imports := []ImportSpec{
		{Path: "google.golang.org/grpc", Name: "grpc"},
		{Path: "context", Name: "context"},
		{Path: "fmt", Name: "fmt"},
		{Path: "cloud.google.com/go/run/apiv2/runpb", Name: "runpb"},
		{Path: "time", Name: "time"},
		{Path: "context", Name: "context"}, // duplicate to test deduplication
	}

	res := PartitionImports(imports)

	wantStd := []ImportSpec{
		{Path: "context", Name: "context", Line: `context "context"`},
		{Path: "fmt", Name: "fmt", Line: `fmt "fmt"`},
		{Path: "time", Name: "time", Line: `time "time"`},
	}
	wantThirdParty := []ImportSpec{
		{Path: "cloud.google.com/go/run/apiv2/runpb", Name: "runpb", Line: `runpb "cloud.google.com/go/run/apiv2/runpb"`},
		{Path: "google.golang.org/grpc", Name: "grpc", Line: `grpc "google.golang.org/grpc"`},
	}

	if diff := cmp.Diff(wantStd, res.Standard); diff != "" {
		t.Errorf("Standard imports mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantThirdParty, res.ThirdParty); diff != "" {
		t.Errorf("ThirdParty imports mismatch (-want +got):\n%s", diff)
	}
}

func TestAnnotateSecretManager(t *testing.T) {
	googleapisDir := findPinnedGoogleapis(t)

	cfg := &parser.ModelConfig{
		SpecificationFormat: config.SpecProtobuf,
		ServiceConfig:       "google/cloud/secretmanager/v1/secretmanager_v1.yaml",
		SpecificationSource: "google/cloud/secretmanager/v1",
		Source: &sources.SourceConfig{
			Sources:     &sources.Sources{Googleapis: googleapisDir},
			ActiveRoots: []string{"googleapis"},
		},
		Codec: map[string]string{
			"copyright-year": "2026",
			"import-path":    "cloud.google.com/go/secretmanager/apiv1",
			"client-package": "secretmanager",
		},
	}

	model, err := parser.CreateModel(cfg)
	if err != nil {
		t.Skipf("cannot create model (protoc might be missing): %v", err)
	}

	ann, err := AnnotateModel(model, cfg)
	if err != nil {
		t.Fatalf("AnnotateModel failed: %v", err)
	}

	if len(ann.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(ann.Services))
	}

	svc := ann.Services[0]
	sAnn, ok := svc.Codec.(*ServiceAnnotation)
	if !ok || sAnn == nil {
		t.Fatalf("expected *ServiceAnnotation on service.Codec")
	}

	if sAnn.ClientName != "Client" {
		t.Errorf("expected ClientName Client, got %s", sAnn.ClientName)
	}
	if sAnn.InternalClientInterface != "internalClient" {
		t.Errorf("expected InternalClientInterface internalClient, got %s", sAnn.InternalClientInterface)
	}
	if sAnn.GRPCClientName != "gRPCClient" {
		t.Errorf("expected GRPCClientName gRPCClient, got %s", sAnn.GRPCClientName)
	}
	if sAnn.RESTClientName != "restClient" {
		t.Errorf("expected RESTClientName restClient, got %s", sAnn.RESTClientName)
	}
	if sAnn.CallOptionsName != "CallOptions" {
		t.Errorf("expected CallOptionsName CallOptions, got %s", sAnn.CallOptionsName)
	}
	if sAnn.DefaultCallOptionsName != "defaultCallOptions" {
		t.Errorf("expected DefaultCallOptionsName defaultCallOptions, got %s", sAnn.DefaultCallOptionsName)
	}
	if sAnn.FileName != "secret_manager_client.go" {
		t.Errorf("expected FileName secret_manager_client.go, got %s", sAnn.FileName)
	}

	// Verify DocExample for SecretManager: NewClient and AccessSecretVersion (alphabetically first method).
	if ann.DocExample.ConstructorName != "NewClient" {
		t.Errorf("expected DocExample.ConstructorName NewClient, got %s", ann.DocExample.ConstructorName)
	}
	if !ann.DocExample.HasMethod {
		t.Errorf("expected DocExample.HasMethod to be true")
	}
	if ann.DocExample.MethodName != "AccessSecretVersion" {
		t.Errorf("expected DocExample.MethodName AccessSecretVersion, got %s", ann.DocExample.MethodName)
	}
	if ann.DocExample.ProtoPkg != "secretmanagerpb" {
		t.Errorf("expected DocExample.ProtoPkg secretmanagerpb, got %s", ann.DocExample.ProtoPkg)
	}
	if ann.DocExample.RequestType != "AccessSecretVersionRequest" {
		t.Errorf("expected DocExample.RequestType AccessSecretVersionRequest, got %s", ann.DocExample.RequestType)
	}

	// Verify DocSummaryLines.
	wantSummaryLines := []string{
		"Stores sensitive data such as API keys, passwords, and certificates.",
		"Provides convenience while improving security.",
	}
	if diff := cmp.Diff(wantSummaryLines, ann.DocSummaryLines); diff != "" {
		t.Errorf("DocSummaryLines mismatch (-want +got):\n%s", diff)
	}

	// Verify DocImports is empty.
	if len(ann.DocImports.Standard) != 0 || len(ann.DocImports.ThirdParty) != 0 {
		t.Errorf("expected DocImports to be empty, got %v", ann.DocImports)
	}

	// Verify HelpersImports has 5 standard and 7 third-party imports.
	if len(ann.HelpersImports.Standard) != 5 {
		t.Errorf("expected 5 standard imports in HelpersImports, got %d", len(ann.HelpersImports.Standard))
	}
	if len(ann.HelpersImports.ThirdParty) != 7 {
		t.Errorf("expected 7 third-party imports in HelpersImports, got %d", len(ann.HelpersImports.ThirdParty))
	}

	// Verify AuxiliaryImports has 1 standard ("iter") and 4 third-party imports.
	wantAuxStd := []ImportSpec{{Path: "iter", Line: `"iter"`}}
	if diff := cmp.Diff(wantAuxStd, ann.AuxiliaryImports.Standard); diff != "" {
		t.Errorf("AuxiliaryImports standard mismatch (-want +got):\n%s", diff)
	}
	if len(ann.AuxiliaryImports.ThirdParty) != 4 {
		t.Errorf("expected 4 third-party imports in AuxiliaryImports, got %d", len(ann.AuxiliaryImports.ThirdParty))
	}

	// Verify MetadataServices.
	if len(ann.MetadataServices) != 1 {
		t.Fatalf("expected 1 MetadataService, got %d", len(ann.MetadataServices))
	}
	metaSvc := ann.MetadataServices[0]
	if metaSvc.Name != "SecretManagerService" {
		t.Errorf("expected MetadataService Name SecretManagerService, got %s", metaSvc.Name)
	}
	if len(metaSvc.Clients) != 2 {
		t.Fatalf("expected 2 clients in MetadataService, got %d", len(metaSvc.Clients))
	}
	if metaSvc.Clients[0].LibraryClient != "Client" || metaSvc.Clients[1].LibraryClient != "Client" {
		t.Errorf("expected LibraryClient Client, got %s and %s", metaSvc.Clients[0].LibraryClient, metaSvc.Clients[1].LibraryClient)
	}

	// Verify mixin methods: GetLocation and ListLocations must be at the end, in alphabetical order.
	nMethods := len(sAnn.Methods)
	if nMethods < 2 {
		t.Fatalf("too few methods: %d", nMethods)
	}
	lastTwo := []string{sAnn.Methods[nMethods-2].Name, sAnn.Methods[nMethods-1].Name}
	wantLastTwo := []string{"GetLocation", "ListLocations"}
	if !slices.Equal(lastTwo, wantLastTwo) {
		t.Errorf("expected last two methods to be %v, got %v", wantLastTwo, lastTwo)
	}

	// Verify iterators are collected and sorted alphabetically.
	var iterNames []string
	for _, it := range ann.Iterators {
		iterNames = append(iterNames, it.TypeName)
	}
	wantIters := []string{"LocationIterator", "SecretIterator", "SecretVersionIterator"}
	if diff := cmp.Diff(wantIters, iterNames); diff != "" {
		t.Errorf("Iterators mismatch (-want +got):\n%s", diff)
	}

	// Verify Milestone 3 fields on SecretManager.
	if sAnn.ExampleNewClientName != "ExampleNewClient" {
		t.Errorf("expected ExampleNewClientName 'ExampleNewClient', got %q", sAnn.ExampleNewClientName)
	}
	if sAnn.ExampleNewRESTClientName != "ExampleNewRESTClient" {
		t.Errorf("expected ExampleNewRESTClientName 'ExampleNewRESTClient', got %q", sAnn.ExampleNewRESTClientName)
	}
	if sAnn.ExampleTestFileName != "secret_manager_client_example_test.go" {
		t.Errorf("expected ExampleTestFileName 'secret_manager_client_example_test.go', got %q", sAnn.ExampleTestFileName)
	}
	if sAnn.ExampleGo123TestFileName != "secret_manager_client_example_go123_test.go" {
		t.Errorf("expected ExampleGo123TestFileName 'secret_manager_client_example_go123_test.go', got %q", sAnn.ExampleGo123TestFileName)
	}

	// Verify ExampleMethods on SecretManager: native methods sorted alphabetically, then Locations.
	var exMethodNames []string
	for _, m := range sAnn.ExampleMethods {
		exMethodNames = append(exMethodNames, m.Name)
	}
	if len(exMethodNames) != len(sAnn.Methods) {
		t.Errorf("expected %d ExampleMethods, got %d", len(sAnn.Methods), len(exMethodNames))
	}
	nativeEx := exMethodNames[:len(exMethodNames)-2]
	if !slices.IsSorted(nativeEx) {
		t.Errorf("native ExampleMethods not sorted alphabetically: %v", nativeEx)
	}
	if exMethodNames[len(exMethodNames)-2] != "GetLocation" || exMethodNames[len(exMethodNames)-1] != "ListLocations" {
		t.Errorf("expected last two ExampleMethods to be GetLocation, ListLocations, got %v", exMethodNames[len(exMethodNames)-2:])
	}

	// Verify PagedExampleMethods: ListSecretVersions, ListSecrets, ListLocations.
	var pagedNames []string
	for _, m := range sAnn.PagedExampleMethods {
		pagedNames = append(pagedNames, m.Name)
	}
	wantPaged := []string{"ListSecretVersions", "ListSecrets", "ListLocations"}
	if diff := cmp.Diff(wantPaged, pagedNames); diff != "" {
		t.Errorf("PagedExampleMethods mismatch (-want +got):\n%s", diff)
	}

	// Verify ExampleImports includes iterator.
	hasIterator := false
	for _, imp := range sAnn.ExampleImports.ThirdParty {
		if imp.Path == "google.golang.org/api/iterator" {
			hasIterator = true
			break
		}
	}
	if !hasIterator {
		t.Errorf("expected ExampleImports to contain google.golang.org/api/iterator")
	}

	// Verify ExampleGo123Imports does NOT include iterator.
	for _, imp := range sAnn.ExampleGo123Imports.ThirdParty {
		if imp.Path == "google.golang.org/api/iterator" {
			t.Errorf("unexpected google.golang.org/api/iterator in ExampleGo123Imports")
		}
	}

	// Verify snippet annotations on AccessSecretVersion.
	var accessM *api.Method
	for _, m := range sAnn.ExampleMethods {
		if m.Name == "AccessSecretVersion" {
			accessM = m
			break
		}
	}
	if accessM == nil {
		t.Fatalf("AccessSecretVersion not found in ExampleMethods")
	}
	accessMAnn := accessM.Codec.(*MethodAnnotation)
	wantRegionTag := "secretmanager_v1_generated_SecretManagerService_AccessSecretVersion_sync"
	if accessMAnn.RegionTag != wantRegionTag {
		t.Errorf("expected RegionTag %q, got %q", wantRegionTag, accessMAnn.RegionTag)
	}
	if !strings.HasPrefix(accessMAnn.SnippetDescription, "AccessSecretVersion accesses a [SecretVersion]") {
		t.Errorf("expected SnippetDescription to start with 'AccessSecretVersion accesses a [SecretVersion]', got %q", accessMAnn.SnippetDescription)
	}
	if len(accessMAnn.Imports.Standard) != 1 || accessMAnn.Imports.Standard[0].Path != "context" {
		t.Errorf("expected standard import 'context', got %v", accessMAnn.Imports.Standard)
	}
	if len(accessMAnn.Imports.ThirdParty) != 2 {
		t.Errorf("expected 2 third-party imports in snippet, got %d", len(accessMAnn.Imports.ThirdParty))
	}
}

func TestReduceServiceName(t *testing.T) {
	tests := []struct {
		svc  string
		pkg  string
		want string
	}{
		{"SecretManagerService", "secretmanager", ""},
		{"SecretManagerService", "foo", "SecretManager"},
		{"Builds", "run", "Builds"},
		{"BuildsService", "run", "Builds"},
		{"IAMPolicy", "iam", "IamPolicy"},
		{"IAMService", "iam", ""},
		{"IAM", "foo", "Iam"},
		{"LocationsServiceV2", "locations", ""},
		{"DataprocV1", "dataproc", ""},
	}

	for _, tt := range tests {
		got := reduceServiceName(tt.svc, tt.pkg)
		if got != tt.want {
			t.Errorf("reduceServiceName(%q, %q) = %q, want %q", tt.svc, tt.pkg, got, tt.want)
		}
	}
}

func TestAnnotateRun(t *testing.T) {
	googleapisDir := findPinnedGoogleapis(t)

	cfg := &parser.ModelConfig{
		SpecificationFormat: config.SpecProtobuf,
		ServiceConfig:       "google/cloud/run/v2/run_v2.yaml",
		SpecificationSource: "google/cloud/run/v2",
		Source: &sources.SourceConfig{
			Sources:     &sources.Sources{Googleapis: googleapisDir},
			ActiveRoots: []string{"googleapis"},
		},
		Codec: map[string]string{
			"copyright-year": "2026",
			"import-path":    "cloud.google.com/go/run/apiv2",
			"client-package": "run",
		},
	}

	model, err := parser.CreateModel(cfg)
	if err != nil {
		t.Skipf("cannot create model (protoc might be missing): %v", err)
	}

	ann, err := AnnotateModel(model, cfg)
	if err != nil {
		t.Fatalf("AnnotateModel failed: %v", err)
	}

	if len(ann.Services) != 8 {
		t.Fatalf("expected 8 services, got %d", len(ann.Services))
	}

	// Find Services service.
	var servicesSvc *api.Service
	for _, s := range ann.Services {
		if s.Name == "Services" {
			servicesSvc = s
			break
		}
	}
	if servicesSvc == nil {
		t.Fatalf("Services service not found")
	}

	sAnn := servicesSvc.Codec.(*ServiceAnnotation)

	// Verify NO Location mixins on Run (run_v2.yaml defines no Location http.rules).
	for _, m := range sAnn.Methods {
		if m.SourceServiceID == locationService {
			t.Errorf("unexpected Location mixin method %s found on Run Services", m.Name)
		}
	}

	// Verify Operations mixin methods at end in alphabetical order.
	var opsMethods []string
	for _, m := range sAnn.Methods {
		if m.SourceServiceID == longrunningService {
			opsMethods = append(opsMethods, m.Name)
		}
	}
	wantOps := []string{"DeleteOperation", "GetOperation", "ListOperations", "WaitOperation"}
	if diff := cmp.Diff(wantOps, opsMethods); diff != "" {
		t.Errorf("Operations mixin methods mismatch (-want +got):\n%s", diff)
	}

	// Verify internal client LRO builders are sorted alphabetically by method name.
	var builderMethods []string
	for _, m := range sAnn.InternalLROBuilders {
		builderMethods = append(builderMethods, m.Name)
	}
	wantBuilders := []string{"CreateService", "DeleteService", "UpdateService"}
	if diff := cmp.Diff(wantBuilders, builderMethods); diff != "" {
		t.Errorf("InternalLROBuilders mismatch (-want +got):\n%s", diff)
	}

	// Verify query parameters on GetIamPolicy includes options.requestedPolicyVersion.
	var getIamPolicyMethod *api.Method
	for _, m := range sAnn.Methods {
		if m.Name == "GetIamPolicy" {
			getIamPolicyMethod = m
			break
		}
	}
	if getIamPolicyMethod == nil {
		t.Fatalf("GetIamPolicy method not found")
	}
	mAnn := getIamPolicyMethod.Codec.(*MethodAnnotation)
	if !slices.Contains(mAnn.QueryParams, "options.requestedPolicyVersion") {
		t.Errorf("expected QueryParams to contain 'options.requestedPolicyVersion', got %v", mAnn.QueryParams)
	}

	// Verify auxiliary operation wrappers are sorted alphabetically.
	var opNames []string
	for _, op := range ann.OperationWrappers {
		opNames = append(opNames, op.Name)
	}
	if !slices.IsSorted(opNames) {
		t.Errorf("OperationWrappers not sorted alphabetically: %v", opNames)
	}
	if len(opNames) != 17 {
		t.Errorf("expected 17 OperationWrappers, got %d: %v", len(opNames), opNames)
	}
	if slices.Contains(opNames, "GetOperationOperation") {
		t.Errorf("unexpected GetOperationOperation in OperationWrappers")
	}
	if slices.Contains(opNames, "WaitOperationOperation") {
		t.Errorf("unexpected WaitOperationOperation in OperationWrappers")
	}

	// Verify auxiliary iterators are sorted alphabetically.
	var iterNames []string
	for _, it := range ann.Iterators {
		iterNames = append(iterNames, it.TypeName)
	}
	if !slices.IsSorted(iterNames) {
		t.Errorf("Iterators not sorted alphabetically: %v", iterNames)
	}

	// Verify Milestone 3 fields on Run Services.
	if sAnn.ExampleNewClientName != "ExampleNewServicesClient" {
		t.Errorf("expected ExampleNewClientName 'ExampleNewServicesClient', got %q", sAnn.ExampleNewClientName)
	}
	if sAnn.ExampleNewRESTClientName != "ExampleNewServicesRESTClient" {
		t.Errorf("expected ExampleNewRESTClientName 'ExampleNewServicesRESTClient', got %q", sAnn.ExampleNewRESTClientName)
	}
	if sAnn.ExampleTestFileName != "services_client_example_test.go" {
		t.Errorf("expected ExampleTestFileName 'services_client_example_test.go', got %q", sAnn.ExampleTestFileName)
	}
	if sAnn.ExampleGo123TestFileName != "services_client_example_go123_test.go" {
		t.Errorf("expected ExampleGo123TestFileName 'services_client_example_go123_test.go', got %q", sAnn.ExampleGo123TestFileName)
	}

	// Verify PagedExampleMethods on Services: ListServices, ListOperations.
	var runPagedNames []string
	for _, m := range sAnn.PagedExampleMethods {
		runPagedNames = append(runPagedNames, m.Name)
	}
	wantRunPaged := []string{"ListServices", "ListOperations"}
	if diff := cmp.Diff(wantRunPaged, runPagedNames); diff != "" {
		t.Errorf("Services PagedExampleMethods mismatch (-want +got):\n%s", diff)
	}

	// Verify snippet annotations on Services.CreateService and DeleteOperation.
	var createSvcM, deleteOpM *api.Method
	for _, m := range sAnn.ExampleMethods {
		switch m.Name {
		case "CreateService":
			createSvcM = m
		case "DeleteOperation":
			deleteOpM = m
		}
	}
	if createSvcM == nil {
		t.Fatalf("CreateService not found in ExampleMethods")
	}
	createMAnn := createSvcM.Codec.(*MethodAnnotation)
	wantCreateTag := "run_v2_generated_Services_CreateService_sync"
	if createMAnn.RegionTag != wantCreateTag {
		t.Errorf("expected RegionTag %q, got %q", wantCreateTag, createMAnn.RegionTag)
	}
	if createMAnn.OperationType != "CreateServiceOperation" {
		t.Errorf("expected OperationType 'CreateServiceOperation', got %q", createMAnn.OperationType)
	}
	if deleteOpM == nil {
		t.Fatalf("DeleteOperation not found in ExampleMethods")
	}
	delMAnn := deleteOpM.Codec.(*MethodAnnotation)
	wantDelTag := "run_v2_generated_Services_DeleteOperation_sync"
	if delMAnn.RegionTag != wantDelTag {
		t.Errorf("expected RegionTag %q, got %q", wantDelTag, delMAnn.RegionTag)
	}
	wantDelDesc := "DeleteOperation is a utility method from google.longrunning.Operations."
	if delMAnn.SnippetDescription != wantDelDesc {
		t.Errorf("expected SnippetDescription %q, got %q", wantDelDesc, delMAnn.SnippetDescription)
	}
}

func TestComputeHelpersImportsGating(t *testing.T) {
	// When HasGRPC is true and HasREST is true, all HTTP and gRPC imports must be present.
	restImports := computeHelpersImports(true, true)
	if len(restImports.Standard) != 5 {
		t.Errorf("expected 5 standard imports when HasREST=true, got %d: %v", len(restImports.Standard), restImports.Standard)
	}
	if len(restImports.ThirdParty) != 7 {
		t.Errorf("expected 7 third-party imports when HasREST=true, got %d: %v", len(restImports.ThirdParty), restImports.ThirdParty)
	}

	var restStdPaths []string
	for _, imp := range restImports.Standard {
		restStdPaths = append(restStdPaths, imp.Path)
	}
	for _, want := range []string{"context", "fmt", "io", "log/slog", "net/http"} {
		if !slices.Contains(restStdPaths, want) {
			t.Errorf("expected standard imports to contain %q, got %v", want, restStdPaths)
		}
	}

	var restThirdPaths []string
	for _, imp := range restImports.ThirdParty {
		restThirdPaths = append(restThirdPaths, imp.Path)
	}
	for _, want := range []string{
		"github.com/googleapis/gax-go/v2/internallog",
		"github.com/googleapis/gax-go/v2/internallog/grpclog",
		"google.golang.org/api/googleapi",
		"google.golang.org/api/option",
		"google.golang.org/grpc",
		"google.golang.org/protobuf/proto",
		"google.golang.org/protobuf/runtime/protoimpl",
	} {
		if !slices.Contains(restThirdPaths, want) {
			t.Errorf("expected third-party imports to contain %q, got %v", want, restThirdPaths)
		}
	}

	// When HasREST is false, HTTP imports (net/http, googleapi, io, internallog) must be omitted.
	noRestImports := computeHelpersImports(true, false)
	if len(noRestImports.Standard) != 3 {
		t.Errorf("expected 3 standard imports when HasREST=false, got %d: %v", len(noRestImports.Standard), noRestImports.Standard)
	}
	if len(noRestImports.ThirdParty) != 5 {
		t.Errorf("expected 5 third-party imports when HasREST=false, got %d: %v", len(noRestImports.ThirdParty), noRestImports.ThirdParty)
	}

	var noRestStdPaths []string
	for _, imp := range noRestImports.Standard {
		noRestStdPaths = append(noRestStdPaths, imp.Path)
	}
	if slices.Contains(noRestStdPaths, "io") || slices.Contains(noRestStdPaths, "net/http") {
		t.Errorf("unexpected HTTP standard imports in no-REST imports: %v", noRestStdPaths)
	}

	var noRestThirdPaths []string
	for _, imp := range noRestImports.ThirdParty {
		noRestThirdPaths = append(noRestThirdPaths, imp.Path)
	}
	if slices.Contains(noRestThirdPaths, "github.com/googleapis/gax-go/v2/internallog") {
		t.Errorf("unexpected internallog in no-REST imports: %v", noRestThirdPaths)
	}
	if slices.Contains(noRestThirdPaths, "google.golang.org/api/googleapi") {
		t.Errorf("unexpected googleapi in no-REST imports: %v", noRestThirdPaths)
	}
	if !slices.Contains(noRestThirdPaths, "github.com/googleapis/gax-go/v2/internallog/grpclog") {
		t.Errorf("expected grpclog to be retained in no-REST imports: %v", noRestThirdPaths)
	}

	// When HasGRPC is false and HasREST is true (e.g. DIREGAPIC), gRPC imports must be omitted.
	diregapicImports := computeHelpersImports(false, true)
	if len(diregapicImports.Standard) != 5 {
		t.Errorf("expected 5 standard imports when HasGRPC=false, HasREST=true, got %d", len(diregapicImports.Standard))
	}
	if len(diregapicImports.ThirdParty) != 4 {
		t.Errorf("expected 4 third-party imports when HasGRPC=false, HasREST=true, got %d: %v", len(diregapicImports.ThirdParty), diregapicImports.ThirdParty)
	}
	var direThirdPaths []string
	for _, imp := range diregapicImports.ThirdParty {
		direThirdPaths = append(direThirdPaths, imp.Path)
	}
	for _, want := range []string{
		"github.com/googleapis/gax-go/v2/internallog",
		"google.golang.org/api/googleapi",
		"google.golang.org/api/option",
		"google.golang.org/protobuf/runtime/protoimpl",
	} {
		if !slices.Contains(direThirdPaths, want) {
			t.Errorf("expected diregapic imports to contain %q, got %v", want, direThirdPaths)
		}
	}
	for _, unwanted := range []string{
		"github.com/googleapis/gax-go/v2/internallog/grpclog",
		"google.golang.org/grpc",
		"google.golang.org/protobuf/proto",
	} {
		if slices.Contains(direThirdPaths, unwanted) {
			t.Errorf("unwanted %q found in diregapic imports: %v", unwanted, direThirdPaths)
		}
	}
}

func TestAnnotateModelTransportAndExportClientInfo(t *testing.T) {
	tests := []struct {
		name                          string
		codec                         map[string]string
		wantModelHasREST              bool
		wantModelHasGRPC              bool
		wantServiceHasREST            bool
		wantServiceHasGRPC            bool
		wantExportSetGoogleClientInfo bool
	}{
		{
			name:                          "default (both transports)",
			codec:                         map[string]string{},
			wantModelHasREST:              true,
			wantModelHasGRPC:              true,
			wantServiceHasREST:            true,
			wantServiceHasGRPC:            true,
			wantExportSetGoogleClientInfo: false,
		},
		{
			name: "grpc only transport",
			codec: map[string]string{
				"transport": "grpc",
			},
			wantModelHasREST:              false,
			wantModelHasGRPC:              true,
			wantServiceHasREST:            false,
			wantServiceHasGRPC:            true,
			wantExportSetGoogleClientInfo: false,
		},
		{
			name: "rest only transport",
			codec: map[string]string{
				"transport": "rest",
			},
			wantModelHasREST:              true,
			wantModelHasGRPC:              false,
			wantServiceHasREST:            true,
			wantServiceHasGRPC:            false,
			wantExportSetGoogleClientInfo: false,
		},
		{
			name: "diregapic forces rest only",
			codec: map[string]string{
				"diregapic": "true",
			},
			wantModelHasREST:              true,
			wantModelHasGRPC:              false,
			wantServiceHasREST:            true,
			wantServiceHasGRPC:            false,
			wantExportSetGoogleClientInfo: false,
		},
		{
			name: "explicit grpc,rest",
			codec: map[string]string{
				"transport": "grpc,rest",
			},
			wantModelHasREST:              true,
			wantModelHasGRPC:              true,
			wantServiceHasREST:            true,
			wantServiceHasGRPC:            true,
			wantExportSetGoogleClientInfo: false,
		},
		{
			name: "export set google client info feature flag enabled",
			codec: map[string]string{
				"F_export_set_google_client_info": "true",
			},
			wantModelHasREST:              true,
			wantModelHasGRPC:              true,
			wantServiceHasREST:            true,
			wantServiceHasGRPC:            true,
			wantExportSetGoogleClientInfo: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := &api.API{
				Name: "sample",
				Services: []*api.Service{
					{
						Name:        "SampleService",
						DefaultHost: "sample.googleapis.com",
					},
				},
			}
			cfg := &parser.ModelConfig{
				Codec: tc.codec,
			}

			mAnn, err := AnnotateModel(model, cfg)
			if err != nil {
				t.Fatalf("AnnotateModel failed: %v", err)
			}

			if mAnn.HasREST != tc.wantModelHasREST {
				t.Errorf("ModelAnnotation.HasREST = %v, want %v", mAnn.HasREST, tc.wantModelHasREST)
			}
			if mAnn.HasGRPC != tc.wantModelHasGRPC {
				t.Errorf("ModelAnnotation.HasGRPC = %v, want %v", mAnn.HasGRPC, tc.wantModelHasGRPC)
			}

			if len(model.Services) == 0 {
				t.Fatalf("no services in model")
			}
			sAnn, ok := model.Services[0].Codec.(*ServiceAnnotation)
			if !ok || sAnn == nil {
				t.Fatalf("service Codec is not *ServiceAnnotation")
			}

			if sAnn.HasREST != tc.wantServiceHasREST {
				t.Errorf("ServiceAnnotation.HasREST = %v, want %v", sAnn.HasREST, tc.wantServiceHasREST)
			}
			if sAnn.HasGRPC != tc.wantServiceHasGRPC {
				t.Errorf("ServiceAnnotation.HasGRPC = %v, want %v", sAnn.HasGRPC, tc.wantServiceHasGRPC)
			}
			if sAnn.HasExportSetGoogleClientInfo != tc.wantExportSetGoogleClientInfo {
				t.Errorf("ServiceAnnotation.HasExportSetGoogleClientInfo = %v, want %v", sAnn.HasExportSetGoogleClientInfo, tc.wantExportSetGoogleClientInfo)
			}
		})
	}
}

func TestExtractQueryParamsCycleDetection(t *testing.T) {
	nodeMsg := &api.Message{
		Name: "Node",
		ID:   ".test.Node",
		Fields: []*api.Field{
			{
				Name:     "val",
				JSONName: "val",
				Typez:    api.TypezString,
			},
			{
				Name:     "next",
				JSONName: "next",
				Typez:    api.TypezMessage,
				TypezID:  ".test.Node",
			},
		},
	}

	model := &api.API{
		Name: "testapi",
		Messages: []*api.Message{
			nodeMsg,
		},
	}

	m := &api.Method{
		Name:      "GetNode",
		InputType: nodeMsg,
		PathInfo: &api.PathInfo{
			Bindings: []*api.PathBinding{
				{
					Verb: "GET",
					PathTemplate: &api.PathTemplate{
						Segments: []api.PathSegment{
							{
								Literal: "v1",
							},
						},
					},
				},
			},
		},
	}

	params := extractQueryParams(m, model)
	if !slices.Contains(params, "val") {
		t.Errorf("expected params to contain %q, got: %v", "val", params)
	}
}

func TestComputeDocCommentFormatting(t *testing.T) {
	tests := []struct {
		name       string
		methodName string
		raw        string
		want       string
	}{
		{
			name:       "truncate 4-space markdown blocks after Specifically:",
			methodName: "Wait",
			raw: "Waits for the specified Operation resource to return as DONE.\n\nThis method is called on a best-effort basis. Specifically:\n\n\n    - In uncommon cases, when the server is overloaded, the request might\n    return before the default deadline is reached.",
			want: "// Wait waits for the specified Operation resource to return as DONE.\n//\n// This method is called on a best-effort basis. Specifically:",
		},
		{
			name:       "truncate 4-space markdown blocks after Note: Use the following APIs to manage network endpoint groups:",
			methodName: "Insert",
			raw: "Creates a network endpoint group in the specified project.\n\nNote: Use the following APIs to manage network endpoint groups:\n\n    -\n    To manage NEGs with zonal scope: zonal API",
			want: "// Insert creates a network endpoint group in the specified project.\n//\n// Note: Use the following APIs to manage network endpoint groups:",
		},
		{
			name:       "autolink perInstanceConfig.name domain",
			methodName: "UpdatePerInstanceConfigs",
			raw: "Inserts or updates per-instance configurations. perInstanceConfig.name serves as a key used to distinguish whether to perform insert or patch.",
			want: "// UpdatePerInstanceConfigs inserts or updates per-instance configurations. perInstanceConfig.name (at http://perInstanceConfig.name) serves as a key used to distinguish whether to perform insert or patch.",
		},
		{
			name:       "plus bullet list items and continuation",
			methodName: "Resize",
			raw: "Resize selection including:\n\n+ The status of the VM instance.\n+ The health of the VM instance.\ncontinuation of health.",
			want: "// Resize resize selection including:\n//\n//   The status of the VM instance.\n//\n//   The health of the VM instance.\n//   continuation of health.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMethodDoc(tt.methodName, tt.raw, false)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("FormatMethodDoc mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestComputeSnippetResultTypeCustomOp(t *testing.T) {
	mAnn := &MethodAnnotation{
		IsCustomOp: true,
	}
	got := computeSnippetResultType(mAnn)
	if got != "*Operation" {
		t.Errorf("expected *Operation, got %q", got)
	}
}

func TestFindPageSizeFieldWrapperGating(t *testing.T) {
	regularMethod := &api.Method{
		InputType: &api.Message{
			Fields: []*api.Field{
				{Name: "page_size", Typez: api.TypezInt32},
			},
		},
	}
	wrapperMethod := &api.Method{
		InputType: &api.Message{
			Fields: []*api.Field{
				{Name: "max_results", Typez: api.TypezMessage, TypezID: ".google.protobuf.UInt32Value"},
			},
		},
	}

	if f := findPageSizeField(regularMethod, false); f == nil || f.Name != "page_size" {
		t.Errorf("expected regular page size field to be found when wrappersAllowed=false")
	}
	if f := findPageSizeField(regularMethod, true); f == nil || f.Name != "page_size" {
		t.Errorf("expected regular page size field to be found when wrappersAllowed=true")
	}

	if f := findPageSizeField(wrapperMethod, false); f != nil {
		t.Errorf("expected wrapper page size field to be ignored when wrappersAllowed=false, got %v", f)
	}
	if f := findPageSizeField(wrapperMethod, true); f == nil || f.Name != "max_results" {
		t.Errorf("expected wrapper page size field to be found when wrappersAllowed=true")
	}
}

func TestComputeServiceImportsPureGRPC(t *testing.T) {
	pureGRPC := &ServiceAnnotation{
		HasGRPC: true,
		HasREST: false,
	}
	imports := computeServiceImports(pureGRPC, nil, nil)
	for _, imp := range imports.Standard {
		if imp.Path == "net/http" {
			t.Errorf("pure gRPC service imports should not contain net/http")
		}
	}
	for _, imp := range imports.ThirdParty {
		if imp.Path == "google.golang.org/protobuf/encoding/protojson" {
			t.Errorf("pure gRPC service imports should not contain protojson")
		}
	}

	dualTransport := &ServiceAnnotation{
		HasGRPC: true,
		HasREST: true,
	}
	dualImports := computeServiceImports(dualTransport, nil, nil)
	hasHTTP := false
	hasProtojson := false
	for _, imp := range dualImports.Standard {
		if imp.Path == "net/http" {
			hasHTTP = true
		}
	}
	for _, imp := range dualImports.ThirdParty {
		if imp.Path == "google.golang.org/protobuf/encoding/protojson" {
			hasProtojson = true
		}
	}
	if !hasHTTP {
		t.Errorf("dual transport service imports should contain net/http")
	}
	if !hasProtojson {
		t.Errorf("dual transport service imports should contain protojson")
	}
}

func TestSelectDocExampleReturnsEmpty(t *testing.T) {
	svc := &api.Service{
		ID:   "test.v1.TestService",
		Name: "TestService",
		Methods: []*api.Method{
			{
				Name:            "DeleteFoo",
				SourceServiceID: "test.v1.TestService",
				InputTypeID:     "test.v1.DeleteFooRequest",
				OutputTypeID:    ".google.protobuf.Empty",
			},
		},
	}
	ex := selectDocExample([]*api.Service{svc}, nil, "test", true, true)
	if !ex.ReturnsEmpty {
		t.Errorf("expected ReturnsEmpty to be true for method returning .google.protobuf.Empty")
	}
}


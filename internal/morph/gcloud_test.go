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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/librarian/internal/sidekick/api"
)

func TestGenerateGcloud(t *testing.T) {
	// Setup temporary directory
	tmpDir := t.TempDir()
	mappingFile := filepath.Join(tmpDir, "mapping.json")
	mappingContent := `{
	  "command": "gcloud secrets create",
	  "message_id": ".google.cloud.secretmanager.v1.CreateSecretRequest",
	  "properties": [
	    {
	      "pos": 0,
	      "field_path": "secretId"
	    },
	    {
	      "flag": "--labels",
	      "field_path": "secret.labels"
	    }
	  ]
	}`
	if err := os.WriteFile(mappingFile, []byte(mappingContent), 0644); err != nil {
		t.Fatal(err)
	}

	requestContent := `{
	  "parent": "projects/my-project/locations/us-east1",
	  "secretId": "my-secret",
	  "secret": {
		"labels": {
		  "env": "prod",
		  "team": "librarian"
		}
	  }
	}`

	// Mock Method with path bindings to test decomposition
	// Template: projects/{project}/locations/{location}
	method := &api.Method{
		PathInfo: &api.PathInfo{
			Bindings: []*api.PathBinding{
				{
					PathTemplate: &api.PathTemplate{
						Segments: []api.PathSegment{
							{Literal: strPtr("projects")},
							{Variable: &api.PathVariable{
								FieldPath: []string{"parent"},
								Segments:  []string{"projects", "*", "locations", "*"},
							}},
						},
					},
				},
			},
		},
	}

	in := &GcloudInput{
		ReqData:     []byte(requestContent),
		OutDir:      tmpDir,
		MappingFile: mappingFile,
		Method:      method,
	}

	if err := GenerateGcloud(context.Background(), in); err != nil {
		t.Fatalf("GenerateGcloud failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "gcloud.sh"))
	if err != nil {
		t.Fatal(err)
	}

	got := string(content)
	// We expect keys to be sorted (labels, then location, then project)
	// location and project should be extracted from parent
	want := `#!/bin/bash
# Auto-generated gcloud command

gcloud secrets create \
  my-secret \
  --labels '{"env":"prod","team":"librarian"}' \
  --location 'us-east1' \
  --project 'my-project' 
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GenerateGcloud output mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerateGcloud_SkipEmpty(t *testing.T) {
	// Setup temporary directory
	tmpDir := t.TempDir()
	mappingFile := filepath.Join(tmpDir, "mapping.json")
	mappingContent := `{
	  "command": "gcloud test",
	  "message_id": "TestMsg",
	  "properties": [
	    {
	      "flag": "--present",
	      "field_path": "present"
	    },
	    {
	      "flag": "--empty",
	      "field_path": "empty"
	    },
		{
		  "flag": "--null",
		  "field_path": "null_val"
		}
	  ]
	}`
	if err := os.WriteFile(mappingFile, []byte(mappingContent), 0644); err != nil {
		t.Fatal(err)
	}

	requestContent := `{
	  "present": "here",
	  "empty": "",
	  "null_val": null
	}`

	in := &GcloudInput{
		ReqData:     []byte(requestContent),
		OutDir:      tmpDir,
		MappingFile: mappingFile,
	}

	if err := GenerateGcloud(context.Background(), in); err != nil {
		t.Fatalf("GenerateGcloud failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "gcloud.sh"))
	if err != nil {
		t.Fatal(err)
	}

	got := string(content)
	want := `#!/bin/bash
# Auto-generated gcloud command

gcloud test \
  --present 'here' 
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GenerateGcloud output mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerateGcloud_BugRepro(t *testing.T) {
	mappingContent := `{
  "command": "gcloud secrets create",
  "message_id": ".google.cloud.secretmanager.v1.CreateSecretRequest",
  "properties": [
    {
      "pos": 0,
      "field_path": "secretId"
    },
    {
      "flag": "--labels",
      "field_path": "secret.labels"
    },
    {
      "flag": "--location",
      "field_path": "parent"
    },
    {
      "flag": "--regional-kms-key-name",
      "field_path": "secret.customerManagedEncryption.kmsKeyName"
    },
    {
      "flag": "--set-annotations",
      "field_path": "secret.annotations"
    },
    {
      "flag": "--tags",
      "field_path": "secret.tags"
    },
    {
      "flag": "--topics",
      "field_path": "secret.topics"
    },
    {
      "flag": "--version-destroy-ttl",
      "field_path": "secret.versionDestroyTtl"
    },
    {
      "flag": "--expire-time",
      "field_path": "secret.expireTime"
    },
    {
      "flag": "--ttl",
      "field_path": "secret.ttl"
    },
    {
      "flag": "--next-rotation-time",
      "field_path": "secret.rotation.nextRotationTime"
    },
    {
      "flag": "--rotation-period",
      "field_path": "secret.rotation.rotationPeriod"
    },
    {
      "flag": "--kms-key-name",
      "field_path": "secret.replication.automatic.customerManagedEncryption.kmsKeyName"
    },
    {
      "flag": "--locations",
      "field_path": "secret.replication.userManaged.replicas"
    },
    {
      "flag": "--replication-policy",
      "field_path": "secret.replication",
      "choices": [
        "automatic",
        "user-managed"
      ]
    }
  ]
}`
	requestContent := `{
  "parent": "projects/workspace",
  "secret": {
    "replication": {
      "userManaged": {
        "replicas": [
          {
            "location": "us-central1"
          }
        ]
      }
    }
  },
  "secretId": "my-secret-id"
}`

	// Mock Method with path bindings to test decomposition
	// Template: projects/{project}/locations/{location}
	method := &api.Method{
		PathInfo: &api.PathInfo{
			Bindings: []*api.PathBinding{
				{
					PathTemplate: &api.PathTemplate{
						Segments: []api.PathSegment{
							{Literal: strPtr("projects")},
							{Variable: &api.PathVariable{
								FieldPath: []string{"parent"},
								Segments:  []string{"projects", "*"},
							}},
						},
					},
				},
			},
		},
	}

	tmpDir := t.TempDir()
	mappingFile := filepath.Join(tmpDir, "gcloud-map.json")
	if err := os.WriteFile(mappingFile, []byte(mappingContent), 0644); err != nil {
		t.Fatal(err)
	}

	in := &GcloudInput{
		ReqData:     []byte(requestContent),
		OutDir:      tmpDir,
		MappingFile: mappingFile,
		Method:      method,
	}

	if err := GenerateGcloud(context.Background(), in); err != nil {
		t.Fatalf("GenerateGcloud failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "gcloud.sh"))
	if err != nil {
		t.Fatal(err)
	}

	got := string(content)
	want := `#!/bin/bash
# Auto-generated gcloud command

gcloud secrets create \
  my-secret-id \
  --locations 'us-central1' \
  --project 'workspace' \
  --replication-policy 'user-managed' 
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GenerateGcloud output mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerateGcloud_MultiBinding(t *testing.T) {
	tmpDir := t.TempDir()
	mappingFile := filepath.Join(tmpDir, "mapping.json")
	// Using the user's provided mapping structure
	mappingContent := `{
	  "command": "gcloud secrets create",
	  "message_id": ".google.cloud.secretmanager.v1.CreateSecretRequest",
	  "properties": [
	    {
	      "pos": 0,
	      "field_path": "secretId"
	    },
	    {
	      "flag": "--replication-policy",
	      "field_path": "secret.replication",
	      "choices": ["automatic", "user-managed"]
	    }
	  ]
	}`
	if err := os.WriteFile(mappingFile, []byte(mappingContent), 0644); err != nil {
		t.Fatal(err)
	}

	requestContent := `{
	  "parent": "projects/workspace/locations/us-central1",
	  "secretId": "my-secret-id",
	  "secret": {
	    "replication": {
	      "automatic": {}
	    }
	  }
	}`

	// Method with TWO bindings.
	// 1. projects/{project}  <-- The code currently picks this one (index 0)
	// 2. projects/{project}/locations/{location} <-- The one we want
	method := &api.Method{
		PathInfo: &api.PathInfo{
			Bindings: []*api.PathBinding{
				{
					PathTemplate: &api.PathTemplate{
						Segments: []api.PathSegment{
							{Literal: strPtr("projects")},
							{Variable: &api.PathVariable{
								FieldPath: []string{"parent"},
								Segments:  []string{"projects", "*"},
							}},
						},
					},
				},
				{
					PathTemplate: &api.PathTemplate{
						Segments: []api.PathSegment{
							{Literal: strPtr("projects")},
							{Variable: &api.PathVariable{
								FieldPath: []string{"parent"},
								Segments:  []string{"projects", "*", "locations", "*"},
							}},
						},
					},
				},
			},
		},
	}

	in := &GcloudInput{
		ReqData:     []byte(requestContent),
		OutDir:      tmpDir,
		MappingFile: mappingFile,
		Method:      method,
	}

	if err := GenerateGcloud(context.Background(), in); err != nil {
		t.Fatalf("GenerateGcloud failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "gcloud.sh"))
	if err != nil {
		t.Fatal(err)
	}

	got := string(content)
	// We expect BOTH project and location to be extracted.
	// If only project is extracted, the test should fail (or we assert it fails).
	if !strings.Contains(got, "--location 'us-central1'") {
		t.Errorf("GenerateGcloud output missing --location flag. Got:\n%s", got)
	}
	if !strings.Contains(got, "--project 'workspace'") {
		t.Errorf("GenerateGcloud output missing --project flag. Got:\n%s", got)
	}
}

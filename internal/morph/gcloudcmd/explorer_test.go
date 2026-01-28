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

package gcloudcmd

import (
	"context"
	"testing"
)

type MockRunner struct {
	CapturedArgs []string
	Output       []byte
	Err          error
}

func (m *MockRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	m.CapturedArgs = args
	return m.Output, m.Err
}

func TestGetHelp(t *testing.T) {
	mock := &MockRunner{
		Output: []byte("usage: gcloud secrets create ..."),
	}
	e := NewExplorer(mock)

	out, err := e.GetHelp(context.Background(), []string{"secrets", "create"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out != "usage: gcloud secrets create ..." {
		t.Errorf("got %q, want %q", out, "usage: gcloud secrets create ...")
	}

	if len(mock.CapturedArgs) != 3 {
		t.Fatalf("got %d args, want 3", len(mock.CapturedArgs))
	}
	if mock.CapturedArgs[2] != "--help" {
		t.Errorf("expected last arg to be --help, got %s", mock.CapturedArgs[2])
	}
}

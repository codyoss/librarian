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
	"strings"
	"testing"

	"github.com/googleapis/librarian/internal/config"
	"github.com/googleapis/librarian/internal/sidekick/parser"
	"github.com/googleapis/librarian/internal/sources"
)

// testdataDir is the shared sidekick testdata tree.
//
// WARNING: these protos are a trimmed 2024 snapshot and have drifted badly from
// the googleapis commit google-cloud-go is generated from. secretmanager's
// service.proto alone differs by ~600 lines and is missing whole RPCs
// (ListSecrets, CreateSecret, AddSecretVersion). They are fine for "did
// anything render" smoke tests and MUST NOT be used as the input to any
// byte-for-byte golden comparison. For that, generate from the pinned commit
// in the librarian tool cache; see harness/verify-pin.sh.
var testdataDir, _ = filepath.Abs("../../testdata")

// TestGenerate is the end-to-end smoke test: build a real model from the
// checked-in googleapis testdata and render the templates into a temp dir.
// It asserts the pipeline works, not that the output is correct.
func TestGenerate(t *testing.T) {
	if _, err := os.Stat(path.Join(testdataDir, "googleapis")); err != nil {
		t.Skipf("missing googleapis testdata: %v", err)
	}
	outDir := t.TempDir()
	cfg := &parser.ModelConfig{
		SpecificationFormat: config.SpecProtobuf,
		ServiceConfig:       "google/cloud/secretmanager/v1/secretmanager_v1.yaml",
		SpecificationSource: "google/cloud/secretmanager/v1",
		Source: &sources.SourceConfig{
			Sources:     &sources.Sources{Googleapis: path.Join(testdataDir, "googleapis")},
			ActiveRoots: []string{"googleapis"},
		},
		Codec: map[string]string{"copyright-year": "2026"},
	}
	model, err := parser.CreateModel(cfg)
	if err != nil {
		t.Skipf("cannot build model (protoc may be unavailable): %v", err)
	}
	if err := Generate(t.Context(), model, outDir, cfg); err != nil {
		t.Fatal(err)
	}
	name := path.Join(outDir, "README.md")
	stat, err := os.Stat(name)
	if err != nil {
		t.Fatalf("expected %s to be generated: %v", name, err)
	}
	// Generated sources must never be executable; a stray mode bit shows up as
	// a diff in the golden tree that is invisible when reading the content.
	if stat.Mode().Perm()&0o111 != 0 {
		t.Errorf("generated file is executable, mode %o: %s", stat.Mode(), name)
	}
}

// TestDeterministicRender renders twice and requires identical bytes. Map
// iteration order is randomized per run, so a map that reaches the output
// fails here rather than intermittently in the differential oracle, where it
// would look like a mysterious flake.
func TestDeterministicRender(t *testing.T) {
	if _, err := os.Stat(path.Join(testdataDir, "googleapis")); err != nil {
		t.Skipf("missing googleapis testdata: %v", err)
	}
	cfg := &parser.ModelConfig{
		SpecificationFormat: config.SpecProtobuf,
		ServiceConfig:       "google/cloud/secretmanager/v1/secretmanager_v1.yaml",
		SpecificationSource: "google/cloud/secretmanager/v1",
		Source: &sources.SourceConfig{
			Sources:     &sources.Sources{Googleapis: path.Join(testdataDir, "googleapis")},
			ActiveRoots: []string{"googleapis"},
		},
		Codec: map[string]string{"copyright-year": "2026"},
	}
	model, err := parser.CreateModel(cfg)
	if err != nil {
		t.Skipf("cannot build model (protoc may be unavailable): %v", err)
	}
	render := func() map[string]string {
		dir := t.TempDir()
		if err := Generate(t.Context(), model, dir, cfg); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first, second := render(), render()
	if len(first) != len(second) {
		t.Fatalf("file count differs between runs: %d vs %d", len(first), len(second))
	}
	for name, a := range first {
		if b, ok := second[name]; !ok {
			t.Errorf("%s present in first render, absent in second", name)
		} else if a != b {
			t.Errorf("%s differs between renders; check for map iteration reaching output", name)
		}
	}
}

// TestNoWallClockReads guards the copyright-year invariant statically.
//
// The stock gapic-generator-go stamps headers with time.Now().Year()
// (gengapic/generator.go:196), which makes its output stop reproducing the
// moment the year rolls over. This codec must take the year from config
// instead, so reading the clock anywhere in the package is a bug.
func TestNoWallClockReads(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// Skip the package doc block, which names time.Now to explain the ban.
		src := string(b)
		if i := strings.Index(src, "\npackage "); i >= 0 {
			src = src[i:]
		}
		if strings.Contains(src, "time.Now") {
			t.Errorf("%s reads the wall clock; take the copyright year from config instead", name)
		}
	}
}

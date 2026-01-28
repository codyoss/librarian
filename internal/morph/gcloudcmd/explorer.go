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
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// Runner interface allows mocking the gcloud command execution
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// GcloudRunner implements Runner using real exec.Command
type GcloudRunner struct{}

func (r *GcloudRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmdStr := fmt.Sprintf("gcloud %s", strings.Join(args, " "))
	slog.Info("Executing command", "command", cmdStr)
	cmd := exec.CommandContext(ctx, "gcloud", args...)
	return cmd.Output()
}

// Explorer provides methods to explore gcloud help
type Explorer struct {
	runner Runner
}

// New creates a new Explorer
func NewExplorer(runner Runner) *Explorer {
	return &Explorer{runner: runner}
}

// GetHelp returns the help output for a given command path
// cmdPath should be like []string{"secrets", "create"} (without "gcloud" prefix)
func (e *Explorer) GetHelp(ctx context.Context, cmdPath []string) (string, error) {
	args := append([]string{}, cmdPath...)
	args = append(args, "--help")
	out, err := e.runner.Run(ctx, args...)
	if err != nil {
		// Sometimes --help returns exit status 0, sometimes it might fail if command doesn't exist.
		// If it's an ExitError, we might still want the stderr/stdout if it contains help?
		// Usually `gcloud ... --help` writes to stdout.
		// If it failed, return error.
		return "", err
	}
	return string(out), nil
}

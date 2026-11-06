// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build acceptance

// Package examples runs the example programs against the live Anthropic API. Each test
// deploys a program, requires an immediate preview and refresh to report no changes,
// which proves that the provider's view of each resource matches the real API's, then
// applies an edit and destroys everything.
package examples

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

// requireCredentials fails a test unless the environment holds every named credential.
// The acceptance job runs only where the repository's secrets are available, so a
// missing credential is a broken job rather than an expected condition. A skip would
// report success for a test that checked nothing.
func requireCredentials(t *testing.T, names ...string) {
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is not set, so this test cannot reach the Anthropic API", name)
		}
	}
}

// credentials skips a test unless the environment holds every named credential. Use it
// for a surface the acceptance organization is not required to grant.
func credentials(t *testing.T, names ...string) {
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Skipf("%s is not set", name)
		}
	}
}

func options(t *testing.T, dir string) integration.ProgramTestOptions {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return integration.ProgramTestOptions{
		Dir:                  filepath.Join(cwd, dir),
		ExpectRefreshChanges: false,
		UseNpm:               true,
		LocalProviders: []integration.LocalDependency{
			{Package: "anthropic", Path: filepath.Join(cwd, "..", "bin")},
		},
		PrePrepareProject: func(project *engine.Projinfo) error {
			return useLocalSDK(project.Root, filepath.Join(cwd, "..", "sdk"))
		},
	}
}

// useLocalSDK points a copied example's dependency on the provider's SDK at the one
// built from this repository, so that the tests exercise it rather than a published
// release, which may not exist yet.
func useLocalSDK(project, sdk string) error {
	if raw, err := os.ReadFile(filepath.Join(project, "package.json")); err == nil {
		var pkg map[string]any
		if err := json.Unmarshal(raw, &pkg); err != nil {
			return err
		}
		pkg["dependencies"].(map[string]any)["@pulumi-labs/anthropic"] = "file:" + filepath.Join(sdk, "nodejs", "bin")
		raw, err := json.MarshalIndent(pkg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(project, "package.json"), raw, 0o600)
	}
	if raw, err := os.ReadFile(filepath.Join(project, "requirements.txt")); err == nil {
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "pulumi-labs-anthropic") {
				lines[i] = filepath.Join(sdk, "python")
			}
		}
		return os.WriteFile(filepath.Join(project, "requirements.txt"), []byte(strings.Join(lines, "\n")), 0o600)
	}
	return nil
}

func TestAgentDelivery(t *testing.T) {
	requireCredentials(t, "ANTHROPIC_API_KEY")
	opts := options(t, "agent-delivery")
	// Stable is pinned to the agent's first version; the canary follows the latest.
	opts.Config = map[string]string{"paused": "true", "stableVersion": "1"}
	opts.Secrets = map[string]string{"githubToken": "acceptance-test-token"}
	opts.EditDirs = []integration.EditDir{{
		// Changing the prompt must create the agent's second version.
		Dir:      filepath.Join(opts.Dir, "..", "testdata", "agent-delivery-edit"),
		Additive: true,
		ExtraRuntimeValidation: func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
			if v := stack.Outputs["agentVersion"]; v != 2.0 {
				t.Errorf("agentVersion = %v, want 2", v)
			}
		},
	}}
	integration.ProgramTest(t, &opts)
}

func TestOrgBootstrap(t *testing.T) {
	credentials(t, "ANTHROPIC_ADMIN_KEY", "ANTHROPIC_AUTH_TOKEN")
	opts := options(t, "org-bootstrap")
	opts.Config = map[string]string{"team": "pulumi-acceptance"}
	integration.ProgramTest(t, &opts)
}

func TestWorkloadIdentity(t *testing.T) {
	credentials(t, "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_ACCEPTANCE_WORKSPACE_ID")
	opts := options(t, "workload-identity")
	opts.Config = map[string]string{"workspaceId": os.Getenv("ANTHROPIC_ACCEPTANCE_WORKSPACE_ID")}
	integration.ProgramTest(t, &opts)
}

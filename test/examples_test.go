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

// Package test runs explicitly opted-in live sample programs and provider engine scenarios.
package test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/test/internal/enginetest"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

// requireCredentials fails a test unless the environment holds every named credential.
// The acceptance job runs only where the repository's secrets are available, so a
// missing credential is a broken job rather than an expected condition. A skip would
// report success for a test that checked nothing.
func requireCredentials(t *testing.T, names ...string) {
	requireLiveMode(t)
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is not set, so this test cannot reach the Anthropic API", name)
		}
	}
}

// Explicit example opt-in must fail on missing credentials, not silently skip.
func exampleOAuthToken(t *testing.T) string {
	t.Helper()
	requireLiveMode(t)
	token := os.Getenv("ANTHROPIC_ADMIN_OAUTH_KEY")
	if token == "" {
		token = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if token == "" {
		t.Fatal("ANTHROPIC_ADMIN_OAUTH_KEY or ANTHROPIC_AUTH_TOKEN is required")
	}
	// The samples use the provider's public OAuth environment variable.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", token)
	return token
}

func exampleName() string { return "pulumi-example-" + strings.ToLower(rand.Text()[:12]) }

// Live examples require separate explicit opt-in.
func requireLiveMode(t *testing.T) {
	t.Helper()
	enginetest.RequireLiveExample(t)
}

func options(t *testing.T, dir string) integration.ProgramTestOptions {
	requireLiveMode(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.ToSlash(t.TempDir())
	if !strings.HasPrefix(backend, "/") {
		backend = "/" + backend
	}
	backendURL := "file://" + backend
	return integration.ProgramTestOptions{
		Verbose:              true,
		NoParallel:           true,
		CloudURL:             backendURL,
		Env:                  []string{"PULUMI_BACKEND_URL=" + backendURL, "PULUMI_ACCESS_TOKEN=", "PULUMI_SKIP_UPDATE_CHECK=true"},
		Dir:                  filepath.Join(cwd, "..", "examples", dir),
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
// built from this repository, so the tests exercise the current source.
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
	requireLiveMode(t)
	opts := options(t, "agent-delivery")
	// Stable is pinned to the agent's first version; the canary follows the latest.
	opts.Config = map[string]string{"enableSchedule": "false", "stableVersion": "1"}
	opts.Secrets = map[string]string{"githubToken": "acceptance-test-token"}
	enginetest.ConfigureWorkspaceExample(t, &opts)
	opts.EditDirs = []integration.EditDir{{
		// Changing the prompt must create the agent's second version.
		Dir:      filepath.Join("testdata", "agent-delivery-edit"),
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
	requireCredentials(t, "ANTHROPIC_ADMIN_KEY")
	exampleOAuthToken(t)
	opts := options(t, "org-bootstrap")
	opts.Config = map[string]string{"team": exampleName()}
	integration.ProgramTest(t, &opts)
}

func TestWorkloadIdentity(t *testing.T) {
	token := exampleOAuthToken(t)
	name := exampleName()
	t.Logf("Live example run name: %s", name)
	client := sdk.NewClient(option.WithoutEnvironmentDefaults(), option.WithMaxRetries(0),
		option.WithAuthToken(token), option.WithHeader("anthropic-beta", "oauth-2025-04-20"))
	workspace, err := client.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: name})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := client.Organization.Workspaces.Archive(context.Background(), workspace.ID)
		require.NoError(t, err)
		observed, err := client.Organization.Workspaces.Get(context.Background(), workspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	opts := options(t, "workload-identity")
	opts.Config = map[string]string{
		"workspaceId": workspace.ID,
		"namePrefix":  name,
		// Exercise configuration without authorizing a real GitHub repository.
		"repository": "pulumi-test/" + name,
	}
	integration.ProgramTest(t, &opts)
}

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

// Package provider assembles the Anthropic provider. Each module of the Pulumi package is
// a Go package beneath this one, so a resource's Go package is its Pulumi module.
package provider

import (
	"context"
	"slices"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/pulumi-labs/pulumi-anthropic/provider/agents"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/functions"
	"github.com/pulumi-labs/pulumi-anthropic/provider/identity"
	"github.com/pulumi-labs/pulumi-anthropic/provider/organization"
	"github.com/pulumi-labs/pulumi-anthropic/provider/security"
	"github.com/pulumi-labs/pulumi-anthropic/provider/skills"
)

// Name is the Pulumi package and plugin name.
const Name = "anthropic"

// Provider builds the Anthropic provider.
func Provider() (p.Provider, error) {
	provider, err := infer.NewProviderBuilder().
		WithDisplayName("Anthropic").
		WithDescription("Manage the Anthropic platform as code: Claude Managed Agents, skills, "+
			"workspaces, members, service accounts, workload identity federation, and more.").
		WithKeywords("anthropic", "claude", "agents", "ai", "category/cloud", "kind/native").
		WithHomepage("https://www.pulumi.com").
		WithRepository("https://github.com/pulumi-labs/pulumi-anthropic").
		WithPublisher("Pulumi Labs").
		WithLicense("Apache-2.0").
		WithPluginDownloadURL("github://api.github.com/pulumi-labs/pulumi-anthropic").
		WithGoImportPath("github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic").
		// Each language's package name is set explicitly. Left unset, the Java
		// generator derives a group of "com.pulumi." from the publisher, with a
		// trailing dot. This map replaces the one the builder would otherwise
		// synthesize, so it also carries the defaults that map held.
		WithLanguageMap(map[string]any{
			"nodejs": map[string]any{
				"packageName":          "@pulumi-labs/anthropic",
				"respectSchemaVersion": true,
			},
			"python": map[string]any{
				"packageName":          "pulumi_labs_anthropic",
				"pyproject":            map[string]any{"enabled": true},
				"respectSchemaVersion": true,
			},
			"java": map[string]any{
				"basePackage":          "com.pulumi.labs",
				"respectSchemaVersion": true,
			},
			"csharp": map[string]any{
				"rootNamespace":        "Pulumi.Labs",
				"respectSchemaVersion": true,
			},
			"go": map[string]any{
				"importBasePath":                 "github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic",
				"generateResourceContainerTypes": true,
				"respectSchemaVersion":           true,
			},
		}).
		WithConfig(infer.Config(&config.Config{})).
		WithResources(slices.Concat(
			agents.Resources(),
			organization.Resources(),
			skills.Resources(),
			identity.Resources(),
			security.Resources(),
		)...).
		WithFunctions(functions.Functions()...).
		// Functions are package-level lookups, so they surface in the index module.
		WithModuleMap(map[tokens.ModuleName]tokens.ModuleName{"functions": "index"}).
		Build()
	if err != nil {
		return p.Provider{}, err
	}
	// Graph dependencies are not platform configuration. Normalize comparison
	// copies only; Check and the engine's saved input annotations remain intact.
	diff := provider.Diff
	provider.Diff = func(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
		req.Inputs = diffWithoutDependencies(property.New(req.Inputs)).AsMap()
		req.OldInputs = diffWithoutDependencies(property.New(req.OldInputs)).AsMap()
		req.State = diffWithoutDependencies(property.New(req.State)).AsMap()
		return diff(ctx, req)
	}
	return config.PreserveConfigMetadata(provider), nil
}

func diffWithoutDependencies(value property.Value) property.Value {
	value = value.WithDependencies(nil)
	if value.IsMap() {
		m := value.AsMap()
		for key, child := range m.All {
			m = m.Set(key, diffWithoutDependencies(child))
		}
		return property.WithGoValue(value, m)
	}
	if value.IsArray() {
		items := make([]property.Value, value.AsArray().Len())
		for i, child := range value.AsArray().All {
			items[i] = diffWithoutDependencies(child)
		}
		return property.WithGoValue(value, items)
	}
	return value
}

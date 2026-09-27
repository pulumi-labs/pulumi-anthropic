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
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/config"
)

// Name is the Pulumi package and plugin name.
const Name = "anthropic"

// Version is the provider version, overridden at release time by the linker.
var Version = "0.1.0-dev"

// Provider builds the Anthropic provider.
func Provider() (p.Provider, error) {
	return infer.NewProviderBuilder().
		WithNamespace("pulumi").
		WithDisplayName("Anthropic").
		WithDescription("Manage the Anthropic platform as code: Claude Managed Agents, skills, "+
			"workspaces, members, service accounts, workload identity federation, and more.").
		WithKeywords("anthropic", "claude", "agents", "ai", "category/cloud", "kind/native").
		WithHomepage("https://www.pulumi.com").
		WithRepository("https://github.com/pulumi/pulumi-anthropic").
		WithPublisher("Pulumi").
		WithLicense("Apache-2.0").
		WithPluginDownloadURL("github://api.github.com/pulumi/pulumi-anthropic").
		WithGoImportPath("github.com/pulumi/pulumi-anthropic/sdk/go/anthropic").
		WithConfig(infer.Config(&config.Config{})).
		Build()
}

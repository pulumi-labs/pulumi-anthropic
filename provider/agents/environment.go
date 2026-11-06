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

package agents

import (
	"context"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// Environment is where Managed Agents sessions run.
type Environment struct {
	rest.Resource[EnvironmentArgs, EnvironmentState, *EnvironmentState]
}

// NewEnvironment returns the Environment resource controller.
func NewEnvironment() *Environment {
	return &Environment{rest.Resource[EnvironmentArgs, EnvironmentState, *EnvironmentState]{
		Collection: environmentCollection,
		EndOfLife:  rest.Delete,
		Tombstones: map[string]any{"metadata": nil},
	}}
}

func (e *Environment) Annotate(a infer.Annotator) {
	a.Describe(e, "A Claude Managed Agents environment: where sessions run, either an "+
		"Anthropic-managed cloud sandbox or a sandbox you host yourself.\n\n"+
		"Environments are not versioned; updates apply in place, and changing `config.type` "+
		"replaces the environment. Deleting this resource deletes the environment. The API "+
		"refuses to delete an environment that sessions still reference, in which case it is "+
		"archived instead: it becomes read-only, running sessions continue, and new sessions "+
		"cannot use it.")
}

type EnvironmentArgs struct {
	Name        string             `pulumi:"name"`
	Description *string            `pulumi:"description,optional"`
	Scope       *EnvironmentScope  `pulumi:"scope,optional"`
	Config      *EnvironmentConfig `pulumi:"config,optional"`
	Metadata    map[string]string  `pulumi:"metadata,optional"`
}

func (e *EnvironmentArgs) Annotate(a infer.Annotator) {
	a.Describe(&e.Name, "The environment's name, 1 to 256 characters.")
	a.Describe(&e.Description, "A description of the environment, up to 1,024 characters.")
	a.Describe(&e.Scope, "Who can use the environment. API organizations accept only `organization`.")
	a.Describe(&e.Config, "Where and how sessions run. Defaults to a cloud sandbox.")
	a.Describe(&e.Metadata, "Arbitrary key-value pairs.")
}

type EnvironmentState struct {
	EnvironmentArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *EnvironmentState) Inputs() *EnvironmentArgs { return &s.EnvironmentArgs }

func (s *EnvironmentState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the environment was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the environment was last updated, in RFC 3339 format.")
}

// EnvironmentScope is who can use an environment.
type EnvironmentScope string

func (EnvironmentScope) Values() []infer.EnumValue[EnvironmentScope] {
	return []infer.EnumValue[EnvironmentScope]{
		{Value: "organization", Description: "Everyone in the organization."},
		{Value: "account", Description: "Only the account that created it."},
	}
}

// EnvironmentType discriminates cloud from self-hosted environments.
type EnvironmentType string

const (
	CloudEnvironment      EnvironmentType = "cloud"
	SelfHostedEnvironment EnvironmentType = "self_hosted"
)

func (EnvironmentType) Values() []infer.EnumValue[EnvironmentType] {
	return []infer.EnumValue[EnvironmentType]{
		{Value: CloudEnvironment, Description: "An Anthropic-managed cloud sandbox."},
		{Value: SelfHostedEnvironment, Description: "A sandbox you run on your own infrastructure, " +
			"authenticated by an environment key created in the Console."},
	}
}

// EnvironmentConfig is where and how an environment's sessions run.
type EnvironmentConfig struct {
	Type       EnvironmentType        `pulumi:"type"                provider:"replaceOnChanges"`
	Networking *EnvironmentNetworking `pulumi:"networking,optional"`
	Packages   *Packages              `pulumi:"packages,optional"`
}

func (c *EnvironmentConfig) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "Whether sessions run in an Anthropic-managed cloud sandbox or on your own "+
		"infrastructure. Changing it replaces the environment.")
	a.Describe(&c.Networking, "The sandbox's network access. Applies to `cloud`.")
	a.Describe(&c.Packages, "Packages installed in the sandbox. Applies to `cloud`.")
}

// NetworkingType discriminates unrestricted from limited network access.
type NetworkingType string

const (
	UnrestrictedNetworking NetworkingType = "unrestricted"
	LimitedNetworking      NetworkingType = "limited"
)

func (NetworkingType) Values() []infer.EnumValue[NetworkingType] {
	return []infer.EnumValue[NetworkingType]{
		{Value: UnrestrictedNetworking, Description: "Any host is reachable."},
		{Value: LimitedNetworking, Description: "Only the listed hosts, and optionally MCP servers " +
			"and package managers, are reachable."},
	}
}

// EnvironmentNetworking is a cloud sandbox's network access.
type EnvironmentNetworking struct {
	Type                 NetworkingType `pulumi:"type"`
	AllowedHosts         []string       `pulumi:"allowedHosts,optional"`
	AllowMcpServers      *bool          `pulumi:"allowMcpServers,optional"`
	AllowPackageManagers *bool          `pulumi:"allowPackageManagers,optional"`
}

func (n *EnvironmentNetworking) Annotate(a infer.Annotator) {
	a.Describe(&n.Type, "Whether network access is unrestricted or limited.")
	a.Describe(&n.AllowedHosts, "Hosts the sandbox can reach. Applies to `limited`.")
	a.Describe(&n.AllowMcpServers, "Whether the agent's MCP servers are reachable. Applies to `limited`.")
	a.Describe(&n.AllowPackageManagers, "Whether package registries are reachable, which "+
		"installing `packages` requires. Applies to `limited`.")
}

// Packages lists packages to install in a cloud sandbox, by package manager.
type Packages struct {
	Apt   []string `pulumi:"apt,optional"`
	Cargo []string `pulumi:"cargo,optional"`
	Gem   []string `pulumi:"gem,optional"`
	Go    []string `pulumi:"go,optional"`
	Npm   []string `pulumi:"npm,optional"`
	Pip   []string `pulumi:"pip,optional"`
}

func (p *Packages) Annotate(a infer.Annotator) {
	a.Describe(&p.Apt, "Debian packages.")
	a.Describe(&p.Cargo, "Rust crates.")
	a.Describe(&p.Gem, "Ruby gems.")
	a.Describe(&p.Go, "Go modules.")
	a.Describe(&p.Npm, "npm packages.")
	a.Describe(&p.Pip, "Python packages.")
}

func (p Packages) empty() bool {
	return len(p.Apt)+len(p.Cargo)+len(p.Gem)+len(p.Go)+len(p.Npm)+len(p.Pip) == 0
}

// ExtendWire sends a cloud sandbox's packages even when none are declared. The API
// keeps stored config fields a request omits, so dropped packages must be sent empty to
// be removed.
func (c EnvironmentConfig) ExtendWire(obj map[string]any) {
	if _, ok := obj["packages"]; !ok && c.Type == CloudEnvironment {
		// Encoding an empty Packages cannot fail.
		obj["packages"], _ = rest.Encode(Packages{})
	}
}

// ExtendWire always sends every package manager's list, for the same reason: a list the
// program drops must be sent empty to be cleared.
func (Packages) ExtendWire(obj map[string]any) {
	for _, manager := range []string{"apt", "cargo", "gem", "go", "npm", "pip"} {
		if _, ok := obj[manager]; !ok {
			obj[manager] = []any{}
		}
	}
}

var (
	_ infer.CustomCheck[EnvironmentArgs]                    = (*Environment)(nil)
	_ infer.CustomUpdate[EnvironmentArgs, EnvironmentState] = (*Environment)(nil)
	_ infer.CustomRead[EnvironmentArgs, EnvironmentState]   = (*Environment)(nil)
	_ infer.CustomDelete[EnvironmentState]                  = (*Environment)(nil)
)

// Check rejects installing packages behind limited networking that blocks package
// managers, which the API would otherwise reject only at deployment time.
func (e *Environment) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[EnvironmentArgs], error) {
	args, failures, err := infer.DefaultCheck[EnvironmentArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[EnvironmentArgs]{Inputs: args, Failures: failures}, err
	}
	// Values unknown during preview decode as unset and are not judged.
	if c := args.Config; c != nil && c.Networking != nil && c.Networking.Type == LimitedNetworking &&
		c.Packages != nil && !c.Packages.empty() &&
		c.Networking.AllowPackageManagers != nil && !*c.Networking.AllowPackageManagers {
		failures = append(failures, p.CheckFailure{
			Property: "config.networking.allowPackageManagers",
			Reason:   "installing packages behind limited networking requires allowPackageManagers to be true",
		})
	}
	return infer.CheckResponse[EnvironmentArgs]{Inputs: args, Failures: failures}, nil
}

// Delete deletes the environment, or archives it if sessions still reference it.
func (e *Environment) Delete(
	ctx context.Context, req infer.DeleteRequest[EnvironmentState],
) (infer.DeleteResponse, error) {
	// Archiving cannot be undone, so it is the fallback only for the conflict of an
	// environment still in use, not for any failure.
	err := rest.End(ctx, e.Collection, req.ID, rest.Delete)
	if rest.IsConflict(err) {
		p.GetLogger(ctx).Warningf("environment %s could not be deleted (%v); archiving it instead", req.ID, err)
		err = rest.End(ctx, e.Collection, req.ID, rest.Archive)
	}
	return infer.DeleteResponse{}, err
}

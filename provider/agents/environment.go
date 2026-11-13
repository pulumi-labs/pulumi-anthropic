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
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Environment is where Managed Agents sessions run.
type Environment struct{ operation *environmentOperationState }

// NewEnvironment returns the complete Environment resource.
func NewEnvironment() infer.InferredResource {
	return environmentResource{infer.Resource(&Environment{})}
}

func (e *Environment) Annotate(a infer.Annotator) {
	a.Describe(e, "A Claude Managed Agents environment: where sessions run, either an "+
		"Anthropic-managed cloud sandbox or a sandbox you host yourself.\n\n"+
		"Environments are not versioned; updates apply in place, and changing `config.type` "+
		"replaces the environment. Deleting this resource permanently deletes the environment; "+
		"it does not archive it.")
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
	a.Describe(&e.Description, "A description of the environment, up to 1,024 characters. Removing a description you previously configured clears it.")
	a.Describe(&e.Scope, "Who can use the environment. API organizations accept only `organization`. Removing a scope you previously configured replaces the environment.")
	a.Describe(&e.Config, "Where and how sessions run. Omit to accept the platform default. Adding or removing this object or changing its type replaces the environment.")
	a.Describe(&e.Metadata, "Arbitrary key-value pairs with nonempty values. Removing an entry from your configuration deletes that entry. Entries added outside Pulumi are left unchanged.")
}

type EnvironmentState struct {
	EnvironmentArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

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
	a.Describe(&c.Networking, "The sandbox's network access. Applies to `cloud`. Removing this configuration replaces the environment.")
	a.Describe(&c.Packages, "Packages to install in the sandbox. Applies to `cloud`. Any package-manager list you omit is treated as an empty list.")
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
	a.Describe(&n.AllowedHosts, "Hosts the sandbox can reach. Applies to `limited`. An empty list allows no listed hosts. Removing a previously configured list has the same effect.")
	a.Describe(&n.AllowMcpServers, "Whether the agent's MCP servers are reachable. Applies to `limited`. Removing this setting disables access.")
	a.Describe(&n.AllowPackageManagers, "Whether package registries are reachable, which "+
		"installing `packages` requires. Applies to `limited`. Must be enabled when packages are configured. Removing this setting disables access.")
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
	// Decode unknown pointers only for type checking, never as evidence of false.
	config := req.NewInputs.Get("config")
	permissionKnown := false
	if config.IsMap() {
		n := config.AsMap().Get("networking")
		if n.IsMap() {
			permissionKnown = !n.AsMap().Get("allowPackageManagers").IsComputed()
		}
	}
	if c := args.Config; permissionKnown && c != nil && c.Networking != nil && c.Networking.Type == LimitedNetworking &&
		c.Packages != nil && !c.Packages.empty() &&
		(c.Networking.AllowPackageManagers == nil || !*c.Networking.AllowPackageManagers) {
		failures = append(failures, p.CheckFailure{
			Property: "config.networking.allowPackageManagers",
			Reason:   "installing packages behind limited networking requires allowPackageManagers to be true",
		})
	}
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	enum := func(path string, v property.Value, first, second string) {
		if v.IsString() && v.AsString() != first && v.AsString() != second {
			fail(path, "must be "+first+" or "+second)
		}
	}
	enum("scope", req.NewInputs.Get("scope"), "organization", "account")
	if config.IsMap() {
		enum("config.type", config.AsMap().Get("type"), "cloud", "self_hosted")
		if n := config.AsMap().Get("networking"); n.IsMap() {
			enum("config.networking.type", n.AsMap().Get("type"), "limited", "unrestricted")
		}
	}
	if v := req.NewInputs.Get("name"); v.IsString() && (len([]rune(v.AsString())) == 0 || len([]rune(v.AsString())) > 256) {
		fail("name", "must contain 1–256 characters")
	}
	if v := req.NewInputs.Get("description"); v.IsString() && len([]rune(v.AsString())) > 1024 {
		fail("description", "must contain at most 1,024 characters")
	}
	if metadata := req.NewInputs.Get("metadata"); metadata.IsMap() {
		for key, value := range metadata.AsMap().All {
			if value.IsString() && value.AsString() == "" {
				fail("metadata."+key, "values must be nonempty; the API interprets empty strings as deletion")
			}
		}
	}
	if config.IsMap() {
		c := config.AsMap()
		if t := c.Get("type"); t.IsString() && t.AsString() == "self_hosted" {
			for _, key := range []string{"networking", "packages"} {
				if v := c.Get(key); !v.IsNull() && !v.IsComputed() {
					fail("config."+key, "applies only to cloud environments")
				}
			}
		}
		if n := c.Get("networking"); n.IsMap() {
			m := n.AsMap()
			if t := m.Get("type"); t.IsString() && t.AsString() == "unrestricted" {
				for _, key := range []string{"allowedHosts", "allowMcpServers", "allowPackageManagers"} {
					if v := m.Get(key); !v.IsNull() && !v.IsComputed() {
						fail("config.networking."+key, "applies only to limited networking")
					}
				}
			}
		}
	}
	return infer.CheckResponse[EnvironmentArgs]{Inputs: args, Failures: failures}, nil
}

func (controller *Environment) Create(ctx context.Context, req infer.CreateRequest[EnvironmentArgs]) (infer.CreateResponse[EnvironmentState], error) {
	req.Inputs = environmentPresence(controller.operation, req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[EnvironmentState]{Output: EnvironmentState{EnvironmentArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}
	if err := environmentValidateVariants(req.Inputs); err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}
	r, err := c.Beta.Environments.New(ctx, environmentNew(req.Inputs))
	if err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}
	s, err := environmentState(r)
	if err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}
	return infer.CreateResponse[EnvironmentState]{ID: r.ID, Output: s}, nil
}

func (controller *Environment) Read(ctx context.Context, req infer.ReadRequest[EnvironmentArgs, EnvironmentState]) (infer.ReadResponse[EnvironmentArgs, EnvironmentState], error) {
	req.Inputs = environmentPresence(controller.operation, req.Inputs)
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, err
	}
	r, err := c.Beta.Environments.Get(ctx, req.ID, anthropic.BetaEnvironmentGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, err
	}
	s, err := environmentState(r)
	if err != nil {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, err
	}
	if r.ArchivedAt != "" {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, nil
	}
	return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{ID: req.ID, Inputs: environmentReadInputs(s.EnvironmentArgs, req.Inputs), State: s}, nil
}

func (controller *Environment) Update(ctx context.Context, req infer.UpdateRequest[EnvironmentArgs, EnvironmentState]) (infer.UpdateResponse[EnvironmentState], error) {
	req.Inputs = environmentPresence(controller.operation, req.Inputs)
	if req.DryRun {
		return infer.UpdateResponse[EnvironmentState]{Output: EnvironmentState{EnvironmentArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[EnvironmentState]{}, err
	}
	// No pre-update GET, equality check, or changed-field patch.
	if err := environmentValidateVariants(req.Inputs); err != nil {
		return infer.UpdateResponse[EnvironmentState]{}, err
	}
	managed := environmentPreviouslyManaged(controller.operation)
	r, err := c.Beta.Environments.Update(ctx, req.ID, environmentUpdate(managed, req.Inputs))
	if err != nil {
		return infer.UpdateResponse[EnvironmentState]{}, err
	}
	s, err := environmentState(r)
	if err != nil {
		return infer.UpdateResponse[EnvironmentState]{}, err
	}
	return infer.UpdateResponse[EnvironmentState]{Output: s}, nil
}

func (controller *Environment) Delete(ctx context.Context, req infer.DeleteRequest[EnvironmentState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.Environments.Delete(ctx, req.ID, anthropic.BetaEnvironmentDeleteParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	// No documented error code identifies a session-reference-only 409. Returning
	// that error is safer than silently archiving on any conflict.
	return infer.DeleteResponse{}, err
}

// environmentResource preserves dependency-bearing Check inputs. Cloud package
// normalization adds missing empty lists without moving any existing logical value.
type environmentResource struct{ infer.InferredResource }

// environmentOperationState belongs to one lifecycle invocation, never to the registered resource.
type environmentOperationState struct{ inputs, oldInputs property.Map }

func (r environmentResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	req.Inputs = environmentCanonicalInputs(req.Inputs)
	resp, err := infer.Resource(&Environment{operation: &environmentOperationState{}}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

// infer v1.5 does not replace a whole parent containing a nested immutable field.
// Keep its raw default Diff (and unknown handling), strengthening only config
// type and removals without documented reset encodings.
func (r environmentResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	// Older framework contexts do not advertise old-input support even when the
	// request supplies it. Prefer those explicit checked inputs over output defaults.
	req.Inputs = environmentCanonicalInputs(req.Inputs)
	if req.OldInputs.Len() != 0 {
		req.OldInputs = environmentCanonicalInputs(req.OldInputs)
		req.State = req.OldInputs
	}
	resp, err := infer.Resource(&Environment{operation: &environmentOperationState{}}).Diff(ctx, req)
	for path, d := range resp.DetailedDiff {
		if path == "config" || path == "config.type" ||
			(path == "scope" && d.Kind == p.Delete) ||
			(path == "config.networking" && d.Kind == p.Delete) {
			switch d.Kind {
			case p.Add:
				d.Kind = p.AddReplace
			case p.Delete:
				d.Kind = p.DeleteReplace
			case p.Update:
				d.Kind = p.UpdateReplace
			}
			resp.DetailedDiff[path] = d
		}
	}
	return resp, err
}

// The pinned typed decoder collapses allocated-empty slices. Carry only the
// original input bag on the invocation-local controller so empty host/package lists remain
// distinguishable from omission; the SDK still performs all wire serialization.
func (r environmentResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	operation := &environmentOperationState{inputs: req.Properties}
	return infer.Resource(&Environment{operation: operation}).Create(ctx, req)
}

func (r environmentResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	operation := &environmentOperationState{inputs: req.Inputs}
	return infer.Resource(&Environment{operation: operation}).Read(ctx, req)
}

func (r environmentResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	operation := &environmentOperationState{inputs: req.Inputs, oldInputs: req.OldInputs}
	return infer.Resource(&Environment{operation: operation}).Update(ctx, req)
}

func (r environmentResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Environment{operation: &environmentOperationState{}}).Delete(ctx, req)
}

// Cloud packages are managed as six complete lists, including when omitted.
// Add only missing paths, preserving all metadata on existing logical values.
func environmentCanonicalInputs(bag property.Map) property.Map {
	config := bag.Get("config")
	if !config.IsMap() {
		return bag
	}
	c := config.AsMap()
	if t := c.Get("type"); !t.IsString() || t.AsString() != "cloud" {
		return bag
	}
	packages := c.Get("packages")
	if !packages.IsNull() && !packages.IsMap() {
		return bag
	} // unknown stays unknown
	p := property.Map{}
	if packages.IsMap() {
		p = packages.AsMap()
	}
	for _, key := range []string{"apt", "cargo", "gem", "go", "npm", "pip"} {
		if p.Get(key).IsNull() {
			p = p.Set(key, property.New([]property.Value{}))
		}
	}
	c = c.Set("packages", property.WithGoValue(packages, p))
	return bag.Set("config", property.WithGoValue(config, c))
}

// State contains observations, not ownership. Only prior checked declarations
// authorize removal patches. No old/new equality comparison is performed here.
func environmentPreviouslyManaged(operation *environmentOperationState) EnvironmentArgs {
	if operation == nil {
		return EnvironmentArgs{} // without checked inputs, observed state is not deletion permission
	}
	bag := operation.oldInputs
	managed := EnvironmentArgs{}
	if v := bag.Get("description"); !v.IsNull() {
		value := ""
		managed.Description = &value
	}
	if v := bag.Get("metadata"); v.IsMap() {
		managed.Metadata = map[string]string{}
		for key := range v.AsMap().All {
			managed.Metadata[key] = ""
		}
	}
	return managed
}

func (p Packages) empty() bool {
	return len(p.Apt)+len(p.Cargo)+len(p.Gem)+len(p.Go)+len(p.Npm)+len(p.Pip) == 0
}

func environmentPresence(operation *environmentOperationState, a EnvironmentArgs) EnvironmentArgs {
	if operation == nil {
		return a
	}
	bag := operation.inputs
	if v := bag.Get("metadata"); v.IsMap() && a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	c := bag.Get("config")
	if a.Config == nil || !c.IsMap() {
		return a
	}
	copy := *a.Config
	a.Config = &copy
	if n := c.AsMap().Get("networking"); n.IsMap() && copy.Networking != nil {
		v := *copy.Networking
		copy.Networking = &v
		if hosts := n.AsMap().Get("allowedHosts"); hosts.IsArray() && hosts.AsArray().Len() == 0 {
			v.AllowedHosts = []string{}
		}
	}
	if p := c.AsMap().Get("packages"); p.IsMap() && copy.Packages != nil {
		v := *copy.Packages
		copy.Packages = &v
		for key, target := range map[string]*[]string{"apt": &v.Apt, "cargo": &v.Cargo, "gem": &v.Gem, "go": &v.Go, "npm": &v.Npm, "pip": &v.Pip} {
			if list := p.AsMap().Get(key); list.IsArray() && list.AsArray().Len() == 0 {
				*target = []string{}
			}
		}
	}
	return a
}

// Read recovers all inputs on import. On refresh, keep intentionally unmanaged
// defaults out of the engine's input bag, while outputs retain the full observation.
// Every declared value, including empty lists and false, is read from the server.
func environmentReadInputs(observed, prior EnvironmentArgs) EnvironmentArgs {
	if prior.Name == "" {
		return observed
	}
	r := observed
	if prior.Description == nil {
		r.Description = nil
	}
	if prior.Scope == nil {
		r.Scope = nil
	}
	if prior.Metadata == nil {
		r.Metadata = nil
	} else {
		// Refresh does not adopt external keys into deletion ownership. Outputs
		// still contain the entire observed map; ID-only imports adopt all keys.
		r.Metadata = map[string]string{}
		for key := range prior.Metadata {
			if value, ok := observed.Metadata[key]; ok {
				r.Metadata[key] = value
			}
		}
	}
	if prior.Config == nil {
		r.Config = nil
		return r
	}
	if observed.Config == nil {
		return r
	}
	c := *observed.Config
	r.Config = &c
	if prior.Config.Networking == nil {
		c.Networking = nil
	} else if c.Networking != nil {
		n := *c.Networking
		c.Networking = &n
		if prior.Config.Networking.AllowedHosts == nil {
			n.AllowedHosts = nil
		}
		if prior.Config.Networking.AllowMcpServers == nil {
			n.AllowMcpServers = nil
		}
		if prior.Config.Networking.AllowPackageManagers == nil {
			n.AllowPackageManagers = nil
		}
	}
	// Every package manager is managed whenever cloud config is declared,
	// even if its original list was omitted. Recover all six observations so
	// changes from managed [] remain visible to the engine's subsequent Diff.
	return r
}

// Containers are cloned: conversion must not mutate engine-owned state. For cloud
// configurations the six package lists are managed as a whole, including removal.
func environmentPackages(p *Packages) anthropic.BetaPackagesParams {
	if p == nil {
		p = &Packages{}
	}
	list := func(v []string) []string { return append([]string{}, v...) }
	return anthropic.BetaPackagesParams{Apt: list(p.Apt), Cargo: list(p.Cargo), Gem: list(p.Gem), Go: list(p.Go), Npm: list(p.Npm), Pip: list(p.Pip)}
}

func environmentCloud(c *EnvironmentConfig, update bool) *anthropic.BetaCloudConfigParams {
	r := &anthropic.BetaCloudConfigParams{Packages: environmentPackages(c.Packages)}
	if n := c.Networking; n != nil {
		switch n.Type {
		case UnrestrictedNetworking:
			v := anthropic.NewBetaUnrestrictedNetworkParam()
			r.Networking.OfUnrestricted = &v
		case LimitedNetworking:
			v := &anthropic.BetaLimitedNetworkParams{AllowedHosts: slices.Clone(n.AllowedHosts)}
			if n.AllowMcpServers != nil {
				v.AllowMCPServers = anthropic.Bool(*n.AllowMcpServers)
			} else if update {
				v.AllowMCPServers = anthropic.Bool(false)
			}
			if n.AllowPackageManagers != nil {
				v.AllowPackageManagers = anthropic.Bool(*n.AllowPackageManagers)
			} else if update {
				v.AllowPackageManagers = anthropic.Bool(false)
			}
			if update && v.AllowedHosts == nil {
				v.AllowedHosts = []string{}
			}
			r.Networking.OfLimited = v
		}
	}
	return r
}

func environmentNew(a EnvironmentArgs) anthropic.BetaEnvironmentNewParams {
	r := anthropic.BetaEnvironmentNewParams{Name: a.Name, Metadata: maps.Clone(a.Metadata)}
	if a.Description != nil {
		r.Description = anthropic.String(*a.Description)
	}
	if a.Scope != nil {
		r.Scope = anthropic.BetaEnvironmentNewParamsScope(*a.Scope)
	}
	if a.Config != nil {
		switch a.Config.Type {
		case CloudEnvironment:
			r.Config.OfCloud = environmentCloud(a.Config, false)
		case SelfHostedEnvironment:
			v := anthropic.NewBetaSelfHostedConfigParams()
			r.Config.OfSelfHosted = &v
		}
	}
	return r
}

func environmentUpdate(old, a EnvironmentArgs) anthropic.BetaEnvironmentUpdateParams {
	r := anthropic.BetaEnvironmentUpdateParams{Name: anthropic.String(a.Name)}
	if old.Description != nil {
		r.Description = param.Null[string]()
	}
	if a.Description != nil {
		r.Description = anthropic.String(*a.Description)
	}
	if a.Scope != nil {
		r.Scope = anthropic.BetaEnvironmentUpdateParamsScope(*a.Scope)
	}
	// Type is immutable and deliberately excluded for self-hosted configurations.
	// Cloud's discriminator selects the SDK patch variant, never changes type.
	if a.Config != nil && a.Config.Type == CloudEnvironment {
		r.Config.OfCloud = environmentCloud(a.Config, true)
	}
	metadata := apiutil.MergedStringMap(old.Metadata, a.Metadata)
	if a.Metadata != nil || old.Metadata != nil {
		r.SetExtraFields(map[string]any{"metadata": metadata})
	}
	return r
}

func environmentState(r *anthropic.BetaEnvironment) (EnvironmentState, error) {
	s := EnvironmentState{}
	if r == nil {
		return s, fmt.Errorf("missing environment response")
	}
	if apiutil.StringField(r.JSON.Name) {
		s.Name = r.Name
	}
	if apiutil.StringField(r.JSON.Description) {
		s.Description = &r.Description
	}
	if apiutil.StringField(r.JSON.Scope) {
		v := EnvironmentScope(r.Scope)
		s.Scope = &v
	}
	if apiutil.StringMapField(r.JSON.Metadata) {
		s.Metadata = maps.Clone(r.Metadata)
	}
	if r.JSON.CreatedAt.Valid() {
		if _, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
			s.CreatedAt = r.CreatedAt
		}
	}
	if r.JSON.UpdatedAt.Valid() {
		if _, err := time.Parse(time.RFC3339Nano, r.UpdatedAt); err == nil {
			s.UpdatedAt = r.UpdatedAt
		}
	}
	var conversionErr error
	if r.JSON.Config.Valid() {
		c := &EnvironmentConfig{Type: EnvironmentType(r.Config.Type)}
		s.Config = c
		if c.Type != CloudEnvironment && c.Type != SelfHostedEnvironment {
			conversionErr = fmt.Errorf("unknown environment config variant")
			s.Config = nil
		}
		if c.Type == CloudEnvironment {
			if !r.Config.JSON.Networking.Valid() || !r.Config.JSON.Packages.Valid() {
				conversionErr = fmt.Errorf("incomplete cloud environment configuration")
			}
			if r.Config.JSON.Networking.Valid() {
				n := r.Config.Networking
				c.Networking = &EnvironmentNetworking{Type: NetworkingType(n.Type)}
				if n.Type == "limited" {
					if !apiutil.BoolField(n.JSON.AllowMCPServers) || !apiutil.BoolField(n.JSON.AllowPackageManagers) || !apiutil.StringListField(n.JSON.AllowedHosts) {
						conversionErr = fmt.Errorf("incomplete limited environment networking")
					}
					if apiutil.StringListField(n.JSON.AllowedHosts) {
						c.Networking.AllowedHosts = slices.Clone(n.AllowedHosts)
					}
					if apiutil.BoolField(n.JSON.AllowMCPServers) {
						c.Networking.AllowMcpServers = &n.AllowMCPServers
					}
					if apiutil.BoolField(n.JSON.AllowPackageManagers) {
						c.Networking.AllowPackageManagers = &n.AllowPackageManagers
					}
				} else if n.Type != "unrestricted" {
					conversionErr = fmt.Errorf("unknown environment networking variant")
					c.Networking = nil
				}
			}
			if r.Config.JSON.Packages.Valid() {
				p := r.Config.Packages
				if !apiutil.StringListField(p.JSON.Apt) || !apiutil.StringListField(p.JSON.Cargo) || !apiutil.StringListField(p.JSON.Gem) || !apiutil.StringListField(p.JSON.Go) || !apiutil.StringListField(p.JSON.Npm) || !apiutil.StringListField(p.JSON.Pip) {
					conversionErr = fmt.Errorf("incomplete environment packages")
				}
				c.Packages = &Packages{}
				if apiutil.StringListField(p.JSON.Apt) {
					c.Packages.Apt = slices.Clone(p.Apt)
				}
				if apiutil.StringListField(p.JSON.Cargo) {
					c.Packages.Cargo = slices.Clone(p.Cargo)
				}
				if apiutil.StringListField(p.JSON.Gem) {
					c.Packages.Gem = slices.Clone(p.Gem)
				}
				if apiutil.StringListField(p.JSON.Go) {
					c.Packages.Go = slices.Clone(p.Go)
				}
				if apiutil.StringListField(p.JSON.Npm) {
					c.Packages.Npm = slices.Clone(p.Npm)
				}
				if apiutil.StringListField(p.JSON.Pip) {
					c.Packages.Pip = slices.Clone(p.Pip)
				}
			}
		}
	}
	if r.ID == "" || !apiutil.StringField(r.JSON.ID) || !apiutil.StringField(r.JSON.Name) || !r.JSON.Config.Valid() || s.CreatedAt == "" || s.UpdatedAt == "" || !apiutil.StringMapField(r.JSON.Metadata) || r.JSON.Description.Raw() == "" || (r.JSON.Description.Raw() != "null" && !apiutil.StringField(r.JSON.Description)) {
		return s, fmt.Errorf("incomplete environment response")
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "null" {
		if _, err := time.Parse(time.RFC3339Nano, r.ArchivedAt); !r.JSON.ArchivedAt.Valid() || err != nil {
			return s, fmt.Errorf("invalid environment archive timestamp")
		}
	}
	if raw := r.JSON.Scope.Raw(); raw != "" && !apiutil.StringField(r.JSON.Scope) {
		return s, fmt.Errorf("invalid environment scope")
	}
	if apiutil.StringField(r.JSON.Scope) && r.Scope != "organization" && r.Scope != "account" {
		return s, fmt.Errorf("unknown environment scope")
	}
	return s, conversionErr
}

func environmentValidateVariants(a EnvironmentArgs) error {
	if a.Scope != nil && *a.Scope != "organization" && *a.Scope != "account" {
		return fmt.Errorf("invalid environment scope")
	}
	if c := a.Config; c != nil {
		if c.Type != CloudEnvironment && c.Type != SelfHostedEnvironment {
			return fmt.Errorf("invalid environment config type")
		}
		if n := c.Networking; n != nil && n.Type != LimitedNetworking && n.Type != UnrestrictedNetworking {
			return fmt.Errorf("invalid environment networking type")
		}
	}
	return nil
}

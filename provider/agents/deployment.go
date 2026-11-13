package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/respjson"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Deployment configures sessions without starting them on demand.
type Deployment struct{ operation *deploymentOperationState }

// NewDeployment returns the complete Deployment resource.
func NewDeployment() infer.InferredResource {
	return deploymentResource{infer.Resource(&Deployment{})}
}

func (d *Deployment) Annotate(a infer.Annotator) {
	a.Describe(d, "A Claude Managed Agents deployment: an agent, an environment, and the events that start each session, on demand or on a cron schedule. Set agent.version to pin a version; otherwise the latest version is pinned whenever configuration is applied. This resource does not start on-demand sessions or manage pausing. Deleting it archives the deployment permanently and stops its schedule.")
}

type DeploymentArgs struct {
	Name          string               `pulumi:"name"`
	Description   *string              `pulumi:"description,optional"`
	Agent         AgentReference       `pulumi:"agent"`
	EnvironmentID string               `pulumi:"environmentId"`
	InitialEvents []map[string]any     `pulumi:"initialEvents"`
	Resources     []DeploymentResource `pulumi:"resources,optional"`
	VaultIDs      []string             `pulumi:"vaultIds,optional"`
	Schedule      *Schedule            `pulumi:"schedule,optional"`
	Budget        *Budget              `pulumi:"budget,optional"`
	Metadata      map[string]string    `pulumi:"metadata,optional"`
}

func (d *DeploymentArgs) Annotate(a infer.Annotator) {
	a.Describe(&d.Name, "The deployment's name.")
	a.Describe(&d.Description, "A description of the deployment. Removing a configured description clears it.")
	a.Describe(&d.Agent, "The agent to run, and optionally the version to pin.")
	a.Describe(&d.EnvironmentID, "The environment sessions run in.")
	a.Describe(&d.InitialEvents, "The events that start each session, 1 to 50, in the API's event format. For example: {type: \"user.message\", content: [{type: \"text\", text: \"Triage new issues.\"}]}.")
	a.Describe(&d.Resources, "Repositories, files, and memory stores mounted into each session, up to 500. Removing the list clears it. Repository tokens are write-only and cannot be recovered on import.")
	a.Describe(&d.VaultIDs, "Vaults whose credentials sessions can use, up to 50. Removing the list clears it.")
	a.Describe(&d.Schedule, "When to run automatically. Omit initially to run only on demand. Removing a configured schedule returns to on-demand operation. This resource does not manage pausing.")
	a.Describe(&d.Budget, "A list-price spend ceiling copied to each future session, not a cumulative deployment limit. Changes do not affect existing sessions. The agent model must have a public list price. Removing a configured budget leaves future sessions uncapped.")
	a.Describe(&d.Metadata, "Arbitrary key-value pairs, up to 16. Keys are 1–64 characters and values at most 512 characters. Removing an entry deletes that entry; entries added outside Pulumi are left unchanged.")
}

type DeploymentState struct {
	DeploymentArgs
	Status    string `pulumi:"status"`
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *DeploymentState) Annotate(a infer.Annotator) {
	a.Describe(&s.Status, "Whether the deployment is active or paused, as observed on the platform. The platform may pause deployments when dependencies cannot run. This resource does not change pause status.")
	a.Describe(&s.CreatedAt, "When the deployment was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the deployment was last updated, in RFC 3339 format.")
}

type AgentReference struct {
	ID      string `pulumi:"id"`
	Version *int   `pulumi:"version,optional"`
}

func (r *AgentReference) Annotate(a infer.Annotator) {
	a.Describe(&r.ID, "The agent's ID.")
	a.Describe(&r.Version, "The agent version to run. If omitted, the latest version is pinned whenever deployment configuration is applied; later versions are not picked up automatically.")
}

type DeploymentResourceType string

func (DeploymentResourceType) Values() []infer.EnumValue[DeploymentResourceType] {
	return []infer.EnumValue[DeploymentResourceType]{{Value: "github_repository", Description: "A GitHub repository, cloned into the sandbox."}, {Value: "file", Description: "An uploaded file, mounted into the sandbox."}, {Value: "memory_store", Description: "A memory store, mounted into the sandbox."}}
}

type DeploymentResource struct {
	Type               DeploymentResourceType `pulumi:"type"`
	URL                *string                `pulumi:"url,optional"`
	Checkout           *Checkout              `pulumi:"checkout,optional"`
	AuthorizationToken *string                `pulumi:"authorizationToken,optional" provider:"secret"`
	FileID             *string                `pulumi:"fileId,optional"`
	MemoryStoreID      *string                `pulumi:"memoryStoreId,optional"`
	Access             *MemoryAccess          `pulumi:"access,optional"`
	Instructions       *string                `pulumi:"instructions,optional"`
	MountPath          *string                `pulumi:"mountPath,optional"`
}

func (r *DeploymentResource) Annotate(a infer.Annotator) {
	a.Describe(&r.Type, "The kind of resource.")
	a.Describe(&r.URL, "The repository's URL. Required for github_repository.")
	a.Describe(&r.Checkout, "The branch or commit to check out. For github_repository; defaults to the default branch.")
	a.Describe(&r.AuthorizationToken, "A token that can read the repository. For github_repository. Write-only; import cannot recover it. Supply it when configuring a private repository.")
	a.Describe(&r.FileID, "The file's ID. Required for file.")
	a.Describe(&r.MemoryStoreID, "The memory store's ID. Required for memory_store.")
	a.Describe(&r.Access, "Whether the agent can write to the memory store. For memory_store; defaults to read_write.")
	a.Describe(&r.Instructions, "How the agent should use the memory store. For memory_store; at most 4096 characters.")
	a.Describe(&r.MountPath, "Where the resource is mounted in the sandbox. For github_repository and file.")
}

type Checkout struct {
	Type CheckoutType `pulumi:"type"`
	Name *string      `pulumi:"name,optional"`
	Sha  *string      `pulumi:"sha,optional"`
}

func (c *Checkout) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "Whether to check out a branch or a commit.")
	a.Describe(&c.Name, "The branch. Required for branch.")
	a.Describe(&c.Sha, "The commit SHA. Required for commit.")
}

type CheckoutType string

func (CheckoutType) Values() []infer.EnumValue[CheckoutType] {
	return []infer.EnumValue[CheckoutType]{{Value: "branch"}, {Value: "commit"}}
}

type MemoryAccess string

func (MemoryAccess) Values() []infer.EnumValue[MemoryAccess] {
	return []infer.EnumValue[MemoryAccess]{{Value: "read_write"}, {Value: "read_only"}}
}

type Schedule struct {
	Expression string `pulumi:"expression"`
	Timezone   string `pulumi:"timezone"`
}

func (s *Schedule) Annotate(a infer.Annotator) {
	a.Describe(&s.Expression, "A cron expression, such as 0 9 * * 1-5.")
	a.Describe(&s.Timezone, "The IANA time zone the expression is evaluated in, such as America/New_York.")
}

// Budget caps the public list-price cost of each future session.
type Budget struct {
	MaxListCost MonetaryAmount `pulumi:"maxListCost"`
}

func (b *Budget) Annotate(a infer.Annotator) {
	a.Describe(&b.MaxListCost, "The most each future session may spend at public list prices, rather than negotiated prices.")
}

type MonetaryAmount struct {
	Amount   string `pulumi:"amount"`
	Currency string `pulumi:"currency"`
}

func (m *MonetaryAmount) Annotate(a infer.Annotator) {
	a.Describe(&m.Amount, "The amount in the currency's minor units, as an integer string: 2500 is $25.00.")
	a.Describe(&m.Currency, "The ISO 4217 currency code. Only USD is supported.")
}

func (controller *Deployment) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[DeploymentArgs], error) {
	a, failures, err := infer.DefaultCheck[DeploymentArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[DeploymentArgs]{Inputs: a, Failures: failures}, err
	}
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	// No normalization moves values. Unknowns stay at their original paths.
	nonempty := func(path string, v property.Value) {
		if v.IsString() && v.AsString() == "" {
			fail(path, "must not be empty")
		}
	}
	nonempty("name", req.NewInputs.Get("name"))
	nonempty("environmentId", req.NewInputs.Get("environmentId"))
	if v := req.NewInputs.Get("paused"); !v.IsNull() {
		fail("paused", "cannot be configured; deployment pause status is read-only")
	}
	if v := req.NewInputs.Get("agent"); v.IsMap() {
		nonempty("agent.id", v.AsMap().Get("id"))
		if n := v.AsMap().Get("version"); n.IsNumber() && (n.AsNumber() < 1 || n.AsNumber() != float64(int64(n.AsNumber()))) {
			fail("agent.version", "must be a positive integer")
		}
	}
	for _, c := range []struct {
		k        string
		min, max int
	}{{"initialEvents", 1, 50}, {"resources", 0, 500}, {"vaultIds", 0, 50}} {
		v := req.NewInputs.Get(c.k)
		if v.IsArray() && (v.AsArray().Len() < c.min || v.AsArray().Len() > c.max) {
			fail(c.k, fmt.Sprintf("must contain %d to %d entries", c.min, c.max))
		}
	}
	if v := req.NewInputs.Get("initialEvents"); v.IsArray() {
		for i, e := range v.AsArray().All {
			path := fmt.Sprintf("initialEvents[%d]", i)
			deploymentValidateEvent(e, path, fail)
			if deploymentKnown(e) && i < len(a.InitialEvents) {
				if _, err := deploymentEvents([]map[string]any{a.InitialEvents[i]}); err != nil {
					fail(path, err.Error())
				}
			}
		}
	}
	if v := req.NewInputs.Get("resources"); v.IsArray() {
		for i, x := range v.AsArray().All {
			if !x.IsMap() {
				continue
			}
			m := x.AsMap()
			path := fmt.Sprintf("resources[%d]", i)
			typ := m.Get("type")
			if !typ.IsString() {
				continue
			}
			allowed := map[string]bool{"type": true}
			required := ""
			switch typ.AsString() {
			case "github_repository":
				required = "url"
				for _, k := range []string{"url", "checkout", "authorizationToken", "mountPath"} {
					allowed[k] = true
				}
			case "file":
				required = "fileId"
				allowed["fileId"] = true
				allowed["mountPath"] = true
			case "memory_store":
				required = "memoryStoreId"
				for _, k := range []string{"memoryStoreId", "access", "instructions"} {
					allowed[k] = true
				}
			default:
				fail(path+".type", "must be github_repository, file or memory_store")
				continue
			}
			if v := m.Get(required); v.IsNull() || (v.IsString() && v.AsString() == "") {
				fail(path+"."+required, "is required for "+typ.AsString())
			}
			for k, v := range m.All {
				if !v.IsNull() && !allowed[k] {
					fail(path+"."+k, "does not apply to "+typ.AsString())
				}
			}
			if v := m.Get("access"); v.IsString() && v.AsString() != "read_only" && v.AsString() != "read_write" {
				fail(path+".access", "must be read_only or read_write")
			}
			if v := m.Get("instructions"); v.IsString() && utf8.RuneCountInString(v.AsString()) > 4096 {
				fail(path+".instructions", "must be at most 4096 characters")
			}
			if c := m.Get("checkout"); c.IsMap() {
				cm := c.AsMap()
				if t := cm.Get("type"); t.IsString() {
					required, other := "name", "sha"
					if t.AsString() == "commit" {
						required, other = other, required
					} else if t.AsString() != "branch" {
						fail(path+".checkout.type", "must be branch or commit")
						continue
					}
					if v := cm.Get(required); v.IsNull() || (v.IsString() && v.AsString() == "") {
						fail(path+".checkout."+required, "is required")
					}
					if !cm.Get(other).IsNull() {
						fail(path+".checkout."+other, "does not apply to "+t.AsString())
					}
				}
			}
		}
	}
	if v := req.NewInputs.Get("metadata"); v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 entries")
		}
		for k, v := range v.AsMap().All {
			if n := utf8.RuneCountInString(k); n < 1 || n > 64 {
				fail("metadata", "keys must be 1–64 characters")
			}
			if v.IsString() && utf8.RuneCountInString(v.AsString()) > 512 {
				fail("metadata."+k, "must be at most 512 characters")
			}
		}
	}
	if b := req.NewInputs.Get("budget"); b.IsMap() {
		if cost := b.AsMap().Get("maxListCost"); cost.IsMap() {
			m := cost.AsMap()
			if v := m.Get("currency"); v.IsString() && v.AsString() != "USD" {
				fail("budget.maxListCost.currency", "must be USD")
			}
			if v := m.Get("amount"); v.IsString() {
				n, ok := new(big.Int).SetString(v.AsString(), 10)
				if !ok || n.Sign() < 0 {
					fail("budget.maxListCost.amount", "must be a nonnegative integer string")
				}
			}
		}
	}
	if s := req.NewInputs.Get("schedule"); s.IsMap() {
		for _, k := range []string{"expression", "timezone"} {
			nonempty("schedule."+k, s.AsMap().Get(k))
		}
	}
	return infer.CheckResponse[DeploymentArgs]{Inputs: a, Failures: failures}, nil
}

func (controller *Deployment) Create(ctx context.Context, req infer.CreateRequest[DeploymentArgs]) (infer.CreateResponse[DeploymentState], error) {
	if b := controller.operation; b != nil {
		req.Inputs = deploymentPresence(req.Inputs, b.inputs)
	}
	if req.DryRun {
		return infer.CreateResponse[DeploymentState]{Output: DeploymentState{DeploymentArgs: req.Inputs}}, nil
	}
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[DeploymentState]{}, err
	}
	params, err := deploymentNew(req.Inputs)
	if err != nil {
		return infer.CreateResponse[DeploymentState]{}, err
	}
	r, err := client.Beta.Deployments.New(ctx, params)
	if err != nil {
		return infer.CreateResponse[DeploymentState]{}, err
	}
	s, err := deploymentObserved(r, req.Inputs, deploymentObservationOptions{})
	if err != nil {
		return infer.CreateResponse[DeploymentState]{}, err
	}
	deploymentSave(controller.operation, s)
	return infer.CreateResponse[DeploymentState]{ID: deploymentIdentity(r), Output: s}, nil
}

func (controller *Deployment) Read(ctx context.Context, req infer.ReadRequest[DeploymentArgs, DeploymentState]) (infer.ReadResponse[DeploymentArgs, DeploymentState], error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, err
	}
	r, err := client.Beta.Deployments.Get(ctx, req.ID, sdk.BetaDeploymentGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, err
	}
	if deploymentIdentity(r) != req.ID {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, fmt.Errorf("deployment read returned a missing or different identity")
	}
	importing := req.Inputs.Name == "" && req.State.Name == ""
	if !importing && req.Inputs.Name == "" {
		req.Inputs = req.State.DeploymentArgs
	}
	if b := controller.operation; b != nil {
		bag := b.inputs
		if bag.Len() == 0 && !importing {
			bag = b.state
		}
		req.Inputs = deploymentPresence(req.Inputs, bag)
	}
	s, err := deploymentObserved(r, req.Inputs, deploymentObservationOptions{importing: importing})
	if err != nil {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[DeploymentArgs, DeploymentState]{}, nil
	}
	deploymentSave(controller.operation, s)
	return infer.ReadResponse[DeploymentArgs, DeploymentState]{ID: req.ID, Inputs: s.DeploymentArgs, State: s}, nil
}

func (controller *Deployment) Update(ctx context.Context, req infer.UpdateRequest[DeploymentArgs, DeploymentState]) (infer.UpdateResponse[DeploymentState], error) {
	if b := controller.operation; b != nil {
		req.Inputs = deploymentPresence(req.Inputs, b.inputs)
		req.State.DeploymentArgs = deploymentPresence(req.State.DeploymentArgs, b.state)
	}
	if req.DryRun {
		return infer.UpdateResponse[DeploymentState]{Output: DeploymentState{DeploymentArgs: req.Inputs}}, nil
	}
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[DeploymentState]{}, err
	}
	params, err := deploymentUpdate(req.State.DeploymentArgs, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[DeploymentState]{}, err
	}
	// The engine owns reads. Always apply full desired mutable managed state.
	r, err := client.Beta.Deployments.Update(ctx, req.ID, params)
	if err != nil {
		return infer.UpdateResponse[DeploymentState]{}, err
	}
	if deploymentIdentity(r) != req.ID {
		return infer.UpdateResponse[DeploymentState]{}, fmt.Errorf("deployment update returned a missing or different identity")
	}
	s, err := deploymentObserved(r, req.Inputs, deploymentObservationOptions{})
	if err != nil {
		return infer.UpdateResponse[DeploymentState]{}, err
	}
	deploymentSave(controller.operation, s)
	return infer.UpdateResponse[DeploymentState]{Output: s}, nil
}

func (controller *Deployment) Delete(ctx context.Context, req infer.DeleteRequest[DeploymentState]) (infer.DeleteResponse, error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = client.Beta.Deployments.Archive(ctx, req.ID, sdk.BetaDeploymentArchiveParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

type deploymentResource struct{ infer.InferredResource }

type deploymentOperationState struct {
	inputs, state property.Map
	observed      *DeploymentState
}

func (r deploymentResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&Deployment{operation: &deploymentOperationState{}}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = deploymentSecrets(req.Inputs)
	}
	return resp, err
}

func (r deploymentResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	// Dynamic event maps can be protected by an ancestor secret after encoding.
	// Compare their values/knownness, not the location of secret wrappers or
	// engine dependency annotations. Check still retains all input metadata.
	for _, bag := range []*property.Map{&req.Inputs, &req.State, &req.OldInputs} {
		if v, ok := bag.GetOk("initialEvents"); ok {
			*bag = bag.Set("initialEvents", deploymentEventComparison(v))
		}
	}
	return infer.Resource(&Deployment{operation: &deploymentOperationState{}}).Diff(ctx, req)
}

func (r deploymentResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	operation := &deploymentOperationState{inputs: req.Properties}
	resp, err := infer.Resource(&Deployment{operation: operation}).Create(ctx, req)
	if err == nil {
		resp.Properties = deploymentOutput(resp.Properties, operation.observed)
		if req.DryRun {
			resp.Properties = deploymentPreview(resp.Properties, req.Properties)
		}
	}
	return resp, err
}

func (r deploymentResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	operation := &deploymentOperationState{inputs: req.Inputs, state: req.Properties}
	resp, err := infer.Resource(&Deployment{operation: operation}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		resp.Properties = deploymentOutput(resp.Properties, operation.observed)
		resp.Inputs = deploymentOutput(resp.Inputs, operation.observed)
		// infer's Read encoder does not reliably retain nested secrets inside
		// dynamic event maps. Protect the collection rather than reconstructing
		// exact leaf wrappers; no input values or dependencies are copied.
		if req.Properties.Get("initialEvents").HasSecrets() || req.Inputs.Get("initialEvents").HasSecrets() {
			if events, ok := resp.Properties.GetOk("initialEvents"); ok {
				resp.Properties = resp.Properties.Set("initialEvents", events.WithSecret(true))
			}
			if events, ok := resp.Inputs.GetOk("initialEvents"); ok {
				resp.Inputs = resp.Inputs.Set("initialEvents", events.WithSecret(true))
			}
		}
	}
	return resp, err
}

func (r deploymentResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	operation := &deploymentOperationState{inputs: req.Inputs, state: req.State}
	resp, err := infer.Resource(&Deployment{operation: operation}).Update(ctx, req)
	if err == nil {
		resp.Properties = deploymentOutput(resp.Properties, operation.observed)
		if req.DryRun {
			resp.Properties = deploymentPreview(resp.Properties, req.Inputs)
		}
	}
	return resp, err
}

func (r deploymentResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Deployment{operation: &deploymentOperationState{}}).Delete(ctx, req)
}

func deploymentKnown(v property.Value) bool {
	if v.IsComputed() {
		return false
	}
	if v.IsMap() {
		for _, x := range v.AsMap().All {
			if !deploymentKnown(x) {
				return false
			}
		}
	}
	if v.IsArray() {
		for _, x := range v.AsArray().All {
			if !deploymentKnown(x) {
				return false
			}
		}
	}
	return true
}

func deploymentValidateEvent(v property.Value, path string, fail func(string, string)) {
	if !v.IsMap() {
		return
	}
	m := v.AsMap()
	typ := m.Get("type")
	if !typ.IsString() {
		return
	}
	allowed := map[string]bool{"type": true}
	switch typ.AsString() {
	case "user.message", "system.message":
		allowed["content"] = true
	case "user.define_outcome":
		for _, k := range []string{"description", "rubric", "max_iterations"} {
			allowed[k] = true
		}
	default:
		fail(path+".type", "must be user.message, system.message or user.define_outcome")
		return
	}
	for k, v := range m.All {
		if !v.IsNull() && !allowed[k] {
			fail(path+"."+k, "is not supported for this event type")
		}
	}
	if v := m.Get("content"); v.IsArray() {
		for i, b := range v.AsArray().All {
			if !b.IsMap() {
				continue
			}
			x := b.AsMap()
			t := x.Get("type")
			if !t.IsString() {
				continue
			}
			bp := fmt.Sprintf("%s.content[%d]", path, i)
			allowed := map[string]bool{"type": true}
			switch t.AsString() {
			case "text":
				allowed["text"] = true
			case "image":
				allowed["source"] = true
			case "document":
				allowed["source"] = true
				allowed["title"] = true
				allowed["context"] = true
			case "redacted":
			default:
				fail(bp+".type", "unknown content type")
			}
			if typ.AsString() == "system.message" && t.AsString() != "text" {
				fail(bp+".type", "system messages accept only text")
			}
			for k, v := range x.All {
				if !v.IsNull() && !allowed[k] {
					fail(bp+"."+k, "is not supported for this content type")
				}
			}
		}
	}
}

// This is comparison-only normalization for dynamic event data, never output
// encoding. Unknowns remain unknown and input bags are immutable.
func deploymentEventComparison(v property.Value) property.Value {
	if v.IsMap() {
		m := v.AsMap()
		for k, x := range m.All {
			m = m.Set(k, deploymentEventComparison(x))
		}
		v = property.WithGoValue(v, m)
	} else if v.IsArray() {
		xs := v.AsArray().AsSlice()
		for i, x := range xs {
			xs[i] = deploymentEventComparison(x)
		}
		v = property.WithGoValue(v, xs)
	}
	return v.WithSecret(false).WithDependencies(nil)
}

func deploymentPresence(a DeploymentArgs, m property.Map) DeploymentArgs {
	if v := m.Get("resources"); v.IsArray() && v.AsArray().Len() == 0 {
		a.Resources = []DeploymentResource{}
	}
	if v := m.Get("vaultIds"); v.IsArray() && v.AsArray().Len() == 0 {
		a.VaultIDs = []string{}
	}
	if v := m.Get("metadata"); v.IsMap() && v.AsMap().Len() == 0 {
		a.Metadata = map[string]string{}
	}
	return a
}

func deploymentOutput(m property.Map, s *DeploymentState) property.Map {
	if s == nil {
		return m
	}
	// Repair only collection encoding, retaining secrecy from infer's encoded
	// outputs. These values are not copied from raw input bags.
	if s.Resources != nil && len(s.Resources) == 0 {
		m = m.Set("resources", property.WithGoValue(m.Get("resources"), []property.Value{}))
	}
	if s.VaultIDs != nil && len(s.VaultIDs) == 0 {
		m = m.Set("vaultIds", property.WithGoValue(m.Get("vaultIds"), []property.Value{}))
	}
	if s.Metadata != nil && len(s.Metadata) == 0 {
		m = m.Set("metadata", property.WithGoValue(m.Get("metadata"), property.Map{}))
	}
	return deploymentSecrets(m)
}

// infer v1.5 erases whole-slice unknowns while hydrating typed previews.
// Restore only those unknowns, not input dependency annotations: custom resource
// outputs are graph nodes in their own right. Other values use infer's encoding.
func deploymentPreview(m, inputs property.Map) property.Map {
	for _, k := range []string{"initialEvents", "resources", "vaultIds"} {
		if v := inputs.Get(k); v.IsComputed() {
			m = m.Set(k, property.New(property.Computed).WithSecret(v.Secret()))
		}
	}
	return deploymentSecrets(m)
}

func deploymentSecrets(m property.Map) property.Map {
	if v := m.Get("resources"); v.IsArray() {
		vs := []property.Value{}
		for _, v := range v.AsArray().All {
			if v.IsMap() {
				x := v.AsMap()
				if token := x.Get("authorizationToken"); !token.IsNull() {
					x = x.Set("authorizationToken", token.WithSecret(true))
					v = property.WithGoValue(v, x)
				}
			}
			vs = append(vs, v)
		}
		m = m.Set("resources", property.WithGoValue(v, vs))
	}
	return m
}

func deploymentSave(operation *deploymentOperationState, s DeploymentState) {
	if b := operation; b != nil {
		b.observed = &s
	}
}

type deploymentObservationOptions struct {
	importing bool
}

// Response events are not request events: the generated response contract permits
// null max_iterations and null document title/context, while request optionals
// accept omission or a value. Decode the public API-shaped event surface into
// fresh maps, remove only these documented nulls, then validate the request form.
// This does not relax validation of user-supplied requests.
func deploymentResponseEvents(observed []sdk.BetaManagedAgentsDeploymentInitialEventUnion, prior []map[string]any, importing bool) ([]map[string]any, error) {
	if len(observed) < 1 || len(observed) > 50 {
		return nil, fmt.Errorf("deployment initialEvents must contain 1 to 50 entries")
	}
	out := make([]map[string]any, len(observed))
	for i, event := range observed {
		var e map[string]any
		if err := json.Unmarshal([]byte(event.RawJSON()), &e); err != nil {
			return nil, err
		}
		var p map[string]any
		if !importing && i < len(prior) && prior[i]["type"] == e["type"] {
			p = prior[i]
		}
		switch e["type"] {
		case "user.define_outcome":
			if e["max_iterations"] == nil {
				delete(e, "max_iterations")
			} else if !importing && e["max_iterations"] == float64(3) {
				// The documented default is not newly managed merely because the
				// platform expanded it. Nondefault observations and explicitly
				// configured values remain observable, including drift.
				if _, configured := p["max_iterations"]; !configured {
					delete(e, "max_iterations")
				}
			}
		case "user.message":
			blocks, _ := e["content"].([]any)
			priorBlocks, _ := p["content"].([]any)
			for j, block := range blocks {
				b, ok := block.(map[string]any)
				if !ok || b["type"] != "document" {
					continue
				}
				for _, key := range []string{"title", "context"} {
					if b[key] == nil {
						delete(b, key)
					}
				}
				source, _ := b["source"].(map[string]any)
				if importing || source["type"] != "text" || source["media_type"] != "text/plain" {
					continue
				}
				var priorSource map[string]any
				if j < len(priorBlocks) {
					if priorBlock, ok := priorBlocks[j].(map[string]any); ok && priorBlock["type"] == "document" {
						priorSource, _ = priorBlock["source"].(map[string]any)
					}
				}
				if _, configured := priorSource["media_type"]; !configured {
					delete(source, "media_type")
				}
			}
		}
		out[i] = e
	}
	if _, err := deploymentEvents(out); err != nil {
		return nil, err
	}
	return out, nil
}

func deploymentAmount(s string) bool {
	n, ok := new(big.Int).SetString(s, 10)
	return ok && n.Sign() >= 0
}

func deploymentOptionalString(f respjson.Field) bool {
	return f.Raw() == "" || f.Raw() == "null" || apiutil.StringField(f)
}

func deploymentIdentity(r *sdk.BetaManagedAgentsDeployment) string {
	if r != nil && apiutil.StringField(r.JSON.ID) {
		return r.ID
	}
	return ""
}

// Map server facts while preserving write-only credentials by repository identity.
func deploymentObserved(r *sdk.BetaManagedAgentsDeployment, prior DeploymentArgs, options deploymentObservationOptions) (DeploymentState, error) {
	s := DeploymentState{}
	if r == nil {
		return s, fmt.Errorf("missing deployment response")
	}
	var errs []error
	invalid := func(name string) { errs = append(errs, fmt.Errorf("invalid or incomplete deployment %s", name)) }
	if deploymentIdentity(r) == "" {
		invalid("identity")
	}
	if apiutil.StringField(r.JSON.Name) && r.Name != "" {
		s.Name = r.Name
	} else {
		invalid("name")
	}
	if apiutil.StringField(r.JSON.EnvironmentID) && r.EnvironmentID != "" {
		s.EnvironmentID = r.EnvironmentID
	} else {
		invalid("environmentId")
	}
	if r.JSON.Agent.Valid() && apiutil.StringField(r.Agent.JSON.ID) && r.Agent.ID != "" && r.Agent.JSON.Version.Valid() && strings.TrimSpace(r.Agent.JSON.Version.Raw()) == fmt.Sprint(r.Agent.Version) && r.Agent.Version > 0 && apiutil.StringField(r.Agent.JSON.Type) && r.Agent.Type == "agent" {
		s.Agent = AgentReference{ID: r.Agent.ID}
		if options.importing || prior.Agent.Version != nil {
			v := int(r.Agent.Version)
			s.Agent.Version = &v
		}
	} else {
		invalid("agent")
	}
	if r.JSON.InitialEvents.Valid() {
		events, err := deploymentResponseEvents(r.InitialEvents, prior.InitialEvents, options.importing)
		if err != nil {
			invalid("initialEvents")
		} else {
			s.InitialEvents = events
		}
	} else {
		invalid("initialEvents")
	}
	if apiutil.StringField(r.JSON.Description) {
		if options.importing || prior.Description != nil {
			v := r.Description
			s.Description = &v
		}
	} else if r.JSON.Description.Raw() == "null" {
		s.Description = nil
	} else {
		invalid("description")
	}
	if r.JSON.Metadata.Valid() {
		var meta map[string]string
		if err := json.Unmarshal([]byte(r.JSON.Metadata.Raw()), &meta); err != nil {
			invalid("metadata")
		} else if options.importing {
			s.Metadata = meta
		} else {
			s.Metadata = map[string]string{}
			for k := range prior.Metadata {
				if v, ok := meta[k]; ok {
					s.Metadata[k] = v
				}
			}
			if prior.Metadata == nil {
				s.Metadata = nil
			}
		}
	} else {
		invalid("metadata")
	}
	if r.JSON.VaultIDs.Valid() {
		var ids []string
		if err := json.Unmarshal([]byte(r.JSON.VaultIDs.Raw()), &ids); err != nil {
			invalid("vaultIds")
		} else if options.importing || prior.VaultIDs != nil {
			s.VaultIDs = ids
		}
	} else {
		invalid("vaultIds")
	}
	if r.JSON.Resources.Valid() {
		resources := make([]DeploymentResource, len(r.Resources))
		valid := true
		for i, v := range r.Resources {
			x := DeploymentResource{Type: DeploymentResourceType(v.Type)}
			if !apiutil.StringField(v.JSON.Type) || !deploymentOptionalString(v.JSON.MountPath) || !deploymentOptionalString(v.JSON.Instructions) {
				valid = false
			}
			ptr := func(f respjson.Field, v string) *string {
				if apiutil.StringField(f) {
					return &v
				}
				return nil
			}
			x.MountPath = ptr(v.JSON.MountPath, v.MountPath)
			switch v.Type {
			case "github_repository":
				x.URL = ptr(v.JSON.URL, v.URL)
				if x.URL == nil || *x.URL == "" {
					valid = false
				}
				// Credentials belong to a repository, not its array position. Never attach
				// a token to a different URL after external reordering or replacement.
				matches := 0
				for _, p := range prior.Resources {
					if p.Type == x.Type && p.URL != nil && x.URL != nil && *p.URL == *x.URL {
						matches++
						x.AuthorizationToken = p.AuthorizationToken
					}
				}
				if matches != 1 {
					x.AuthorizationToken = nil
					// Identical repository identities cannot reveal credential changes. Keep
					// the known write-only value at its original slot, never a different URL.
					if i < len(prior.Resources) {
						p := prior.Resources[i]
						if p.Type == x.Type && p.URL != nil && x.URL != nil && *p.URL == *x.URL {
							x.AuthorizationToken = p.AuthorizationToken
						}
					}
				}
				if v.JSON.Checkout.Valid() {
					c := v.Checkout
					x.Checkout = &Checkout{Type: CheckoutType(c.Type)}
					if !apiutil.StringField(c.JSON.Type) {
						valid = false
					}
					switch c.Type {
					case "branch":
						x.Checkout.Name = ptr(c.JSON.Name, c.Name)
						if x.Checkout.Name == nil {
							valid = false
						}
					case "commit":
						x.Checkout.Sha = ptr(c.JSON.Sha, c.Sha)
						if x.Checkout.Sha == nil {
							valid = false
						}
					default:
						valid = false
					}
				} else if v.JSON.Checkout.Raw() != "" && v.JSON.Checkout.Raw() != "null" {
					valid = false
				}
			case "file":
				x.FileID = ptr(v.JSON.FileID, v.FileID)
				if x.FileID == nil || *x.FileID == "" {
					valid = false
				}
			case "memory_store":
				x.MemoryStoreID = ptr(v.JSON.MemoryStoreID, v.MemoryStoreID)
				if x.MemoryStoreID == nil || *x.MemoryStoreID == "" {
					valid = false
				}
				x.Instructions = ptr(v.JSON.Instructions, v.Instructions)
				if apiutil.StringField(v.JSON.Access) && (v.Access == "read_only" || v.Access == "read_write") {
					a := MemoryAccess(v.Access)
					x.Access = &a
				} else if v.JSON.Access.Raw() != "" && v.JSON.Access.Raw() != "null" {
					valid = false
				}
			default:
				valid = false
			}
			// Defaults remain unconfigured on managed reads. Explicit zero/empty values
			// still observe drift; only originally absent optional fields are omitted.
			if !options.importing && i < len(prior.Resources) {
				p := prior.Resources[i]
				if p.Type == x.Type {
					if p.MountPath == nil {
						x.MountPath = nil
					}
					if p.Checkout == nil {
						x.Checkout = nil
					}
					if p.Access == nil {
						x.Access = nil
					}
					if p.Instructions == nil {
						x.Instructions = nil
					}
				}
			}
			resources[i] = x
		}
		if valid {
			if options.importing || prior.Resources != nil {
				s.Resources = resources
			}
		} else {
			invalid("resources")
		}
	} else {
		invalid("resources")
	}
	if r.JSON.Schedule.Raw() == "null" {
		s.Schedule = nil
	} else if r.JSON.Schedule.Valid() && apiutil.StringField(r.Schedule.JSON.Type) && r.Schedule.Type == "cron" && apiutil.StringField(r.Schedule.JSON.Expression) && apiutil.StringField(r.Schedule.JSON.Timezone) {
		if options.importing || prior.Schedule != nil {
			s.Schedule = &Schedule{Expression: r.Schedule.Expression, Timezone: r.Schedule.Timezone}
		}
	} else {
		invalid("schedule")
	}
	if r.JSON.Budget.Raw() == "null" || r.JSON.Budget.Raw() == "" {
		s.Budget = nil
	} else if r.JSON.Budget.Valid() && apiutil.StringField(r.Budget.JSON.Type) && r.Budget.Type == "limit" && r.Budget.JSON.MaxListCost.Valid() && apiutil.StringField(r.Budget.MaxListCost.JSON.Amount) && deploymentAmount(r.Budget.MaxListCost.Amount) && apiutil.StringField(r.Budget.MaxListCost.JSON.Currency) && r.Budget.MaxListCost.Currency == "USD" {
		if options.importing || prior.Budget != nil {
			s.Budget = &Budget{MaxListCost: MonetaryAmount{Amount: r.Budget.MaxListCost.Amount, Currency: string(r.Budget.MaxListCost.Currency)}}
		}
	} else {
		invalid("budget")
	}
	if apiutil.StringField(r.JSON.Status) && (r.Status == "active" || r.Status == "paused") {
		s.Status = string(r.Status)
	} else {
		invalid("status")
	}
	if r.JSON.CreatedAt.Valid() && !r.CreatedAt.IsZero() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	} else {
		invalid("createdAt")
	}
	if r.JSON.UpdatedAt.Valid() && !r.UpdatedAt.IsZero() {
		s.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	} else {
		invalid("updatedAt")
	}
	if r.JSON.ArchivedAt.Raw() != "null" && (!r.JSON.ArchivedAt.Valid() || r.ArchivedAt.IsZero()) {
		invalid("archivedAt")
	}
	if len(errs) > 0 {
		return DeploymentState{}, fmt.Errorf("%v", errs)
	}
	return s, nil
}

func deploymentNew(a DeploymentArgs) (sdk.BetaDeploymentNewParams, error) {
	p := sdk.BetaDeploymentNewParams{Name: a.Name, EnvironmentID: a.EnvironmentID, Metadata: maps.Clone(a.Metadata), VaultIDs: slices.Clone(a.VaultIDs)}
	if a.Agent.Version == nil {
		p.Agent.OfString = sdk.String(a.Agent.ID)
	} else {
		p.Agent.OfBetaManagedAgentsAgents = &sdk.BetaManagedAgentsAgentParams{ID: a.Agent.ID, Version: sdk.Int(int64(*a.Agent.Version))}
	}
	if a.Description != nil {
		p.Description = sdk.String(*a.Description)
	}
	if a.Schedule != nil {
		p.Schedule = sdk.BetaManagedAgentsScheduleParams{Expression: a.Schedule.Expression, Timezone: a.Schedule.Timezone}
	}
	if a.Budget != nil {
		p.Budget = sdk.BetaManagedAgentsBudgetLimitParam{MaxListCost: sdk.BetaMonetaryAmountParam{Amount: a.Budget.MaxListCost.Amount, Currency: sdk.BetaCurrency(a.Budget.MaxListCost.Currency)}}
	}
	if a.Resources != nil {
		p.Resources = make([]sdk.BetaDeploymentNewParamsResourceUnion, len(a.Resources))
	}
	for i, r := range a.Resources {
		switch r.Type {
		case "github_repository":
			if r.URL == nil {
				return p, fmt.Errorf("resources[%d].url is required", i)
			}
			v := &sdk.BetaManagedAgentsGitHubRepositoryResourceParams{URL: *r.URL}
			if r.AuthorizationToken != nil {
				v.AuthorizationToken = *r.AuthorizationToken
				if *r.AuthorizationToken == "" {
					// The generated plain string uses omitzero. Preserve explicit
					// empty presence rather than claiming an omitted token was sent.
					v.SetExtraFields(map[string]any{"authorization_token": ""})
				}
			}
			if r.MountPath != nil {
				v.MountPath = sdk.String(*r.MountPath)
			}
			if c := r.Checkout; c != nil {
				switch c.Type {
				case "branch":
					if c.Name == nil {
						return p, fmt.Errorf("branch name is required")
					}
					v.Checkout.OfBranch = &sdk.BetaManagedAgentsBranchCheckoutParam{Name: *c.Name}
				case "commit":
					if c.Sha == nil {
						return p, fmt.Errorf("commit sha is required")
					}
					v.Checkout.OfCommit = &sdk.BetaManagedAgentsCommitCheckoutParam{Sha: *c.Sha}
				default:
					return p, fmt.Errorf("unknown checkout type %q", c.Type)
				}
			}
			p.Resources[i].OfGitHubRepository = v
		case "file":
			if r.FileID == nil {
				return p, fmt.Errorf("resources[%d].fileId is required", i)
			}
			v := &sdk.BetaManagedAgentsFileResourceParams{FileID: *r.FileID}
			if r.MountPath != nil {
				v.MountPath = sdk.String(*r.MountPath)
			}
			p.Resources[i].OfFile = v
		case "memory_store":
			if r.MemoryStoreID == nil {
				return p, fmt.Errorf("resources[%d].memoryStoreId is required", i)
			}
			v := &sdk.BetaManagedAgentsMemoryStoreResourceParam{MemoryStoreID: *r.MemoryStoreID}
			if r.Access != nil {
				v.Access = sdk.BetaManagedAgentsMemoryStoreResourceParamAccess(*r.Access)
			}
			if r.Instructions != nil {
				v.Instructions = sdk.String(*r.Instructions)
			}
			p.Resources[i].OfMemoryStore = v
		default:
			return p, fmt.Errorf("unknown resource type %q", r.Type)
		}
	}
	var err error
	p.InitialEvents, err = deploymentEvents(a.InitialEvents)
	return p, err
}

func deploymentUpdate(old, a DeploymentArgs) (sdk.BetaDeploymentUpdateParams, error) {
	n, err := deploymentNew(a)
	if err != nil {
		return sdk.BetaDeploymentUpdateParams{}, err
	}
	p := sdk.BetaDeploymentUpdateParams{Name: sdk.String(a.Name), EnvironmentID: sdk.String(a.EnvironmentID), InitialEvents: n.InitialEvents, Description: n.Description, Schedule: n.Schedule, Budget: n.Budget, VaultIDs: n.VaultIDs}
	p.Agent.OfString = n.Agent.OfString
	p.Agent.OfBetaManagedAgentsAgents = n.Agent.OfBetaManagedAgentsAgents
	if n.Resources != nil {
		p.Resources = make([]sdk.BetaDeploymentUpdateParamsResourceUnion, len(n.Resources))
	}
	for i, r := range n.Resources {
		p.Resources[i] = sdk.BetaDeploymentUpdateParamsResourceUnion{OfGitHubRepository: r.OfGitHubRepository, OfFile: r.OfFile, OfMemoryStore: r.OfMemoryStore}
	}
	if a.Description == nil && old.Description != nil {
		p.Description = param.Null[string]()
	}
	if a.Schedule == nil && old.Schedule != nil {
		p.Schedule = param.NullStruct[sdk.BetaManagedAgentsScheduleParams]()
	}
	if a.Budget == nil && old.Budget != nil {
		p.Budget = param.NullStruct[sdk.BetaManagedAgentsBudgetLimitParam]()
	}
	if a.Resources == nil && old.Resources != nil {
		p.Resources = param.NullSlice[[]sdk.BetaDeploymentUpdateParamsResourceUnion]()
	}
	if a.VaultIDs == nil && old.VaultIDs != nil {
		p.VaultIDs = param.NullSlice[[]string]()
	}
	metadata := apiutil.MergedStringMap(old.Metadata, a.Metadata)
	if a.Metadata != nil || old.Metadata != nil {
		p.SetExtraFields(map[string]any{"metadata": metadata})
	}
	return p, nil
}

// initialEvents deliberately retains its public API-shaped map surface. Select
// generated variants explicitly; unknown or extraneous fields are never silently
// sent as unvalidated extra request fields.
func deploymentEvents(events []map[string]any) ([]sdk.BetaManagedAgentsDeploymentInitialEventParamsUnion, error) {
	out := make([]sdk.BetaManagedAgentsDeploymentInitialEventParamsUnion, len(events))
	for i, e := range events {
		var err error
		keys := []string{"type", "content"}
		if e["type"] == "user.define_outcome" {
			keys = []string{"type", "description", "rubric", "max_iterations"}
		}
		if err := deploymentEventKeys(e, keys...); err != nil {
			return nil, err
		}
		switch e["type"] {
		case "user.message", "system.message":
			blocks, ok := e["content"].([]any)
			if !ok {
				return nil, fmt.Errorf("initialEvents[%d].content must be an array", i)
			}
			if e["type"] == "system.message" {
				b := make([]sdk.BetaManagedAgentsSystemContentBlockParam, len(blocks))
				for j, x := range blocks {
					m, ok := x.(map[string]any)
					if !ok || m["type"] != "text" {
						return nil, fmt.Errorf("system content must be text")
					}
					if err := deploymentEventKeys(m, "type", "text"); err != nil {
						return nil, err
					}
					text, ok := m["text"].(string)
					if !ok {
						return nil, fmt.Errorf("text is required")
					}
					b[j] = sdk.BetaManagedAgentsSystemContentBlockParam{Text: text}
				}
				out[i] = sdk.BetaManagedAgentsDeploymentInitialEventParamsOfSystemMessage(b)
			} else {
				b := make([]sdk.BetaManagedAgentsUserMessageEventParamsContentUnion, len(blocks))
				for j, x := range blocks {
					m, ok := x.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("content must be an object")
					}
					b[j], err = deploymentContent(m)
					if err != nil {
						return nil, err
					}
				}
				out[i] = sdk.BetaManagedAgentsDeploymentInitialEventParamsOfUserMessage(b)
			}
		case "user.define_outcome":
			desc, ok := e["description"].(string)
			if !ok {
				return nil, fmt.Errorf("outcome description is required")
			}
			r, ok := e["rubric"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("outcome rubric is required")
			}
			v := &sdk.BetaManagedAgentsUserDefineOutcomeEventParams{Description: desc}
			key := "content"
			if r["type"] == "file" {
				key = "file_id"
			}
			if err := deploymentEventKeys(r, "type", key); err != nil {
				return nil, err
			}
			switch r["type"] {
			case "text":
				s, ok := r["content"].(string)
				if !ok {
					return nil, fmt.Errorf("rubric content is required")
				}
				v.Rubric.OfText = &sdk.BetaManagedAgentsTextRubricParams{Content: s}
			case "file":
				s, ok := r["file_id"].(string)
				if !ok {
					return nil, fmt.Errorf("rubric file_id is required")
				}
				v.Rubric.OfFile = &sdk.BetaManagedAgentsFileRubricParams{FileID: s}
			default:
				return nil, fmt.Errorf("unknown rubric type")
			}
			if n, ok := e["max_iterations"]; ok {
				f, ok := n.(float64)
				if !ok || f != float64(int64(f)) || f < 1 || f > 20 {
					return nil, fmt.Errorf("max_iterations must be an integer from 1 to 20")
				}
				v.MaxIterations = sdk.Int(int64(f))
			}
			out[i].OfUserDefineOutcome = v
		default:
			return nil, fmt.Errorf("initialEvents[%d] has unknown type", i)
		}
	}
	return out, nil
}

func deploymentEventKeys(m map[string]any, keys ...string) error {
	for k := range m {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("unsupported event field %q", k)
		}
	}
	return nil
}

func deploymentContent(m map[string]any) (sdk.BetaManagedAgentsUserMessageEventParamsContentUnion, error) {
	var b sdk.BetaManagedAgentsUserMessageEventParamsContentUnion
	keys := []string{"type"}
	switch m["type"] {
	case "text":
		keys = append(keys, "text")
	case "image":
		keys = append(keys, "source")
	case "document":
		keys = append(keys, "source", "title", "context")
	}
	if err := deploymentEventKeys(m, keys...); err != nil {
		return b, err
	}
	str := func(m map[string]any, k string) (string, error) {
		v, ok := m[k].(string)
		if !ok {
			return "", fmt.Errorf("content %s must be a string", k)
		}
		return v, nil
	}
	switch m["type"] {
	case "text":
		v, e := str(m, "text")
		if e != nil {
			return b, e
		}
		b.OfText = &sdk.BetaManagedAgentsTextBlockParam{Text: v}
	case "redacted":
		b.OfRedacted = &sdk.BetaManagedAgentsRedactedBlockParam{}
	case "image", "document":
		s, ok := m["source"].(map[string]any)
		if !ok {
			return b, fmt.Errorf("content source is required")
		}
		sourceKeys := []string{"type"}
		switch s["type"] {
		case "url":
			sourceKeys = append(sourceKeys, "url")
		case "file":
			sourceKeys = append(sourceKeys, "file_id")
		case "base64", "text":
			sourceKeys = append(sourceKeys, "data", "media_type")
		}
		if err := deploymentEventKeys(s, sourceKeys...); err != nil {
			return b, err
		}
		if s["type"] == "text" {
			if media, ok := s["media_type"]; ok && media != "text/plain" {
				return b, fmt.Errorf("text document media_type must be text/plain")
			}
		}
		image := &sdk.BetaManagedAgentsImageBlockParam{}
		doc := &sdk.BetaManagedAgentsDocumentBlockParam{}
		switch s["type"] {
		case "url":
			v, e := str(s, "url")
			if e != nil {
				return b, e
			}
			image.Source.OfURL = &sdk.BetaManagedAgentsURLImageSourceParam{URL: v}
			doc.Source.OfURL = &sdk.BetaManagedAgentsURLDocumentSourceParam{URL: v}
		case "file":
			v, e := str(s, "file_id")
			if e != nil {
				return b, e
			}
			image.Source.OfFile = &sdk.BetaManagedAgentsFileImageSourceParam{FileID: v}
			doc.Source.OfFile = &sdk.BetaManagedAgentsFileDocumentSourceParam{FileID: v}
		case "base64":
			v, e := str(s, "data")
			if e != nil {
				return b, e
			}
			media, e := str(s, "media_type")
			if e != nil {
				return b, e
			}
			image.Source.OfBase64 = &sdk.BetaManagedAgentsBase64ImageSourceParam{Data: v, MediaType: media}
			doc.Source.OfBase64 = &sdk.BetaManagedAgentsBase64DocumentSourceParam{Data: v, MediaType: media}
		case "text":
			if m["type"] != "document" {
				return b, fmt.Errorf("image does not accept text source")
			}
			v, e := str(s, "data")
			if e != nil {
				return b, e
			}
			doc.Source.OfText = &sdk.BetaManagedAgentsPlainTextDocumentSourceParam{Data: v}
		default:
			return b, fmt.Errorf("unknown content source type")
		}
		if m["type"] == "image" {
			b.OfImage = image
		} else {
			if v, ok := m["title"]; ok {
				s, ok := v.(string)
				if !ok {
					return b, fmt.Errorf("title must be a string")
				}
				doc.Title = sdk.String(s)
			}
			if v, ok := m["context"]; ok {
				s, ok := v.(string)
				if !ok {
					return b, fmt.Errorf("context must be a string")
				}
				doc.Context = sdk.String(s)
			}
			b.OfDocument = doc
		}
	default:
		return b, fmt.Errorf("unknown content type")
	}
	return b, nil
}

package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"google.golang.org/protobuf/types/known/structpb"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// effectiveConfigInputs decodes only the one object-valued config property.
// Pulumi's official RPC unmarshaler owns signature/unknown/output semantics;
// ordinary string properties are never interpreted as JSON or wire metadata.
func effectiveConfigInputs(inputs property.Map) (property.Map, error) {
	v := inputs.Get("federation")
	if !v.IsString() {
		return inputs, nil
	}
	var payload any
	if err := json.Unmarshal([]byte(v.AsString()), &payload); err != nil {
		return inputs, fmt.Errorf("federation must be an object or valid Pulumi JSON-encoded object")
	}
	wire, err := structpb.NewStruct(map[string]any{"federation": payload})
	if err != nil {
		return inputs, fmt.Errorf("invalid federation configuration encoding")
	}
	decoded, err := plugin.UnmarshalProperties(wire, plugin.MarshalOptions{
		KeepUnknowns: true, KeepSecrets: true, KeepOutputValues: true,
		KeepResources: true, RejectAssets: true,
	})
	if err != nil {
		return inputs, fmt.Errorf("invalid federation configuration encoding")
	}
	value := resource.FromResourcePropertyValue(resource.NewProperty(decoded)).AsMap().Get("federation")
	// A string wrapper and an encoded output may each carry metadata.
	deps := append(value.Dependencies()[:0:0], value.Dependencies()...)
	for _, dep := range v.Dependencies() {
		found := false
		for _, existing := range deps {
			if dep == existing {
				found = true
				break
			}
		}
		if !found {
			deps = append(deps, dep)
		}
	}
	value = value.WithSecret(value.Secret() || v.Secret()).WithDependencies(deps)
	return inputs.Set("federation", value), nil
}

// PreserveConfigMetadata wraps the inferred provider's CheckConfig. Install after
// infer.Provider: infer v1.5.0 apply_secrets.go's deferred wrappers overwrite
// unknownness for pre-secret unknowns and drop output dependencies. Config
// normalization never moves paths, so metadata can be restored at the same path.
// It also prevents typed DiffConfig's unknown-to-zero decoding from claiming
// replacement: an unknown value is not evidence of an actual boundary change.
// This adapter does not change validation, defaults, or known normalized values.
func PreserveConfigMetadata(provider p.Provider) p.Provider {
	provider.CheckConfig = func(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
		original, err := effectiveConfigInputs(req.Inputs)
		if err != nil {
			return p.CheckResponse{Inputs: req.Inputs, Failures: []p.CheckFailure{{Property: "federation", Reason: err.Error()}}}, nil
		}
		// The legacy complexconfig middleware rewrites even ordinary strings
		// and loses nested metadata in encoded objects. A fresh CheckConfig
		// receiver carries effective inputs explicitly across typed decoding.
		// It never owns or configures SDK clients.
		inputs := withoutDependencies(property.New(original)).AsMap()
		operation := &Config{checkInputs: &inputs}
		check := infer.Wrap(p.Provider{}, infer.Options{Config: infer.Config(operation)}).CheckConfig
		req.Inputs = property.NewMap(nil)
		resp, err := check(ctx, req)
		if err != nil {
			return resp, err
		}
		restored := restoreMetadata(property.New(original), property.New(resp.Inputs))
		resp.Inputs = restored.AsMap()
		// The engine passes version at the top level and prohibits changing it.
		// DefaultCheck uses a resource decoder, so Config.Check filters it only
		// from typed validation; preserve the exact original value here.
		for key, value := range original.All {
			if reservedConfigField(key) {
				resp.Inputs = resp.Inputs.Set(key, value)
			}
		}
		return resp, nil
	}
	provider.DiffConfig = func(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
		originalInputs, originalState := req.Inputs, req.State
		// Typed CustomDiff decodes new inputs using the strict resource decoder.
		// Plugin version is provenance, not an Anthropic management boundary.
		req.Inputs = withoutDependencies(property.New(configValidationInputs(req.Inputs))).AsMap()
		req.State = withoutDependencies(property.New(configValidationInputs(req.State))).AsMap()
		req.OldInputs = withoutDependencies(property.New(configValidationInputs(req.OldInputs))).AsMap()
		// Diff's typed decoder also needs an invocation-local receiver; do not
		// hydrate proposed configuration into the configured client owner.
		diff := infer.Wrap(p.Provider{}, infer.Options{Config: infer.Config(&Config{})}).DiffConfig
		resp, err := diff(ctx, req)
		if err != nil {
			return resp, err
		}
		for _, key := range []string{"baseUrl", "workspaceId", "apiKey", "adminKey", "authToken", "federation"} {
			old, next := originalState.Get(key), originalInputs.Get(key)
			if old.HasComputed() || next.HasComputed() {
				// Preserve a possible edit as an ordinary update, not a proven
				// retargeting. Equal unknowns are unchanged. Never interpret
				// the typed decoder's zero/default as a known identity.
				if equalConfigValue(old, next) {
					delete(resp.DetailedDiff, key)
				} else {
					if resp.DetailedDiff == nil {
						resp.DetailedDiff = map[string]p.PropertyDiff{}
					}
					resp.DetailedDiff[key] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
				}
			}
		}
		resp.HasChanges = len(resp.DetailedDiff) != 0
		return resp, nil
	}
	return provider
}

// Compare configuration values recursively, including identity-token command arguments.
// Ignore metadata for value/knownness equality, including federation children.
func equalConfigValue(a, b property.Value) bool {
	if a.IsMap() && b.IsMap() {
		if a.AsMap().Len() != b.AsMap().Len() {
			return false
		}
		for key, child := range a.AsMap().All {
			other, ok := b.AsMap().GetOk(key)
			if !ok || !equalConfigValue(child, other) {
				return false
			}
		}
		return true
	}
	if a.IsArray() && b.IsArray() {
		if a.AsArray().Len() != b.AsArray().Len() {
			return false
		}
		for i, child := range a.AsArray().All {
			if !equalConfigValue(child, b.AsArray().Get(i)) {
				return false
			}
		}
		return true
	}
	return a.WithSecret(false).WithDependencies(nil).Equals(b.WithSecret(false).WithDependencies(nil))
}

// Dependencies have no effect on local validation/diff. Infer v1.5's secret
// processing drops them, so restore them from effective inputs after Check.
func withoutDependencies(v property.Value) property.Value {
	if v.IsMap() {
		m := v.AsMap()
		for key, child := range m.All {
			m = m.Set(key, withoutDependencies(child))
		}
		return property.New(m).WithSecret(v.Secret())
	}
	if v.IsArray() {
		items := make([]property.Value, v.AsArray().Len())
		for i, child := range v.AsArray().All {
			items[i] = withoutDependencies(child)
		}
		return property.New(items).WithSecret(v.Secret())
	}
	return v.WithDependencies(nil)
}

func restoreMetadata(original, checked property.Value) property.Value {
	if original.IsComputed() {
		return original.WithSecret(original.Secret() || checked.Secret())
	}
	if original.IsMap() && checked.IsMap() {
		m := checked.AsMap()
		for key, value := range original.AsMap().All {
			m = m.Set(key, restoreMetadata(value, m.Get(key)))
		}
		checked = property.New(m).WithSecret(checked.Secret())
	}
	if original.IsArray() && checked.IsArray() {
		items := make([]property.Value, checked.AsArray().Len())
		for i, child := range checked.AsArray().All {
			items[i] = child
			if i < original.AsArray().Len() {
				items[i] = restoreMetadata(original.AsArray().Get(i), child)
			}
		}
		checked = property.New(items).WithSecret(checked.Secret())
	}
	return checked.WithSecret(original.Secret() || checked.Secret()).WithDependencies(original.Dependencies())
}

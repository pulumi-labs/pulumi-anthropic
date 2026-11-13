package config

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Federation authenticates through an explicitly selected OIDC identity source.
type Federation struct {
	OrganizationID   string        `pulumi:"organizationId"`
	RuleID           string        `pulumi:"ruleId"`
	IdentityToken    IdentityToken `pulumi:"identityToken" provider:"secret"`
	ServiceAccountID string        `pulumi:"serviceAccountId,optional"`
}

func (f *Federation) Annotate(a infer.Annotator) {
	// The config module is reserved by language SDK generators for stack getters.
	a.SetToken("index", "Federation")
	a.Describe(f, "Workload identity federation using an explicitly configured OIDC identity token source. Files are reread and commands are rerun when a new access token is needed. No default token file or authentication profile is used.")
	a.Describe(&f.OrganizationID, "The organization the federation rule belongs to. Required for the token exchange; this value does not select an organization for API-key operations.")
	a.Describe(&f.RuleID, "Federation rule ID (fdrl_...). The server's rule determines granted scopes; OAuth-only identity operations require org:admin.")
	a.Describe(&f.IdentityToken, "The OIDC assertion source. Specify exactly one of token, file or command. This configuration is secret; file contents and command output are not saved in provider state. Assertions with a jti claim must be fresh for each exchange; use a command that obtains a new assertion rather than sharing a previously exchanged token.")
	a.Describe(&f.ServiceAccountID, "Optional expected service-account target (svac_...). Omit for user-targeted rules.")
}

// normalizeBaseURL mirrors the SDK's trailing-slash directory semantics without
// cleaning paths (which could change routing). Errors never echo input URLs.
func normalizeBaseURL(raw string) (string, error) {
	if raw == "" {
		return "https://api.anthropic.com/", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("baseUrl must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		host := u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		u.Host = host
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	return u.String(), nil
}

// Match infer/resource.go diff's reserved-field policy: version belongs to
// the engine; double-underscore properties are internal (including infer's
// __pulumi-go-provider-infer state marker). Never ignore arbitrary typos.
func reservedConfigField(key string) bool {
	return key == "version" || strings.HasPrefix(key, "__")
}

func configValidationInputs(inputs property.Map) property.Map {
	for key := range inputs.All {
		if reservedConfigField(key) {
			inputs = inputs.Delete(key)
		}
	}
	return inputs
}

var _ infer.CustomCheck[*Config] = (*Config)(nil)
var _ infer.CustomDiff[*Config, *Config] = (*Config)(nil)

func (controller *Config) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[*Config], error) {
	if controller.checkInputs != nil {
		req.NewInputs = *controller.checkInputs
	}
	c, failures, err := infer.DefaultCheck[Config](ctx, configValidationInputs(req.NewInputs))
	if err != nil {
		return infer.CheckResponse[*Config]{}, err
	}
	if !req.NewInputs.Get("baseUrl").IsComputed() {
		base, err := normalizeBaseURL(c.BaseURL)
		if err != nil {
			failures = append(failures, p.CheckFailure{Property: "baseUrl", Reason: err.Error()})
		} else {
			c.BaseURL = base
		}
	}
	failures = append(failures, validateConfig(c, &req.NewInputs)...)
	return infer.CheckResponse[*Config]{Inputs: &c, Failures: failures}, nil
}

// A nil bag means Configure's entirely known inputs. Unknown fields are never
// mistaken for missing required values by cross-field validation.
func validateConfig(c Config, bag *property.Map) []p.CheckFailure {
	var failures []p.CheckFailure
	known := func(key string) bool { return bag == nil || !bag.Get(key).IsComputed() }
	fail := func(key, reason string) { failures = append(failures, p.CheckFailure{Property: key, Reason: reason}) }
	if c.Federation == nil || !known("federation") {
		return failures
	}
	if known("authToken") && c.AuthToken != "" {
		fail("authToken", "authToken and federation are mutually exclusive")
	}
	if known("workspaceId") && c.WorkspaceID == "" {
		fail("workspaceId", "workspaceId is required with federation (use default for the default workspace)")
	}
	fieldKnown := func(key string) bool {
		if bag == nil {
			return true
		}
		v := bag.Get("federation")
		return !v.IsMap() || !v.AsMap().Get(key).IsComputed()
	}
	if fieldKnown("organizationId") && c.Federation.OrganizationID == "" {
		fail("federation.organizationId", "must not be empty")
	}
	if fieldKnown("ruleId") && !strings.HasPrefix(c.Federation.RuleID, "fdrl_") {
		fail("federation.ruleId", "expected a federation rule ID with fdrl_ prefix")
	}
	if fieldKnown("identityToken") {
		var raw *property.Value
		if bag != nil && bag.Get("federation").IsMap() {
			value := bag.Get("federation").AsMap().Get("identityToken")
			raw = &value
		}
		failures = append(failures, validateIdentityToken(c.Federation.IdentityToken, raw)...)
	}
	if fieldKnown("serviceAccountId") && c.Federation.ServiceAccountID != "" && !strings.HasPrefix(c.Federation.ServiceAccountID, "svac_") {
		fail("federation.serviceAccountId", "expected a service account ID with svac_ prefix")
	}
	return failures
}

func (*Config) Diff(_ context.Context, req infer.DiffRequest[*Config, *Config]) (infer.DiffResponse, error) {
	old, next := *req.State, *req.Inputs
	// Check normalizes normally. Normalize here too for old/un-normalized state.
	if base, err := normalizeBaseURL(old.BaseURL); err == nil {
		old.BaseURL = base
	}
	if base, err := normalizeBaseURL(next.BaseURL); err == nil {
		next.BaseURL = base
	}
	details := map[string]p.PropertyDiff{}
	add := func(key string, a, b any, replace bool) {
		if reflect.DeepEqual(a, b) {
			return
		}
		kind := p.Update
		if replace {
			kind = p.UpdateReplace
		}
		details[key] = p.PropertyDiff{Kind: kind, InputDiff: true}
	}
	add("baseUrl", old.BaseURL, next.BaseURL, true)
	// Workspace selection affects only part of this provider's resource surface.
	// Do not replace organization-scoped resources when that selection changes.
	add("workspaceId", old.WorkspaceID, next.WorkspaceID, false)
	// Credentials and federation settings update without provider replacement.
	add("apiKey", old.APIKey, next.APIKey, false)
	add("adminKey", old.AdminKey, next.AdminKey, false)
	add("authToken", old.AuthToken, next.AuthToken, false)
	add("federation", old.Federation, next.Federation, false)
	return infer.DiffResponse{HasChanges: len(details) != 0, DetailedDiff: details}, nil
}

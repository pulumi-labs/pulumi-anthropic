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

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// FederationIssuer is an OIDC identity provider whose tokens workloads exchange for
// Anthropic access tokens.
type FederationIssuer struct{}

// NewFederationIssuer returns the complete FederationIssuer resource.
func NewFederationIssuer() infer.InferredResource {
	return federationIssuerResource{infer.Resource(&FederationIssuer{})}
}

func (f *FederationIssuer) Annotate(a infer.Annotator) {
	a.Describe(f, "A workload identity federation issuer: an OIDC identity provider, such as "+
		"GitHub Actions, a Kubernetes cluster, or a cloud's workload identity service, whose "+
		"signed tokens workloads exchange for short-lived Anthropic access tokens. Which tokens "+
		"are accepted, and which service account they act as, is decided by `FederationRule`s.\n\n"+
		"Managing federation requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`); Admin API keys are not accepted. Issuers backing rules with "+
		"scopes other than `workspace:developer` or `workspace:inference` must be updated in the Console. "+
		"Creation registers trust configuration; it does not wait for signing-key polling or verify token exchange.\n\n"+
		"Optional settings omitted from the start use platform defaults at creation; later changes to those settings outside this resource are preserved until you configure them. Import by issuer ID recovers the current settings.\n\n"+
		"The API cannot delete issuers, so deleting this resource archives the issuer. Archiving "+
		"is refused while a live rule references the issuer.")
}

type FederationIssuerArgs struct {
	Name                  string `pulumi:"name"`
	IssuerURL             string `pulumi:"issuerUrl"`
	CheckJti              *bool  `pulumi:"checkJti,optional"`
	MaxJwtLifetimeSeconds *int   `pulumi:"maxJwtLifetimeSeconds,optional"`
	Jwks                  *Jwks  `pulumi:"jwks,optional"`
}

func (f *FederationIssuerArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.Name, "A lowercase slug that uniquely names the issuer in the organization, "+
		"1 to 255 lowercase letters, digits or hyphens. Renaming updates the existing issuer.")
	a.Describe(&f.IssuerURL, "The exact `iss` claim of accepted tokens, including case and trailing slashes. In discovery mode without a discovery base, this must be a public HTTPS URL with no credentials, query or fragment.")
	a.Describe(&f.CheckJti, "Whether to reject tokens whose `jti` claim has been seen before. "+
		"Tokens without a `jti` claim are still accepted. Defaults to `true`; removing a previously configured value restores `true`.")
	a.Describe(&f.MaxJwtLifetimeSeconds, "The longest lifetime, in seconds, a token may have "+
		"to be accepted, between 1 and 176,400. Tokens must carry both `iat` and `exp`. Defaults to 3,600; removing a previously configured value restores 3,600.")
	a.Describe(&f.Jwks, "Where the issuer's signing keys come from. Defaults to OIDC discovery "+
		"from the issuer URL. Removing a previously configured key source restores discovery from the issuer URL with system TLS trust. Changing the key source replaces its entire configuration; removed overrides do not carry forward.")
}

type FederationIssuerState struct {
	FederationIssuerArgs
	PollStatus            *PollStatus `pulumi:"pollStatus,optional"`
	JwksPollingDisabledAt *string     `pulumi:"jwksPollingDisabledAt,optional"`
	CreatedAt             string      `pulumi:"createdAt"`
	UpdatedAt             string      `pulumi:"updatedAt"`
}

func (f *FederationIssuerState) Annotate(a infer.Annotator) {
	a.Describe(&f.PollStatus, "The health of signing-key polling, when available. Usually absent for inline keys or before polling starts.")
	a.Describe(&f.JwksPollingDisabledAt, "When signing-key polling was automatically paused after repeated failures, in RFC 3339 format. After repairing the key endpoint, resume polling outside this resource using the Console or the issuer update API; this resource does not automatically resume it.")
	a.Describe(&f.CreatedAt, "When the issuer was created, in RFC 3339 format.")
	a.Describe(&f.UpdatedAt, "When the issuer was last updated, in RFC 3339 format.")
}

// JwksType discriminates the sources of an issuer's signing keys.
type JwksType string

func (JwksType) Values() []infer.EnumValue[JwksType] {
	return []infer.EnumValue[JwksType]{
		{Value: "discovery", Description: "Discover the keys through the issuer's OIDC discovery document."},
		{Value: "explicit_url", Description: "Fetch the keys from an explicit JWKS URL."},
		{Value: "inline", Description: "Use keys supplied inline, for issuers that are not reachable."},
	}
}

// Jwks is where an issuer's signing keys come from. Which fields apply depends on the type.
type Jwks struct {
	Type          JwksType         `pulumi:"type"`
	CaCertPem     *string          `pulumi:"caCertPem,optional"`
	DiscoveryBase *string          `pulumi:"discoveryBase,optional"`
	URL           *string          `pulumi:"url,optional"`
	Keys          []map[string]any `pulumi:"keys,optional"`
}

func (j *Jwks) Annotate(a infer.Annotator) {
	a.Describe(j, "The complete source configuration for public signing keys. Only fields belonging to the selected type may be configured.")
	a.Describe(&j.Type, "The source of the signing keys.")
	a.Describe(&j.CaCertPem, "A PEM-encoded CA certificate to trust when fetching the keys, up to "+
		"8,192 characters. For `discovery` and `explicit_url`.")
	a.Describe(&j.DiscoveryBase, "The base URL of the OIDC discovery document, when it differs "+
		"from the issuer URL. For `discovery`; must be a public HTTPS URL with no credentials, query or fragment. Removing it returns discovery to the issuer URL.")
	a.Describe(&j.URL, "The public HTTPS JWKS URL, without credentials, query or fragment. Required for `explicit_url`.")
	a.Describe(&j.Keys, "A nonempty array of public signing keys as JSON Web Keys. Required for `inline`; private key material and symmetric keys are not accepted.")
}

// PollStatus reports the health of the platform's polling of an issuer's signing keys.
type PollStatus struct {
	ConsecutiveFailures int     `pulumi:"consecutiveFailures"`
	LastFetchedAt       *string `pulumi:"lastFetchedAt,optional"`
	NextPollAt          *string `pulumi:"nextPollAt,optional"`
}

func (p *PollStatus) Annotate(a infer.Annotator) {
	a.Describe(p, "The health and schedule of automatic signing-key polling, when available.")
	a.Describe(&p.ConsecutiveFailures, "How many polls in a row have failed.")
	a.Describe(&p.LastFetchedAt, "When the keys were last fetched, in RFC 3339 format.")
	a.Describe(&p.NextPollAt, "When the keys will next be fetched, in RFC 3339 format.")
}

func (controller *FederationIssuer) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[FederationIssuerArgs], error) {
	in, failures, err := infer.DefaultCheck[FederationIssuerArgs](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[FederationIssuerArgs]{}, err
	}
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	m := req.NewInputs
	if v := m.Get("name"); v.IsString() && !regexp.MustCompile(`^[a-z0-9-]{1,255}$`).MatchString(v.AsString()) {
		fail("name", "must contain 1–255 lowercase letters, digits or hyphens")
	}
	if v := m.Get("issuerUrl"); v.IsString() && v.AsString() == "" {
		fail("issuerUrl", "must not be empty")
	}
	if v := m.Get("maxJwtLifetimeSeconds"); v.IsNumber() && (v.AsNumber() < 1 || v.AsNumber() > 176400 || math.Trunc(v.AsNumber()) != v.AsNumber()) {
		fail("maxJwtLifetimeSeconds", "must be an integer between 1 and 176400")
	}
	j := m.Get("jwks")
	if j.IsNull() {
		issuerFetchedURL(m.Get("issuerUrl"), "issuerUrl", fail)
	}
	if j.IsMap() {
		v := j.AsMap()
		typ := v.Get("type")
		for _, k := range []string{"caCertPem", "discoveryBase", "url"} {
			if x := v.Get(k); x.IsString() && k == "caCertPem" && utf8.RuneCountInString(x.AsString()) > 8192 {
				fail("jwks."+k, "must not exceed 8192 characters")
			}
		}
		if typ.IsString() {
			disallow := func(keys ...string) {
				for _, k := range keys {
					if x := v.Get(k); !x.IsNull() && !x.IsComputed() {
						fail("jwks."+k, "not supported for "+typ.AsString())
					}
				}
			}
			switch typ.AsString() {
			case "discovery":
				disallow("url", "keys")
				if v.Get("discoveryBase").IsNull() {
					issuerFetchedURL(m.Get("issuerUrl"), "issuerUrl", fail)
				} else {
					issuerFetchedURL(v.Get("discoveryBase"), "jwks.discoveryBase", fail)
				}
			case "explicit_url":
				disallow("discoveryBase", "keys")
				issuerFetchedURL(v.Get("url"), "jwks.url", fail)
			case "inline":
				disallow("url", "discoveryBase", "caCertPem")
				keys := v.Get("keys")
				if keys.IsNull() || (keys.IsArray() && keys.AsArray().Len() == 0) {
					fail("jwks.keys", "must contain public signing keys")
				}
				if keys.IsArray() {
					for i := 0; i < keys.AsArray().Len(); i++ {
						key := keys.AsArray().Get(i)
						if !key.IsMap() {
							if !key.IsComputed() {
								fail(fmt.Sprintf("jwks.keys[%d]", i), "must be a public JWK object")
							}
							continue
						}
						km := key.AsMap()
						base := fmt.Sprintf("jwks.keys[%d]", i)
						for _, k := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
							if !km.Get(k).IsNull() {
								fail(base+"."+k, "private or symmetric key material is not accepted")
							}
						}
						kty := km.Get("kty")
						if kty.IsString() {
							required := []string{}
							switch kty.AsString() {
							case "RSA":
								required = []string{"n", "e"}
							case "EC":
								required = []string{"crv", "x", "y"}
							case "OKP":
								required = []string{"crv", "x"}
							default:
								fail(base+".kty", "must be RSA, EC or OKP")
							}
							for _, k := range required {
								x := km.Get(k)
								if !x.IsComputed() && (!x.IsString() || x.AsString() == "") {
									fail(base+"."+k, "required public key member")
								}
							}
						} else if !kty.IsComputed() {
							fail(base+".kty", "must be a string key type")
						}
					}
				}
			default:
				fail("jwks.type", "must be discovery, explicit_url or inline")
			}
		}
	}
	return infer.CheckResponse[FederationIssuerArgs]{Inputs: in, Failures: failures}, nil
}

func (controller *FederationIssuer) Create(ctx context.Context, req infer.CreateRequest[FederationIssuerArgs]) (infer.CreateResponse[FederationIssuerState], error) {
	if req.DryRun {
		// Non-nil observation placeholders let infer mark optional fields unknown.
		return infer.CreateResponse[FederationIssuerState]{Output: FederationIssuerState{FederationIssuerArgs: req.Inputs, PollStatus: &PollStatus{}, JwksPollingDisabledAt: new(string)}}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.CreateResponse[FederationIssuerState]{}, err
	}
	u := issuerUpdateParams(req.Inputs, FederationIssuerState{})
	r, err := c.Organization.Federation.Issuers.New(ctx, sdk.OrganizationFederationIssuerNewParams{Name: req.Inputs.Name, IssuerURL: req.Inputs.IssuerURL, CheckJTI: u.CheckJTI, MaxJWTLifetimeSeconds: u.MaxJWTLifetimeSeconds, JWKS: sdk.OrganizationFederationIssuerNewParamsJWKSUnion{OfDiscovery: u.JWKS.OfDiscovery, OfExplicitURL: u.JWKS.OfExplicitURL, OfInline: u.JWKS.OfInline}})
	if err != nil {
		return infer.CreateResponse[FederationIssuerState]{}, err
	}
	out, id, err := issuerObserved(r, "", req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[FederationIssuerState]{}, err
	}
	return infer.CreateResponse[FederationIssuerState]{ID: id, Output: out}, err
}

func (controller *FederationIssuer) Read(ctx context.Context, req infer.ReadRequest[FederationIssuerArgs, FederationIssuerState]) (infer.ReadResponse[FederationIssuerArgs, FederationIssuerState], error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{}, err
	}
	r, err := c.Organization.Federation.Issuers.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{}, err
	}
	if r != nil && r.ID == req.ID && issuerIdentity(r, req.ID) && apiutil.JSONString(r.JSON.ArchivedAt.Raw()) && !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{}, nil
	}
	out, id, err := issuerObserved(r, req.ID, req.Inputs, req.Inputs.Name == "")
	if err != nil {
		return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{}, err
	}
	return infer.ReadResponse[FederationIssuerArgs, FederationIssuerState]{ID: id, Inputs: out.FederationIssuerArgs, State: out}, err
}

func (controller *FederationIssuer) Update(ctx context.Context, req infer.UpdateRequest[FederationIssuerArgs, FederationIssuerState]) (infer.UpdateResponse[FederationIssuerState], error) {
	if req.DryRun {
		out := req.State
		out.FederationIssuerArgs = req.Inputs
		return infer.UpdateResponse[FederationIssuerState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.UpdateResponse[FederationIssuerState]{}, err
	}
	r, err := c.Organization.Federation.Issuers.Update(ctx, req.ID, issuerUpdateParams(req.Inputs, req.State))
	if err != nil {
		return infer.UpdateResponse[FederationIssuerState]{}, err
	}
	out, _, err := issuerObserved(r, req.ID, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[FederationIssuerState]{}, err
	}
	return infer.UpdateResponse[FederationIssuerState]{Output: out}, err
}

func (controller *FederationIssuer) Delete(ctx context.Context, req infer.DeleteRequest[FederationIssuerState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Federation.Issuers.Archive(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve exact input metadata in checks.
type federationIssuerResource struct{ infer.InferredResource }

func (r federationIssuerResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&FederationIssuer{}).Check(ctx, req)
	// No normalization: preserve exact values, secrets and dependencies, including
	// wrapped unknowns which infer's typed representation cannot represent.
	if err == nil && len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

func (r federationIssuerResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&FederationIssuer{}).Diff(ctx, req)
}

func (r federationIssuerResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	out, err := infer.Resource(&FederationIssuer{}).Create(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = issuerPreviewUnknowns(out.Properties, req.Properties)
	}
	return out, err
}

func (r federationIssuerResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&FederationIssuer{}).Read(ctx, req)
}

func (r federationIssuerResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	out, err := infer.Resource(&FederationIssuer{}).Update(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = issuerPreviewUnknowns(out.Properties, req.Inputs)
	}
	return out, err
}

func (r federationIssuerResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&FederationIssuer{}).Delete(ctx, req)
}

// Infer's dynamic JWK map encoding can move unknowns between key members or
// lose nested secrecy when collapsing the object. Conservatively mark unresolved
// JWKS unknown as a whole, protecting any secret descendant. This never copies
// input values or dependency annotations; known JWKS outputs use infer encoding.
func issuerPreviewUnknowns(outputs, inputs property.Map) property.Map {
	jwks := inputs.Get("jwks")
	if jwks.HasComputed() {
		value := property.New(property.Computed).WithSecret(jwks.HasSecrets() || outputs.Get("jwks").HasSecrets())
		return outputs.Set("jwks", value)
	}
	return outputs
}

// Only statically reject clearly nonpublic destinations; Check never resolves DNS.
func issuerFetchedURL(v property.Value, path string, fail func(string, string)) {
	if v.IsComputed() {
		return
	}
	if !v.IsString() {
		if v.IsNull() {
			fail(path, "a public HTTPS URL is required")
		}
		return
	}
	u, err := url.Parse(v.AsString())
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fail(path, "must be a public HTTPS URL without credentials, query or fragment")
		return
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	ip := net.ParseIP(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast())) {
		fail(path, "must address a public host")
	}
}

func issuerJWKS(j *Jwks) sdk.OrganizationFederationIssuerUpdateParamsJWKSUnion {
	var out sdk.OrganizationFederationIssuerUpdateParamsJWKSUnion
	if j == nil {
		return out
	}
	switch j.Type {
	case "discovery":
		v := &sdk.JWKSDiscoveryParam{}
		if j.CaCertPem != nil {
			v.CACertPEM = sdk.String(*j.CaCertPem)
		}
		if j.DiscoveryBase != nil {
			v.DiscoveryBase = sdk.String(*j.DiscoveryBase)
		}
		out.OfDiscovery = v
	case "explicit_url":
		v := &sdk.JWKSExplicitURLParam{}
		if j.URL != nil {
			v.URL = *j.URL
		}
		if j.CaCertPem != nil {
			v.CACertPEM = sdk.String(*j.CaCertPem)
		}
		out.OfExplicitURL = v
	case "inline":
		out.OfInline = &sdk.JWKSInlineParam{Keys: j.Keys}
	}
	return out
}

func issuerUpdateParams(in FederationIssuerArgs, old FederationIssuerState) sdk.OrganizationFederationIssuerUpdateParams {
	out := sdk.OrganizationFederationIssuerUpdateParams{Name: sdk.String(in.Name), IssuerURL: sdk.String(in.IssuerURL), JWKS: issuerJWKS(in.Jwks)}
	if in.CheckJti != nil {
		out.CheckJTI = sdk.Bool(*in.CheckJti)
	} else if old.CheckJti != nil {
		out.CheckJTI = sdk.Bool(true)
	}
	if in.MaxJwtLifetimeSeconds != nil {
		out.MaxJWTLifetimeSeconds = sdk.Int(int64(*in.MaxJwtLifetimeSeconds))
	} else if old.MaxJwtLifetimeSeconds != nil {
		out.MaxJWTLifetimeSeconds = sdk.Int(3600)
	}
	if in.Jwks == nil && old.Jwks != nil {
		out.JWKS = issuerJWKS(&Jwks{Type: "discovery"})
	}
	return out
}

func issuerIdentity(r *sdk.FederationIssuer, expected string) bool {
	return r != nil && apiutil.JSONString(r.JSON.ID.Raw()) && r.ID != "" && (expected == "" || expected == r.ID) && (r.JSON.Type.Raw() == "" || r.JSON.Type.Raw() == `"federation_issuer"`)
}

func issuerTime(raw string, v time.Time) (*string, error) {
	if raw == "null" || raw == "" {
		return nil, nil
	}
	if !apiutil.JSONString(raw) || v.IsZero() {
		return nil, errors.New("invalid issuer timestamp")
	}
	s := v.Format(time.RFC3339Nano)
	return &s, nil
}

// Validate wire shapes before returning server facts.
func issuerObserved(r *sdk.FederationIssuer, expected string, accepted FederationIssuerArgs, importing bool) (FederationIssuerState, string, error) {
	out := FederationIssuerState{}
	if !issuerIdentity(r, expected) {
		return out, "", errors.New("invalid federation issuer identity or type")
	}
	var invalid error
	bad := func(field string) { invalid = errors.Join(invalid, fmt.Errorf("invalid or missing issuer %s", field)) }
	if apiutil.JSONString(r.JSON.Name.Raw()) && r.Name != "" {
		out.Name = r.Name
	} else {
		bad("name")
	}
	if apiutil.JSONString(r.JSON.IssuerURL.Raw()) && r.IssuerURL != "" {
		out.IssuerURL = r.IssuerURL
	} else {
		bad("issuer_url")
	}
	if importing || accepted.CheckJti != nil {
		if apiutil.JSONBool(r.JSON.CheckJTI.Raw()) {
			v := r.CheckJTI
			out.CheckJti = &v
		} else {
			bad("check_jti")
		}
	}
	if importing || accepted.MaxJwtLifetimeSeconds != nil {
		if apiutil.JSONInt64(r.JSON.MaxJWTLifetimeSeconds.Raw()) && r.MaxJWTLifetimeSeconds >= 1 && r.MaxJWTLifetimeSeconds <= 176400 {
			v := int(r.MaxJWTLifetimeSeconds)
			out.MaxJwtLifetimeSeconds = &v
		} else {
			bad("max_jwt_lifetime_seconds")
		}
	}
	if importing || accepted.Jwks != nil {
		j := r.JWKS
		value := &Jwks{Type: JwksType(j.Type)}
		valid := apiutil.JSONString(j.JSON.Type.Raw())
		optional := func(raw, s string) *string {
			if raw == "" || raw == "null" {
				return nil
			}
			if !apiutil.JSONString(raw) {
				valid = false
				return nil
			}
			v := s
			return &v
		}
		switch j.Type {
		case "discovery":
			value.CaCertPem = optional(j.JSON.CACertPEM.Raw(), j.CACertPEM)
			value.DiscoveryBase = optional(j.JSON.DiscoveryBase.Raw(), j.DiscoveryBase)
		case "explicit_url":
			value.CaCertPem = optional(j.JSON.CACertPEM.Raw(), j.CACertPEM)
			value.URL = optional(j.JSON.URL.Raw(), j.URL)
			valid = valid && value.URL != nil && *value.URL != ""
		case "inline":
			var keys []map[string]any
			raw := j.JSON.Keys.Raw()
			valid = valid && raw != "" && raw != "null" && json.Unmarshal([]byte(raw), &keys) == nil && len(keys) > 0
			value.Keys = j.Keys
			for _, key := range keys {
				for _, member := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
					if key[member] != nil {
						valid = false
					}
				}
				var required []string
				switch key["kty"] {
				case "RSA":
					required = []string{"n", "e"}
				case "EC":
					required = []string{"crv", "x", "y"}
				case "OKP":
					required = []string{"crv", "x"}
				default:
					valid = false
				}
				for _, member := range required {
					s, ok := key[member].(string)
					if !ok || s == "" {
						valid = false
					}
				}
			}
		default:
			valid = false
		}
		if valid {
			out.Jwks = value
		} else {
			bad("jwks")
		}
	}
	for _, v := range []struct {
		key, raw string
		value    time.Time
		dest     *string
	}{{"createdAt", r.JSON.CreatedAt.Raw(), r.CreatedAt, &out.CreatedAt}, {"updatedAt", r.JSON.UpdatedAt.Raw(), r.UpdatedAt, &out.UpdatedAt}} {
		t, err := issuerTime(v.raw, v.value)
		if err != nil || t == nil {
			bad(v.key)
		} else {
			*v.dest = *t
		}
	}
	if t, err := issuerTime(r.JSON.JWKSPollingDisabledAt.Raw(), r.JWKSPollingDisabledAt); err != nil {
		bad("jwksPollingDisabledAt")
	} else if t != nil {
		out.JwksPollingDisabledAt = t
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "" && raw != "null" {
		if _, err := issuerTime(raw, r.ArchivedAt); err != nil {
			bad("archived_at")
		} else {
			bad("archived issuer")
		}
	}
	if raw := r.JSON.PollStatus.Raw(); raw != "" && raw != "null" {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
			bad("poll_status")
		} else {
			out.PollStatus = &PollStatus{}
			poll := r.PollStatus
			if apiutil.JSONInt64(poll.JSON.ConsecutiveFailures.Raw()) && poll.ConsecutiveFailures >= 0 {
				out.PollStatus.ConsecutiveFailures = int(poll.ConsecutiveFailures)
			} else {
				bad("poll_status.consecutive_failures")
			}
			for _, v := range []struct {
				key, raw string
				value    time.Time
				dest     **string
			}{{"lastFetchedAt", poll.JSON.LastFetchedAt.Raw(), poll.LastFetchedAt, &out.PollStatus.LastFetchedAt}, {"nextPollAt", poll.JSON.NextPollAt.Raw(), poll.NextPollAt, &out.PollStatus.NextPollAt}} {
				t, err := issuerTime(v.raw, v.value)
				if err != nil {
					bad("poll_status." + v.key)
				} else if t != nil {
					*v.dest = t
				}
			}
		}
	}
	return out, r.ID, invalid
}

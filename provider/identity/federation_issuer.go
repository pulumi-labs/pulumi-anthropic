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
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// FederationIssuer is an OIDC identity provider whose tokens workloads exchange for
// Anthropic access tokens.
type FederationIssuer struct {
	rest.Resource[FederationIssuerArgs, FederationIssuerState, *FederationIssuerState]
}

// NewFederationIssuer returns the FederationIssuer resource controller.
func NewFederationIssuer() *FederationIssuer {
	return &FederationIssuer{rest.Resource[FederationIssuerArgs, FederationIssuerState, *FederationIssuerState]{
		Collection: federationIssuerCollection,
		EndOfLife:  rest.Archive,
	}}
}

func (f *FederationIssuer) Annotate(a infer.Annotator) {
	a.Describe(f, "A workload identity federation issuer: an OIDC identity provider, such as "+
		"GitHub Actions, a Kubernetes cluster, or a cloud's workload identity service, whose "+
		"signed tokens workloads exchange for short-lived Anthropic access tokens. Which tokens "+
		"are accepted, and which service account they act as, is decided by `FederationRule`s.\n\n"+
		"Managing federation requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`).\n\n"+
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
		"1 to 255 characters.")
	a.Describe(&f.IssuerURL, "The issuer URL, which must match the `iss` claim of accepted tokens.")
	a.Describe(&f.CheckJti, "Whether to reject tokens whose `jti` claim has been seen before. "+
		"Defaults to `true`.")
	a.Describe(&f.MaxJwtLifetimeSeconds, "The longest lifetime, in seconds, a token may have "+
		"to be accepted, between 1 and 176,400. Defaults to 3,600.")
	a.Describe(&f.Jwks, "Where the issuer's signing keys come from. Defaults to OIDC discovery "+
		"from the issuer URL.")
}

type FederationIssuerState struct {
	FederationIssuerArgs
	PollStatus *PollStatus `pulumi:"pollStatus,optional"`
	CreatedAt  string      `pulumi:"createdAt"`
	UpdatedAt  string      `pulumi:"updatedAt"`
}

func (f *FederationIssuerState) Inputs() *FederationIssuerArgs { return &f.FederationIssuerArgs }

func (f *FederationIssuerState) Annotate(a infer.Annotator) {
	a.Describe(&f.PollStatus, "The health of the platform's polling of the issuer's signing keys.")
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
	a.Describe(&j.Type, "The source of the signing keys.")
	a.Describe(&j.CaCertPem, "A PEM-encoded CA certificate to trust when fetching the keys, up to "+
		"8,192 characters. For `discovery` and `explicit_url`.")
	a.Describe(&j.DiscoveryBase, "The base URL of the OIDC discovery document, when it differs "+
		"from the issuer URL. For `discovery`.")
	a.Describe(&j.URL, "The JWKS URL. Required for `explicit_url`.")
	a.Describe(&j.Keys, "The signing keys, as JSON Web Keys. Required for `inline`.")
}

// PollStatus reports the health of the platform's polling of an issuer's signing keys.
type PollStatus struct {
	ConsecutiveFailures int     `pulumi:"consecutiveFailures"`
	LastFetchedAt       *string `pulumi:"lastFetchedAt,optional"`
	NextPollAt          *string `pulumi:"nextPollAt,optional"`
}

func (p *PollStatus) Annotate(a infer.Annotator) {
	a.Describe(&p.ConsecutiveFailures, "How many polls in a row have failed.")
	a.Describe(&p.LastFetchedAt, "When the keys were last fetched, in RFC 3339 format.")
	a.Describe(&p.NextPollAt, "When the keys will next be fetched, in RFC 3339 format.")
}

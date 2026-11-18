// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const certificateTestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func certificateTestPEM(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("testdata/tunnel-ca.pem")
	require.NoError(t, e)
	return string(b)
}

func certificateObject() fakeapi.Object {
	return fakeapi.Object{"id": "tcrt_a", "tunnel_id": "tnl_a", "type": "tunnel_certificate", "archived_at": nil, "created_at": "2026-11-01T00:00:00Z", "expires_at": nil, "fingerprint": certificateTestFingerprint}
}

func certificateInputs(t *testing.T) property.Map {
	return providertest.Props(map[string]any{"tunnelId": "tnl_a", "caCertificatePem": certificateTestPEM(t)})
}

func TestTunnelCertificateCheckMetadata(t *testing.T) {
	s, api := providertest.New(t)
	for _, v := range []property.Value{property.New(property.Computed), property.New(certificateTestPEM(t)), property.New(certificateTestPEM(t)).WithSecret(true), property.New(certificateTestPEM(t)).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::x:y:Z::dep"})} {
		inputs := property.NewMap(map[string]property.Value{"tunnelId": property.New("tnl_a"), "caCertificatePem": v})
		before := inputs
		c, e := s.Check(p.CheckRequest{Urn: tunnelCertificateURN, Inputs: inputs})
		require.NoError(t, e)
		require.Empty(t, c.Failures)
		require.Equal(t, inputs, c.Inputs)
		require.Equal(t, before, inputs)
		c2, e := s.Check(p.CheckRequest{Urn: tunnelCertificateURN, Inputs: c.Inputs})
		require.NoError(t, e)
		require.Equal(t, c.Inputs, c2.Inputs)
		preview, e := s.Create(p.CreateRequest{Urn: tunnelCertificateURN, Properties: c.Inputs, DryRun: true})
		require.NoError(t, e)
		requireOutputValue(t, v, preview.Properties.Get("caCertificatePem"))
		require.True(t, preview.Properties.Get("fingerprint").IsComputed())
	}
	for _, bad := range []string{"", "not PEM", strings.Repeat("x", 8193), certificateTestPEM(t) + certificateTestPEM(t), certificateTestPEM(t) + "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----", "-----BEGIN CERTIFICATE-----\nignored material\n" + certificateTestPEM(t)} {
		c, e := s.Check(p.CheckRequest{Urn: tunnelCertificateURN, Inputs: providertest.Props(map[string]any{"tunnelId": "tnl_a", "caCertificatePem": bad})})
		require.NoError(t, e)
		require.NotEmpty(t, c.Failures)
		require.Equal(t, "caCertificatePem", c.Failures[0].Property)
	}
	require.Empty(t, api.Requests())
}

func TestTunnelCertificateReadPreservesWriteOnlyPEM(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/tunnels/tnl_a/certificates", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, certificateObject())
	})
	api.Handle("GET /v1/tunnels/tnl_a/certificates/tcrt_a", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, certificateObject())
	})
	created, inputs := providertest.Create(t, s, tunnelCertificateURN, certificateInputs(t))
	require.Equal(t, "tnl_a/tcrt_a", created.ID)
	read := providertest.Read(t, s, tunnelCertificateURN, created, inputs)
	require.Equal(t, inputs, read.Inputs)
	read2 := providertest.Read(t, s, tunnelCertificateURN, created, inputs)
	require.Equal(t, read, read2)
	diff, e := s.Diff(p.DiffRequest{Urn: tunnelCertificateURN, ID: created.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, e)
	require.False(t, diff.HasChanges)
	imported, e := s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: created.ID})
	require.NoError(t, e)
	require.Equal(t, "tnl_a", imported.Inputs.Get("tunnelId").AsString())
	require.True(t, imported.Inputs.Get("caCertificatePem").IsNull())
	require.True(t, imported.Properties.Get("caCertificatePem").IsNull())
	require.True(t, imported.Properties.Get("expiresAt").IsNull())
	require.Equal(t, "active", imported.Properties.Get("status").AsString())
	for _, key := range []string{"tunnelId", "caCertificatePem"} {
		m := map[string]any{"tunnelId": "tnl_a", "caCertificatePem": certificateTestPEM(t)}
		m[key] = m[key].(string) + "different"
		diff, e := s.Diff(p.DiffRequest{Urn: tunnelCertificateURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: providertest.Props(m)})
		require.NoError(t, e)
		require.Contains(t, []p.DiffKind{p.UpdateReplace, p.AddReplace, p.DeleteReplace}, diff.DetailedDiff[key].Kind)
		require.False(t, diff.DeleteBeforeReplace)
	}
}

func TestTunnelCertificateMalformedCreateOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/tunnels/tnl_a/certificates", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		o := certificateObject()
		o["fingerprint"] = map[string]any{"bad": "shape"}
		fakeapi.WriteJSON(w, o)
	})
	inputs := certificateInputs(t)
	created, e := s.Create(p.CreateRequest{Urn: tunnelCertificateURN, Properties: inputs})
	require.Error(t, e)
	var initFailed infer.ResourceInitFailedError
	require.NotErrorAs(t, e, &initFailed)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	require.Len(t, api.Requests(), 1)
	api.Handle("GET /v1/tunnels/tnl_a/certificates/tcrt_a", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, certificateObject())
	})
	read, e := s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a", Inputs: inputs, Properties: inputs})
	require.NoError(t, e)
	_, e = s.Update(p.UpdateRequest{Urn: tunnelCertificateURN, ID: read.ID, State: read.Properties, Inputs: inputs})
	require.NoError(t, e)
}

func TestTunnelCertificateUpdateHasNoObservationGate(t *testing.T) {
	s, api := providertest.New(t)
	inputs := certificateInputs(t)
	// Optional observations may be absent in legacy saved state. Update has no
	// platform operation and must not fetch them or demand readiness.
	state := inputs.Set("fingerprint", property.New(certificateTestFingerprint)).Set("createdAt", property.New("2026-11-01T00:00:00Z"))
	out, err := s.Update(p.UpdateRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a", State: state, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, out.PartialState)
	require.Equal(t, state, out.Properties)
	require.Empty(t, api.Requests())
}

func TestTunnelCertificateIdentityRetirement(t *testing.T) {
	s, api := providertest.New(t)
	path := "/v1/tunnels/tnl_a/certificates/tcrt_a"
	obj := certificateObject()
	status := 200
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		if status != 200 {
			fakeapi.WriteError(w, status, "synthetic error")
			return
		}
		fakeapi.WriteJSON(w, obj)
	})
	for _, bad := range []string{"", "tnl_a", "tnl_a/", "/tcrt_a", "tnl_a/tcrt_a/extra"} {
		_, e := s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: bad})
		require.Error(t, e)
	}
	require.Empty(t, api.Requests())
	obj["tunnel_id"] = "tnl_foreign"
	_, e := s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a"})
	require.Error(t, e)
	obj = certificateObject()
	obj["id"] = "tcrt_foreign"
	_, e = s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a"})
	require.Error(t, e)
	obj = certificateObject()
	obj["archived_at"] = "2026-11-02T00:00:00Z"
	r, e := s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a"})
	require.NoError(t, e)
	require.Empty(t, r.ID)
	status = http.StatusForbidden
	_, e = s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a"})
	require.Error(t, e)
	status = http.StatusNotFound
	r, e = s.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a"})
	require.NoError(t, e)
	require.Empty(t, r.ID)
	api.Handle("POST "+path+"/archive", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, certificateObject())
	})
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a", Properties: providertest.Props(map[string]any{"tunnelId": "tnl_a", "fingerprint": certificateTestFingerprint, "createdAt": "2026-11-01T00:00:00Z", "expiresAt": ""})}))
	require.Equal(t, path+"/archive", api.Last().Path)
	require.Equal(t, "mcp-tunnels-2026-06-22", api.Last().Header.Get("anthropic-beta"))
}

const tunnelCertificateToken = "anthropic:agents:TunnelCertificate"

var tunnelCertificateURN = providertest.URN(tunnelCertificateToken)

func TestTunnelCertificateLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	tunnelServerBehavior(api)

	pem := certificateTestPEM(t)
	api.SetHooks("certificates", fakeapi.Hooks{OnCreate: func(o fakeapi.Object) {
		delete(o, "ca_certificate_pem")
		o["tunnel_id"] = "tnl_1"
		o["fingerprint"] = certificateTestFingerprint
		o["expires_at"] = nil
	}})
	created, inputs := providertest.Create(t, server, tunnelCertificateURN, providertest.Props(map[string]any{
		"tunnelId": "tnl_1", "caCertificatePem": pem,
	}))
	req := api.LastRequest(t, http.MethodPost, "/v1/tunnels/tnl_1/certificates")
	assert.Equal(t, "mcp-tunnels-2026-06-22", req.Header.Get("anthropic-beta"))
	assert.Equal(t, fakeapi.Object{"ca_certificate_pem": pem}, req.Body)
	require.Regexp(t, `^tnl_1/certificate_\d+$`, created.ID)
	assert.Equal(t, certificateTestFingerprint, created.Properties.Get("fingerprint").AsString())
	assert.True(t, created.Properties.Get("expiresAt").IsNull())

	refreshed := providertest.Read(t, server, tunnelCertificateURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs)
	assert.True(t, refreshed.Properties.Get("expiresAt").IsNull())

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: tunnelCertificateURN})
	require.NoError(t, err)
	assert.Equal(t, "tnl_1", imported.Inputs.Get("tunnelId").AsString())

	d := providertest.Diff(t, server, tunnelCertificateURN, created, inputs, providertest.Props(map[string]any{
		"tunnelId": "tnl_1", "caCertificatePem": pem + "\n",
	}))
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["caCertificatePem"].Kind, "certificates cannot be updated")

	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: tunnelCertificateURN,
		Properties: created.Properties}))
	parent, certificateID, ok := strings.Cut(created.ID, "/")
	require.True(t, ok)
	require.Equal(t, "tnl_1", parent)
	require.NotEmpty(t, certificateID)
	last := api.Last()
	assert.Equal(t, "/v1/tunnels/tnl_1/certificates/"+certificateID+"/archive", last.Path)
}

// Unusable allocations fail ordinarily; legacy incomplete state refreshes explicitly.
func TestTunnelCertificateUnusableAllocationThenRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		missing     bool
		status      string
	}{
		{name: "missing-expiration", field: "expires_at", missing: true},
		{name: "malformed-expiration", field: "expires_at", value: "not-a-time"},
		{name: "numeric-expiration", field: "expires_at", value: 42},
		{name: "missing-archive", field: "archived_at", missing: true},
		{name: "malformed-archive", field: "archived_at", value: "not-a-time"},
		{name: "missing-parent", field: "tunnel_id", missing: true},
		{name: "malformed-parent", field: "tunnel_id", value: 42},
		{name: "archived", field: "archived_at", value: "2026-11-02T00:00:00Z", status: "archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, api := providertest.New(t)
			api.Handle("POST /v1/tunnels/tnl_a/certificates", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				obj := certificateObject()
				if tc.missing {
					delete(obj, tc.field)
				} else {
					obj[tc.field] = tc.value
				}
				fakeapi.WriteJSON(w, obj)
			})
			inputs := certificateInputs(t)
			created, err := server.Create(p.CreateRequest{Urn: tunnelCertificateURN, Properties: inputs})
			require.Error(t, err)
			var initFailed infer.ResourceInitFailedError
			require.NotErrorAs(t, err, &initFailed)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Zero(t, created.Properties.Len())
			require.Len(t, api.Requests(), 1)
			api.Handle("GET /v1/tunnels/tnl_a/certificates/tcrt_a", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				obj := certificateObject()
				if tc.status == "archived" {
					obj["archived_at"] = tc.value
				} else {
					obj["expires_at"] = "2030-01-01T00:00:00Z"
				}
				fakeapi.WriteJSON(w, obj)
			})
			read, err := server.Read(p.ReadRequest{Urn: tunnelCertificateURN, ID: "tnl_a/tcrt_a", Inputs: inputs, Properties: inputs})
			require.NoError(t, err)
			if tc.status == "archived" {
				require.Empty(t, read.ID)
				require.Len(t, api.Requests(), 2)
				return
			}
			require.Equal(t, "2030-01-01T00:00:00Z", read.Properties.Get("expiresAt").AsString())
			require.Equal(t, "active", read.Properties.Get("status").AsString())
			_, err = server.Update(p.UpdateRequest{Urn: tunnelCertificateURN, ID: read.ID, State: read.Properties, Inputs: inputs})
			require.NoError(t, err)
			require.Len(t, api.Requests(), 2)
		})
	}
}

func TestTunnelCertificateRejectsSkippedPEMPrefixBeforeCreate(t *testing.T) {
	server, api := providertest.New(t)
	inputs := providertest.Props(map[string]any{"tunnelId": "tnl_a", "caCertificatePem": "-----BEGIN CERTIFICATE-----\nignored material\n" + certificateTestPEM(t)})
	_, err := server.Create(p.CreateRequest{Urn: tunnelCertificateURN, Properties: inputs})
	require.Error(t, err)
	require.Empty(t, api.Requests())
}

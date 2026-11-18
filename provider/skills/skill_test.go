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

package skills_test

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	"github.com/pulumi-labs/pulumi-anthropic/provider/skills"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const skillToken = "anthropic:skills:Skill"

var skillURN = resource.NewURN("test", "test", "", skillToken, "skill")

// upload is one multipart request the fake received.
type upload struct {
	Path   string
	Fields map[string]string
	Files  map[string]string // path within the skill -> contents
}

// skillServer mimics the Skills API's multipart endpoints and records each upload. The
// stored skills are then read and deleted through the fake's generic REST handling.
type skillServer struct {
	mu      sync.Mutex
	uploads []upload
}

func (s *skillServer) Uploads() []upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]upload(nil), s.uploads...)
}

func newSkillServer(t *testing.T, api *fakeapi.Server) *skillServer {
	s := &skillServer{}
	api.Handle("POST /v1/skills", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		u := s.record(t, r)
		id := api.NewID("skill")
		name := u.Fields["display_name"]
		if name == "" {
			name = "derived-from-frontmatter"
		}
		obj := fakeapi.Object{
			"id": id, "type": "skill", "display_name": name, "source": map[string]any{"type": "custom"},
			"latest_version_id": api.NewID("skver"),
			"created_at":        "2026-09-25T00:00:00Z", "updated_at": "2026-09-25T00:00:00Z",
		}
		api.Put("/v1/skills/"+id, obj)
		fakeapi.WriteJSON(w, obj)
	})
	api.Handle("POST /v1/skills/{id}/versions", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		s.record(t, r)
		path := "/v1/skills/" + r.PathValue("id")
		if api.Object(path) == nil {
			fakeapi.WriteError(w, http.StatusNotFound, "no such skill")
			return
		}
		version := api.NewID("skver")
		api.Mutate(path, func(obj fakeapi.Object) {
			obj["latest_version_id"] = version
			obj["updated_at"] = "2026-09-26T00:00:00Z"
		})
		fakeapi.WriteJSON(w, fakeapi.Object{"id": version, "type": "skill_version", "skill_id": r.PathValue("id"),
			"created_at": "2026-09-26T00:00:00Z", "name": "triage", "description": "Triage tickets"})
	})
	api.Handle("GET /v1/skills/{id}/versions/{version}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": r.PathValue("version"), "skill_id": r.PathValue("id"), "type": "skill_version", "name": "triage", "description": "Triage", "created_at": "2026-09-25T00:00:00Z"})
	})
	return s
}

// record parses a multipart upload, keeping each file's full path as sent: Go's
// FileHeader.Filename would strip the directories the API depends on.
func (s *skillServer) record(t *testing.T, r *http.Request) upload {
	u := upload{Path: r.URL.Path, Fields: map[string]string{}, Files: map[string]string{}}
	reader, err := r.MultipartReader()
	require.NoError(t, err)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		require.NoError(t, err)
		content, err := io.ReadAll(part)
		require.NoError(t, err)
		if params["name"] == "files[]" {
			u.Files[params["filename"]] = string(content)
		} else {
			u.Fields[params["name"]] = string(content)
		}
	}
	s.mu.Lock()
	s.uploads = append(s.uploads, u)
	s.mu.Unlock()
	return u
}

func archive(t *testing.T, files map[string]string) property.Value {
	assets := map[string]any{}
	for name, content := range files {
		a, err := resource.NewTextAsset(content)
		require.NoError(t, err)
		assets[name] = a
	}
	a, err := resource.NewAssetArchive(assets)
	require.NoError(t, err)
	return property.New(a)
}

var skillV1 = map[string]string{
	"SKILL.md":       "---\nname: triage\n---\nTriage incoming tickets.",
	"scripts/run.py": "print('triage')",
}

func TestSkillLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	skills := newSkillServer(t, api)

	skillV2 := map[string]string{"SKILL.md": "---\nname: triage\n---\nTriage tickets by severity."}
	var created property.Map

	integration.LifeCycleTest{
		Resource: skillToken,
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{
				"source": archive(t, skillV1), "displayName": "Ticket triage",
			}),
			Hook: func(_, output property.Map) {
				created = output
				uploads := skills.Uploads()
				require.Len(t, uploads, 1)
				assert.Equal(t, upload{
					Path:   "/v1/skills",
					Fields: map[string]string{"display_name": "Ticket triage"},
					Files:  skillV1,
				}, uploads[0], "files are sent under their paths within the skill")
				assert.Regexp(t, `^skver_`, output.Get("latestVersionId").AsString())

				req := api.LastRequest(t, http.MethodPost, "/v1/skills")
				assert.Empty(t, req.Header.Get("anthropic-beta"), "the Skills API is GA")
			},
		},
		Updates: []integration.Operation{
			{
				// The same content, freshly built, is not a change.
				Inputs: providertest.Props(map[string]any{
					"source": archive(t, skillV1), "displayName": "Ticket triage",
				}),
			},
			{
				Inputs: providertest.Props(map[string]any{
					"source": archive(t, skillV2), "displayName": "Ticket triage",
				}),
				Hook: func(_, output property.Map) {
					uploads := skills.Uploads()
					require.Len(t, uploads, 2, "an unchanged archive must not upload a version")
					assert.Regexp(t, `^/v1/skills/skill_\d+/versions$`, uploads[1].Path)
					assert.Equal(t, skillV2, uploads[1].Files)
					assert.NotEqual(t, created.Get("latestVersionId"), output.Get("latestVersionId"),
						"a new version becomes the latest")
				},
			},
		},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Regexp(t, `^/v1/skills/skill_\d+$`, last.Path, "deleting a skill deletes it and its versions")
}

func TestSkillDisplayNameReplaces(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	newSkillServer(t, api)

	created, _ := providertest.Create(t, server, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1), "displayName": "A"}))
	diff, err := server.Diff(p.DiffRequest{ID: created.ID, Urn: skillURN, State: created.Properties,
		Inputs: check(t, server, map[string]any{"source": archive(t, skillV1), "displayName": "B"})})
	require.NoError(t, err)
	assert.True(t, diff.HasChanges)
	assert.Equal(t, map[string]p.PropertyDiff{"displayName": {Kind: p.UpdateReplace, InputDiff: true}}, diff.DetailedDiff)
}

// A directory edited in place keeps its path, so only the hash recorded at upload time
// can reveal that its contents changed.
func TestSkillDirectoryEditedInPlace(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	newSkillServer(t, api)

	dir := t.TempDir()
	skillMD := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("---\nname: triage\n---\nv1"), 0o600))
	source := func() property.Value {
		a, err := resource.NewPathArchive(dir)
		require.NoError(t, err)
		return property.New(a)
	}

	created, _ := providertest.Create(t, server, skillURN, providertest.Props(map[string]any{"source": source()}))
	unchanged, err := server.Diff(p.DiffRequest{ID: created.ID, Urn: skillURN, State: created.Properties,
		Inputs: check(t, server, map[string]any{"source": source()})})
	require.NoError(t, err)
	assert.False(t, unchanged.HasChanges)

	require.NoError(t, os.WriteFile(skillMD, []byte("---\nname: triage\n---\nv2"), 0o600))
	changed, err := server.Diff(p.DiffRequest{ID: created.ID, Urn: skillURN, State: created.Properties,
		Inputs: check(t, server, map[string]any{"source": source()})})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{"source": {Kind: p.Update, InputDiff: true}}, changed.DetailedDiff)
}

func TestSkillRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	newSkillServer(t, api)

	created, inputs := providertest.Create(t, server, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
	path := "/v1/skills/" + created.ID
	api.Mutate(path, func(obj fakeapi.Object) { obj["latest_version_id"] = "skver_elsewhere" })

	read, err := server.Read(p.ReadRequest{ID: created.ID, Urn: skillURN,
		Properties: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	assert.Equal(t, "skver_elsewhere", read.Properties.Get("latestVersionId").AsString(),
		"a version uploaded outside of Pulumi is reported")
	assert.Equal(t, created.Properties.Get("source"), read.Inputs.Get("source"),
		"the declared source, which the API cannot return, is kept")
	_, hasName := read.Inputs.GetOk("displayName")
	assert.False(t, hasName, "the server's derived display name does not leak into the inputs")

	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: skillURN, Properties: created.Properties}))
	read, err = server.Read(p.ReadRequest{ID: created.ID, Urn: skillURN, Properties: created.Properties})
	require.NoError(t, err)
	assert.Empty(t, read.ID, "a deleted skill reads as gone")
}

func check(t *testing.T, server integration.Server, inputs map[string]any) property.Map {
	t.Helper()
	resp, err := server.Check(p.CheckRequest{Urn: skillURN, Inputs: providertest.Props(inputs)})
	require.NoError(t, err)
	require.Empty(t, resp.Failures)
	return resp.Inputs
}

// TestCanonicalPathArchiveFromEngine covers a FileArchive as the Pulumi engine delivers it
// over gRPC: with an empty asset map alongside its path. The path must win; reading the
// empty assets instead uploads an empty skill and drops the path from state.
func TestCanonicalPathArchiveFromEngine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o600))
	fromEngine := &resource.Archive{Sig: resource.ArchiveSig, Path: dir, Assets: map[string]any{}}

	c, err := skills.CanonicalForTest(fromEngine)
	require.NoError(t, err)
	assert.Equal(t, dir, c.Path)
	assert.Nil(t, c.Assets)

	contents, err := c.Open()
	require.NoError(t, err)
	defer func() { _ = contents.Close() }()
	name, _, err := contents.Next()
	require.NoError(t, err)
	assert.Equal(t, "SKILL.md", name)
}

func TestSkillUnknownArchiveMetadata(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			t.Run(map[bool]string{true: "known", false: "unknown"}[known]+map[bool]string{true: "secret", false: "public"}[secret], func(t *testing.T) {
				s, api := providertest.New(t)
				source := property.New(property.Computed)
				if known {
					source = archive(t, skillV1)
				}
				source = source.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::source"})
				inputs := providertest.Props(map[string]any{"source": source})
				before := inputs
				checked, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				require.Equal(t, source, checked.Inputs.Get("source"))
				again, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: checked.Inputs})
				require.NoError(t, err)
				require.Empty(t, again.Failures)
				require.Equal(t, checked.Inputs, again.Inputs)
				preview, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: checked.Inputs, DryRun: true})
				require.NoError(t, err)
				require.Equal(t, source.WithDependencies(nil), preview.Properties.Get("source"))
				assertSkillNoOutputDependencies(t, property.New(preview.Properties))
				updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: "preview", State: preview.Properties, Inputs: checked.Inputs, DryRun: true})
				require.NoError(t, err)
				require.Equal(t, source.WithDependencies(nil), updated.Properties.Get("source"))
				assertSkillNoOutputDependencies(t, property.New(updated.Properties))
				require.True(t, preview.Properties.Get("latestVersionId").IsComputed())
				require.Equal(t, before, inputs)
				require.Empty(t, api.Requests())
			})
		}
	}
}

func TestSkillUnknownLabelPreview(t *testing.T) {
	s, api := providertest.New(t)
	inputs := providertest.Props(map[string]any{"source": archive(t, skillV1), "displayName": property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::name"})})
	checked, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, inputs, checked.Inputs)
	preview, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, preview.Properties.Get("displayName").IsComputed())
	require.True(t, preview.Properties.Get("displayName").Secret())
	assertSkillNoOutputDependencies(t, property.New(preview.Properties))
	metadataOnly := checked.Inputs.Set("displayName", property.New(property.Computed))
	diff, err := s.Diff(p.DiffRequest{Urn: skillURN, ID: "preview", State: preview.Properties, Inputs: metadataOnly})
	require.NoError(t, err)
	require.False(t, diff.HasChanges, "secret/dependency metadata changes do not change the unknown label's meaning")
	require.Empty(t, api.Requests())
}

func TestSkillNameChangeReplaces(t *testing.T) {
	s, api := providertest.New(t)
	newSkillServer(t, api)
	created, _ := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
	desired := check(t, s, map[string]any{"source": archive(t, map[string]string{"SKILL.md": "---\nname: renamed\n---\nReplacement instructions."})})
	diff, err := s.Diff(p.DiffRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: desired})
	require.NoError(t, err)
	require.Equal(t, p.UpdateReplace, diff.DetailedDiff["source"].Kind)
	require.False(t, diff.DeleteBeforeReplace)
	unknown := desired.Set("source", property.New(property.Computed))
	_, err = s.Diff(p.DiffRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: unknown})
	require.NoError(t, err, "unknown archive name cannot cause validation or file reads")
}

func TestSkillDisplayNameValidation(t *testing.T) {
	s, api := providertest.New(t)
	for _, name := range []string{"", strings.Repeat("a", 255), strings.Repeat("é", 255), strings.Repeat("a", 256)} {
		inputs := providertest.Props(map[string]any{"source": archive(t, skillV1), "displayName": name})
		checked, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: inputs})
		require.NoError(t, err)
		if name == strings.Repeat("a", 256) {
			require.Len(t, checked.Failures, 1)
			require.Equal(t, "displayName", checked.Failures[0].Property)
		} else {
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
		}
	}
	require.Empty(t, api.Requests())
}

func TestSkillReadPreservesStateOnlyLocalArchive(t *testing.T) {
	s, api := providertest.New(t)
	newSkillServer(t, api)
	source := archive(t, skillV1).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::source"})
	created, _ := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": source}))
	assertSkillNoOutputDependencies(t, property.New(created.Properties))
	require.Equal(t, source.WithDependencies(nil), created.Properties.Get("source"))
	// Historical state may still carry dependencies. Retain that raw write-only
	// input without forwarding its dependencies onto the custom-resource output.
	read, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID, Properties: created.Properties.Set("source", source)})
	require.NoError(t, err)
	require.Equal(t, source, read.Inputs.Get("source"))
	require.Equal(t, source.WithDependencies(nil), read.Properties.Get("source"))
	assertSkillNoOutputDependencies(t, property.New(read.Properties))
}

func TestSkillUpdateUsesUploadResultWithoutRead(t *testing.T) {
	s, api := providertest.New(t)
	uploads := newSkillServer(t, api)
	created, inputs := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
	api.Handle("GET /v1/skills/{id}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		t.Error("Update must not refresh internally")
		fakeapi.WriteError(w, http.StatusForbidden, "read forbidden")
	})
	// A version upload reports its own accepted pin, without a follow-up read
	// that could race with another upload.
	updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.NotEmpty(t, updated.Properties.Get("latestVersionId").AsString())
	require.Equal(t, "2026-09-26T00:00:00Z", updated.Properties.Get("updatedAt").AsString())
	require.Len(t, uploads.Uploads(), 2)
	require.Equal(t, skillV1, uploads.Uploads()[1].Files)
	require.Empty(t, uploads.Uploads()[1].Fields, "immutable display name is not sent to version create")
	require.Len(t, api.Requests(), 3, "Create observes the initial name; Update makes only its upload request")
}

func TestSkillImportedDeleteWithoutSource(t *testing.T) {
	s, api := providertest.New(t)
	newSkillServer(t, api)
	created, _ := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
	imported, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID})
	require.NoError(t, err)
	require.True(t, imported.Properties.Get("source").IsNull(), "import cannot download local files")
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: skillURN, ID: imported.ID, Properties: imported.Properties}), "imported skills can be deleted without a local archive")
}

func TestSkillImportedStateCanAcceptLocalSource(t *testing.T) {
	s, api := providertest.New(t)
	uploads := newSkillServer(t, api)
	created, _ := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
	imported, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID})
	require.NoError(t, err)
	require.True(t, imported.Properties.Get("source").IsNull())
	source := archive(t, skillV1).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::source"})
	inputs := imported.Inputs.Set("source", source)
	diff, err := s.Diff(p.DiffRequest{Urn: skillURN, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.Update, diff.DetailedDiff["source"].Kind)
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, n+1, len(api.Requests()), "Update uploads without an internal refresh")
	require.Len(t, uploads.Uploads(), 2)
	require.Equal(t, source.WithDependencies(nil), updated.Properties.Get("source"))
	assertSkillNoOutputDependencies(t, property.New(updated.Properties))
	require.True(t, imported.Properties.Get("source").IsNull(), "decoder projection must not manufacture an imported source")
}

func TestSkillReadErrorsAndImport(t *testing.T) {
	for _, status := range []int{200, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/skills/{id}/versions/{version}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				fakeapi.WriteJSON(w, fakeapi.Object{"id": r.PathValue("version"), "skill_id": r.PathValue("id"), "type": "skill_version", "name": "imported"})
			})
			api.Handle("GET /v1/skills/{id}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				if status != 200 {
					fakeapi.WriteError(w, status, "synthetic fault")
					return
				}
				fakeapi.WriteJSON(w, fakeapi.Object{"id": "skill_import", "type": "skill", "source": map[string]any{"type": "custom"}, "display_name": "Imported", "latest_version_id": "version_1", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
			})
			read, err := s.Read(p.ReadRequest{Urn: skillURN, ID: "skill_import"})
			if status == 403 || status == 500 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if status == 404 {
				require.Empty(t, read.ID)
				return
			}
			require.Equal(t, "skill_import", read.ID)
			require.Equal(t, "Imported", read.Inputs.Get("displayName").AsString())
			require.True(t, read.Inputs.Get("source").IsNull(), "files are not recoverable")
			repeated, err := s.Read(p.ReadRequest{Urn: skillURN, ID: "skill_import"})
			require.NoError(t, err)
			require.Equal(t, read, repeated)
			checked, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: read.Inputs})
			require.NoError(t, err)
			require.NotEmpty(t, checked.Failures, "an updateable program still requires a local archive")
		})
	}
}

func TestSkillUnusableVersionResponseReturnsOrdinaryError(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(fakeapi.Object)
	}{
		{"wrong parent", func(o fakeapi.Object) { o["skill_id"] = "other" }},
		{"wrong kind", func(o fakeapi.Object) { o["type"] = "skill" }},
		{"missing name", func(o fakeapi.Object) { delete(o, "name") }},
		{"null name", func(o fakeapi.Object) { o["name"] = nil }},
		{"empty name", func(o fakeapi.Object) { o["name"] = "" }},
		{"malformed name", func(o fakeapi.Object) { o["name"] = 42 }},
		{"conflicting name", func(o fakeapi.Object) { o["name"] = "other-skill" }},
		{"missing pin", func(o fakeapi.Object) { delete(o, "id") }},
		{"invalid timestamp", func(o fakeapi.Object) { o["created_at"] = false }},
		{"missing description", func(o fakeapi.Object) { delete(o, "description") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			newSkillServer(t, api)
			created, _ := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": archive(t, skillV1)}))
			api.Handle("POST /v1/skills/"+created.ID+"/versions", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				response := fakeapi.Object{"id": "version_new", "skill_id": created.ID, "type": "skill_version", "name": "triage", "description": "test", "created_at": "2026-01-02T00:00:00Z"}
				tc.edit(response)
				fakeapi.WriteJSON(w, response)
			})
			desired := check(t, s, map[string]any{"source": archive(t, map[string]string{"SKILL.md": "---\nname: triage\n---\nNew instructions."})})
			before := len(api.Requests())
			updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: desired})
			require.Error(t, err)
			require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
			require.Nil(t, updated.PartialState)
			require.Zero(t, updated.Properties.Len(), "an unusable response must not reconstruct accepted state")
			renamed := check(t, s, map[string]any{"source": archive(t, map[string]string{"SKILL.md": "---\nname: renamed\n---\nDifferent immutable name."})})
			diff, err := s.Diff(p.DiffRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: renamed})
			require.NoError(t, err)
			require.Equal(t, p.UpdateReplace, diff.DetailedDiff["source"].Kind)
			require.Len(t, api.Requests(), before+1, "Update and Diff must not refresh internally")
		})
	}
}

func TestSkillUnusableCreateResponseReturnsOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/skills", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "skill_allocated", "latest_version_id": "version_initial", "created_at": false})
	})
	inputs := check(t, s, map[string]any{"source": archive(t, skillV1), "displayName": "Desired"})
	created, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: inputs})
	require.Error(t, err)
	require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	require.Len(t, api.Requests(), 1, "an unusable allocation response must not trigger version lookup")
}

func TestSkillPostUploadLookupFailureReturnsOrdinaryError(t *testing.T) {
	for _, fault := range []string{"forbidden", "not found", "wrong parent", "wrong pin", "wrong kind", "missing name"} {
		t.Run(fault, func(t *testing.T) {
			s, api := providertest.New(t)
			uploads := newSkillServer(t, api)
			api.Handle("GET /v1/skills/skill_0001/versions/{version}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				if fault == "forbidden" || fault == "not found" {
					status := http.StatusForbidden
					if fault == "not found" {
						status = http.StatusNotFound
					}
					fakeapi.WriteError(w, status, "synthetic lookup failure")
					return
				}
				response := fakeapi.Object{"id": r.PathValue("version"), "skill_id": "skill_0001", "type": "skill_version", "name": "triage"}
				switch fault {
				case "wrong parent":
					response["skill_id"] = "other"
				case "wrong pin":
					response["id"] = "other"
				case "wrong kind":
					response["type"] = "skill"
				case "missing name":
					delete(response, "name")
				}
				fakeapi.WriteJSON(w, response)
			})
			inputs := check(t, s, map[string]any{"source": archive(t, skillV1)})
			created, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: inputs})
			require.Error(t, err)
			require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Zero(t, created.Properties.Len())
			require.Len(t, uploads.Uploads(), 1)
			require.Equal(t, skillV1, uploads.Uploads()[0].Files)
			require.Len(t, api.Requests(), 2, "lookup failure must not add uploads or compensation")
			require.NotNil(t, api.Object("/v1/skills/skill_0001"), "the successful allocation may remain orphaned")
		})
	}
}

func TestSkillRejectedUploadReturnsOrdinaryError(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			s, api := providertest.New(t)
			inputs := check(t, s, map[string]any{"source": archive(t, skillV1)})
			reject := func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				fakeapi.WriteError(w, http.StatusBadRequest, "synthetic upload rejected")
			}
			if operation == "create" {
				api.Handle("POST /v1/skills", reject)
				created, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: inputs})
				require.ErrorContains(t, err, "synthetic upload rejected")
				require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
				require.Nil(t, created.PartialState)
				require.Empty(t, created.ID)
				require.Zero(t, created.Properties.Len())
				require.Len(t, api.Requests(), 1)
				return
			}
			newSkillServer(t, api)
			created, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: inputs})
			require.NoError(t, err)
			api.Handle("POST /v1/skills/"+created.ID+"/versions", reject)
			before := len(api.Requests())
			updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: created.ID, State: created.Properties, Inputs: inputs})
			require.ErrorContains(t, err, "synthetic upload rejected")
			require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
			require.Nil(t, updated.PartialState)
			require.Zero(t, updated.Properties.Len())
			require.Len(t, api.Requests(), before+1)
		})
	}
}

func TestSkillLegacyIncompleteStateRefreshBeforeUpdate(t *testing.T) {
	s, api := providertest.New(t)
	uploads := newSkillServer(t, api)
	source := archive(t, skillV1).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::source"})
	created, inputs := providertest.Create(t, s, skillURN, providertest.Props(map[string]any{"source": source}))
	legacy := created.Properties.Delete("name").Delete("latestVersionId").Delete("createdAt").Delete("updatedAt")
	read, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID, Inputs: inputs, Properties: legacy})
	require.NoError(t, err)
	require.Equal(t, "triage", read.Properties.Get("name").AsString())
	require.Equal(t, created.Properties.Get("createdAt"), read.Properties.Get("createdAt"))
	require.Equal(t, source.WithDependencies(nil), read.Properties.Get("source"))
	assertSkillNoOutputDependencies(t, property.New(read.Properties))
	before := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: read.ID, State: read.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, source.WithDependencies(nil), updated.Properties.Get("source"))
	assertSkillNoOutputDependencies(t, property.New(updated.Properties))
	require.Equal(t, "triage", updated.Properties.Get("name").AsString())
	require.Equal(t, read.Properties.Get("createdAt"), updated.Properties.Get("createdAt"))
	require.NotEqual(t, read.Properties.Get("latestVersionId"), updated.Properties.Get("latestVersionId"))
	require.Len(t, api.Requests(), before+1, "only the engine-style Read hydrates legacy state")
	require.Equal(t, skillV1, uploads.Uploads()[1].Files)
}

func assertSkillNoOutputDependencies(t *testing.T, v property.Value) {
	t.Helper()
	require.Empty(t, v.Dependencies())
	if v.IsMap() {
		for _, child := range v.AsMap().All {
			assertSkillNoOutputDependencies(t, child)
		}
	} else if v.IsArray() {
		for _, child := range v.AsArray().All {
			assertSkillNoOutputDependencies(t, child)
		}
	}
}

func TestSkillAppliedOutputMetadata(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "secret"}[secret], func(t *testing.T) {
			s, api := providertest.New(t)
			uploads := newSkillServer(t, api)
			deps := []resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::source"}
			source := archive(t, skillV1).WithSecret(secret).WithDependencies(deps)
			label := property.New("Ticket triage").WithSecret(secret).WithDependencies(deps)
			inputs := providertest.Props(map[string]any{"source": source, "displayName": label})
			checked, err := s.Check(p.CheckRequest{Urn: skillURN, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
			created, err := s.Create(p.CreateRequest{Urn: skillURN, Properties: checked.Inputs})
			require.NoError(t, err)
			assertSkillNoOutputDependencies(t, property.New(created.Properties))
			require.Equal(t, source.WithDependencies(nil), created.Properties.Get("source"))
			require.Equal(t, label.WithDependencies(nil), created.Properties.Get("displayName"))

			read, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID, Inputs: checked.Inputs, Properties: created.Properties})
			require.NoError(t, err)
			assertSkillNoOutputDependencies(t, property.New(read.Properties))
			require.Equal(t, source, read.Inputs.Get("source"))
			require.Equal(t, source.WithDependencies(nil), read.Properties.Get("source"))
			require.Equal(t, label.WithDependencies(nil), read.Properties.Get("displayName"))

			files := map[string]string{"SKILL.md": "---\nname: triage\n---\nUpdated instructions."}
			nextSource := archive(t, files).WithSecret(secret).WithDependencies(deps)
			desired := checked.Inputs.Set("source", nextSource)
			baseline := len(api.Requests())
			updated, err := s.Update(p.UpdateRequest{Urn: skillURN, ID: created.ID, State: read.Properties, OldInputs: checked.Inputs, Inputs: desired})
			require.NoError(t, err)
			assertSkillNoOutputDependencies(t, property.New(updated.Properties))
			require.Equal(t, nextSource.WithDependencies(nil), updated.Properties.Get("source"))
			require.Equal(t, label.WithDependencies(nil), updated.Properties.Get("displayName"))
			require.Equal(t, baseline+1, len(api.Requests()), "Update only uploads; no internal GET")
			require.Equal(t, files, uploads.Uploads()[1].Files)
			require.NotEqual(t, created.Properties.Get("latestVersionId"), updated.Properties.Get("latestVersionId"))
			require.Equal(t, source, inputs.Get("source"))
			require.Equal(t, nextSource, desired.Get("source"))

			imported, err := s.Read(p.ReadRequest{Urn: skillURN, ID: created.ID})
			require.NoError(t, err)
			assertSkillNoOutputDependencies(t, property.New(imported.Properties))
			require.True(t, imported.Properties.Get("source").IsNull(), "import cannot recover local files")
		})
	}
}

func TestSkillObservationPresence(t *testing.T) {
	for _, raw := range []string{
		`{"id":"skill_known"}`,
		`{"id":"skill_known","latest_version_id":42,"created_at":false,"source":{"type":42}}`,
		`{"id":42,"latest_version_id":"version_known"}`,
	} {
		var r sdk.Skill
		require.NoError(t, json.Unmarshal([]byte(raw), &r))
		out, id, err := skills.SkillObservationForTest(&r, skills.SkillArgs{}, false)
		require.Error(t, err)
		require.Empty(t, id)
		require.Equal(t, skills.SkillState{}, out)
	}
}

func TestSkillMalformedLabelIsNotReplacedWithDesiredValue(t *testing.T) {
	label := "desired"
	base := `{"id":"skill_known","type":"skill","source":{"type":"custom"},"latest_version_id":"version_known","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"`
	for _, value := range []string{`null`, `42`, `{}`} {
		var r sdk.Skill
		require.NoError(t, json.Unmarshal([]byte(base+`,"display_name":`+value+`}`), &r))
		out, id, err := skills.SkillObservationForTest(&r, skills.SkillArgs{DisplayName: &label}, false)
		require.Error(t, err)
		require.Empty(t, id)
		require.Equal(t, skills.SkillState{}, out, "an invalid observation must not reconstruct desired state")
	}
	var r sdk.Skill
	require.NoError(t, json.Unmarshal([]byte(base+`}`), &r))
	out, id, err := skills.SkillObservationForTest(&r, skills.SkillArgs{DisplayName: &label}, false)
	require.Error(t, err)
	require.Empty(t, id)
	require.Equal(t, skills.SkillState{}, out, "an omitted required label is not an accepted-response fallback")

	require.NoError(t, json.Unmarshal([]byte(base+`,"display_name":"observed"}`), &r))
	out, id, err = skills.SkillObservationForTest(&r, skills.SkillArgs{DisplayName: &label}, false)
	require.NoError(t, err)
	require.Equal(t, "skill_known", id)
	require.Equal(t, "observed", *out.DisplayName, "a usable response supplies the actual label")
	require.Equal(t, "version_known", out.LatestVersionID)
	require.Equal(t, "2026-01-01T00:00:00Z", out.CreatedAt)
	require.Equal(t, "2026-01-02T00:00:00Z", out.UpdatedAt)
}

func TestSkillVersionObservationIdentity(t *testing.T) {
	for _, parent := range []string{`"other"`, `42`, `null`} {
		var r sdk.SkillVersion
		require.NoError(t, json.Unmarshal([]byte(`{"id":"wrong_pin","skill_id":`+parent+`,"created_at":"2026-01-01T00:00:00Z","type":"skill_version","name":"test","description":"test"}`), &r))
		out := skills.SkillState{}
		require.Error(t, skills.SkillVersionObservationForTest(&r, "skill_known", &out))
		require.Empty(t, out.LatestVersionID)
		require.Empty(t, out.UpdatedAt)
	}
}

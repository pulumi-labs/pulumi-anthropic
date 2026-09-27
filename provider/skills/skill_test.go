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
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
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
		fakeapi.WriteJSON(w, fakeapi.Object{"id": version, "type": "skill_version", "skill_id": r.PathValue("id")})
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

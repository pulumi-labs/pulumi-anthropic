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

package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go/option"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/archive"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/sig"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Skill is a custom Agent Skill uploaded to a workspace. Uploading new content creates a
// new immutable version; the skill's metadata cannot be changed in place.
type Skill struct {
	rest.Resource[SkillArgs, SkillState, *SkillState]
}

// NewSkill returns the Skill resource controller.
func NewSkill() *Skill {
	return &Skill{rest.Resource[SkillArgs, SkillState, *SkillState]{
		Collection: skillCollection,
		EndOfLife:  rest.Delete,
	}}
}

func (s *Skill) Annotate(a infer.Annotator) {
	a.Describe(s, "A custom Agent Skill: a directory of instructions, scripts, and resources, "+
		"with a `SKILL.md` at its root, that Claude loads when relevant. Skills can be attached "+
		"to Managed Agents and used from the Messages API.\n\n"+
		"Skills are versioned. When the content of `source` changes, a new immutable version is "+
		"uploaded and becomes `latestVersionId`; earlier versions are kept, so agents that pin one "+
		"keep working. Deleting the resource deletes the skill and all of its versions.")
}

type SkillArgs struct {
	Source      *resource.Archive `pulumi:"source"               wire:"-"`
	DisplayName *string           `pulumi:"displayName,optional" provider:"replaceOnChanges"`
}

func (s *SkillArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Source, "The skill's files, such as a `FileArchive` of the skill's directory. "+
		"`SKILL.md` must sit at the root of the archive, or at the root of its single top-level "+
		"directory. Up to 30 MB uncompressed.")
	a.Describe(&s.DisplayName, "A human-readable label for the skill, up to 255 characters. "+
		"Defaults to the `name` in the `SKILL.md` frontmatter. The API cannot rename a skill, so "+
		"changing this replaces it.")
}

type SkillState struct {
	SkillArgs
	LatestVersionID string `pulumi:"latestVersionId"`
	CreatedAt       string `pulumi:"createdAt"`
	UpdatedAt       string `pulumi:"updatedAt"`
}

func (s *SkillState) Inputs() *SkillArgs { return &s.SkillArgs }

func (s *SkillState) Annotate(a infer.Annotator) {
	a.Describe(&s.LatestVersionID, "The ID of the newest version (`skver_...`), which agents "+
		"can pin in a skill reference's `version`.")
	a.Describe(&s.CreatedAt, "When the skill was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the skill was last updated, in RFC 3339 format.")
}

var (
	_ infer.CustomDiff[SkillArgs, SkillState]   = (*Skill)(nil)
	_ infer.CustomUpdate[SkillArgs, SkillState] = (*Skill)(nil)
	_ infer.CustomRead[SkillArgs, SkillState]   = (*Skill)(nil)
	_ infer.CustomDelete[SkillState]            = (*Skill)(nil)
)

var _ infer.CustomCheck[SkillArgs] = (*Skill)(nil)

// Check resolves the source to the single representation the engine reads (see
// canonical), so that it is uploaded, hashed, and recorded in state consistently.
func (s *Skill) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SkillArgs], error) {
	args, failures, err := infer.DefaultCheck[SkillArgs](ctx, req.NewInputs)
	if err == nil && args.Source != nil {
		args.Source, err = canonical(args.Source)
	}
	return infer.CheckResponse[SkillArgs]{Inputs: args, Failures: failures}, err
}

// Create uploads the skill's first version.
func (s *Skill) Create(
	ctx context.Context, req infer.CreateRequest[SkillArgs],
) (infer.CreateResponse[SkillState], error) {
	out := SkillState{SkillArgs: req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[SkillState]{Output: out}, nil
	}
	var fields map[string]string
	if req.Inputs.DisplayName != nil {
		fields = map[string]string{"display_name": *req.Inputs.DisplayName}
	}
	var raw []byte
	if err := s.upload(ctx, s.Collection.Path, req.Inputs.Source, fields, &raw); err != nil {
		return infer.CreateResponse[SkillState]{}, err
	}
	out, id, err := rest.DecodeState[SkillArgs, SkillState](raw, req.Inputs)
	return infer.CreateResponse[SkillState]{ID: id, Output: out}, rest.InitFailed(err)
}

// Diff is the standard diff, except that the source is compared by content hash: an
// archive's identity is its contents, not the path it was read from. The engine hashes an
// archive when the program registers it, so the state holds the hash of what was
// uploaded even if a directory was since edited.
func (s *Skill) Diff(
	_ context.Context, req infer.DiffRequest[SkillArgs, SkillState],
) (infer.DiffResponse, error) {
	oldHash, err := hash(req.State.Source)
	if err != nil {
		return infer.DiffResponse{}, err
	}
	newHash, err := hash(req.Inputs.Source)
	if err != nil {
		return infer.DiffResponse{}, err
	}
	old, new := req.State.SkillArgs, req.Inputs
	old.Source, new.Source = nil, nil
	diff := rest.Diff(old, new)
	if oldHash != newHash || newHash == "" {
		diff.DetailedDiff["source"] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
		diff.HasChanges = true
	}
	return diff, nil
}

// Update uploads the new content as a new version, which becomes the latest.
func (s *Skill) Update(
	ctx context.Context, req infer.UpdateRequest[SkillArgs, SkillState],
) (infer.UpdateResponse[SkillState], error) {
	out := req.State
	out.SkillArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[SkillState]{Output: out}, nil
	}
	versions := s.Collection.Sub(req.ID, "versions")
	if err := s.upload(ctx, versions.Path, req.Inputs.Source, nil, nil); err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	var raw []byte
	if err := s.Collection.Get(ctx, req.ID, &raw); err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	out, _, err := rest.DecodeState[SkillArgs, SkillState](raw, req.Inputs)
	return infer.UpdateResponse[SkillState]{Output: out}, err
}

// upload POSTs a skill version's files, plus any form fields, as multipart form data. Each
// file is a files[] part named by its path within the archive, which is how the API
// learns the skill's directory layout.
func (s *Skill) upload(
	ctx context.Context, path string, source *resource.Archive, fields map[string]string, res any,
) error {
	if source == nil || !source.HasContents() {
		return errors.New("source must be an archive with contents")
	}
	source, err := canonical(source)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := form.WriteField(k, v); err != nil {
			return err
		}
	}
	reader, err := source.Open()
	if err != nil {
		return fmt.Errorf("reading source: %w", err)
	}
	defer func() { _ = reader.Close() }()
	for {
		name, blob, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading source: %w", err)
		}
		part, err := form.CreateFormFile("files[]", name)
		if err == nil {
			_, err = io.Copy(part, blob)
		}
		_ = blob.Close()
		if err != nil {
			return fmt.Errorf("encoding %s: %w", name, err)
		}
	}
	if err := form.Close(); err != nil {
		return err
	}
	return s.Collection.Do(ctx, http.MethodPost, path, nil, res,
		option.WithRequestBody(form.FormDataContentType(), body.Bytes()))
}

// hash returns an archive's content hash, or "" for an archive whose contents are not
// yet known, as during a preview. The engine hashes every archive a program registers, so
// its hash is authoritative; it is also all that survives of an archive the provider
// framework decodes from state, which drops the archive's path.
func hash(a *resource.Archive) (string, error) {
	if a == nil || a.Hash != "" || !a.HasContents() {
		if a == nil {
			return "", nil
		}
		return a.Hash, nil
	}
	c, err := canonical(a)
	if err != nil {
		return "", err
	}
	if err := c.EnsureHash(); err != nil {
		return "", fmt.Errorf("hashing source: %w", err)
	}
	return c.Hash, nil
}

// canonical returns the archive as the Pulumi SDK itself reads it. An archive can arrive
// with more than one representation set: the engine delivers a path archive with an empty
// asset map alongside its path, and the provider framework delivers an asset archive's
// members in their serialized form. Deserialize resolves both, preferring the path, then
// the URI, then the assets, exactly as the engine does.
func canonical(a *resource.Archive) (*resource.Archive, error) {
	fields := map[string]any{sig.Key: archive.ArchiveSig, archive.ArchiveHashProperty: a.Hash}
	if a.Path != "" {
		fields[archive.ArchivePathProperty] = a.Path
	}
	if a.URI != "" {
		fields[archive.ArchiveURIProperty] = a.URI
	}
	if a.Assets != nil {
		fields[archive.ArchiveAssetsProperty] = a.Assets
	}
	c, _, err := archive.Deserialize(fields)
	if err != nil {
		return nil, fmt.Errorf("reading source: %w", err)
	}
	return c, nil
}

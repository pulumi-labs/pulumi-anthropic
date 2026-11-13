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
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/archive"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/sig"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"gopkg.in/yaml.v3"
)

// Skill is a versioned custom workspace skill.
type Skill struct{}

// NewSkill returns the complete Skill resource.
func NewSkill() infer.InferredResource {
	return skillResource{infer.Resource(&Skill{})}
}

func (s *Skill) Annotate(a infer.Annotator) {
	a.Describe(s, "A custom Agent Skill: a directory of instructions, scripts, and resources, "+
		"with a `SKILL.md` at its root, that Claude loads when relevant. Skills can be attached "+
		"to Managed Agents and used from the Messages API.\n\n"+
		"Content changes upload a new immutable version; earlier versions remain available to pinned agents. "+
		"The skill name in SKILL.md is immutable; changing it requires replacing the resource. "+
		"Deleting the resource deletes the skill and all of its versions. Imported skills require "+
		"a local source archive before they can be updated; their files cannot be downloaded through this resource.")
}

type SkillArgs struct {
	Source      *resource.Archive `pulumi:"source"`
	DisplayName *string           `pulumi:"displayName,optional" provider:"replaceOnChanges"`
}

func (s *SkillArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Source, "The skill's files, such as a `FileArchive` of the skill's directory. "+
		"`SKILL.md` must sit at the root of the archive, or at the root of its single top-level "+
		"directory. Up to 30 MB uncompressed. The skill name must remain unchanged for subsequent versions.")
	a.Describe(&s.DisplayName, "A human-readable label for the skill, up to 255 characters. "+
		"Defaults to the `name` in the `SKILL.md` frontmatter. Changing or removing a configured label replaces the skill.")
}

type SkillState struct {
	SkillArgs
	Name            string `pulumi:"name"`
	LatestVersionID string `pulumi:"latestVersionId"`
	CreatedAt       string `pulumi:"createdAt"`
	UpdatedAt       string `pulumi:"updatedAt"`
}

func (s *SkillState) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "The skill's immutable name, taken from the initial SKILL.md frontmatter or its enclosing directory. Changing this name in your source archive replaces the skill.")
	a.Describe(&s.LatestVersionID, "The version created by the most recent upload, which agents "+
		"can pin in a skill reference's `version`. Refresh reports the skill's current newest version.")
	a.Describe(&s.CreatedAt, "When the skill was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the skill was last updated, in RFC 3339 format.")
}

func (*Skill) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SkillArgs], error) {
	args, failures, err := infer.DefaultCheck[SkillArgs](ctx, req.NewInputs)
	if err == nil && args.Source != nil {
		args.Source, err = canonical(args.Source)
	}
	if name := req.NewInputs.Get("displayName"); name.IsString() && utf8.RuneCountInString(name.AsString()) > 255 {
		failures = append(failures, p.CheckFailure{Property: "displayName", Reason: "must be at most 255 characters"})
	}
	return infer.CheckResponse[SkillArgs]{Inputs: args, Failures: failures}, err
}

// Compare archives by their engine-recorded content hashes, not physical paths.
func (*Skill) Diff(_ context.Context, req infer.DiffRequest[SkillArgs, SkillState]) (infer.DiffResponse, error) {
	oldHash, err := hash(req.State.Source)
	if err != nil {
		return infer.DiffResponse{}, err
	}
	newHash, err := hash(req.Inputs.Source)
	if err != nil {
		return infer.DiffResponse{}, err
	}
	diff := infer.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	old, next := req.State.DisplayName, req.Inputs.DisplayName
	if (old == nil) != (next == nil) || old != nil && next != nil && *old != *next {
		kind := p.UpdateReplace
		if old == nil {
			kind = p.AddReplace
		} else if next == nil {
			kind = p.DeleteReplace
		}
		diff.HasChanges = true
		diff.DetailedDiff["displayName"] = p.PropertyDiff{Kind: kind, InputDiff: true}
	}
	if oldHash != newHash || newHash == "" {
		kind := p.Update
		if req.State.Name != "" && newHash != "" && req.Inputs.Source != nil && req.Inputs.Source.HasContents() {
			name, nameErr := skillArchiveName(req.Inputs.Source)
			if nameErr != nil {
				return infer.DiffResponse{}, nameErr
			}
			if name != req.State.Name {
				kind = p.UpdateReplace
			}
		}
		diff.DetailedDiff["source"] = p.PropertyDiff{Kind: kind, InputDiff: true}
		diff.HasChanges = true
	}
	return diff, nil
}

func (*Skill) Create(ctx context.Context, req infer.CreateRequest[SkillArgs]) (infer.CreateResponse[SkillState], error) {
	if req.DryRun {
		return infer.CreateResponse[SkillState]{Output: SkillState{SkillArgs: req.Inputs}}, nil
	}
	files, err := skillFiles(req.Inputs.Source)
	if err != nil {
		return infer.CreateResponse[SkillState]{}, err
	}
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[SkillState]{}, err
	}
	params := anthropic.SkillNewParams{Files: files}
	if req.Inputs.DisplayName != nil {
		params.DisplayName = anthropic.String(*req.Inputs.DisplayName)
	}
	r, err := client.Skills.New(ctx, params)
	if err != nil {
		return infer.CreateResponse[SkillState]{}, err
	}
	out, id, err := skillObservation(r, req.Inputs, false)
	if err == nil {
		version, readErr := client.Skills.Versions.Get(ctx, out.LatestVersionID, anthropic.SkillVersionGetParams{SkillID: id})
		if readErr != nil {
			err = readErr
		} else {
			err = skillVersionName(version, id, out.LatestVersionID, &out)
		}
	}
	if err != nil {
		return infer.CreateResponse[SkillState]{}, err
	}
	return infer.CreateResponse[SkillState]{ID: id, Output: out}, nil
}

func (*Skill) Read(ctx context.Context, req infer.ReadRequest[SkillArgs, SkillState]) (infer.ReadResponse[SkillArgs, SkillState], error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[SkillArgs, SkillState]{}, err
	}
	r, err := client.Skills.Get(ctx, req.ID, anthropic.SkillGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[SkillArgs, SkillState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[SkillArgs, SkillState]{}, err
	}
	inputs := req.Inputs
	if inputs.Source == nil {
		inputs.Source = req.State.Source
	}
	if inputs.DisplayName == nil {
		inputs.DisplayName = req.State.DisplayName
	}
	out, id, err := skillObservation(r, inputs, inputs.Source == nil && inputs.DisplayName == nil)
	if err == nil && id != req.ID {
		err = errors.New("skill response identity does not match requested identity")
	}
	if err != nil {
		return infer.ReadResponse[SkillArgs, SkillState]{}, err
	}
	version, err := client.Skills.Versions.Get(ctx, out.LatestVersionID, anthropic.SkillVersionGetParams{SkillID: id})
	if err != nil {
		return infer.ReadResponse[SkillArgs, SkillState]{}, err
	}
	if err := skillVersionName(version, id, out.LatestVersionID, &out); err != nil {
		return infer.ReadResponse[SkillArgs, SkillState]{}, err
	}
	return infer.ReadResponse[SkillArgs, SkillState]{ID: id, Inputs: out.SkillArgs, State: out}, nil
}

func (*Skill) Update(ctx context.Context, req infer.UpdateRequest[SkillArgs, SkillState]) (infer.UpdateResponse[SkillState], error) {
	out := SkillState{SkillArgs: req.Inputs, Name: req.State.Name, CreatedAt: req.State.CreatedAt}
	if req.DryRun {
		return infer.UpdateResponse[SkillState]{Output: out}, nil
	}
	// A version upload cannot establish or change the immutable label. Retain
	// only the label already known from engine-supplied state.
	out.DisplayName = req.State.DisplayName
	files, err := skillFiles(req.Inputs.Source)
	if err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	// The engine supplies state and owns refresh. Send the entire desired bundle;
	// never replace the upload's pin with a GET.
	r, err := client.Skills.Versions.New(ctx, req.ID, anthropic.SkillVersionNewParams{Files: files})
	if err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	if err := skillVersionObservation(r, req.ID, &out); err != nil {
		return infer.UpdateResponse[SkillState]{}, err
	}
	return infer.UpdateResponse[SkillState]{Output: out}, nil
}

func (*Skill) Delete(ctx context.Context, req infer.DeleteRequest[SkillState]) (infer.DeleteResponse, error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = client.Skills.Delete(ctx, req.ID, anthropic.SkillDeleteParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// infer v1.5 attempts to structurally decode an unknown archive and rejects its
// missing signature. Substitute a valid archive only across that typed decoding
// boundary, restoring the logical archive value afterward. Check retains input
// metadata; outputs retain data, unknownness and secrecy, but never input
// dependencies. No unknown archive is ever opened or sent to the API.
type skillResource struct{ infer.InferredResource }

func (r skillResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	original := req.Inputs
	req.Inputs = skillDecodeInputs(original)
	resp, err := infer.Resource(&Skill{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		// Check has no value-moving normalization; retain dependencies and wrappers
		// which infer's typed re-encoding otherwise loses.
		resp.Inputs = original
	}
	return resp, err
}

func (r skillResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	unknown := req.Inputs.Get("source").IsComputed()
	oldName, newName := req.State.Get("displayName"), req.Inputs.Get("displayName")
	unknownNameChange := oldName.IsComputed() != newName.IsComputed()
	req.Inputs = skillDecodeInputs(req.Inputs)
	req.State = skillDecodeState(req.State)
	if unknown {
		req.State = req.State.Set("name", property.New(""))
	}
	resp, err := infer.Resource(&Skill{}).Diff(ctx, req)
	if err == nil && unknownNameChange {
		if resp.DetailedDiff == nil {
			resp.DetailedDiff = map[string]p.PropertyDiff{}
		}
		resp.DetailedDiff["displayName"] = p.PropertyDiff{Kind: p.UpdateReplace, InputDiff: true}
		resp.HasChanges = true
	}
	if err == nil && unknown {
		if resp.DetailedDiff == nil {
			resp.DetailedDiff = map[string]p.PropertyDiff{}
		}
		resp.DetailedDiff["source"] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
		resp.HasChanges = true
	}
	return resp, err
}

func (r skillResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	original := req.Properties
	if !req.DryRun && original.Get("source").IsComputed() {
		return p.CreateResponse{}, errors.New("source must be known before uploading")
	}
	req.Properties = skillDecodeInputs(original)
	resp, err := infer.Resource(&Skill{}).Create(ctx, req)
	if err == nil {
		// Raw archive restoration preserves local files and unknown previews across
		// the decoder accommodation. An archive is atomic, so only its outer
		// dependency annotation needs removing at this custom-resource boundary.
		resp.Properties = resp.Properties.Set("source", original.Get("source").WithDependencies(nil))
	}
	return resp, err
}

func (r skillResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&Skill{}).Read(ctx, req)
	source := req.Inputs.Get("source")
	if source.IsNull() {
		source = req.Properties.Get("source")
	}
	if err == nil && resp.ID != "" && !source.IsNull() {
		resp.Inputs = resp.Inputs.Set("source", source)
		// The local write-only archive cannot be recovered from the API. Keep its
		// contents and secrecy without forwarding input/state dependencies.
		resp.Properties = resp.Properties.Set("source", source.WithDependencies(nil))
	}
	return resp, err
}

func (r skillResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	original := req.Inputs
	if !req.DryRun && original.Get("source").IsComputed() {
		return p.UpdateResponse{}, errors.New("source must be known before uploading")
	}
	req.Inputs = skillDecodeInputs(original)
	req.State = skillDecodeState(req.State)
	resp, err := infer.Resource(&Skill{}).Update(ctx, req)
	if err == nil {
		resp.Properties = resp.Properties.Set("source", original.Get("source").WithDependencies(nil))
	}
	return resp, err
}

func (r skillResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	req.Properties = skillDecodeState(req.Properties)
	return infer.Resource(&Skill{}).Delete(ctx, req)
}

// Imported skills have no downloadable source archive. Retain the existing
// decoder-only accommodation for that ordinary write-only absence; never add it
// to checked inputs or Read results. No missing server observations are filled.
func skillDecodeState(state property.Map) property.Map {
	if state.Get("source").IsNull() {
		state = state.Set("source", property.New(&resource.Archive{Sig: archive.ArchiveSig, Assets: map[string]any{}}))
	}
	return skillDecodeInputs(state)
}

func skillDecodeInputs(inputs property.Map) property.Map {
	if inputs.Get("source").IsComputed() {
		return inputs.Set("source", property.New(&resource.Archive{Sig: archive.ArchiveSig, Assets: map[string]any{}}))
	}
	return inputs
}

// Filename(), unlike Name(), preserves the full archive-relative path in the SDK.
type skillFile struct {
	*bytes.Reader
	filename string
}

// Validate SDK raw presence as well as decoded values: zero values alone cannot
// distinguish absent, mistyped, and valid response fields.
func skillObservation(r *anthropic.Skill, accepted SkillArgs, importing bool) (SkillState, string, error) {
	if r == nil || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID == "" ||
		!apiutil.JSONString(r.JSON.LatestVersionID.Raw()) || r.LatestVersionID == "" ||
		!apiutil.JSONString(r.JSON.CreatedAt.Raw()) || r.CreatedAt.IsZero() ||
		!apiutil.JSONString(r.JSON.UpdatedAt.Raw()) || r.UpdatedAt.IsZero() ||
		!apiutil.JSONString(r.JSON.DisplayName.Raw()) || r.JSON.Type.Raw() != `"skill"` ||
		!apiutil.JSONString(r.Source.JSON.Type.Raw()) || r.Source.Type != anthropic.SkillSourceTypeCustom {
		return SkillState{}, "", errors.New("incomplete or unsupported skill response")
	}
	out := SkillState{
		SkillArgs: accepted, LatestVersionID: r.LatestVersionID,
		CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano),
	}
	if importing || accepted.DisplayName != nil {
		name := r.DisplayName
		out.DisplayName = &name
	}
	return out, r.ID, nil
}

func skillVersionObservation(r *anthropic.SkillVersion, id string, out *SkillState) error {
	if r == nil {
		return errors.New("incomplete skill version response")
	}
	// A wrong parent or object kind must never contribute a version pin or timestamp.
	if raw := r.JSON.Type.Raw(); raw != "" && raw != `"skill_version"` {
		return errors.New("unsupported skill version response")
	}
	if !apiutil.JSONString(r.JSON.SkillID.Raw()) || r.SkillID != id {
		return errors.New("skill version response identity does not match requested identity")
	}
	if !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID == "" ||
		!apiutil.JSONString(r.JSON.CreatedAt.Raw()) || r.CreatedAt.IsZero() || r.JSON.Type.Raw() != `"skill_version"` ||
		!apiutil.JSONString(r.JSON.Name.Raw()) || r.Name == "" || !apiutil.JSONString(r.JSON.Description.Raw()) {
		return errors.New("incomplete skill version response")
	}
	if out.Name != "" && out.Name != r.Name {
		return errors.New("skill version response name does not match immutable skill name")
	}
	out.Name = r.Name
	out.LatestVersionID = r.ID
	out.UpdatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	return nil
}

// A version GET must corroborate both the requested parent and requested pin
// before any subordinate observation is incorporated into the skill state.
func skillVersionName(r *anthropic.SkillVersion, id, version string, out *SkillState) error {
	if r == nil || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID != version ||
		!apiutil.JSONString(r.JSON.SkillID.Raw()) || r.SkillID != id ||
		r.JSON.Type.Raw() != `"skill_version"` || !apiutil.JSONString(r.JSON.Name.Raw()) || r.Name == "" {
		return errors.New("incomplete or mismatched skill version response")
	}
	out.Name = r.Name
	return nil
}

// Read only the desired archive; never reopen the prior filesystem path to
// infer a name it had when first uploaded. The authoritative prior name is an
// observed version field, not a reconstruction of old local files.
func skillArchiveName(source *resource.Archive) (string, error) {
	files, err := skillFiles(source)
	if err != nil {
		return "", err
	}
	var target string
	for _, reader := range files {
		if reader.(skillFile).filename == "SKILL.md" {
			target = "SKILL.md"
			break
		}
	}
	if target == "" {
		var directory string
		for _, reader := range files {
			parts := strings.Split(reader.(skillFile).filename, "/")
			if len(parts) < 2 || (directory != "" && directory != parts[0]) {
				return "", errors.New("skill source must have SKILL.md at its root or in one top-level directory")
			}
			directory = parts[0]
		}
		if directory != "" {
			target = directory + "/SKILL.md"
		}
	}
	for _, reader := range files {
		f := reader.(skillFile)
		if f.filename != target {
			continue
		}
		data, err := io.ReadAll(f.Reader)
		if err != nil {
			return "", err
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines := strings.Split(text, "\n")
		if len(lines) > 1 && lines[0] == "---" {
			for end := 1; end < len(lines); end++ {
				if lines[end] != "---" {
					continue
				}
				var front struct {
					Name string `yaml:"name"`
				}
				if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &front); err != nil {
					return "", errors.New("invalid SKILL.md frontmatter")
				}
				if front.Name != "" {
					return front.Name, nil
				}
				break
			}
		}
		if directory := path.Dir(f.filename); directory != "." && !strings.Contains(directory, "/") {
			return directory, nil
		}
	}
	return "", errors.New("source must contain SKILL.md with a name or in a named top-level directory")
}

func (f skillFile) Filename() string { return f.filename }

func skillFiles(source *resource.Archive) ([]io.Reader, error) {
	if source == nil || !source.HasContents() {
		return nil, errors.New("source must be an archive with contents")
	}
	source, err := canonical(source)
	if err != nil {
		return nil, err
	}
	reader, err := source.Open()
	if err != nil {
		return nil, fmt.Errorf("reading source: %w", err)
	}
	defer func() { _ = reader.Close() }()
	var files []io.Reader
	remaining := int64(30 * 1024 * 1024)
	for {
		name, blob, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading source: %w", err)
		}
		if name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			_ = blob.Close()
			return nil, errors.New("skill files must have safe archive-relative paths")
		}
		data, err := io.ReadAll(io.LimitReader(blob, remaining+1))
		_ = blob.Close()
		if err != nil {
			return nil, fmt.Errorf("reading skill file: %w", err)
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, errors.New("skill source exceeds 30 MB uncompressed")
		}
		files = append(files, skillFile{Reader: bytes.NewReader(data), filename: name})
	}
	return files, nil
}

// The engine-recorded hash remains authoritative even if the original directory
// has since disappeared or changed.
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

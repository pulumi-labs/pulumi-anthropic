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

// Package rest implements the Anthropic API's uniform REST conventions once, so that
// each Pulumi resource is declared as data: a collection, wire-shaped input and output
// structs, and what deleting it means.
//
// The conventions are: POST to a collection creates; GET on an item reads; POST on an
// item partially updates, where omitted fields keep their value and null clears them;
// DELETE deletes; and POST to an item's /archive archives, which is irreversible.
package rest

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

// Collection is a REST collection in the Anthropic API, such as v1/agents or
// v1/vaults/{id}/credentials.
type Collection struct {
	// Plane selects the credential the collection's endpoints accept.
	Plane config.Plane
	// Path is the collection's path relative to the base URL, e.g. "v1/agents".
	Path string
	// Beta is the anthropic-beta header value the endpoints require, if any.
	Beta string
	// GA marks a generally available endpoint, which the beta=true query parameter
	// (sent by the official SDKs on every beta endpoint) must not be added to.
	GA bool
}

// Item returns the path of the collection member with the given ID.
func (c Collection) Item(id string) string {
	return c.Path + "/" + url.PathEscape(id)
}

// Sub returns the named sub-collection of the member with the given ID.
func (c Collection) Sub(id, name string) Collection {
	c.Path = c.Item(id) + "/" + name
	return c
}

// Do issues a request against path (a collection or item path from this collection).
// body is JSON-encoded unless nil; if res is non-nil, it receives the response body.
// opts are applied last, so they can override the body's encoding (as a multipart upload
// does with option.WithRequestBody).
func (c Collection) Do(
	ctx context.Context, method, path string, body, res any, opts ...option.RequestOption,
) error {
	client, err := config.ClientFor(ctx, c.Plane)
	if err != nil {
		return err
	}
	var base []option.RequestOption
	if c.Beta != "" {
		base = append(base, option.WithHeader("anthropic-beta", c.Beta))
	}
	if !c.GA {
		base = append(base, option.WithQuery("beta", "true"))
	}
	return client.Execute(ctx, method, path, body, res, append(base, opts...)...)
}

// Get reads the item with the given ID into res.
func (c Collection) Get(ctx context.Context, id string, res any) error {
	return c.Do(ctx, http.MethodGet, c.Item(id), nil, res)
}

// Action POSTs to one of an item's action endpoints, such as "archive" or "pause".
func (c Collection) Action(ctx context.Context, id, action string, res any) error {
	return c.Do(ctx, http.MethodPost, c.Item(id)+"/"+action, nil, res)
}

// IsNotFound reports whether err is an API error with status 404.
func IsNotFound(err error) bool {
	var apiErr *anthropic.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is an API error with status 409.
func IsConflict(err error) bool {
	var apiErr *anthropic.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// JoinID encodes the ID of a resource identified by a parent and child ID pair, such as a
// workspace member, as "parent/child".
func JoinID(parent, child string) string { return parent + "/" + child }

// SplitID splits an ID produced by JoinID.
func SplitID(id string) (parent, child string, err error) {
	parent, child, ok := strings.Cut(id, "/")
	if !ok || parent == "" || child == "" {
		return "", "", errors.New("expected an ID of the form <parent-id>/<child-id>, got " + id)
	}
	return parent, child, nil
}

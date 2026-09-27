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

package rest

import (
	"context"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// page is one page of a list response. The API paginates in two styles: older endpoints
// return has_more and last_id, to be passed back as after_id; newer ones return a
// next_page cursor, to be passed back as page.
type page[T any] struct {
	Data     []T     `pulumi:"data"`
	HasMore  bool    `pulumi:"hasMore,optional"`
	LastID   *string `pulumi:"lastId,optional"`
	NextPage *string `pulumi:"nextPage,optional"`
}

// List returns every item of a collection, following either pagination style. An empty
// list is non-nil, so that it is reported as an empty array rather than omitted.
func List[T any](ctx context.Context, c Collection) ([]T, error) {
	all := []T{}
	var opts []option.RequestOption
	for {
		var raw []byte
		if err := c.Do(ctx, http.MethodGet, c.Path, nil, &raw, opts...); err != nil {
			return nil, err
		}
		var p page[T]
		if err := Decode(raw, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Data...)
		switch {
		case len(p.Data) == 0:
			return all, nil
		case p.NextPage != nil && *p.NextPage != "":
			opts = []option.RequestOption{option.WithQuery("page", *p.NextPage)}
		case p.HasMore && p.LastID != nil:
			opts = []option.RequestOption{option.WithQuery("after_id", *p.LastID)}
		default:
			return all, nil
		}
	}
}

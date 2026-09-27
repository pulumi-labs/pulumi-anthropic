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

package agents

import (
	"net/url"
	"strings"
)

// canonicalMCPServerURL returns an MCP server URL in the form the API stores for a vault
// credential: scheme and host lowercased, the default HTTPS port dropped, and trailing
// slashes trimmed from the path, which is "/" when empty. Declaring the URL in that form
// keeps a refresh from reporting the API's normalization as drift, which, since the URL
// cannot change in place, would replace the credential. A string that is not an absolute
// URL is left for the API to reject.
func canonicalMCPServerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "https" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

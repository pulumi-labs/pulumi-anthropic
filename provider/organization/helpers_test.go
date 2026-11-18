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

package organization_test

import (
	"errors"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/stretchr/testify/require"
)

func requireOrdinaryError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var initError infer.ResourceInitFailedError
	require.False(t, errors.As(err, &initError), "initialization checkpoints are not supported")
}

// urn returns a test URN for a resource type of this module, by name.
func urn(name string) resource.URN {
	return providertest.URN(tokens.Type("anthropic:organization:" + name))
}

// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents

import sdk "github.com/anthropics/anthropic-sdk-go/v2"

// Test-only bridges let the external resource suites retain direct conversion
// regressions without adding exports to the production package.
var (
	AgentNewForTest                = agentNew
	AgentUpdateForTest             = agentUpdate
	AgentStrictToolsForTest        = agentStrictTools
	AgentObservedMultiForTest      = agentObservedMulti
	AgentObservedReferencesForTest = agentObservedReferences
	CanonicalMCPServerURLForTest   = canonicalMCPServerURL
	VaultStateForTest              = vaultState
	VaultNewForTest                = vaultNew
	VaultUpdateForTest             = vaultUpdate
)

func AgentObservedForTest(r *sdk.BetaManagedAgentsAgent, prior AgentArgs, importing bool) (AgentState, error) {
	return agentObserved(r, prior, agentObservationOptions{importing: importing})
}
func AgentPtrForTest[T any](value T) *T { return agentPtr(value) }

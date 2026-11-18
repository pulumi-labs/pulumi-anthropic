// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package skills

// Test-only access keeps archive and response conversion regressions in the
// external Skill suite without exposing implementation details in production.
var (
	CanonicalForTest               = canonical
	SkillObservationForTest        = skillObservation
	SkillVersionObservationForTest = skillVersionObservation
)

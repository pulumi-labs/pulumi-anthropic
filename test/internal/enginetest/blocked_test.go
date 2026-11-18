package enginetest

import "testing"

func TestBlockedLiveGate(t *testing.T) {
	oldLive, oldInclude := *live, *includeBlocked
	defer func() { *live, *includeBlocked = oldLive, oldInclude }()
	for _, tc := range []struct {
		name                string
		live, include, skip bool
	}{
		{"offline", false, false, true},
		{"blocked flag alone", false, true, true},
		{"ordinary live", true, false, true},
		{"explicit blocked live", true, true, false},
	} {
		*live, *includeBlocked = tc.live, tc.include
		skipped, completed := false, false
		t.Run(tc.name, func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			RequireBlockedLive(t, "synthetic blocker")
			completed = true
		})
		if skipped != tc.skip || completed == tc.skip {
			t.Fatalf("%s: skipped=%v completed=%v", tc.name, skipped, completed)
		}
	}
}

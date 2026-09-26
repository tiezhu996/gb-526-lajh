package service

import (
	"regexp"
	"testing"
)

var planCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{2,39}$`)

func TestReusePlanCode(t *testing.T) {
	cases := []struct {
		name       string
		sourceCode string
		profileID  uint
		attempt    int
		want       string
	}{
		{name: "first reuse appends profile suffix", sourceCode: "TRAIN-30A", profileID: 7, attempt: 1, want: "TRAIN-30A-R7"},
		{name: "second attempt adds counter", sourceCode: "TRAIN-30A", profileID: 7, attempt: 2, want: "TRAIN-30A-R7-2"},
		{name: "exactly forty characters is kept", sourceCode: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", profileID: 12, attempt: 1, want: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-R12"},
		{name: "long source code is truncated to forty characters", sourceCode: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789AB", profileID: 12, attempt: 1, want: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-R12"},
		{name: "truncation strips trailing hyphen", sourceCode: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-9", profileID: 3, attempt: 1, want: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-R3"},
		{name: "long source with counter stays within limit", sourceCode: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789AB", profileID: 12, attempt: 3, want: "ABCDEFGHIJKLMNOPQRSTUVWXYZ01234567-R12-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reusePlanCode(tc.sourceCode, tc.profileID, tc.attempt)
			if got != tc.want {
				t.Fatalf("reusePlanCode(%q, %d, %d) = %q, want %q", tc.sourceCode, tc.profileID, tc.attempt, got, tc.want)
			}
			if len(got) > 40 {
				t.Fatalf("reusePlanCode(%q, %d, %d) = %q exceeds 40 characters", tc.sourceCode, tc.profileID, tc.attempt, got)
			}
			if !planCodePattern.MatchString(got) {
				t.Fatalf("reusePlanCode(%q, %d, %d) = %q fails the plan code pattern", tc.sourceCode, tc.profileID, tc.attempt, got)
			}
		})
	}
}

package policy

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_ValidPolicy(t *testing.T) {
	data := []byte(`version: 1
rules:
  - match: "myapp/*"
    keep_last: 3
    older_than: 30d
    protect_tags:
      - latest
      - "v[0-9]+\\.[0-9]+\\.[0-9]+"`)

	policy, err := Load(data)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if policy.Version != 1 {
		t.Fatalf("Version mismatch: got %d want 1", policy.Version)
	}
	if len(policy.Rules) != 1 {
		t.Fatalf("Rule count mismatch: got %d want 1", len(policy.Rules))
	}
	if policy.Rules[0].KeepLast != 3 {
		t.Fatalf("KeepLast mismatch: got %d want 3", policy.Rules[0].KeepLast)
	}
	if policy.Rules[0].OlderThan != "30d" {
		t.Fatalf("OlderThan mismatch: got %q want %q", policy.Rules[0].OlderThan, "30d")
	}
	if len(policy.Rules[0].ProtectTags) != 2 {
		t.Fatalf("ProtectTags mismatch: got %d want 2", len(policy.Rules[0].ProtectTags))
	}
}

func TestLoad_StrictRejectsUnknownFields(t *testing.T) {
	data := []byte(`version: 1
rules:
  - match: "myapp/*"
    unknown_field: true`)

	_, err := Load(data)
	if err == nil {
		t.Fatal("Load() accepted an unknown field")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("error should mention unknown field, got %v", err)
	}
}

func TestParseOlderThan(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "days", input: "30d", want: 30 * 24 * time.Hour},
		{name: "hours", input: "12h", want: 12 * time.Hour},
		{name: "minutes", input: "45m", want: 45 * time.Minute},
		{name: "bad", input: "nope", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOlderThan(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("parseOlderThan() unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOlderThan() returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseOlderThan(%q) = %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestValidatePolicy_RejectsInvalidRules(t *testing.T) {
	tests := []struct {
		name string
		p    Policy
		want string
	}{
		{name: "version", p: Policy{Version: 2, Rules: []Rule{{Match: "myapp/*", KeepLast: 1}}}, want: "expected 1"},
		{name: "empty rules", p: Policy{Version: 1}, want: "no rules configured"},
		{name: "empty match", p: Policy{Version: 1, Rules: []Rule{{Match: "", KeepLast: 1}}}, want: "match is required"},
		{name: "bad glob", p: Policy{Version: 1, Rules: []Rule{{Match: "[", KeepLast: 1}}}, want: "invalid glob"},
		{name: "bad keep_last", p: Policy{Version: 1, Rules: []Rule{{Match: "myapp/*", KeepLast: 0}}}, want: "keep_last must be >= 1"},
		{name: "bad duration", p: Policy{Version: 1, Rules: []Rule{{Match: "myapp/*", KeepLast: 1, OlderThan: "bogus"}}}, want: "invalid older_than"},
		{name: "bad regex", p: Policy{Version: 1, Rules: []Rule{{Match: "myapp/*", KeepLast: 1, ProtectTags: []string{"["}}}}, want: "invalid protect_tags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePolicy(tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validatePolicy() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRuleKeepLast_DefaultsToOne(t *testing.T) {
	if got := ruleKeepLast(Rule{}); got != 1 {
		t.Fatalf("ruleKeepLast(Rule{}) = %d want 1", got)
	}
}

func TestParseOlderThan_UppercaseAndEmpty(t *testing.T) {
	if got, err := parseOlderThan("7D"); err != nil || got != 7*24*time.Hour {
		t.Fatalf("parseOlderThan(7D) = (%v, %v) want (%v, nil)", got, err, 7*24*time.Hour)
	}
	if _, err := parseOlderThan(""); err == nil {
		t.Fatal("parseOlderThan(\"\") unexpectedly succeeded")
	}
}

func TestEvaluateRepository_InvalidOlderThanReturnsError(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{{Name: "v1", Digest: "sha256:a", Created: now.Add(-2 * 24 * time.Hour)}}
	_, err := EvaluateRepository(now, "myapp/web", tags, Rule{Match: "myapp/*", KeepLast: 1, OlderThan: "bad"})
	if err == nil {
		t.Fatal("EvaluateRepository() unexpectedly accepted an invalid duration")
	}
}

func TestEvaluateRepository_ProtectAndKeepLast(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "latest", Digest: "sha256:aaa", Created: now.Add(-12 * time.Hour)},
		{Name: "build-2", Digest: "sha256:bbb", Created: now.Add(-24 * time.Hour)},
		{Name: "build-1", Digest: "sha256:ccc", Created: now.Add(-48 * time.Hour)},
		{Name: "build-0", Digest: "sha256:ddd", Created: now.Add(-72 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 2, ProtectTags: []string{"latest"}}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}

	if got := countActions(plan.Tags, "delete"); got != 2 {
		t.Fatalf("delete count = %d want 2", got)
	}
	if got := countActions(plan.Tags, "keep"); got != 2 {
		t.Fatalf("keep count = %d want 2", got)
	}
	if !hasReason(plan.Tags, "latest", "protected") {
		t.Fatal("latest tag was not protected")
	}
	if !hasReason(plan.Tags, "build-2", "keep_last") {
		t.Fatal("newest non-protected tag was not kept by keep_last")
	}
}

func TestEvaluateRepository_UsesOlderThanThreshold(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "newest", Digest: "sha256:aaa", Created: now.Add(-12 * time.Hour)},
		{Name: "young", Digest: "sha256:bbb", Created: now.Add(-10 * 24 * time.Hour)},
		{Name: "old", Digest: "sha256:ccc", Created: now.Add(-45 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1, OlderThan: "30d"}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if !hasReason(plan.Tags, "young", "younger-than-threshold") {
		t.Fatal("young tag should be kept because it is younger than the threshold")
	}
	if !hasAction(plan.Tags, "old", "delete") {
		t.Fatal("old tag should be a delete candidate")
	}
}

func TestEvaluateRepository_SharedDigestStaysKept(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "latest", Digest: "sha256:shared", Created: now.Add(-1 * time.Hour)},
		{Name: "rollout", Digest: "sha256:shared", Created: now.Add(-12 * time.Hour)},
		{Name: "old", Digest: "sha256:other", Created: now.Add(-47 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1, ProtectTags: []string{"latest"}}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if !hasReason(plan.Tags, "rollout", "shares-digest-with-kept-tag") {
		t.Fatal("rollout should remain kept because it shares a digest with latest")
	}
	if hasAction(plan.Tags, "old", "delete") == false {
		t.Fatal("old should still be deletable when it does not share the protected digest")
	}
}

func TestEvaluateRepository_DefensiveFloorKeepsNewest(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "v1", Digest: "sha256:one", Created: now.Add(-48 * time.Hour)},
		{Name: "v2", Digest: "sha256:two", Created: now.Add(-24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if !hasReason(plan.Tags, "v2", "keep_last") {
		t.Fatal("newest tag should be kept by keep_last")
	}
}

func TestEvaluateRepository_UnknownAgeIsNeverDeleted(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "current", Digest: "sha256:current", Created: now.Add(-12 * time.Hour)},
		{Name: "epoch", Digest: "sha256:epoch", Created: time.Unix(0, 0).UTC()},
		{Name: "old", Digest: "sha256:old", Created: now.Add(-100 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if !hasReason(plan.Tags, "epoch", "unknown-age") {
		t.Fatal("epoch-created tag should never be deleted")
	}
	if !hasAction(plan.Tags, "old", "delete") {
		t.Fatal("non-epoch older tag should be eligible for delete")
	}
}

func TestEvaluatePolicy_NoMatchingRuleSkipsRepo(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	policy := Policy{Version: 1, Rules: []Rule{{Match: "other/*", KeepLast: 1}}}
	tags := []Tag{{Name: "tag", Digest: "sha256:123", Created: now.Add(-3 * 24 * time.Hour)}}

	plan, err := EvaluatePolicy(now, policy, "myapp/web", tags)
	if err != nil {
		t.Fatalf("EvaluatePolicy() error: %v", err)
	}
	if plan.SkippedReason != "no-matching-rule" {
		t.Fatalf("SkippedReason = %q want %q", plan.SkippedReason, "no-matching-rule")
	}
}

func TestInvariant_I1_DryRunDefault(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "newest", Digest: "sha256:new", Created: now.Add(-1 * time.Hour)},
		{Name: "oldest", Digest: "sha256:old", Created: now.Add(-30 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if countActions(plan.Tags, "delete") == 0 {
		t.Fatal("dry-run plan should still contain a delete candidate")
	}
}

func TestInvariant_I2_ProtectedTagsNeverDeleted(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "latest", Digest: "sha256:latest", Created: now.Add(-2 * time.Hour)},
		{Name: "v1.0.0", Digest: "sha256:v1", Created: now.Add(-5 * 24 * time.Hour)},
		{Name: "v0.9.0", Digest: "sha256:v0", Created: now.Add(-45 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1, ProtectTags: []string{"latest", "v[0-9]+\\.[0-9]+\\.[0-9]+"}}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if hasAction(plan.Tags, "latest", "delete") {
		t.Fatal("latest must never be deleted")
	}
	if hasAction(plan.Tags, "v1.0.0", "delete") {
		t.Fatal("version tag must never be deleted when protected")
	}
}

func TestInvariant_I3_NewestTagNeverDeleted(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "v3", Digest: "sha256:3", Created: now.Add(-2 * time.Hour)},
		{Name: "v2", Digest: "sha256:2", Created: now.Add(-6 * time.Hour)},
		{Name: "v1", Digest: "sha256:1", Created: now.Add(-20 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if hasAction(plan.Tags, "v3", "delete") {
		t.Fatal("newest tag must never be deleted")
	}
}

func TestInvariant_I4_SharesDigestWithKeptTag(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "latest", Digest: "sha256:shared", Created: now.Add(-2 * time.Hour)},
		{Name: "rollout", Digest: "sha256:shared", Created: now.Add(-12 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1, ProtectTags: []string{"latest"}}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if hasAction(plan.Tags, "rollout", "delete") {
		t.Fatal("shared digest tag must stay kept when one tagged reference is protected")
	}
	if !hasReason(plan.Tags, "rollout", "shares-digest-with-kept-tag") {
		t.Fatal("shared digest tag should be marked as shares-digest-with-kept-tag")
	}
}

func TestInvariant_I5_UnknownAgeNeverDeleted(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "unknown", Digest: "sha256:epoch", Created: time.Unix(0, 0).UTC()},
		{Name: "current", Digest: "sha256:current", Created: now.Add(-12 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if hasAction(plan.Tags, "unknown", "delete") {
		t.Fatal("unknown-age tag must not be deleted")
	}
}

func TestInvariant_I6_NoMatchingRuleSkipped(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	policy := Policy{Version: 1, Rules: []Rule{{Match: "other/*", KeepLast: 1}}}
	tags := []Tag{{Name: "tag", Digest: "sha256:123", Created: now.Add(-3 * 24 * time.Hour)}}

	plan, err := EvaluatePolicy(now, policy, "myapp/web", tags)
	if err != nil {
		t.Fatalf("EvaluatePolicy() error: %v", err)
	}
	if plan.SkippedReason == "" {
		t.Fatal("repository without a matching rule should be skipped")
	}
}

func TestInvariant_I7_MaxDeleteWouldAbort(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "n1", Digest: "sha256:1", Created: now.Add(-10 * 24 * time.Hour)},
		{Name: "n2", Digest: "sha256:2", Created: now.Add(-11 * 24 * time.Hour)},
		{Name: "n3", Digest: "sha256:3", Created: now.Add(-12 * 24 * time.Hour)},
		{Name: "n4", Digest: "sha256:4", Created: now.Add(-13 * 24 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}
	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if len(plan.DigestsToDelete) <= 2 {
		t.Fatalf("plan should exceed a small max-delete threshold in a dry-run scenario, got %d", len(plan.DigestsToDelete))
	}
}

func TestInvariant_I8_InspectErrorIsNotDeleted(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	tags := []Tag{
		{Name: "unknown", Digest: "sha256:missing", Created: time.Time{}},
		{Name: "keep", Digest: "sha256:keep", Created: now.Add(-2 * time.Hour)},
	}
	rule := Rule{Match: "myapp/*", KeepLast: 1}

	plan, err := EvaluateRepository(now, "myapp/web", tags, rule)
	if err != nil {
		t.Fatalf("EvaluateRepository() error: %v", err)
	}
	if hasAction(plan.Tags, "unknown", "delete") {
		t.Fatal("unknown inspection outcome should never delete")
	}
}

func countActions(decisions []TagDecision, action string) int {
	count := 0
	for _, d := range decisions {
		if d.Action == action {
			count++
		}
	}
	return count
}

func hasAction(decisions []TagDecision, tag, action string) bool {
	for _, d := range decisions {
		if d.Tag == tag && d.Action == action {
			return true
		}
	}
	return false
}

func hasReason(decisions []TagDecision, tag, reason string) bool {
	for _, d := range decisions {
		if d.Tag == tag && d.Reason == reason {
			return true
		}
	}
	return false
}

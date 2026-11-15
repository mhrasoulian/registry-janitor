package policy

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v3"
)

// Policy configures repository pruning behavior for a registry.
type Policy struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Rule matches a repository and controls the keep/delete calculation.
type Rule struct {
	Match       string   `yaml:"match"`
	KeepLast    int      `yaml:"keep_last"`
	OlderThan   string   `yaml:"older_than,omitempty"`
	ProtectTags []string `yaml:"protect_tags,omitempty"`
}

// Tag describes a single repository tag as it exists in the registry.
type Tag struct {
	Name    string
	Digest  string
	Created time.Time
}

// TagDecision is the computed action for one tag.
type TagDecision struct {
	Tag     string
	Digest  string
	Created time.Time
	Age     time.Duration
	Action  string
	Reason  string
}

// RepositoryPlan is the policy output for a single repository.
type RepositoryPlan struct {
	Repo            string
	Tags            []TagDecision
	DigestsToDelete []string
	SkippedReason   string
}

// Plan is the full evaluation result for one or more repositories.
type Plan struct {
	Repositories []RepositoryPlan
}

func Load(data []byte) (Policy, error) {
	var policy Policy
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, err
	}
	if err := validatePolicy(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func validatePolicy(p Policy) error {
	if p.Version != 1 {
		return fmt.Errorf("version: expected 1, got %d", p.Version)
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("rules: no rules configured")
	}
	for i, rule := range p.Rules {
		if err := validateRule(i, rule); err != nil {
			return err
		}
	}
	return nil
}

func validateRule(index int, rule Rule) error {
	if strings.TrimSpace(rule.Match) == "" {
		return fmt.Errorf("rule %d: match is required", index)
	}
	if _, err := path.Match(rule.Match, "example/repo"); err != nil {
		return fmt.Errorf("rule %d: invalid glob %q: %w", index, rule.Match, err)
	}
	if rule.KeepLast < 1 {
		return fmt.Errorf("rule %d: keep_last must be >= 1", index)
	}
	if rule.OlderThan != "" {
		if _, err := parseOlderThan(rule.OlderThan); err != nil {
			return fmt.Errorf("rule %d: invalid older_than %q: %w", index, rule.OlderThan, err)
		}
	}
	for i, pattern := range rule.ProtectTags {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("rule %d: invalid protect_tags[%d] %q: %w", index, i, pattern, err)
		}
	}
	return nil
}

func parseOlderThan(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(trimmed, "d") || strings.HasSuffix(trimmed, "D") {
		numberPart := strings.TrimSuffix(trimmed, "d")
		numberPart = strings.TrimSuffix(numberPart, "D")
		if numberPart == "" {
			return 0, fmt.Errorf("invalid day duration %q", raw)
		}
		days, err := strconv.Atoi(numberPart)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid day duration %q", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(trimmed)
}

func EvaluatePolicy(now time.Time, policy Policy, repo string, tags []Tag) (RepositoryPlan, error) {
	for _, rule := range policy.Rules {
		if matchesRule(repo, rule.Match) {
			return EvaluateRepository(now, repo, tags, rule)
		}
	}
	return RepositoryPlan{Repo: repo, SkippedReason: "no-matching-rule"}, nil
}

func EvaluateRepository(now time.Time, repo string, tags []Tag, rule Rule) (RepositoryPlan, error) {
	if _, err := parseOlderThan(rule.OlderThan); err != nil && rule.OlderThan != "" {
		return RepositoryPlan{}, err
	}

	sorted := append([]Tag(nil), tags...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].Created.Equal(sorted[j].Created) {
			return sorted[i].Created.After(sorted[j].Created)
		}
		return strings.Compare(sorted[i].Name, sorted[j].Name) < 0
	})

	keepReasons := make(map[string]string)
	for _, tag := range sorted {
		if isUnknownAge(tag.Created) {
			keepReasons[tag.Name] = "unknown-age"
		}
	}
	for _, tag := range sorted {
		if _, exists := keepReasons[tag.Name]; exists {
			continue
		}
		if matchesProtectTag(tag.Name, rule.ProtectTags) {
			keepReasons[tag.Name] = "protected"
		}
	}
	for i, tag := range sorted {
		if i >= ruleKeepLast(rule) {
			continue
		}
		if _, exists := keepReasons[tag.Name]; !exists {
			keepReasons[tag.Name] = "keep_last"
		}
	}
	if rule.OlderThan != "" {
		threshold, err := parseOlderThan(rule.OlderThan)
		if err != nil {
			return RepositoryPlan{}, err
		}
		for _, tag := range sorted {
			if _, exists := keepReasons[tag.Name]; exists {
				continue
			}
			if now.Sub(tag.Created) < threshold {
				keepReasons[tag.Name] = "younger-than-threshold"
			}
		}
	}

	deleteCandidates := make(map[string]bool)
	for _, tag := range sorted {
		if _, exists := keepReasons[tag.Name]; !exists {
			deleteCandidates[tag.Name] = true
		}
	}

	for _, tag := range sorted {
		if _, exists := deleteCandidates[tag.Name]; !exists {
			continue
		}
		for _, same := range findSameDigestTags(sorted, tag.Digest) {
			if _, candidate := deleteCandidates[same]; !candidate {
				for _, tagName := range findSameDigestTags(sorted, tag.Digest) {
					if _, ok := deleteCandidates[tagName]; ok {
						delete(deleteCandidates, tagName)
						keepReasons[tagName] = "shares-digest-with-kept-tag"
					}
				}
				break
			}
		}
	}

	if len(deleteCandidates) == len(sorted) && len(sorted) > 0 {
		newest := sorted[0].Name
		delete(deleteCandidates, newest)
		keepReasons[newest] = "defensive-floor"
	}

	decisionMap := make(map[string]TagDecision)
	for _, tag := range sorted {
		d := TagDecision{
			Tag:     tag.Name,
			Digest:  tag.Digest,
			Created: tag.Created,
			Age:     now.Sub(tag.Created),
		}
		if isUnknownAge(tag.Created) {
			d.Age = 0
		}
		if _, deleteCandidate := deleteCandidates[tag.Name]; deleteCandidate {
			d.Action = "delete"
			d.Reason = "delete-candidate"
		} else if reason, ok := keepReasons[tag.Name]; ok {
			d.Action = "keep"
			d.Reason = reason
		} else {
			d.Action = "keep"
			d.Reason = "keep"
		}
		decisionMap[tag.Name] = d
	}

	out := make([]TagDecision, 0, len(sorted))
	for _, tag := range sorted {
		out = append(out, decisionMap[tag.Name])
	}

	deleteDigests := make(map[string]struct{})
	for _, tag := range sorted {
		if _, ok := deleteCandidates[tag.Name]; ok {
			deleteDigests[tag.Digest] = struct{}{}
		}
	}
	deletions := make([]string, 0, len(deleteDigests))
	for digest := range deleteDigests {
		deletions = append(deletions, digest)
	}
	sort.Strings(deletions)

	return RepositoryPlan{Repo: repo, Tags: out, DigestsToDelete: deletions}, nil
}

func matchesRule(repo, pattern string) bool {
	matched, err := path.Match(pattern, repo)
	return err == nil && matched
}

func ruleKeepLast(rule Rule) int {
	if rule.KeepLast < 1 {
		return 1
	}
	return rule.KeepLast
}

func matchesProtectTag(tagName string, protectTags []string) bool {
	for _, pattern := range protectTags {
		if matched, err := regexp.MatchString("^(?:"+pattern+")$", tagName); err == nil && matched {
			return true
		}
	}
	return false
}

func isUnknownAge(created time.Time) bool {
	if created.IsZero() {
		return true
	}
	return created.Equal(time.Unix(0, 0).UTC())
}

func findSameDigestTags(tags []Tag, digest string) []string {
	var names []string
	for _, tag := range tags {
		if tag.Digest == digest {
			names = append(names, tag.Name)
		}
	}
	return names
}

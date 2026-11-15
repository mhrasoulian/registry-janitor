package prune

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/mhrasoulian/registry-janitor/internal/output"
	"github.com/mhrasoulian/registry-janitor/internal/policy"
	"github.com/mhrasoulian/registry-janitor/internal/registry"
)

// Plan is a collection of tag actions for a registry snapshot.
type Plan struct {
	Entries []output.Entry
}

func BuildPlan(ctx context.Context, client registry.Client, repos []string, p policy.Policy, now time.Time) ([]Plan, error) {
	plans := make([]Plan, 0, len(repos))
	for _, repo := range repos {
		entryPlan := Plan{}
		tagList, err := client.Tags(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("listing tags for %s: %w", repo, err)
		}
		if len(tagList) == 0 {
			continue
		}

		items := make([]policy.Tag, 0, len(tagList))
		inspectFailed := false
		for _, tag := range tagList {
			info, err := client.Inspect(ctx, repo, tag)
			if err != nil {
				slog.WarnContext(ctx, "tag inspection failed; skipping repository", "repository", repo, "tag", tag)
				entryPlan.Entries = append(entryPlan.Entries, output.Entry{
					Repository: repo,
					Tag:        tag,
					Created:    "unknown",
					Age:        "unknown",
					Action:     "skip",
					Reason:     "inspect-error",
				})
				inspectFailed = true
				break
			}
			items = append(items, policy.Tag{Name: tag, Digest: info.Digest, Created: info.Created})
		}
		if inspectFailed {
			plans = append(plans, entryPlan)
			continue
		}

		repoPlan, err := policy.EvaluatePolicy(now, p, repo, items)
		if err != nil {
			return nil, err
		}
		if repoPlan.SkippedReason != "" {
			continue
		}
		for _, decision := range repoPlan.Tags {
			entryPlan.Entries = append(entryPlan.Entries, output.Entry{
				Repository: repo,
				Tag:        decision.Tag,
				Digest:     decision.Digest,
				Created:    formatTimestamp(decision.Created),
				Age:        formatAge(decision.Age),
				Action:     decision.Action,
				Reason:     decision.Reason,
			})
		}
		plans = append(plans, entryPlan)
	}
	return plans, nil
}

func formatAge(duration time.Duration) string {
	if duration < 0 {
		return "0s"
	}
	if duration < time.Minute {
		return "<1m"
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
	if duration < 24*time.Hour {
		return fmt.Sprintf("%dh", int(duration.Hours()))
	}
	return fmt.Sprintf("%dd", int(duration.Hours()/24))
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

// Execute deletes digest-by-digest and returns counts for deleted/failed/kept.
func Execute(ctx context.Context, client registry.Client, repos []string, p policy.Policy, now time.Time, maxDelete int, dryRun bool) (int, int, int, []output.Entry, error) {
	if maxDelete < 0 {
		return 0, 0, 0, nil, fmt.Errorf("max-delete must be >= 0")
	}

	plan, err := BuildPlan(ctx, client, repos, p, now)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	allEntries := make([]output.Entry, 0)
	for _, repoPlan := range plan {
		allEntries = append(allEntries, repoPlan.Entries...)
	}
	if dryRun {
		return 0, 0, 0, allEntries, nil
	}

	deleteDigests := make(map[string]struct{})
	for _, entry := range allEntries {
		if entry.Action == "delete" {
			deleteDigests[entry.Repository+"@"+entry.Digest] = struct{}{}
		}
	}
	if len(deleteDigests) > maxDelete {
		return 0, 0, 0, allEntries, fmt.Errorf("planned deletions (%d) exceed max-delete (%d)", len(deleteDigests), maxDelete)
	}

	deleted := 0
	failed := 0
	kept := 0
	for _, entry := range allEntries {
		if entry.Action == "keep" {
			kept++
		}
	}
	deletedDigests := make(map[string]struct{})
	for _, entry := range allEntries {
		if entry.Action != "delete" {
			continue
		}
		key := entry.Repository + "@" + entry.Digest
		if _, alreadyAttempted := deletedDigests[key]; alreadyAttempted {
			continue
		}
		deletedDigests[key] = struct{}{}
		if err := client.DeleteManifest(ctx, entry.Repository, entry.Digest); err != nil {
			failed++
			continue
		}
		deleted++
	}
	return deleted, failed, kept, allEntries, nil
}

func RepositoriesFromCatalog(ctx context.Context, client registry.Client, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	return client.Repositories(ctx)
}

func OrderedRepos(repos []string) []string {
	ordered := append([]string(nil), repos...)
	sort.Strings(ordered)
	return ordered
}

func NormalizeRepos(repoNames []string) []string {
	cleaned := make([]string, 0, len(repoNames))
	seen := map[string]struct{}{}
	for _, repo := range repoNames {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		cleaned = append(cleaned, repo)
	}
	return cleaned
}

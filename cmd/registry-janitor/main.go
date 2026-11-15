package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mhrasoulian/registry-janitor/internal/output"
	"github.com/mhrasoulian/registry-janitor/internal/policy"
	"github.com/mhrasoulian/registry-janitor/internal/prune"
	"github.com/mhrasoulian/registry-janitor/internal/registry"
)

var version = "dev"

var newRegistryClient = func(host string, insecure bool) registry.Client {
	return registry.NewClient(host, insecure)
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	return e.err.Error()
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		var exitErr exitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.err)
			os.Exit(commandExitCode(err))
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(commandExitCode(err))
	}
}

var rootCmd = newRootCommand()

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "registry-janitor",
		Short:         "List and prune OCI image tags in a registry",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List tags in one or more repositories",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd)
		},
	}
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Build and optionally apply a tag-pruning plan",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd)
		},
	}
	versionCommand := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
	root.AddCommand(versionCommand)
	root.AddCommand(list)
	root.AddCommand(prune)

	list.Flags().String("registry", "", "registry host[:port]")
	list.Flags().StringSlice("repo", nil, "repository names to inspect")
	list.Flags().Bool("insecure", false, "allow insecure HTTP")
	list.Flags().String("output", "table", "output format: table|json")

	prune.Flags().String("registry", "", "registry host[:port]")
	prune.Flags().String("policy", "", "path to the YAML prune policy")
	prune.Flags().StringSlice("repo", nil, "repositories to inspect")
	prune.Flags().Bool("insecure", false, "allow insecure HTTP")
	prune.Flags().Bool("execute", false, "apply the plan")
	prune.Flags().Int("max-delete", 100, "maximum number of digests to delete at once")
	prune.Flags().String("output", "table", "output format: table|json")

	return root
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr exitError
	if errors.As(err, &exitErr) {
		return exitErr.code
	}
	return 1
}

func runList(cmd *cobra.Command) error {
	registryHost, err := cmd.Flags().GetString("registry")
	if err != nil {
		return err
	}
	if strings.TrimSpace(registryHost) == "" {
		return exitError{code: 2, err: fmt.Errorf("--registry is required")}
	}

	insecure, err := cmd.Flags().GetBool("insecure")
	if err != nil {
		return err
	}
	outputType, err := cmd.Flags().GetString("output")
	if err != nil {
		return err
	}
	if err := validateOutputFormat(outputType); err != nil {
		return err
	}

	repos, err := cmd.Flags().GetStringSlice("repo")
	if err != nil {
		return err
	}
	client := newRegistryClient(registryHost, insecure)
	if len(repos) == 0 {
		repos, err = client.Repositories(cmd.Context())
		if err != nil {
			return err
		}
	}

	type entry struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Digest     string `json:"digest"`
		Created    string `json:"created"`
	}
	entries := make([]entry, 0)
	inspectionFailures := 0
	for _, repo := range repos {
		tags, err := client.Tags(cmd.Context(), repo)
		if err != nil {
			return err
		}
		sort.Strings(tags)
		for _, tag := range tags {
			info, err := client.Inspect(cmd.Context(), repo, tag)
			if err != nil {
				slog.WarnContext(cmd.Context(), "tag inspection failed", "repository", repo, "tag", tag)
				inspectionFailures++
				continue
			}
			entries = append(entries, entry{
				Repository: repo,
				Tag:        tag,
				Digest:     info.Digest,
				Created:    formatCreated(info.Created),
			})
		}
	}

	switch outputType {
	case "json":
		payload, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(payload)); err != nil {
			return err
		}
	case "table":
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "REPOSITORY\tTAG\tDIGEST\tCREATED")
		for _, item := range entries {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.Repository, item.Tag, item.Digest, item.Created)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	if inspectionFailures > 0 {
		return exitError{code: 1, err: fmt.Errorf("%d tag inspection(s) failed", inspectionFailures)}
	}
	return nil
}

func runPrune(cmd *cobra.Command) error {
	registryHost, err := cmd.Flags().GetString("registry")
	if err != nil {
		return err
	}
	if strings.TrimSpace(registryHost) == "" {
		return exitError{code: 2, err: fmt.Errorf("--registry is required")}
	}
	outputType, err := cmd.Flags().GetString("output")
	if err != nil {
		return err
	}
	if err := validateOutputFormat(outputType); err != nil {
		return err
	}
	policyPath, err := cmd.Flags().GetString("policy")
	if err != nil {
		return err
	}
	if strings.TrimSpace(policyPath) == "" {
		return exitError{code: 2, err: fmt.Errorf("--policy is required")}
	}
	insecure, err := cmd.Flags().GetBool("insecure")
	if err != nil {
		return err
	}
	maxDelete, err := cmd.Flags().GetInt("max-delete")
	if err != nil {
		return err
	}
	if maxDelete < 0 {
		return exitError{code: 2, err: fmt.Errorf("--max-delete must be >= 0")}
	}

	policyBytes, err := os.ReadFile(policyPath)
	if err != nil {
		return exitError{code: 2, err: fmt.Errorf("reading policy file %q: %w", policyPath, err)}
	}
	cfg, err := policy.Load(policyBytes)
	if err != nil {
		return exitError{code: 2, err: err}
	}
	client := newRegistryClient(registryHost, insecure)
	repoNames, err := cmd.Flags().GetStringSlice("repo")
	if err != nil {
		return err
	}
	if len(repoNames) == 0 {
		repoNames, err = client.Repositories(cmd.Context())
		if err != nil {
			return err
		}
	}
	repoNames = prune.NormalizeRepos(repoNames)
	repoNames = prune.OrderedRepos(repoNames)
	if !mustExecute(cmd) {
		plans, err := prune.BuildPlan(cmd.Context(), client, repoNames, cfg, time.Now().UTC())
		if err != nil {
			return err
		}
		entries := make([]output.Entry, 0)
		for _, plan := range plans {
			entries = append(entries, plan.Entries...)
		}
		if err := printPruneOutput(cmd.OutOrStdout(), outputType, entries); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "DRY RUN: nothing was deleted. Re-run with --execute to apply.")
		if skipped := countAction(entries, "skip"); skipped > 0 {
			return exitError{code: 1, err: fmt.Errorf("%d repository inspection(s) failed", skipped)}
		}
		return nil
	}
	deleted, failed, kept, entries, err := prune.Execute(cmd.Context(), client, repoNames, cfg, time.Now().UTC(), maxDelete, false)
	if err != nil && failed == 0 {
		return exitError{code: 2, err: err}
	}
	if err := printPruneOutput(cmd.OutOrStdout(), outputType, entries); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "deleted=%d failed=%d kept=%d\n", deleted, failed, kept)
	var failures []string
	if failed > 0 {
		if err != nil {
			failures = append(failures, fmt.Sprintf("%d manifest deletion(s) failed: %v", failed, err))
		} else {
			failures = append(failures, fmt.Sprintf("%d manifest deletion(s) failed", failed))
		}
	}
	if skipped := countAction(entries, "skip"); skipped > 0 {
		failures = append(failures, fmt.Sprintf("%d repository inspection(s) failed", skipped))
	}
	if len(failures) > 0 {
		return exitError{code: 1, err: errors.New(strings.Join(failures, "; "))}
	}
	return nil
}

func countAction(entries []output.Entry, action string) int {
	count := 0
	for _, entry := range entries {
		if entry.Action == action {
			count++
		}
	}
	return count
}

func validateOutputFormat(outputType string) error {
	if outputType != "table" && outputType != "json" {
		return exitError{code: 2, err: fmt.Errorf("unsupported output format %q: choose table or json", outputType)}
	}
	return nil
}

func mustExecute(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("execute")
	return v
}

func printPruneOutput(w io.Writer, outputType string, entries []output.Entry) error {
	if outputType == "json" {
		payload, err := output.JSON(entries)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(payload))
		return err
	}
	_, err := fmt.Fprintln(w, output.Table(entries))
	return err
}

func formatCreated(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

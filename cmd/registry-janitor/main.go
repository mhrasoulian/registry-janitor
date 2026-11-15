package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
			os.Exit(exitErr.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:           "registry-janitor",
	Short:         "List and prune OCI image tags in a registry",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List tags in one or more repositories",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runList(cmd)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version)
	},
}

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Build and optionally apply a tag-pruning plan",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPrune(cmd)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(pruneCmd)

	listCmd.Flags().String("registry", "", "registry host[:port]")
	listCmd.Flags().StringSlice("repo", nil, "repository names to inspect")
	listCmd.Flags().Bool("insecure", false, "allow insecure HTTP")
	listCmd.Flags().String("output", "table", "output format: table|json")

	pruneCmd.Flags().String("registry", "", "registry host[:port]")
	pruneCmd.Flags().String("policy", "", "path to the YAML prune policy")
	pruneCmd.Flags().StringSlice("repo", nil, "repositories to inspect")
	pruneCmd.Flags().Bool("insecure", false, "allow insecure HTTP")
	pruneCmd.Flags().Bool("execute", false, "apply the plan")
	pruneCmd.Flags().Int("max-delete", 100, "maximum number of digests to delete at once")
	pruneCmd.Flags().String("output", "table", "output format: table|json")
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
	if outputType != "table" && outputType != "json" {
		return exitError{code: 2, err: fmt.Errorf("unsupported output format %q: choose table or json", outputType)}
	}

	repos, err := cmd.Flags().GetStringSlice("repo")
	if err != nil {
		return err
	}
	client := registry.NewClient(registryHost, insecure)
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
	for _, repo := range repos {
		tags, err := client.Tags(cmd.Context(), repo)
		if err != nil {
			return err
		}
		sort.Strings(tags)
		for _, tag := range tags {
			info, err := client.Inspect(cmd.Context(), repo, tag)
			if err != nil {
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
		fmt.Println(string(payload))
	case "table":
		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "REPOSITORY\tTAG\tDIGEST\tCREATED")
		for _, item := range entries {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.Repository, item.Tag, item.Digest, item.Created)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
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
	client := registry.NewClient(registryHost, insecure)
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
	outputType, err := cmd.Flags().GetString("output")
	if err != nil {
		return err
	}
	if !mustExecute(cmd) {
		plans, err := prune.BuildPlan(cmd.Context(), client, repoNames, cfg, time.Now().UTC())
		if err != nil {
			return err
		}
		entries := make([]output.Entry, 0)
		for _, plan := range plans {
			entries = append(entries, plan.Entries...)
		}
		printPruneOutput(outputType, entries)
		fmt.Println("DRY RUN: nothing was deleted. Re-run with --execute to apply.")
		return nil
	}
	deleted, failed, kept, entries, err := prune.Execute(cmd.Context(), client, repoNames, cfg, time.Now().UTC(), maxDelete, false)
	if err != nil {
		return exitError{code: 2, err: err}
	}
	if outputType == "json" {
		printPruneOutput(outputType, entries)
	} else {
		printPruneOutput(outputType, entries)
	}
	fmt.Printf("deleted=%d failed=%d kept=%d\n", deleted, failed, kept)
	return nil
}

func mustExecute(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("execute")
	return v
}

func printPruneOutput(outputType string, entries []output.Entry) {
	if outputType == "json" {
		payload, err := output.JSON(entries)
		if err == nil {
			fmt.Println(string(payload))
			return
		}
	}
	fmt.Println(output.Table(entries))
}

func formatCreated(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

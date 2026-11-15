package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mhrasoulian/registry-janitor/internal/registry"
)

var version = "dev"

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "registry-janitor",
	Short: "List and prune OCI image tags in a registry",
	Version: version,
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

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(listCmd)

	listCmd.Flags().String("registry", "", "registry host[:port]")
	listCmd.Flags().StringSlice("repo", nil, "repository names to inspect")
	listCmd.Flags().Bool("insecure", false, "allow insecure HTTP")
	listCmd.Flags().String("output", "table", "output format: table|json")
}

func runList(cmd *cobra.Command) error {
	registryHost, err := cmd.Flags().GetString("registry")
	if err != nil {
		return err
	}
	if strings.TrimSpace(registryHost) == "" {
		return fmt.Errorf("--registry is required")
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
		return fmt.Errorf("unsupported output format %q: choose table or json", outputType)
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

func formatCreated(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

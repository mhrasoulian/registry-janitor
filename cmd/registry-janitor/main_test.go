package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/mhrasoulian/registry-janitor/internal/registry"
)

type testRegistryClient struct {
	repositories []string
	tags         map[string][]string
	inspect      map[string]registry.InspectResult
	inspectErr   map[string]error
	deleteErr    map[string]error
	deletes      []string
}

func (c *testRegistryClient) Repositories(context.Context) ([]string, error) {
	return append([]string(nil), c.repositories...), nil
}

func (c *testRegistryClient) Tags(_ context.Context, repo string) ([]string, error) {
	return append([]string(nil), c.tags[repo]...), nil
}

func (c *testRegistryClient) Inspect(_ context.Context, repo, tag string) (registry.InspectResult, error) {
	key := repo + ":" + tag
	if err := c.inspectErr[key]; err != nil {
		return registry.InspectResult{}, err
	}
	result, ok := c.inspect[key]
	if !ok {
		return registry.InspectResult{}, fmt.Errorf("missing test inspection for %s", key)
	}
	return result, nil
}

func (c *testRegistryClient) DeleteManifest(_ context.Context, repo, digest string) error {
	key := repo + "@" + digest
	c.deletes = append(c.deletes, key)
	if err := c.deleteErr[key]; err != nil {
		return err
	}
	remaining := c.tags[repo][:0]
	for _, tag := range c.tags[repo] {
		if c.inspect[repo+":"+tag].Digest != digest {
			remaining = append(remaining, tag)
		}
	}
	c.tags[repo] = remaining
	return nil
}

func TestPruneDeletionFailureReturnsExitCodeOne(t *testing.T) {
	root, _, _ := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags: map[string][]string{"demo/app": {"recent", "old1", "old2", "old3"}},
		inspect: map[string]registry.InspectResult{
			"demo/app:recent": {Digest: "sha256:recent", Created: now.Add(-time.Hour)},
			"demo/app:old1":   {Digest: "sha256:old1", Created: now.Add(-2 * time.Hour)},
			"demo/app:old2":   {Digest: "sha256:old2", Created: now.Add(-3 * time.Hour)},
			"demo/app:old3":   {Digest: "sha256:old3", Created: now.Add(-4 * time.Hour)},
		},
		deleteErr: map[string]error{"demo/app@sha256:old2": errors.New("delete failed")},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	var output bytes.Buffer
	err := executeCommand(root, []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", policyPath,
		"--repo", "demo/app", "--execute", "--max-delete", "100", "--output", "table",
	}, &output)
	if commandExitCode(err) != 1 {
		t.Fatalf("runPrune() error = %v, want exit code 1", err)
	}
	if got, want := len(client.deletes), 3; got != want {
		t.Fatalf("delete calls = %d, want %d (continue after failure)", got, want)
	}
	if !strings.Contains(output.String(), "deleted=2 failed=1 kept=1") {
		t.Fatalf("output %q does not contain deletion summary", output.String())
	}
}

func TestInvariant_I1_DryRunDefault(t *testing.T) {
	root, _, pruneCmd := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags: map[string][]string{"demo/app": {"newest", "older"}},
		inspect: map[string]registry.InspectResult{
			"demo/app:newest": {Digest: "sha256:newest", Created: now.Add(-time.Hour)},
			"demo/app:older":  {Digest: "sha256:older", Created: now.Add(-48 * time.Hour)},
		},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	executeFlag := pruneCmd.Flags().Lookup("execute")
	if executeFlag.DefValue != "false" || executeFlag.Value.String() != "false" {
		t.Fatalf("--execute default = %q (value %q), want false", executeFlag.DefValue, executeFlag.Value.String())
	}
	before, err := client.Tags(context.Background(), "demo/app")
	if err != nil {
		t.Fatalf("read initial tags: %v", err)
	}
	policyPath := writeTestPolicy(t)
	var output bytes.Buffer
	err = executeCommand(root, []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", policyPath,
		"--repo", "demo/app",
	}, &output)
	if err != nil {
		t.Fatalf("prune dry-run: %v", err)
	}
	if len(client.deletes) != 0 {
		t.Fatalf("DeleteManifest calls = %v, want zero", client.deletes)
	}
	after, err := client.Tags(context.Background(), "demo/app")
	if err != nil {
		t.Fatalf("read tags after dry-run: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("tags after dry-run = %v, want unchanged %v", after, before)
	}
	const notice = "DRY RUN: nothing was deleted. Re-run with --execute to apply.\n"
	if !strings.HasSuffix(output.String(), notice) {
		t.Fatalf("output does not end with dry-run notice: %q", output.String())
	}
}

func TestInvariant_I7_MaxDeleteAbortsBeforeDeletion(t *testing.T) {
	root, _, _ := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags: map[string][]string{"demo/app": {"newest", "old1", "old2"}},
		inspect: map[string]registry.InspectResult{
			"demo/app:newest": {Digest: "sha256:newest", Created: now.Add(-time.Hour)},
			"demo/app:old1":   {Digest: "sha256:old1", Created: now.Add(-48 * time.Hour)},
			"demo/app:old2":   {Digest: "sha256:old2", Created: now.Add(-72 * time.Hour)},
		},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	before, err := client.Tags(context.Background(), "demo/app")
	if err != nil {
		t.Fatalf("read initial tags: %v", err)
	}
	policyPath := writeTestPolicy(t)
	var output bytes.Buffer
	err = executeCommand(root, []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", policyPath,
		"--repo", "demo/app", "--execute", "--max-delete", "1",
	}, &output)
	if commandExitCode(err) != 2 {
		t.Fatalf("prune error = %v, want exit code 2", err)
	}
	if len(client.deletes) != 0 {
		t.Fatalf("DeleteManifest calls = %v, want zero", client.deletes)
	}
	after, err := client.Tags(context.Background(), "demo/app")
	if err != nil {
		t.Fatalf("read tags after rejected plan: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("tags after rejected plan = %v, want unchanged %v", after, before)
	}
}

func TestPruneJSONOutputIncludesEverySchemaField(t *testing.T) {
	root, _, _ := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags:    map[string][]string{"demo/app": {"newest"}},
		inspect: map[string]registry.InspectResult{"demo/app:newest": {Digest: "sha256:newest", Created: now}},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	var output bytes.Buffer
	err := executeCommand(root, []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", writeTestPolicy(t),
		"--repo", "demo/app", "--output", "json",
	}, &output)
	if err != nil {
		t.Fatalf("prune JSON dry-run: %v", err)
	}
	jsonOutput, _, ok := strings.Cut(output.String(), "\nDRY RUN:")
	if !ok {
		t.Fatalf("output has no dry-run notice: %q", output.String())
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatalf("parse JSON output: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("JSON entries = %d, want 1", len(entries))
	}
	for _, field := range []string{"repository", "tag", "digest", "created", "age", "action", "reason"} {
		if _, ok := entries[0][field]; !ok {
			t.Errorf("JSON entry is missing schema field %q", field)
		}
	}
}

func TestExamplePolicyRunsAgainstSeededRepositories(t *testing.T) {
	root, _, _ := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags: map[string][]string{
			"demo/api":    {"latest", "v1.0.0", "v0.9.0", "v0.8.0", "v0.7.0"},
			"demo/worker": {"latest", "v1.0.0", "v0.9.0", "v0.8.0", "v0.7.0"},
		},
		inspect: map[string]registry.InspectResult{},
	}
	for _, repo := range []string{"demo/api", "demo/worker"} {
		for _, seededTag := range []struct {
			tag    string
			digest string
			age    time.Duration
		}{
			{tag: "latest", digest: "sha256:shared", age: 100 * 24 * time.Hour},
			{tag: "v1.0.0", digest: "sha256:shared", age: 100 * 24 * time.Hour},
			{tag: "v0.9.0", digest: "sha256:recent-old", age: 35 * 24 * time.Hour},
			{tag: "v0.8.0", digest: "sha256:older", age: 65 * 24 * time.Hour},
			{tag: "v0.7.0", digest: "sha256:oldest", age: 100 * 24 * time.Hour},
		} {
			client.inspect[repo+":"+seededTag.tag] = registry.InspectResult{
				Digest: seededTag.digest, Created: now.Add(-seededTag.age),
			}
		}
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	policyPath := filepath.Join("..", "..", "examples", "policy.yaml")
	var output bytes.Buffer
	err := executeCommand(root, []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", policyPath,
		"--repo", "demo/api,demo/worker",
	}, &output)
	if err != nil {
		t.Fatalf("run tool with example policy: %v", err)
	}
	for _, expected := range []string{"demo/api", "demo/worker", "latest", "v1.0.0", "protected"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("example policy output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestInvalidOutputFormatReturnsExitCodeTwoForListAndPrune(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "list",
			args: []string{"list", "--registry", "localhost:5000", "--output", "yaml"},
		},
		{
			name: "prune",
			args: []string{"prune", "--registry", "localhost:5000", "--output", "yaml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _, _ := testCommands(t)
			var output bytes.Buffer
			err := executeCommand(root, tt.args, &output)
			if commandExitCode(err) != 2 {
				t.Fatalf("command error = %v, want exit code 2", err)
			}
			if !strings.Contains(err.Error(), "table or json") {
				t.Fatalf("error %q does not list allowed output values", err)
			}
		})
	}
}

func TestInvariant_I8_InspectionFailureSkipsRepositoryAndContinues(t *testing.T) {
	root, _, _ := testCommands(t)
	now := time.Now().UTC()
	client := &testRegistryClient{
		tags: map[string][]string{
			"demo/bad":  {"broken", "old"},
			"demo/good": {"new", "old"},
		},
		inspect: map[string]registry.InspectResult{
			"demo/bad:old":  {Digest: "sha256:bad-old", Created: now.Add(-48 * time.Hour)},
			"demo/good:new": {Digest: "sha256:good-new", Created: now.Add(-time.Hour)},
			"demo/good:old": {Digest: "sha256:good-old", Created: now.Add(-48 * time.Hour)},
		},
		inspectErr: map[string]error{"demo/bad:broken": errors.New("inspect failed")},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })

	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	pruneArgs := []string{
		"prune", "--registry", "localhost:5000", "--insecure", "--policy", policyPath,
		"--repo", "demo/bad,demo/good", "--execute", "--max-delete", "100", "--output", "table",
	}
	var output bytes.Buffer
	var logs bytes.Buffer
	restoreLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(restoreLogger) })

	err := executeCommand(root, pruneArgs, &output)
	if commandExitCode(err) != 1 {
		t.Fatalf("runPrune() error = %v, want exit code 1", err)
	}
	if got, want := len(client.deletes), 1; got != want {
		t.Fatalf("delete calls = %d, want %d", got, want)
	}
	if client.deletes[0] != "demo/good@sha256:good-old" {
		t.Fatalf("delete calls = %v, want only the healthy repository deletion", client.deletes)
	}
	if got, want := len(client.tags["demo/bad"]), 2; got != want {
		t.Fatalf("bad repository tags remaining = %v, want both tags preserved", client.tags["demo/bad"])
	}
	if !strings.Contains(output.String(), "demo/bad") ||
		!strings.Contains(output.String(), "broken") ||
		!strings.Contains(output.String(), "skip") ||
		!strings.Contains(output.String(), "inspect-error") {
		t.Fatalf("prune output %q does not report the skipped tag", output.String())
	}
	if !strings.Contains(logs.String(), "demo/bad") || !strings.Contains(logs.String(), "broken") {
		t.Fatalf("warning log %q does not name repository and tag", logs.String())
	}
}

func TestListReportsInspectionFailureAndReturnsExitCodeOne(t *testing.T) {
	root, _, _ := testCommands(t)
	client := &testRegistryClient{
		tags: map[string][]string{"demo/app": {"broken", "healthy"}},
		inspect: map[string]registry.InspectResult{
			"demo/app:healthy": {Digest: "sha256:healthy"},
		},
		inspectErr: map[string]error{"demo/app:broken": errors.New("inspect failed")},
	}
	restoreClient := newRegistryClient
	newRegistryClient = func(string, bool) registry.Client { return client }
	t.Cleanup(func() { newRegistryClient = restoreClient })
	var output bytes.Buffer
	var logs bytes.Buffer
	restoreLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(restoreLogger) })

	err := executeCommand(root, []string{
		"list", "--registry", "localhost:5000", "--repo", "demo/app", "--output", "table",
	}, &output)
	if commandExitCode(err) != 1 {
		t.Fatalf("runList() error = %v, want exit code 1", err)
	}
	if !strings.Contains(output.String(), "healthy") {
		t.Fatalf("list output %q omitted the successfully inspected tag", output.String())
	}
	if !strings.Contains(logs.String(), "demo/app") || !strings.Contains(logs.String(), "broken") {
		t.Fatalf("warning log %q does not name repository and tag", logs.String())
	}
}

func testCommands(t *testing.T) (*cobra.Command, *cobra.Command, *cobra.Command) {
	t.Helper()
	root := newRootCommand()
	list, _, err := root.Find([]string{"list"})
	if err != nil {
		t.Fatalf("find list command: %v", err)
	}
	pruneCmd, _, err := root.Find([]string{"prune"})
	if err != nil {
		t.Fatalf("find prune command: %v", err)
	}
	return root, list, pruneCmd
}

func executeCommand(root *cobra.Command, args []string, output io.Writer) error {
	root.SetArgs(args)
	root.SetOut(output)
	root.SetErr(io.Discard)
	return root.Execute()
}

func writeTestPolicy(t *testing.T) string {
	t.Helper()
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return policyPath
}

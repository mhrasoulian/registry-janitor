package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	return c.deleteErr[key]
}

func TestPruneDeletionFailureReturnsExitCodeOne(t *testing.T) {
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
	setPruneFlags(t, policyPath, "true", "100", "table")
	var output bytes.Buffer
	pruneCmd.SetOut(&output)

	err := runPrune(pruneCmd)
	var commandErr exitError
	if !errors.As(err, &commandErr) || commandErr.code != 1 {
		t.Fatalf("runPrune() error = %v, want exit code 1", err)
	}
	if got, want := len(client.deletes), 3; got != want {
		t.Fatalf("delete calls = %d, want %d (continue after failure)", got, want)
	}
	if !strings.Contains(output.String(), "deleted=2 failed=1 kept=1") {
		t.Fatalf("output %q does not contain deletion summary", output.String())
	}
}

func setPruneFlags(t *testing.T, policyPath, execute, maxDelete, outputType string) {
	t.Helper()
	flags := map[string]string{
		"registry":   "localhost:5000",
		"policy":     policyPath,
		"repo":       "demo/app",
		"insecure":   "true",
		"execute":    execute,
		"max-delete": maxDelete,
		"output":     outputType,
	}
	for name, value := range flags {
		if err := pruneCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
}

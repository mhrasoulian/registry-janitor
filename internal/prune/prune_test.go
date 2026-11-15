package prune

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	goregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/mhrasoulian/registry-janitor/internal/policy"
	regclient "github.com/mhrasoulian/registry-janitor/internal/registry"
)

func TestBuildPlanDryRun(t *testing.T) {
	reg := goregistry.New()
	srv := httptest.NewServer(reg)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	client := regclient.NewClient(host, true)
	for _, tag := range []string{"latest", "v1.0.0", "v0.9.0"} {
		img, err := random.Image(1024, 1)
		if err != nil {
			t.Fatalf("random.Image() error: %v", err)
		}
		img, err = mutate.CreatedAt(img, v1.Time{Time: time.Now().Add(-24 * time.Hour)})
		if err != nil {
			t.Fatalf("mutate.CreatedAt() error: %v", err)
		}
		ref, err := name.NewTag(host+"/demo/app:"+tag, name.Insecure)
		if err != nil {
			t.Fatalf("name.NewTag() error: %v", err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("remote.Write() error: %v", err)
		}
	}

	cfg, err := policy.Load([]byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
    protect_tags:
      - latest
`))
	if err != nil {
		t.Fatalf("policy.Load() error: %v", err)
	}
	plans, err := BuildPlan(context.Background(), client, []string{"demo/app"}, cfg, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildPlan() error: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("BuildPlan() returned no plan")
	}
	deleteCount := 0
	for _, plan := range plans {
		for _, entry := range plan.Entries {
			if entry.Action == "delete" {
				deleteCount++
			}
		}
	}
	if deleteCount == 0 {
		t.Fatal("dry-run plan should have at least one delete candidate")
	}
	deleted, failed, kept, _, err := Execute(context.Background(), client, []string{"demo/app"}, cfg, time.Now().UTC(), 100, true)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if deleted != 0 || failed != 0 || kept != 0 {
		t.Fatalf("dry-run should not delete anything: got deleted=%d failed=%d kept=%d", deleted, failed, kept)
	}
}

func TestExecuteRespectsMaxDelete(t *testing.T) {
	reg := goregistry.New()
	srv := httptest.NewServer(reg)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	client := regclient.NewClient(host, true)
	for _, tag := range []string{"latest", "v1.0.0", "v0.9.0"} {
		img, err := random.Image(1024, 1)
		if err != nil {
			t.Fatalf("random.Image() error: %v", err)
		}

		img, err = mutate.CreatedAt(img, v1.Time{Time: time.Now().Add(-24 * time.Hour)})
		if err != nil {
			t.Fatalf("mutate.CreatedAt() error: %v", err)
		}
		ref, err := name.NewTag(host+"/demo/app:"+tag, name.Insecure)
		if err != nil {
			t.Fatalf("name.NewTag() error: %v", err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("remote.Write() error: %v", err)
		}
	}

	cfg, err := policy.Load([]byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
    protect_tags:
      - latest
`))
	if err != nil {
		t.Fatalf("policy.Load() error: %v", err)
	}
	_, _, _, _, err = Execute(context.Background(), client, []string{"demo/app"}, cfg, time.Now().UTC(), 0, false)
	if err == nil {
		t.Fatal("Execute() should reject an over-limit delete plan")
	}
}

type countingClient struct {
	tags    []string
	inspect map[string]regclient.InspectResult
	deletes []string
}

func (c *countingClient) Repositories(context.Context) ([]string, error) {
	return []string{"demo/app"}, nil
}

func (c *countingClient) Tags(context.Context, string) ([]string, error) {
	return append([]string(nil), c.tags...), nil
}

func (c *countingClient) Inspect(_ context.Context, _, tag string) (regclient.InspectResult, error) {
	return c.inspect[tag], nil
}

func (c *countingClient) DeleteManifest(_ context.Context, repo, digest string) error {
	c.deletes = append(c.deletes, repo+"@"+digest)
	return nil
}

func TestExecuteCountsAndDeletesEachCandidateDigestOnce(t *testing.T) {
	now := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	client := &countingClient{
		tags: []string{"newest", "old-a", "old-b"},
		inspect: map[string]regclient.InspectResult{
			"newest": {Digest: "sha256:newest", Created: now.Add(-time.Hour)},
			"old-a":  {Digest: "sha256:shared", Created: now.Add(-48 * time.Hour)},
			"old-b":  {Digest: "sha256:shared", Created: now.Add(-72 * time.Hour)},
		},
	}
	cfg, err := policy.Load([]byte(`version: 1
rules:
  - match: "demo/*"
    keep_last: 1
`))
	if err != nil {
		t.Fatalf("policy.Load() error: %v", err)
	}

	deleted, failed, kept, _, err := Execute(
		context.Background(), client, []string{"demo/app"}, cfg, now, 1, false,
	)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if deleted != 1 || failed != 0 || kept != 1 {
		t.Fatalf("Execute() counts = (%d, %d, %d), want (1, 0, 1)", deleted, failed, kept)
	}
	if len(client.deletes) != 1 || client.deletes[0] != "demo/app@sha256:shared" {
		t.Fatalf("DeleteManifest calls = %v, want the shared digest once", client.deletes)
	}
}

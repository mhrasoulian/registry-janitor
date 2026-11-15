package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	goregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestValidateLocalRegistry(t *testing.T) {
	tests := []struct {
		value string
		want  string
		valid bool
	}{
		{value: "localhost:5000", want: "localhost:5000", valid: true},
		{value: "http://localhost:5000", want: "localhost:5000", valid: true},
		{value: "127.0.0.1:5000", want: "127.0.0.1:5000", valid: true},
		{value: "registry.example.invalid:5000", valid: false},
		{value: "localhost:5000/path", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := validateLocalRegistry(tt.value)
			if tt.valid {
				if err != nil {
					t.Fatalf("validateLocalRegistry() error: %v", err)
				}
				if got != tt.want {
					t.Fatalf("validateLocalRegistry() = %q, want %q", got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatal("validateLocalRegistry() unexpectedly accepted a non-local registry")
			}
		})
	}
}

func TestSeedPushesExpectedTagsAndSharedDigestToLocalRegistry(t *testing.T) {
	server := httptest.NewServer(goregistry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	if err := seed(host, true); err != nil {
		t.Fatalf("seed() error: %v", err)
	}

	for _, repo := range []string{"demo/api", "demo/worker"} {
		repoRef, err := name.NewRepository(host+"/"+repo, name.Insecure)
		if err != nil {
			t.Fatalf("create repository reference: %v", err)
		}
		tags, err := remote.List(repoRef, remote.WithAuth(authn.Anonymous))
		if err != nil {
			t.Fatalf("list tags for %s: %v", repo, err)
		}
		if len(tags) != 5 {
			t.Fatalf("tag count for %s = %d, want 5: %v", repo, len(tags), tags)
		}

		digests := make(map[string]string, len(tags))
		ages := make(map[string]time.Duration, len(tags))
		for _, tag := range tags {
			ref := repoRef.Tag(tag)
			desc, err := remote.Get(ref, remote.WithAuth(authn.Anonymous))
			if err != nil {
				t.Fatalf("get %s:%s: %v", repo, tag, err)
			}
			digests[tag] = desc.Digest.String()
			img, err := desc.Image()
			if err != nil {
				t.Fatalf("read image %s:%s: %v", repo, tag, err)
			}
			config, err := img.ConfigFile()
			if err != nil {
				t.Fatalf("read config %s:%s: %v", repo, tag, err)
			}
			ages[tag] = time.Since(config.Created.Time)
		}

		if digests["latest"] != digests["v1.0.0"] {
			t.Errorf("%s latest and v1.0.0 do not share a digest", repo)
		}
		for tag, wantAge := range map[string]time.Duration{
			"latest": 100 * 24 * time.Hour,
			"v1.0.0": 100 * 24 * time.Hour,
			"v0.9.0": 35 * 24 * time.Hour,
			"v0.8.0": 65 * 24 * time.Hour,
			"v0.7.0": 100 * 24 * time.Hour,
		} {
			if difference := ages[tag] - wantAge; difference < -time.Minute || difference > time.Minute {
				t.Errorf("%s %s age = %v, want about %v", repo, tag, ages[tag], wantAge)
			}
		}
	}
}

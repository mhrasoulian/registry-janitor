package registry

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestInMemoryRegistrySupportsManifestDelete(t *testing.T) {
	reg := registry.New()
	srv := httptest.NewServer(reg)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random.Image() error: %v", err)
	}
	img, err = mutate.CreatedAt(img, v1.Time{Time: time.Now()})
	if err != nil {
		t.Fatalf("mutate.CreatedAt() error: %v", err)
	}
	ref, err := name.NewTag(host+"/demo/app:latest", name.Insecure)
	if err != nil {
		t.Fatalf("name.NewTag() error: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write() error: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest() error: %v", err)
	}
	deleteRef, err := name.NewDigest(host+"/demo/app@" + digest.String())
	if err != nil {
		t.Fatalf("name.NewDigest() error: %v", err)
	}
	if err := remote.Delete(deleteRef, remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatalf("remote.Delete() failed: %v", err)
	}
}

func TestRemoteClient_ListAndInspect(t *testing.T) {
	reg := registry.New()
	srv := httptest.NewServer(reg)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	client := NewClient(host, true)

	for _, tag := range []string{"latest", "v1.0.0"} {
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

	repos, err := client.Repositories(context.Background())
	if err != nil {
		t.Fatalf("Repositories() error: %v", err)
	}
	if len(repos) == 0 {
		t.Fatal("Repositories() returned no repos")
	}

	tags, err := client.Tags(context.Background(), "demo/app")
	if err != nil {
		t.Fatalf("Tags() error: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("Tags() length = %d want 2", len(tags))
	}

	info, err := client.Inspect(context.Background(), "demo/app", "latest")
	if err != nil {
		t.Fatalf("Inspect() error: %v", err)
	}
	if info.Digest == "" {
		t.Fatal("Inspect() returned an empty digest")
	}
	if info.Created.IsZero() {
		t.Fatal("Inspect() returned an unknown creation time")
	}
}

func TestRemoteClientDeleteManifest(t *testing.T) {
	reg := registry.New()
	srv := httptest.NewServer(reg)
	defer srv.Close()
	client := NewClient(strings.TrimPrefix(srv.URL, "http://"), true)

	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random.Image() error: %v", err)
	}
	img, err = mutate.CreatedAt(img, v1.Time{Time: time.Now()})
	if err != nil {
		t.Fatalf("mutate.CreatedAt() error: %v", err)
	}
	ref, err := name.NewTag(strings.TrimPrefix(srv.URL, "http://")+"/demo/app:v1", name.Insecure)
	if err != nil {
		t.Fatalf("name.NewTag() error: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write() error: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest() error: %v", err)
	}
	if err := client.DeleteManifest(context.Background(), "demo/app", digest.String()); err != nil {
		t.Fatalf("DeleteManifest() error: %v", err)
	}
}

var _ v1.Image = empty.Image

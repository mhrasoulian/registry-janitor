package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Client is a minimal OCI Distribution V2 registry client.
type Client interface {
	Repositories(ctx context.Context) ([]string, error)
	Tags(ctx context.Context, repo string) ([]string, error)
	Inspect(ctx context.Context, repo, tag string) (InspectResult, error)
	DeleteManifest(ctx context.Context, repo, digest string) error
}

// InspectResult describes what was discovered for a single tag.
type InspectResult struct {
	Digest  string
	Created time.Time
}

// RemoteClient talks to an OCI Distribution v2 registry over HTTP(S).
type RemoteClient struct {
	Registry string
	Insecure bool
}

func NewClient(registry string, insecure bool) *RemoteClient {
	return &RemoteClient{Registry: normalizeHost(registry), Insecure: insecure}
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	return host
}

func (c *RemoteClient) auth() authn.Authenticator {
	username := strings.TrimSpace(os.Getenv("REGISTRY_USERNAME"))
	password := strings.TrimSpace(os.Getenv("REGISTRY_PASSWORD"))
	if username == "" && password == "" {
		return authn.Anonymous
	}
	return authn.FromConfig(authn.AuthConfig{Username: username, Password: password})
}

func (c *RemoteClient) registryRef() (name.Registry, error) {
	if c.Insecure {
		return name.NewRegistry(c.Registry, name.Insecure)
	}
	return name.NewRegistry(c.Registry)
}

func (c *RemoteClient) repoRef(repo string) (name.Repository, error) {
	reg, err := c.registryRef()
	if err != nil {
		return name.Repository{}, err
	}
	return name.NewRepository(reg.Name() + "/" + repo)
}

func (c *RemoteClient) Repositories(ctx context.Context) ([]string, error) {
	reg, err := c.registryRef()
	if err != nil {
		return nil, err
	}
	catalog, err := remote.Catalog(ctx, reg, remote.WithAuth(c.auth()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if strings.Contains(strings.ToLower(err.Error()), "disabled") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, fmt.Errorf("catalog is disabled; pass --repo to list specific repositories: %w", err)
		}
		return nil, fmt.Errorf("listing repositories: %w", err)
	}
	return catalog, nil
}

func (c *RemoteClient) Tags(ctx context.Context, repo string) ([]string, error) {
	repoRef, err := c.repoRef(repo)
	if err != nil {
		return nil, err
	}
	list, err := remote.List(repoRef, remote.WithContext(ctx), remote.WithAuth(c.auth()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("listing tags for %s: %w", repo, err)
	}
	return list, nil
}

func (c *RemoteClient) Inspect(ctx context.Context, repo, tag string) (InspectResult, error) {
	repoRef, err := c.repoRef(repo)
	if err != nil {
		return InspectResult{}, err
	}
	ref := repoRef.Tag(tag)
	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuth(c.auth()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return InspectResult{}, err
		}
		return InspectResult{}, fmt.Errorf("inspecting %s:%s: %w", repo, tag, err)
	}

	if idx, err := desc.ImageIndex(); err == nil && idx != nil {
		manifest, err := idx.IndexManifest()
		if err != nil {
			return InspectResult{}, fmt.Errorf("reading index manifest for %s:%s: %w", repo, tag, err)
		}
		if len(manifest.Manifests) == 0 {
			return InspectResult{Digest: desc.Digest.String()}, nil
		}
		img, err := idx.Image(manifest.Manifests[0].Digest)
		if err != nil {
			return InspectResult{}, fmt.Errorf("reading child image for %s:%s: %w", repo, tag, err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return InspectResult{Digest: desc.Digest.String()}, nil
		}
		created := time.Time{}
		if !cfg.Created.IsZero() {
			created = cfg.Created.Time
		}
		return InspectResult{Digest: desc.Digest.String(), Created: created}, nil
	}

	img, err := desc.Image()
	if err != nil {
		return InspectResult{}, fmt.Errorf("reading image for %s:%s: %w", repo, tag, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return InspectResult{Digest: desc.Digest.String()}, nil
	}
	created := time.Time{}
	if !cfg.Created.IsZero() {
		created = cfg.Created.Time
	}
	return InspectResult{Digest: desc.Digest.String(), Created: created}, nil
}

func (c *RemoteClient) DeleteManifest(ctx context.Context, repo, digest string) error {
	repoRef, err := c.repoRef(repo)
	if err != nil {
		return err
	}
	digestRef, err := name.NewDigest(repoRef.Name() + "@" + digest)
	if err != nil {
		return fmt.Errorf("creating digest reference for %s@%s: %w", repo, digest, err)
	}
	err = remote.Delete(digestRef, remote.WithContext(ctx), remote.WithAuth(c.auth()))
	if err == nil {
		return nil
	}
	var statusErr *transport.Error
	if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("delete is not enabled for this registry: set REGISTRY_STORAGE_DELETE_ENABLED=true: %w", err)
	}
	return fmt.Errorf("deleting %s@%s: %w", repo, digest, err)
}

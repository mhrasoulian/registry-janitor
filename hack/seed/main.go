package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func main() {
	registryFlag := flag.String("registry", "localhost:5000", "local registry host[:port]")
	insecure := flag.Bool("insecure", false, "use insecure HTTP")
	flag.Parse()

	registryHost, err := validateLocalRegistry(*registryFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := seed(registryHost, *insecure); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validateLocalRegistry(value string) (string, error) {
	value = strings.TrimSpace(value)
	parseValue := value
	if !strings.Contains(value, "://") {
		parseValue = "//" + value
	}
	parsed, err := url.Parse(parseValue)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("--registry must be a local host[:port]")
	}
	if parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("--registry must use http or https")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" {
		return "", fmt.Errorf("--registry must point to localhost or 127.0.0.1")
	}
	return parsed.Host, nil
}

func seed(registryHost string, insecure bool) error {
	options := []name.Option{}
	if insecure {
		options = append(options, name.Insecure)
	}

	for _, repo := range []string{"demo/api", "demo/worker"} {
		shared, err := imageCreatedAt(100 * 24 * time.Hour)
		if err != nil {
			return fmt.Errorf("creating shared image for %s: %w", repo, err)
		}
		for _, tag := range []string{"latest", "v1.0.0"} {
			if err := push(registryHost, repo, tag, shared, options); err != nil {
				return err
			}
		}

		for _, imageTag := range []struct {
			tag string
			age time.Duration
		}{
			{tag: "v0.9.0", age: 35 * 24 * time.Hour},
			{tag: "v0.8.0", age: 65 * 24 * time.Hour},
			{tag: "v0.7.0", age: 100 * 24 * time.Hour},
		} {
			img, err := imageCreatedAt(imageTag.age)
			if err != nil {
				return fmt.Errorf("creating image %s:%s: %w", repo, imageTag.tag, err)
			}
			if err := push(registryHost, repo, imageTag.tag, img, options); err != nil {
				return err
			}
		}
	}
	fmt.Println("Seeded demo/api and demo/worker.")
	return nil
}

func imageCreatedAt(age time.Duration) (v1.Image, error) {
	img, err := random.Image(1024, 1)
	if err != nil {
		return nil, err
	}
	return mutate.CreatedAt(img, v1.Time{Time: time.Now().UTC().Add(-age)})
}

func push(registryHost, repo, tag string, img v1.Image, options []name.Option) error {
	ref, err := name.NewTag(registryHost+"/"+repo+":"+tag, options...)
	if err != nil {
		return fmt.Errorf("creating reference for %s:%s: %w", repo, tag, err)
	}
	if err := remote.Write(ref, img, remote.WithAuth(authn.Anonymous)); err != nil {
		return fmt.Errorf("pushing %s:%s: %w", repo, tag, err)
	}
	return nil
}

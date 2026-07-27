package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type entrypointResult struct {
	command []string
	args    []string
	ts      time.Time
}

// ImageResolver resolves container image ENTRYPOINT/CMD by querying the
// container registry. Results are cached in memory with a TTL.
type ImageResolver struct {
	client      client.Reader
	cacheTTL    time.Duration
	mu          sync.RWMutex
	cache       map[string]entrypointResult
	resolveFunc func(ctx context.Context, image string, pod *corev1.Pod) ([]string, []string, error)
}

func NewImageResolver(c client.Reader, cacheTTL time.Duration) *ImageResolver {
	return &ImageResolver{
		client:   c,
		cacheTTL: cacheTTL,
		cache:    make(map[string]entrypointResult),
	}
}

// Resolve returns the ENTRYPOINT (as command) and CMD (as args) for the given
// image. It reads the image manifest from the registry, authenticating with
// the pod's imagePullSecrets if available.
func (r *ImageResolver) Resolve(ctx context.Context, image string, pod *corev1.Pod) (command, args []string, err error) {
	if r.resolveFunc != nil {
		return r.resolveFunc(ctx, image, pod)
	}

	r.mu.RLock()
	if cached, ok := r.cache[image]; ok && time.Since(cached.ts) < r.cacheTTL {
		r.mu.RUnlock()
		return cached.command, cached.args, nil
	}
	r.mu.RUnlock()

	ref, err := name.ParseReference(image)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing image ref %q: %w", image, err)
	}

	keychain := r.buildKeychain(ctx, pod)

	desc, err := remote.Image(ref, remote.WithAuthFromKeychain(keychain), remote.WithContext(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("fetching image %q: %w", image, err)
	}

	cfg, err := desc.ConfigFile()
	if err != nil {
		return nil, nil, fmt.Errorf("reading config for %q: %w", image, err)
	}

	command = cfg.Config.Entrypoint
	args = cfg.Config.Cmd

	r.mu.Lock()
	r.cache[image] = entrypointResult{command: command, args: args, ts: time.Now()}
	r.mu.Unlock()

	return command, args, nil
}

// FakeEntrypoint holds entrypoint/cmd data for test fakes.
type FakeEntrypoint struct {
	Entrypoint []string
	Cmd        []string
}

// NewFakeResolver creates an ImageResolver backed by a static map for testing.
func NewFakeResolver(entries map[string]FakeEntrypoint) *ImageResolver {
	return &ImageResolver{
		cache: make(map[string]entrypointResult),
		resolveFunc: func(_ context.Context, image string, _ *corev1.Pod) ([]string, []string, error) {
			if e, ok := entries[image]; ok {
				return e.Entrypoint, e.Cmd, nil
			}
			return nil, nil, fmt.Errorf("image %q not found in fake resolver", image)
		},
	}
}

// buildKeychain constructs an authn.Keychain from the pod's imagePullSecrets
// and the cluster-wide pull secret (openshift-config/pull-secret). Falls back
// to the default keychain if no secrets are found.
func (r *ImageResolver) buildKeychain(ctx context.Context, pod *corev1.Pod) authn.Keychain {
	var keychains []authn.Keychain

	for _, ref := range pod.Spec.ImagePullSecrets {
		secret := &corev1.Secret{}
		key := client.ObjectKey{Name: ref.Name, Namespace: pod.Namespace}
		if err := r.client.Get(ctx, key, secret); err != nil {
			continue
		}

		if kc := keychainFromSecret(secret); kc != nil {
			keychains = append(keychains, kc)
		}
	}

	clusterPullSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: "pull-secret", Namespace: "openshift-config"}, clusterPullSecret); err == nil {
		if kc := keychainFromSecret(clusterPullSecret); kc != nil {
			keychains = append(keychains, kc)
		}
	}

	keychains = append(keychains, authn.DefaultKeychain)
	return authn.NewMultiKeychain(keychains...)
}

// dockerConfigJSON is the minimal structure of a Docker config.json / .dockerconfigjson.
type dockerConfigJSON struct {
	Auths map[string]dockerConfigEntry `json:"auths"`
}

type dockerConfigEntry struct {
	Auth string `json:"auth"`
}

func keychainFromSecret(secret *corev1.Secret) authn.Keychain {
	var auths map[string]dockerConfigEntry

	switch secret.Type {
	case corev1.SecretTypeDockerConfigJson:
		data := secret.Data[corev1.DockerConfigJsonKey]
		if len(data) == 0 {
			return nil
		}
		var cfg dockerConfigJSON
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil
		}
		auths = cfg.Auths
	case corev1.SecretTypeDockercfg:
		data := secret.Data[corev1.DockerConfigKey]
		if len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, &auths); err != nil {
			return nil
		}
	default:
		return nil
	}

	if len(auths) == 0 {
		return nil
	}

	return &staticKeychain{auths: auths}
}

type staticKeychain struct {
	auths map[string]dockerConfigEntry
}

func (s *staticKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	if entry, ok := s.auths[resource.RegistryStr()]; ok && entry.Auth != "" {
		return authn.FromConfig(authn.AuthConfig{Auth: entry.Auth}), nil
	}
	return authn.Anonymous, nil
}

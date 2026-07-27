package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/opendatahub-io/log-normalizer-operator/api/v1alpha1"
)

const (
	annotationInject   = "lognormalizer.opendatahub.io/inject"
	annotationInjected = "lognormalizer.opendatahub.io/injected"
	volumeName         = "log-normalizer-pipe"
	mountPath          = "/var/run/lognormalizer"
	sidecarName        = "log-normalizer"
	wrapperCopyName    = "log-normalizer-copy-wrapper"
	wrapperPath        = mountPath + "/wrapper"
	defaultCRName      = "default"
)

type PodInjector struct {
	client.Client
	decoder  admission.Decoder
	resolver *ImageResolver
}

func (p *PodInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	if err := p.decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding pod: %w", err))
	}

	if pod.Annotations[annotationInjected] == "true" {
		return admission.Allowed("already injected")
	}

	if pod.Annotations[annotationInject] == "false" {
		return admission.Allowed("injection opted out")
	}

	cfg := &v1alpha1.LogNormalizerConfig{}
	if err := p.Get(ctx, types.NamespacedName{Name: defaultCRName}, cfg); err != nil {
		if k8serr.IsNotFound(err) {
			return admission.Allowed("no LogNormalizerConfig found")
		}

		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("fetching LogNormalizerConfig: %w", err))
	}

	rawPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("marshaling original pod: %w", err))
	}

	if p.resolver != nil {
		ResolveEntrypoints(ctx, p.resolver, pod)
	}

	InjectSidecar(pod, cfg)

	mutatedPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("marshaling mutated pod: %w", err))
	}

	return admission.PatchResponseFromRaw(rawPod, mutatedPod)
}

// InjectSidecar adds the log-normalizer sidecar, shared volume, wraps app container
// commands to tee stdout/stderr into the named pipe, and sets annotations on the pod.
func InjectSidecar(pod *corev1.Pod, cfg *v1alpha1.LogNormalizerConfig) {
	pipePath := mountPath + "/log.pipe"

	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    wrapperCopyName,
		Image:   cfg.Spec.SidecarImage,
		Command: []string{"/wrapper", "--install", wrapperPath},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeName,
			MountPath: mountPath,
		}},
	})

	restartAlways := corev1.ContainerRestartPolicyAlways
	sidecarEnv := append(BuildEnvVars(cfg), resourceEnvVars()...)
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:          sidecarName,
		Image:         cfg.Spec.SidecarImage,
		Command:       []string{"/normalizer"},
		Resources:     cfg.Spec.Resources,
		RestartPolicy: &restartAlways,
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeName,
			MountPath: mountPath,
		}},
		Env: sidecarEnv,
	})

	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]

		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
		})

		c.Env = append(c.Env, corev1.EnvVar{
			Name:  "LOG_NORMALIZER_PIPE_PATH",
			Value: pipePath,
		})

		wrapContainerCommand(c)
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations[annotationInjected] = "true"
}

// wrapContainerCommand rewrites a container's command to run through the wrapper
// binary (copied into the shared volume), which tees stdout/stderr into the named
// pipe. Containers without a command are skipped (their entrypoints should have
// been resolved by resolveEntrypoints beforehand).
func wrapContainerCommand(c *corev1.Container) {
	if len(c.Command) == 0 {
		return
	}

	originalCmd := make([]string, len(c.Command))
	copy(originalCmd, c.Command)

	newArgs := append(originalCmd, c.Args...)

	c.Command = []string{wrapperPath}
	c.Args = newArgs
}

// BuildEnvVars constructs the environment variables for the normalizer sidecar.
func BuildEnvVars(cfg *v1alpha1.LogNormalizerConfig) []corev1.EnvVar {
	pipePath := mountPath + "/log.pipe"
	envs := []corev1.EnvVar{
		{Name: "LOG_NORMALIZER_PIPE_PATH", Value: pipePath},
		{Name: "LOG_NORMALIZER_OUTPUT_FORMAT", Value: cfg.Spec.OutputFormat},
		{Name: "LOG_NORMALIZER_DRAIN_SIMILARITY", Value: strconv.FormatFloat(cfg.Spec.Drain.SimilarityThreshold, 'f', -1, 64)},
		{Name: "LOG_NORMALIZER_DRAIN_MAX_CLUSTERS", Value: strconv.Itoa(cfg.Spec.Drain.MaxClusters)},
		{Name: "LOG_NORMALIZER_MULTILINE_ENABLED", Value: strconv.FormatBool(cfg.Spec.Multiline.Enabled)},
		{Name: "LOG_NORMALIZER_MULTILINE_MAX_LINES", Value: strconv.Itoa(cfg.Spec.Multiline.MaxLines)},
		{Name: "LOG_NORMALIZER_MULTILINE_FLUSH_TIMEOUT_MS", Value: strconv.Itoa(cfg.Spec.Multiline.FlushTimeoutMs)},
		{Name: "LOG_NORMALIZER_CONSOLIDATORS", Value: strings.Join(cfg.Spec.Consolidators, ",")},
	}
	if cfg.Spec.OTLPEndpoint != "" {
		envs = append(envs, corev1.EnvVar{
			Name:  "LOG_NORMALIZER_OTLP_ENDPOINT",
			Value: cfg.Spec.OTLPEndpoint,
		})
	}
	return envs
}

// resourceEnvVars returns Downward API env vars that expose pod identity
// to the normalizer sidecar for OTel resource attribute tagging.
func resourceEnvVars() []corev1.EnvVar {
	fieldRef := func(path string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: path},
		}
	}
	return []corev1.EnvVar{
		{Name: "LOG_NORMALIZER_POD_NAME", ValueFrom: fieldRef("metadata.name")},
		{Name: "LOG_NORMALIZER_NAMESPACE", ValueFrom: fieldRef("metadata.namespace")},
		{Name: "LOG_NORMALIZER_NODE_NAME", ValueFrom: fieldRef("spec.nodeName")},
		{Name: "LOG_NORMALIZER_POD_UID", ValueFrom: fieldRef("metadata.uid")},
	}
}

// ResolveEntrypoints fills in the Command field for containers that don't
// have one by querying the container registry for the image ENTRYPOINT/CMD.
// Errors are logged and silently skipped so the pod can still be created.
func ResolveEntrypoints(ctx context.Context, resolver *ImageResolver, pod *corev1.Pod) {
	log := ctrl.LoggerFrom(ctx)
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if len(c.Command) > 0 {
			continue
		}

		entrypoint, cmd, err := resolver.Resolve(ctx, c.Image, pod)
		if err != nil {
			log.V(1).Info("could not resolve image entrypoint, skipping wrapping",
				"container", c.Name, "image", c.Image, "error", err)
			continue
		}

		switch {
		case len(entrypoint) > 0:
			c.Command = entrypoint
			if len(c.Args) == 0 && len(cmd) > 0 {
				c.Args = cmd
			}
		case len(cmd) > 0:
			c.Command = cmd[:1]
			if len(c.Args) == 0 && len(cmd) > 1 {
				c.Args = cmd[1:]
			}
		}
	}
}

func SetupWithManager(mgr ctrl.Manager) error {
	resolver := NewImageResolver(mgr.GetClient(), 5*time.Minute)
	injector := &PodInjector{
		Client:   mgr.GetClient(),
		decoder:  admission.NewDecoder(mgr.GetScheme()),
		resolver: resolver,
	}

	mgr.GetWebhookServer().Register("/mutate-pods", &admission.Webhook{Handler: injector})

	return nil
}

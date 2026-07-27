package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/opendatahub-io/log-normalizer-operator/api/v1alpha1"
	"github.com/opendatahub-io/log-normalizer-operator/internal/webhook"
)

func newTestConfig() *v1alpha1.LogNormalizerConfig {
	return &v1alpha1.LogNormalizerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.LogNormalizerConfigSpec{
			NamespaceSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"lognormalizer.opendatahub.io/inject": "enabled"},
			},
			SidecarImage: "quay.io/opendatahub/log-normalizer:test",
			OutputFormat: "json",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
			Drain: v1alpha1.DrainConfig{
				SimilarityThreshold: 0.4,
				MaxClusters:         1000,
			},
			Multiline: v1alpha1.MultilineConfig{
				Enabled:        true,
				MaxLines:       100,
				FlushTimeoutMs: 2000,
			},
			Consolidators: []string{"go"},
		},
	}
}

func TestInjectSidecar(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "busybox:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	require.Len(t, pod.Spec.InitContainers, 2)

	wrapperCopy := pod.Spec.InitContainers[0]
	assert.Equal(t, "log-normalizer-copy-wrapper", wrapperCopy.Name)
	assert.Equal(t, "quay.io/opendatahub/log-normalizer:test", wrapperCopy.Image)
	assert.Equal(t, []string{"/wrapper", "--install", "/var/run/lognormalizer/wrapper"}, wrapperCopy.Command)
	assert.Nil(t, wrapperCopy.RestartPolicy)

	sidecar := pod.Spec.InitContainers[1]
	assert.Equal(t, "log-normalizer", sidecar.Name)
	assert.Equal(t, "quay.io/opendatahub/log-normalizer:test", sidecar.Image)
	assert.Equal(t, corev1.ContainerRestartPolicyAlways, *sidecar.RestartPolicy)
	assert.Equal(t, resource.MustParse("50m"), sidecar.Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), sidecar.Resources.Limits[corev1.ResourceMemory])

	assert.Len(t, pod.Spec.Volumes, 1)
	assert.Equal(t, "log-normalizer-pipe", pod.Spec.Volumes[0].Name)

	assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, "/var/run/lognormalizer", pod.Spec.Containers[0].VolumeMounts[0].MountPath)

	assert.Equal(t, "true", pod.Annotations["lognormalizer.opendatahub.io/injected"])

	hasPipePath := false
	for _, env := range sidecar.Env {
		if env.Name == "LOG_NORMALIZER_PIPE_PATH" {
			hasPipePath = true
			assert.Equal(t, "/var/run/lognormalizer/log.pipe", env.Value)
		}
	}
	assert.True(t, hasPipePath, "sidecar should have LOG_NORMALIZER_PIPE_PATH env var")
}

func TestInjectSidecar_AlreadyInjected(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-pod",
			Namespace:   "default",
			Annotations: map[string]string{"lognormalizer.opendatahub.io/injected": "true"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "busybox:latest"},
			},
		},
	}

	rawPod, _ := json.Marshal(pod)
	req := admission.Request{}
	req.Object = runtime.RawExtension{Raw: rawPod}

	// Verify that InjectSidecar on a pre-annotated pod would add init containers
	// (the Handle function is the one that gates on annotation, not InjectSidecar).
	// This tests the isolation: InjectSidecar is pure mutation.
	webhook.InjectSidecar(pod, cfg)
	assert.Len(t, pod.Spec.InitContainers, 2)
}

func TestInjectSidecar_PreservesExistingContainers(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
				{Name: "sidecar", Image: "envoy:latest"},
			},
			InitContainers: []corev1.Container{
				{Name: "init-db", Image: "flyway:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	assert.Len(t, pod.Spec.Containers, 2, "should preserve existing containers")
	assert.Len(t, pod.Spec.InitContainers, 3, "should append wrapper-copy and sidecar to existing init containers")
	assert.Equal(t, "init-db", pod.Spec.InitContainers[0].Name)
	assert.Equal(t, "log-normalizer-copy-wrapper", pod.Spec.InitContainers[1].Name)
	assert.Equal(t, "log-normalizer", pod.Spec.InitContainers[2].Name)

	// All containers get the volume mount
	for _, c := range pod.Spec.Containers {
		assert.Len(t, c.VolumeMounts, 1, "container %s should have volume mount", c.Name)
		assert.Equal(t, "/var/run/lognormalizer", c.VolumeMounts[0].MountPath)
	}
}

func TestInjectSidecar_WrapsExplicitCommand(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "myapp:latest",
					Command: []string{"/usr/bin/myapp"},
					Args:    []string{"--port", "8080"},
				},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	c := pod.Spec.Containers[0]
	assert.Equal(t, []string{"/var/run/lognormalizer/wrapper"}, c.Command,
		"command should be rewritten to the wrapper binary in the shared volume")
	assert.Equal(t, []string{"/usr/bin/myapp", "--port", "8080"}, c.Args,
		"original command and args should be preserved as wrapper args")
}

func TestInjectSidecar_SkipsWrapForNoCommand(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	c := pod.Spec.Containers[0]
	assert.Empty(t, c.Command, "containers without explicit command should not be wrapped")
	assert.Empty(t, c.Args, "args should remain empty for unwrapped containers")
}

func TestInjectSidecar_AppContainerGetsPipePathEnv(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	c := pod.Spec.Containers[0]
	hasPipePath := false
	for _, env := range c.Env {
		if env.Name == "LOG_NORMALIZER_PIPE_PATH" {
			hasPipePath = true
			assert.Equal(t, "/var/run/lognormalizer/log.pipe", env.Value)
		}
	}
	assert.True(t, hasPipePath, "app container should have LOG_NORMALIZER_PIPE_PATH env var")
}

func TestInjectSidecar_AllContainersGetVolumeMountAndEnv(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app1", Image: "app1:latest", Command: []string{"/app1"}},
				{Name: "app2", Image: "app2:latest", Command: []string{"/app2"}},
				{Name: "no-cmd", Image: "nocmd:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	for _, c := range pod.Spec.Containers {
		assert.Len(t, c.VolumeMounts, 1, "container %s should have volume mount", c.Name)
		assert.Equal(t, "/var/run/lognormalizer", c.VolumeMounts[0].MountPath)

		hasPipePath := false
		for _, env := range c.Env {
			if env.Name == "LOG_NORMALIZER_PIPE_PATH" {
				hasPipePath = true
			}
		}
		assert.True(t, hasPipePath, "container %s should have LOG_NORMALIZER_PIPE_PATH", c.Name)
	}

	assert.Equal(t, []string{"/var/run/lognormalizer/wrapper"}, pod.Spec.Containers[0].Command)
	assert.Equal(t, []string{"/app1"}, pod.Spec.Containers[0].Args)

	assert.Equal(t, []string{"/var/run/lognormalizer/wrapper"}, pod.Spec.Containers[1].Command)
	assert.Equal(t, []string{"/app2"}, pod.Spec.Containers[1].Args)

	assert.Empty(t, pod.Spec.Containers[2].Command, "container without command should not be wrapped")
}

func TestBuildEnvVars(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	envVars := webhook.BuildEnvVars(cfg)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Equal(t, "/var/run/lognormalizer/log.pipe", envMap["LOG_NORMALIZER_PIPE_PATH"])
	assert.Equal(t, "json", envMap["LOG_NORMALIZER_OUTPUT_FORMAT"])
	assert.Equal(t, "0.4", envMap["LOG_NORMALIZER_DRAIN_SIMILARITY"])
	assert.Equal(t, "1000", envMap["LOG_NORMALIZER_DRAIN_MAX_CLUSTERS"])
	assert.Equal(t, "true", envMap["LOG_NORMALIZER_MULTILINE_ENABLED"])
	assert.Equal(t, "100", envMap["LOG_NORMALIZER_MULTILINE_MAX_LINES"])
	assert.Equal(t, "2000", envMap["LOG_NORMALIZER_MULTILINE_FLUSH_TIMEOUT_MS"])
	assert.Equal(t, "go", envMap["LOG_NORMALIZER_CONSOLIDATORS"])

	_, hasDrainDepth := envMap["LOG_NORMALIZER_DRAIN_DEPTH"]
	assert.False(t, hasDrainDepth, "DRAIN_DEPTH should not be set")

	_, hasOTLP := envMap["LOG_NORMALIZER_OTLP_ENDPOINT"]
	assert.False(t, hasOTLP, "OTLP_ENDPOINT should not be set when empty in config")
}

func TestBuildEnvVars_WithOTLPEndpoint(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	cfg.Spec.OTLPEndpoint = "collector.opendatahub.svc:4317"

	envVars := webhook.BuildEnvVars(cfg)

	envMap := make(map[string]string)
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}

	assert.Equal(t, "collector.opendatahub.svc:4317", envMap["LOG_NORMALIZER_OTLP_ENDPOINT"])
}

func TestInjectSidecar_SidecarHasResourceEnvVars(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
			},
		},
	}

	webhook.InjectSidecar(pod, cfg)

	sidecar := pod.Spec.InitContainers[1]
	assert.Equal(t, "log-normalizer", sidecar.Name)

	envByName := make(map[string]corev1.EnvVar)
	for _, env := range sidecar.Env {
		envByName[env.Name] = env
	}

	expectedDownwardAPI := map[string]string{
		"LOG_NORMALIZER_POD_NAME":  "metadata.name",
		"LOG_NORMALIZER_NAMESPACE": "metadata.namespace",
		"LOG_NORMALIZER_NODE_NAME": "spec.nodeName",
		"LOG_NORMALIZER_POD_UID":   "metadata.uid",
	}

	for envName, fieldPath := range expectedDownwardAPI {
		env, ok := envByName[envName]
		assert.True(t, ok, "sidecar should have %s env var", envName)
		if ok {
			require.NotNil(t, env.ValueFrom, "%s should use ValueFrom", envName)
			require.NotNil(t, env.ValueFrom.FieldRef, "%s should use FieldRef", envName)
			assert.Equal(t, fieldPath, env.ValueFrom.FieldRef.FieldPath, "%s field path", envName)
		}
	}
}

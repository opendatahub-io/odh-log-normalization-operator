package webhook_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/log-normalizer-operator/internal/webhook"
)

func TestResolveEntrypoints_SetsCommandFromEntrypoint(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "no-cmd", Image: "registry.example.com/myapp:latest"},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{
		"registry.example.com/myapp:latest": {
			Entrypoint: []string{"/usr/bin/myapp"},
			Cmd:        []string{"--serve"},
		},
	})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	c := pod.Spec.Containers[0]
	assert.Equal(t, []string{"/usr/bin/myapp"}, c.Command)
	assert.Equal(t, []string{"--serve"}, c.Args)
}

func TestResolveEntrypoints_CmdOnly(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "cmd-only", Image: "registry.example.com/cmdapp:v1"},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{
		"registry.example.com/cmdapp:v1": {
			Cmd: []string{"/bin/sh", "-c", "echo hello"},
		},
	})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	c := pod.Spec.Containers[0]
	assert.Equal(t, []string{"/bin/sh"}, c.Command)
	assert.Equal(t, []string{"-c", "echo hello"}, c.Args)
}

func TestResolveEntrypoints_SkipsContainersWithCommand(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "has-cmd", Image: "myapp:v1", Command: []string{"/app"}},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{
		"myapp:v1": {Entrypoint: []string{"/should-not-be-used"}},
	})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	c := pod.Spec.Containers[0]
	assert.Equal(t, []string{"/app"}, c.Command, "should not override existing command")
}

func TestResolveEntrypoints_PreservesExistingArgs(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "has-args", Image: "myapp:v1", Args: []string{"--flag"}},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{
		"myapp:v1": {
			Entrypoint: []string{"/app"},
			Cmd:        []string{"--default-flag"},
		},
	})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	c := pod.Spec.Containers[0]
	assert.Equal(t, []string{"/app"}, c.Command)
	assert.Equal(t, []string{"--flag"}, c.Args, "pod-level args override image CMD")
}

func TestResolveEntrypoints_UnresolvableImageSkipped(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "unknown", Image: "unknown-registry.example.com/myapp:v1"},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	c := pod.Spec.Containers[0]
	assert.Empty(t, c.Command, "unresolvable image should leave command empty")
	assert.Empty(t, c.Args)
}

func TestResolveEntrypoints_MultipleContainersMixed(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "has-cmd", Image: "myapp:v1", Command: []string{"/app"}},
				{Name: "no-cmd", Image: "registry.example.com/sidecar:v2"},
				{Name: "unresolvable", Image: "unknown.example.com/x:v1"},
			},
		},
	}

	resolver := webhook.NewFakeResolver(map[string]webhook.FakeEntrypoint{
		"registry.example.com/sidecar:v2": {Entrypoint: []string{"/sidecar-bin"}},
	})

	webhook.ResolveEntrypoints(context.Background(), resolver, pod)

	assert.Equal(t, []string{"/app"}, pod.Spec.Containers[0].Command, "pre-existing command unchanged")
	assert.Equal(t, []string{"/sidecar-bin"}, pod.Spec.Containers[1].Command, "resolved from registry")
	assert.Empty(t, pod.Spec.Containers[2].Command, "unresolvable left empty")
}

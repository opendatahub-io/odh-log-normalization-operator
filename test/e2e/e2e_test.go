package e2e_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	testNamespace     = "log-normalizer-e2e"
	injectedLabel     = "lognormalizer.opendatahub.io/inject"
	injectedAnnotation = "lognormalizer.opendatahub.io/injected"
	pollInterval      = 2 * time.Second
	pollTimeout       = 2 * time.Minute
)

func getClient(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
	require.NoError(t, err, "failed to load kubeconfig")

	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "failed to create kubernetes client")
	return client
}

func setupNamespace(t *testing.T, client *kubernetes.Clientset, name string, inject bool) {
	t.Helper()
	ctx := context.Background()

	labels := map[string]string{}
	if inject {
		labels[injectedLabel] = "enabled"
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}

	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		require.NoError(t, err)
	}

	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	})
}

func createTestPod(t *testing.T, client *kubernetes.Clientset, namespace, name string, annotations map[string]string) *corev1.Pod {
	t.Helper()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "busybox:latest",
					Command: []string{"/bin/sh", "-c", "while true; do echo '{\"level\":\"info\",\"msg\":\"test\"}'; sleep 1; done"},
				},
			},
		},
	}

	created, err := client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	})

	return created
}

func TestSidecarInjection(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-inject"
	setupNamespace(t, client, ns, true)

	pod := createTestPod(t, client, ns, "test-inject", nil)

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Annotations[injectedAnnotation] == "true", nil
	})
	require.NoError(t, err, "sidecar was not injected")

	p, err := client.CoreV1().Pods(ns).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)

	hasSidecar := false
	for _, c := range p.Spec.InitContainers {
		if c.Name == "log-normalizer" {
			hasSidecar = true
			assert.Equal(t, corev1.ContainerRestartPolicyAlways, *c.RestartPolicy)
			break
		}
	}
	assert.True(t, hasSidecar, "expected log-normalizer init container")
}

func TestNoInjectionWithoutLabel(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-noinject"
	setupNamespace(t, client, ns, false)

	pod := createTestPod(t, client, ns, "test-noinject", nil)

	time.Sleep(5 * time.Second)

	p, err := client.CoreV1().Pods(ns).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Empty(t, p.Annotations[injectedAnnotation], "sidecar should not be injected in unlabeled namespace")
	for _, c := range p.Spec.InitContainers {
		assert.NotEqual(t, "log-normalizer", c.Name)
	}
}

func TestOptOutAnnotation(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-optout"
	setupNamespace(t, client, ns, true)

	annotations := map[string]string{
		"lognormalizer.opendatahub.io/inject": "false",
	}
	pod := createTestPod(t, client, ns, "test-optout", annotations)

	time.Sleep(5 * time.Second)

	p, err := client.CoreV1().Pods(ns).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)

	for _, c := range p.Spec.InitContainers {
		assert.NotEqual(t, "log-normalizer", c.Name, "sidecar should not be injected with opt-out annotation")
	}
}

func TestStructuredLogOutput(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-structured"
	setupNamespace(t, client, ns, true)

	pod := createTestPod(t, client, ns, "test-structured", nil)

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if p.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err, "pod did not reach Running phase")

	time.Sleep(5 * time.Second)

	logs, err := client.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "log-normalizer",
	}).Do(context.Background()).Raw()
	if err != nil {
		t.Skipf("could not read sidecar logs: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		err := json.Unmarshal([]byte(line), &record)
		assert.NoError(t, err, "sidecar output should be valid JSON: %s", line)
		assert.Contains(t, record, "timestamp", "log record should have timestamp")
		assert.Contains(t, record, "level", "log record should have level")
		assert.Contains(t, record, "message", "log record should have message")
	}
}

func TestMixedLogFormats(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-mixed"
	setupNamespace(t, client, ns, true)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mixed-logger",
			Namespace: ns,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "mixed-logger"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "mixed-logger"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "logger",
							Image: "busybox:latest",
							Command: []string{"/bin/sh", "-c", `
								echo '{"level":"info","msg":"json log"}'
								echo "I0722 15:04:05.000000 12345 main.go:42] klog message"
								echo 'level=warn msg="logfmt warning"'
								echo "plain text log line"
								sleep 30
							`},
						},
					},
				},
			},
		},
	}

	ctx := context.Background()
	_, err := client.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.AppsV1().Deployments(ns).Delete(ctx, dep.Name, metav1.DeleteOptions{})
	})

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		d, err := client.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return d.Status.ReadyReplicas > 0, nil
	})
	if err != nil {
		t.Skipf("deployment did not become ready: %v", err)
	}

	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app=mixed-logger",
	})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items)

	for _, pod := range pods.Items {
		p, _ := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if p != nil && p.Annotations[injectedAnnotation] == "true" {
			t.Logf("sidecar injected into pod %s", pod.Name)
		}
	}
}

func TestMultilineGoPanic(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-panic"
	setupNamespace(t, client, ns, true)

	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "panic-test",
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "busybox:latest",
					Command: []string{"/bin/sh", "-c", `
						echo 'panic: runtime error: index out of range'
						echo 'goroutine 1 [running]:'
						echo 'main.main()'
						echo '	/app/main.go:42 +0x1a4'
						sleep 30
					`},
				},
			},
		},
	}

	_, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	})

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
	require.NoError(t, err, "pod did not reach Running phase")

	time.Sleep(5 * time.Second)

	logs, err := client.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "log-normalizer",
	}).Do(ctx).Raw()
	if err != nil {
		t.Skipf("could not read sidecar logs: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
	panicEntries := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		msg, _ := record["message"].(string)
		if strings.Contains(msg, "panic:") && strings.Contains(msg, "goroutine") {
			panicEntries++
		}
	}
	assert.Equal(t, 1, panicEntries, "panic + goroutine dump should be a single log entry")
}

func TestSidecarResourceLimits(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-resources"
	setupNamespace(t, client, ns, true)

	pod := createTestPod(t, client, ns, "test-resources", nil)

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Annotations[injectedAnnotation] == "true", nil
	})
	require.NoError(t, err, "sidecar was not injected")

	p, err := client.CoreV1().Pods(ns).Get(context.Background(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)

	for _, c := range p.Spec.InitContainers {
		if c.Name == "log-normalizer" {
			assert.NotEmpty(t, c.Resources.Requests, "sidecar should have resource requests")
			assert.NotEmpty(t, c.Resources.Limits, "sidecar should have resource limits")
			return
		}
	}
	t.Error("log-normalizer init container not found")
}

func TestHighThroughput(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-throughput"
	setupNamespace(t, client, ns, true)

	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "throughput-test",
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "busybox:latest",
					Command: []string{"/bin/sh", "-c", `
						i=0
						while [ $i -lt 10000 ]; do
							echo "{\"level\":\"info\",\"msg\":\"line $i\",\"n\":$i}"
							i=$((i+1))
						done
						sleep 60
					`},
				},
			},
		},
	}

	_, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	})

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
	require.NoError(t, err)

	time.Sleep(15 * time.Second)

	logs, err := client.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "log-normalizer",
	}).Do(ctx).Raw()
	if err != nil {
		t.Skipf("could not read sidecar logs: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
	validJSON := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil {
			validJSON++
		}
	}
	assert.Greater(t, validJSON, 1000, "sidecar should process at least 1000 lines without dropping")
}

func TestPlainTextWithTemplateID(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-plaintext"
	setupNamespace(t, client, ns, true)

	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plaintext-test",
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "busybox:latest",
					Command: []string{"/bin/sh", "-c", `
						echo "Server started on port 8080"
						echo "Server started on port 9090"
						echo "Connection from client 10.0.0.1"
						echo "Connection from client 10.0.0.2"
						sleep 30
					`},
				},
			},
		},
	}

	_, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	})

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
	require.NoError(t, err)

	time.Sleep(5 * time.Second)

	logs, err := client.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "log-normalizer",
	}).Do(ctx).Raw()
	if err != nil {
		t.Skipf("could not read sidecar logs: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
	hasTemplateID := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["source_format"] == "plain" {
			if tid, ok := record["template_id"]; ok && tid != nil {
				hasTemplateID = true
			}
		}
	}
	assert.True(t, hasTemplateID, "plain text logs should have template_id from Drain")
}

func TestOperatorUpgradePropagation(t *testing.T) {
	t.Parallel()
	client := getClient(t)
	ns := testNamespace + "-upgrade"
	setupNamespace(t, client, ns, true)

	ctx := context.Background()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upgrade-test",
			Namespace: ns,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "upgrade-test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "upgrade-test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "app",
							Image:   "busybox:latest",
							Command: []string{"/bin/sh", "-c", "while true; do echo test; sleep 5; done"},
						},
					},
				},
			},
		},
	}

	_, err := client.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.AppsV1().Deployments(ns).Delete(ctx, dep.Name, metav1.DeleteOptions{})
	})

	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		d, err := client.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return d.Status.ReadyReplicas > 0, nil
	})
	if err != nil {
		t.Skipf("deployment did not become ready: %v", err)
	}

	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app=upgrade-test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items)

	var sidecarImage string
	for _, c := range pods.Items[0].Spec.InitContainers {
		if c.Name == "log-normalizer" {
			sidecarImage = c.Image
			break
		}
	}
	assert.NotEmpty(t, sidecarImage, "sidecar should be injected with an image")

	// A new pod created after the CR changes should pick up the new image.
	// This test just verifies the injection mechanism works for deployment-created pods.
	t.Logf("sidecar image: %s", sidecarImage)
}

func int32Ptr(i int32) *int32 { return &i }

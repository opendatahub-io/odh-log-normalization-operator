package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	lognormalizerv1alpha1 "github.com/opendatahub-io/log-normalizer-operator/api/v1alpha1"
)

const (
	webhookName       = "log-normalizer-injector"
	webhookPath       = "/mutate-pods"
	injectedAnnotation = "lognormalizer.opendatahub.io/injected"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lognormalizer.opendatahub.io,resources=lognormalizerconfigs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=lognormalizer.opendatahub.io,resources=lognormalizerconfigs/status,verbs=update;patch

// LogNormalizerConfigReconciler reconciles LogNormalizerConfig objects.
type LogNormalizerConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

func (r *LogNormalizerConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("lognormalizerconfig", req.NamespacedName)

	config := &lognormalizerv1alpha1.LogNormalizerConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if k8serr.IsNotFound(err) {
			log.Info("LogNormalizerConfig deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching LogNormalizerConfig: %w", err)
	}

	if err := r.reconcileWebhook(ctx, config); err != nil {
		r.setCondition(config, "WebhookReady", metav1.ConditionFalse, "ReconcileFailed", err.Error())
		if statusErr := r.Status().Update(ctx, config); statusErr != nil {
			log.Error(statusErr, "unable to update status after webhook failure")
		}
		return ctrl.Result{}, fmt.Errorf("reconciling webhook: %w", err)
	}

	r.setCondition(config, "WebhookReady", metav1.ConditionTrue, "WebhookConfigured", "MutatingWebhookConfiguration is up to date")

	injectedCount, err := r.countInjectedPods(ctx, config)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("counting injected pods: %w", err)
	}

	config.Status.InjectedPods = int32(injectedCount)
	config.Status.Phase = phaseFromConditions(config)

	if err := r.Status().Update(ctx, config); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *LogNormalizerConfigReconciler) reconcileWebhook(
	ctx context.Context,
	config *lognormalizerv1alpha1.LogNormalizerConfig,
) error {
	if err := r.ensureWebhookService(ctx); err != nil {
		return fmt.Errorf("ensuring webhook service: %w", err)
	}

	caBundle, err := r.loadCABundle(ctx)
	if err != nil {
		return fmt.Errorf("loading CA bundle: %w", err)
	}

	sideEffects := admissionregistrationv1.SideEffectClassNone
	failurePolicy := admissionregistrationv1.Ignore

	desired := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: webhookName,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, desired, func() error {
		if err := controllerutil.SetOwnerReference(config, desired, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference: %w", err)
		}

		desired.Webhooks = []admissionregistrationv1.MutatingWebhook{
			{
				Name:                    webhookName + ".lognormalizer.opendatahub.io",
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
				NamespaceSelector:       &config.Spec.NamespaceSelector,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: caBundle,
					Service: &admissionregistrationv1.ServiceReference{
						Name:      "log-normalizer-webhook",
						Namespace: "log-normalizer-system",
						Path:      ptr.To(webhookPath),
						Port:      ptr.To(int32(443)),
					},
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
						},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("creating or updating MutatingWebhookConfiguration: %w", err)
	}

	r.Log.Info("reconciled webhook", "result", result)
	return nil
}

const (
	tlsSecretName = "log-normalizer-webhook-tls"
	operatorNS    = "log-normalizer-system"
	serviceName   = "log-normalizer-webhook"
	certValidDays = 365

	// DefaultCertDir is the default path where controller-runtime's webhook
	// server looks for TLS certificates.
	DefaultCertDir = "/tmp/k8s-webhook-server/serving-certs"
)

// EnsureServingCerts checks whether TLS cert files exist at certDir. If not, it
// looks up the TLS secret in the cluster. If the secret also doesn't exist, it
// generates self-signed certificates, creates the secret, and writes the cert
// files to disk. This must be called before the manager starts so the webhook
// server has serving certs on startup.
func EnsureServingCerts(ctx context.Context, cfg *rest.Config, certDir string) error {
	log := ctrl.Log.WithName("setup").WithName("tls")

	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	if fileExists(certPath) && fileExists(keyPath) {
		log.Info("TLS cert files already present", "dir", certDir)
		return nil
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	secret, err := clientset.CoreV1().Secrets(operatorNS).Get(ctx, tlsSecretName, metav1.GetOptions{})
	if err != nil {
		if !k8serr.IsNotFound(err) {
			return fmt.Errorf("getting TLS secret: %w", err)
		}

		log.Info("TLS secret not found, generating self-signed certificates")
		secret, err = createSelfSignedSecret(ctx, clientset)
		if err != nil {
			return fmt.Errorf("creating self-signed TLS secret: %w", err)
		}
	}

	return writeCertFiles(certDir, secret.Data)
}

func createSelfSignedSecret(ctx context.Context, clientset kubernetes.Interface) (*corev1.Secret, error) {
	dnsNames := []string{
		serviceName + "." + operatorNS + ".svc",
		serviceName + "." + operatorNS + ".svc.cluster.local",
	}

	caCert, caKey, caPEM, err := generateCA()
	if err != nil {
		return nil, fmt.Errorf("generating CA: %w", err)
	}

	certPEM, keyPEM, err := generateServerCert(caCert, caKey, dnsNames)
	if err != nil {
		return nil, fmt.Errorf("generating server cert: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tlsSecretName,
			Namespace: operatorNS,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":          caPEM,
			corev1.TLSCertKey: certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
		},
	}

	created, err := clientset.CoreV1().Secrets(operatorNS).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating secret: %w", err)
	}
	return created, nil
}

func writeCertFiles(certDir string, data map[string][]byte) error {
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return fmt.Errorf("creating cert dir: %w", err)
	}

	for _, f := range []struct {
		name string
		key  string
	}{
		{"tls.crt", corev1.TLSCertKey},
		{"tls.key", corev1.TLSPrivateKeyKey},
	} {
		content, ok := data[f.key]
		if !ok || len(content) == 0 {
			return fmt.Errorf("secret missing key %q", f.key)
		}
		if err := os.WriteFile(filepath.Join(certDir, f.name), content, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", f.name, err)
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (r *LogNormalizerConfigReconciler) ensureWebhookService(ctx context.Context) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: operatorNS,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = map[string]string{"app": "log-normalizer-operator"}
		svc.Spec.Ports = []corev1.ServicePort{{
			Port:       443,
			TargetPort: intstr.FromInt32(9443),
			Protocol:   corev1.ProtocolTCP,
		}}
		return nil
	})
	return err
}

// loadCABundle reads the CA bundle from the webhook TLS secret.
func (r *LogNormalizerConfigReconciler) loadCABundle(ctx context.Context) ([]byte, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: tlsSecretName, Namespace: operatorNS}
	if err := r.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting webhook TLS secret: %w", err)
	}

	if ca, ok := secret.Data["ca.crt"]; ok && len(ca) > 0 {
		return ca, nil
	}
	if cert, ok := secret.Data[corev1.TLSCertKey]; ok && len(cert) > 0 {
		return cert, nil
	}
	return nil, fmt.Errorf("webhook TLS secret has no ca.crt or tls.crt")
}

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "log-normalizer-webhook-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Duration(certValidDays) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, key, certPEM, nil
}

func generateServerCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, dnsNames []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Duration(certValidDays) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func (r *LogNormalizerConfigReconciler) countInjectedPods(
	ctx context.Context,
	_ *lognormalizerv1alpha1.LogNormalizerConfig,
) (int, error) {
	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabels{"lognormalizer.opendatahub.io/inject": "enabled"}); err != nil {
		return 0, fmt.Errorf("listing namespaces: %w", err)
	}

	count := 0
	for i := range nsList.Items {
		var pods corev1.PodList
		if err := r.List(ctx, &pods, client.InNamespace(nsList.Items[i].Name)); err != nil {
			return 0, fmt.Errorf("listing pods in %s: %w", nsList.Items[i].Name, err)
		}
		for j := range pods.Items {
			if pods.Items[j].Annotations[injectedAnnotation] == "true" {
				count++
			}
		}
	}

	return count, nil
}

func (r *LogNormalizerConfigReconciler) setCondition(
	config *lognormalizerv1alpha1.LogNormalizerConfig,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	for i, c := range config.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				config.Status.Conditions[i].LastTransitionTime = now
			}
			config.Status.Conditions[i].Status = status
			config.Status.Conditions[i].Reason = reason
			config.Status.Conditions[i].Message = message
			return
		}
	}
	config.Status.Conditions = append(config.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

func phaseFromConditions(config *lognormalizerv1alpha1.LogNormalizerConfig) string {
	for _, c := range config.Status.Conditions {
		if c.Type == "WebhookReady" && c.Status == metav1.ConditionTrue {
			return "Ready"
		}
	}
	return "Pending"
}

func (r *LogNormalizerConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lognormalizerv1alpha1.LogNormalizerConfig{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Complete(r)
}

// SetupWithManager creates the LogNormalizerConfigReconciler and registers it with the manager.
func SetupWithManager(mgr ctrl.Manager) error {
	reconciler := &LogNormalizerConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("LogNormalizerConfig"),
	}
	return reconciler.SetupWithManager(mgr)
}

// EnsureDefaultConfig creates a default LogNormalizerConfig if none exists.
// The sidecar image is read from the RELATED_IMAGE_NORMALIZER env var (OLM
// convention). If unset, the CRD default is used.
func EnsureDefaultConfig(ctx context.Context, c client.Client) error {
	log := ctrl.Log.WithName("setup").WithName("bootstrap")

	list := &lognormalizerv1alpha1.LogNormalizerConfigList{}
	if err := c.List(ctx, list); err != nil {
		return fmt.Errorf("listing LogNormalizerConfigs: %w", err)
	}
	if len(list.Items) > 0 {
		log.Info("LogNormalizerConfig already exists, skipping bootstrap", "name", list.Items[0].Name)
		return nil
	}

	config := &lognormalizerv1alpha1.LogNormalizerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: lognormalizerv1alpha1.LogNormalizerConfigSpec{
			NamespaceSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"lognormalizer.opendatahub.io/inject": "enabled",
				},
			},
		},
	}

	if img := os.Getenv("RELATED_IMAGE_NORMALIZER"); img != "" {
		config.Spec.SidecarImage = img
	}

	if err := c.Create(ctx, config); err != nil {
		return fmt.Errorf("creating default LogNormalizerConfig: %w", err)
	}
	log.Info("created default LogNormalizerConfig", "sidecarImage", config.Spec.SidecarImage)
	return nil
}

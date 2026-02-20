/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/assembler"
	"github.com/marcus-qen/legator/internal/controller"
	"github.com/marcus-qen/legator/internal/lifecycle"
	_ "github.com/marcus-qen/legator/internal/metrics" // Register Prometheus metrics
	"github.com/marcus-qen/legator/internal/multicluster"
	"github.com/marcus-qen/legator/internal/provider"
	"github.com/marcus-qen/legator/internal/ratelimit"
	"github.com/marcus-qen/legator/internal/resolver"
	"github.com/marcus-qen/legator/internal/retention"
	"github.com/marcus-qen/legator/internal/runner"
	"github.com/marcus-qen/legator/internal/scheduler"
	"github.com/marcus-qen/legator/internal/telemetry"
	"github.com/marcus-qen/legator/internal/tools"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var otelEndpoint string
	var retentionTTL string
	var retentionScanInterval string
	var retentionMaxBatch int
	var retentionPreserveMin int
	var drainTimeout string
	var maxConcurrentCluster int
	var maxConcurrentPerAgent int
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&otelEndpoint, "otel-endpoint", "",
		"OTLP gRPC endpoint for tracing (e.g. tempo:4317). Empty disables tracing. "+
			"Also configurable via OTEL_EXPORTER_OTLP_ENDPOINT env var.")
	flag.StringVar(&retentionTTL, "retention-ttl", "168h",
		"How long to keep completed LegatorRuns (e.g. 168h for 7 days). Set to 0 to disable retention.")
	flag.StringVar(&retentionScanInterval, "retention-scan-interval", "1h",
		"How often to scan for expired LegatorRuns.")
	flag.IntVar(&retentionMaxBatch, "retention-max-batch", 100,
		"Maximum LegatorRuns to delete per scan.")
	flag.IntVar(&retentionPreserveMin, "retention-preserve-min", 5,
		"Keep at least this many runs per agent regardless of TTL.")
	flag.StringVar(&drainTimeout, "drain-timeout", "30s",
		"Maximum time to wait for in-flight runs on shutdown.")
	flag.IntVar(&maxConcurrentCluster, "max-concurrent-cluster", 10,
		"Cluster-wide maximum simultaneous agent runs.")
	flag.IntVar(&maxConcurrentPerAgent, "max-concurrent-per-agent", 1,
		"Per-agent maximum simultaneous runs.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Allow env var override for OTLP endpoint
	if otelEndpoint == "" {
		otelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	// Initialise OpenTelemetry tracing
	shutdownTracer, err := telemetry.InitTraceProvider(context.Background(), otelEndpoint, "0.1.0")
	if err != nil {
		setupLog.Error(err, "Failed to initialise OTel tracing — continuing without traces")
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdownTracer(ctx); err != nil {
				setupLog.Error(err, "Failed to shutdown OTel tracer")
			}
		}()
		if otelEndpoint != "" {
			setupLog.Info("OTel tracing enabled", "endpoint", otelEndpoint)
		}
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "b3aef6a8.legator.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// Multi-cluster client factory
	clientFactory := multicluster.NewClientFactory(mgr.GetClient(), mgr.GetScheme())
	setupLog.Info("Multi-cluster client factory initialised")

	// Rate limiter
	rateLimiter := ratelimit.NewLimiter(ratelimit.Config{
		MaxConcurrentCluster:   maxConcurrentCluster,
		MaxConcurrentPerAgent:  maxConcurrentPerAgent,
		MaxRunsPerHourCluster:  200,
		MaxRunsPerHourPerAgent: 30,
		BurstAllowance:         3,
	})
	setupLog.Info("Rate limiter initialised",
		"maxConcurrentCluster", maxConcurrentCluster,
		"maxConcurrentPerAgent", maxConcurrentPerAgent,
	)

	// Retention controller
	retTTL, err := time.ParseDuration(retentionTTL)
	if err != nil {
		setupLog.Error(err, "Invalid retention-ttl, using default 168h")
		retTTL = 168 * time.Hour
	}
	retScan, err := time.ParseDuration(retentionScanInterval)
	if err != nil {
		setupLog.Error(err, "Invalid retention-scan-interval, using default 1h")
		retScan = 1 * time.Hour
	}
	if retTTL > 0 {
		retCtrl := retention.NewController(mgr.GetClient(), retention.Config{
			TTL:                 retTTL,
			ScanInterval:        retScan,
			MaxDeleteBatch:      retentionMaxBatch,
			PreserveMinPerAgent: retentionPreserveMin,
		}, ctrl.Log)
		if err := mgr.Add(retCtrl); err != nil {
			setupLog.Error(err, "Failed to add retention controller")
			os.Exit(1)
		}
		setupLog.Info("Retention controller registered",
			"ttl", retTTL,
			"scanInterval", retScan,
			"preserveMin", retentionPreserveMin,
		)
	}

	// Build assembler and runner (before scheduler, which needs the runner)
	asm := assembler.New(mgr.GetClient())
	agentRunner := runner.NewRunner(mgr.GetClient(), asm, ctrl.Log.WithName("runner"))

	// Scheduler (with rate limiting and runner)
	schedCfg := scheduler.DefaultConfig()
	schedCfg.MaxConcurrentRuns = maxConcurrentCluster
	sched := scheduler.New(mgr.GetClient(), agentRunner, ctrl.Log, schedCfg)
	if err := mgr.Add(sched); err != nil {
		setupLog.Error(err, "Failed to add scheduler")
		os.Exit(1)
	}

	// Graceful shutdown manager
	drainDur, err := time.ParseDuration(drainTimeout)
	if err != nil {
		setupLog.Error(err, "Invalid drain-timeout, using default 30s")
		drainDur = 30 * time.Second
	}
	shutdownMgr := lifecycle.NewShutdownManager(sched.RunTrackerRef(), drainDur, ctrl.Log)

	// Provider factory: resolves model tier → LLM provider with API key from Secret
	providerFactory := func(agent *corev1alpha1.LegatorAgent, mtc *corev1alpha1.ModelTierConfig) (provider.Provider, error) {
		// Look up the ModelTierConfig if not provided
		if mtc == nil {
			mtc = &corev1alpha1.ModelTierConfig{}
			if err := mgr.GetClient().Get(context.Background(), client.ObjectKey{Name: "default"}, mtc); err != nil {
				return nil, fmt.Errorf("failed to get ModelTierConfig: %w", err)
			}
		}
		// Find the tier mapping
		tier := agent.Spec.Model.Tier
		var tierSpec *corev1alpha1.TierMapping
		for i := range mtc.Spec.Tiers {
			if mtc.Spec.Tiers[i].Tier == tier {
				tierSpec = &mtc.Spec.Tiers[i]
				break
			}
		}
		if tierSpec == nil {
			return nil, fmt.Errorf("tier %q not found in ModelTierConfig", tier)
		}
		// Resolve API key from defaultAuth secretRef
		apiKey := ""
		if mtc.Spec.DefaultAuth != nil && mtc.Spec.DefaultAuth.SecretRef != "" {
			secret := &corev1.Secret{}
			ns := agent.Namespace
			if err := mgr.GetClient().Get(context.Background(), client.ObjectKey{
				Namespace: ns,
				Name:      mtc.Spec.DefaultAuth.SecretRef,
			}, secret); err != nil {
				return nil, fmt.Errorf("failed to get auth secret %q: %w", mtc.Spec.DefaultAuth.SecretRef, err)
			}
			key := mtc.Spec.DefaultAuth.SecretKey
			if key == "" {
				key = "api-key"
			}
			apiKey = string(secret.Data[key])
		}
		cfg := provider.ProviderConfig{
			Type:   tierSpec.Provider,
			APIKey: apiKey,
		}
		if tierSpec.Endpoint != "" {
			cfg.Endpoint = tierSpec.Endpoint
		}
		switch tierSpec.Provider {
		case "anthropic":
			return provider.NewAnthropicProvider(cfg)
		case "openai":
			return provider.NewOpenAIProvider(cfg)
		default:
			return nil, fmt.Errorf("unsupported provider: %s", tierSpec.Provider)
		}
	}

	// Tool registry factory: builds tools for an agent
	toolRegistryFactory := func(agent *corev1alpha1.LegatorAgent, env *resolver.ResolvedEnvironment) (*tools.Registry, error) {
		reg := tools.NewRegistry()
		// Build credential store for HTTP tools from resolved environment
		var credStore *tools.HTTPCredentialStore
		if env != nil && len(env.Credentials) > 0 {
			credMappings := buildHTTPCredentialMappings(env)
			setupLog.Info("HTTP credential mappings built",
				"agent", agent.Name,
				"credentialCount", len(env.Credentials),
				"mappingCount", len(credMappings))
			if len(credMappings) > 0 {
				credStore = tools.NewHTTPCredentialStore(credMappings)
			}
		} else {
			if env == nil {
				setupLog.Info("No environment for HTTP credentials", "agent", agent.Name)
			} else {
				setupLog.Info("No credentials in environment", "agent", agent.Name, "credentialCount", len(env.Credentials))
			}
		}
		// Register HTTP tools with credential store
		httpGet := tools.NewHTTPGetTool()
		if credStore != nil {
			httpGet = httpGet.WithCredentials(credStore)
		}
		reg.Register(httpGet)
		reg.Register(tools.NewHTTPPostTool())
		reg.Register(tools.NewHTTPDeleteTool())
		// Register kubectl tools using in-cluster config
		restCfg := mgr.GetConfig()
		cs, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
		}
		dc, err := dynamic.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create dynamic client: %w", err)
		}
		reg.Register(tools.NewKubectlGetTool(cs, dc))
		reg.Register(tools.NewKubectlDescribeTool(dc))
		reg.Register(tools.NewKubectlLogsTool(cs))
		reg.Register(tools.NewKubectlRolloutTool(dc))
		reg.Register(tools.NewKubectlScaleTool(dc))
		reg.Register(tools.NewKubectlDeleteTool(dc))
		reg.Register(tools.NewKubectlApplyTool(dc))

		// Register SSH tool if environment has SSH credentials
		if env != nil {
			sshCreds := buildSSHCredentials(env)
			if len(sshCreds) > 0 {
				sshTool := tools.NewSSHTool(sshCreds)
				reg.Register(sshTool)
				setupLog.Info("SSH tool registered",
					"agent", agent.Name,
					"hostCount", len(sshCreds))
			}
		}

		return reg, nil
	}

	// Wire RunConfigFactory into scheduler so scheduled runs get providers + tools
	sched.RunConfigFactory = func(agent *corev1alpha1.LegatorAgent) (runner.RunConfig, error) {
		cfg := runner.RunConfig{}
		p, err := providerFactory(agent, nil)
		if err != nil {
			return cfg, fmt.Errorf("provider factory: %w", err)
		}
		cfg.Provider = p
		// Resolve environment for credential-aware HTTP tools
		var resolvedEnv *resolver.ResolvedEnvironment
		if agent.Spec.EnvironmentRef != "" {
			envResolver := resolver.NewEnvironmentResolver(mgr.GetClient(), agent.Namespace)
			resolvedEnv, _ = envResolver.Resolve(context.Background(), agent.Spec.EnvironmentRef)
			// Non-fatal: tools work without creds, just can't auth
		}
		reg, err := toolRegistryFactory(agent, resolvedEnv)
		if err != nil {
			return cfg, fmt.Errorf("tool registry factory: %w", err)
		}
		cfg.ToolRegistry = reg
		return cfg, nil
	}
	sched.RateLimiter = rateLimiter

	_ = clientFactory
	_ = shutdownMgr

	if err := (&controller.LegatorAgentReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		Runner:              agentRunner,
		ProviderFactory:     providerFactory,
		ToolRegistryFactory: toolRegistryFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "LegatorAgent")
		os.Exit(1)
	}
	if err := (&controller.LegatorEnvironmentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "LegatorEnvironment")
		os.Exit(1)
	}
	if err := (&controller.LegatorRunReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "LegatorRun")
		os.Exit(1)
	}
	if err := (&controller.ModelTierConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ModelTierConfig")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// buildHTTPCredentialMappings maps known API URL prefixes to credential tokens
// from the resolved environment. This allows HTTP tools to auto-inject auth headers.
func buildHTTPCredentialMappings(env *resolver.ResolvedEnvironment) map[string]string {
	mappings := make(map[string]string)

	// Map well-known credential names to API URL prefixes
	knownMappings := map[string][]string{
		"github-pat": {"https://api.github.com"},
	}

	for credName, prefixes := range knownMappings {
		credData, ok := env.Credentials[credName]
		if !ok {
			continue
		}
		// Look for "token" key first, then any key
		token := credData["token"]
		if token == "" {
			for _, v := range credData {
				token = v
				break
			}
		}
		if token == "" {
			continue
		}
		for _, prefix := range prefixes {
			mappings[prefix] = token
		}
	}

	return mappings
}

// buildSSHCredentials extracts SSH credentials from the resolved environment.
// Looks for credentials with type "ssh-key" or "ssh-password" and builds
// SSHCredential structs for the SSH tool.
func buildSSHCredentials(env *resolver.ResolvedEnvironment) map[string]*tools.SSHCredential {
	creds := make(map[string]*tools.SSHCredential)

	for name, data := range env.Credentials {
		// SSH credentials must have a "host" key
		host := data["host"]
		if host == "" {
			continue
		}
		user := data["user"]
		if user == "" {
			user = data["username"]
		}
		if user == "" {
			continue
		}

		cred := &tools.SSHCredential{
			Host:      host,
			User:      user,
			AllowSudo: data["allow-sudo"] == "true",
			AllowRoot: data["allow-root"] == "true",
		}

		// Check for private key
		if pk := data["private-key"]; pk != "" {
			cred.PrivateKey = []byte(pk)
		} else if pk := data["ssh-private-key"]; pk != "" {
			cred.PrivateKey = []byte(pk)
		}

		// Check for password
		if pw := data["password"]; pw != "" {
			cred.Password = pw
		}

		// Must have at least one auth method
		if len(cred.PrivateKey) == 0 && cred.Password == "" {
			continue
		}

		creds[name] = cred
	}

	return creds
}

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
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"fmt"

	"github.com/go-logr/logr"
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
	"github.com/marcus-qen/legator/internal/a2a"
	"github.com/marcus-qen/legator/internal/anomaly"
	"github.com/marcus-qen/legator/internal/api"
	apiauth "github.com/marcus-qen/legator/internal/api/auth"
	apirbac "github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/approval"
	"github.com/marcus-qen/legator/internal/assembler"
	"github.com/marcus-qen/legator/internal/chatops"
	connectivitypkg "github.com/marcus-qen/legator/internal/connectivity"
	"github.com/marcus-qen/legator/internal/controller"
	"github.com/marcus-qen/legator/internal/events"
	"github.com/marcus-qen/legator/internal/inventory"
	"github.com/marcus-qen/legator/internal/lifecycle"
	_ "github.com/marcus-qen/legator/internal/metrics" // Register Prometheus metrics
	"github.com/marcus-qen/legator/internal/multicluster"
	"github.com/marcus-qen/legator/internal/notify"
	"github.com/marcus-qen/legator/internal/provider"
	"github.com/marcus-qen/legator/internal/ratelimit"
	"github.com/marcus-qen/legator/internal/resolver"
	"github.com/marcus-qen/legator/internal/retention"
	"github.com/marcus-qen/legator/internal/runner"
	"github.com/marcus-qen/legator/internal/scheduler"
	"github.com/marcus-qen/legator/internal/state"
	"github.com/marcus-qen/legator/internal/telemetry"
	"github.com/marcus-qen/legator/internal/tools"
	vaultpkg "github.com/marcus-qen/legator/internal/vault"
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
	var apiListenAddr string
	var apiOIDCIssuer string
	var apiOIDCAudience string
	var apiAdminGroup string
	var apiOperatorGroup string
	var apiViewerGroup string
	var chatopsTelegramBotToken string
	var chatopsTelegramBindings string
	var chatopsTelegramAPIBaseURL string
	var chatopsTelegramPollInterval time.Duration
	var chatopsTelegramLongPollTimeout time.Duration
	var chatopsTelegramConfirmationTTL time.Duration
	var headscaleAPIURL string
	var headscaleAPIKey string
	var headscaleSyncInterval time.Duration
	var anomalyScanInterval time.Duration
	var anomalyLookback time.Duration
	var anomalyFrequencyWindow time.Duration
	var anomalyFrequencyThreshold int
	var anomalyScopeSpikeMultiplier float64
	var anomalyMinScopeSpikeDelta int
	var anomalyTargetDriftMinSamples int
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
	var webhookListenAddr string
	flag.StringVar(&webhookListenAddr, "webhook-listen-address", ":9443",
		"The address the webhook trigger endpoint listens on. Set to 0 to disable.")
	flag.StringVar(&apiListenAddr, "api-bind-address", ":8090",
		"The address the Legator API server listens on. Set to 0 to disable.")
	flag.StringVar(&apiOIDCIssuer, "api-oidc-issuer", os.Getenv("API_OIDC_ISSUER"),
		"OIDC issuer URL for API JWT validation.")
	flag.StringVar(&apiOIDCAudience, "api-oidc-audience", os.Getenv("API_OIDC_AUDIENCE"),
		"OIDC audience (client ID) for API JWT validation.")
	flag.StringVar(&apiAdminGroup, "api-admin-group", envOrDefault("API_ADMIN_GROUP", "legator-admin"),
		"OIDC group granted admin role.")
	flag.StringVar(&apiOperatorGroup, "api-operator-group", envOrDefault("API_OPERATOR_GROUP", "legator-operator"),
		"OIDC group granted operator role.")
	flag.StringVar(&apiViewerGroup, "api-viewer-group", envOrDefault("API_VIEWER_GROUP", "legator-viewer"),
		"OIDC group granted viewer role.")
	flag.StringVar(&chatopsTelegramBotToken, "chatops-telegram-bot-token", os.Getenv("CHATOPS_TELEGRAM_BOT_TOKEN"),
		"Telegram bot token for ChatOps MVP. Empty disables ChatOps polling.")
	flag.StringVar(&chatopsTelegramBindings, "chatops-telegram-bindings", os.Getenv("CHATOPS_TELEGRAM_BINDINGS"),
		"JSON array mapping Telegram chat IDs to API identities.")
	flag.StringVar(&chatopsTelegramAPIBaseURL, "chatops-telegram-api-base-url", os.Getenv("CHATOPS_TELEGRAM_API_BASE_URL"),
		"Legator API base URL used by Telegram ChatOps commands.")
	flag.DurationVar(&chatopsTelegramPollInterval, "chatops-telegram-poll-interval", 2*time.Second,
		"Polling interval between Telegram getUpdates calls.")
	flag.DurationVar(&chatopsTelegramLongPollTimeout, "chatops-telegram-long-poll-timeout", 25*time.Second,
		"Long-poll timeout for Telegram getUpdates requests.")
	flag.DurationVar(&chatopsTelegramConfirmationTTL, "chatops-telegram-confirmation-ttl", 2*time.Minute,
		"Typed confirmation timeout for Telegram approval/deny workflows.")
	flag.StringVar(&headscaleAPIURL, "headscale-api-url", os.Getenv("HEADSCALE_API_URL"),
		"Headscale API base URL (enables inventory sync when set with --headscale-api-key).")
	flag.StringVar(&headscaleAPIKey, "headscale-api-key", os.Getenv("HEADSCALE_API_KEY"),
		"Headscale API key (enables inventory sync when set with --headscale-api-url).")
	flag.DurationVar(&headscaleSyncInterval, "headscale-sync-interval", 30*time.Second,
		"How often to poll Headscale for inventory updates.")
	flag.DurationVar(&anomalyScanInterval, "anomaly-scan-interval", 2*time.Minute,
		"How often to evaluate anomaly heuristics against recent runs.")
	flag.DurationVar(&anomalyLookback, "anomaly-lookback", 24*time.Hour,
		"Lookback window used for anomaly baseline comparisons.")
	flag.DurationVar(&anomalyFrequencyWindow, "anomaly-frequency-window", 30*time.Minute,
		"Window used for run-frequency anomaly checks.")
	flag.IntVar(&anomalyFrequencyThreshold, "anomaly-frequency-threshold", 6,
		"Run count threshold inside anomaly-frequency-window before emitting frequency anomalies.")
	flag.Float64Var(&anomalyScopeSpikeMultiplier, "anomaly-scope-spike-multiplier", 2.5,
		"Multiplier over baseline action count to classify a scope spike anomaly.")
	flag.IntVar(&anomalyMinScopeSpikeDelta, "anomaly-min-scope-delta", 5,
		"Minimum absolute action-count increase required to emit a scope spike anomaly.")
	flag.IntVar(&anomalyTargetDriftMinSamples, "anomaly-target-drift-min-samples", 5,
		"Minimum historical run samples required before target-drift anomalies are evaluated.")
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

	// Webhook trigger handler — expose the scheduler's webhook handler over HTTP
	if webhookListenAddr != "" && webhookListenAddr != "0" {
		mux := http.NewServeMux()
		mux.Handle("/webhook/", sched.WebhookHandler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})

		go func() {
			setupLog.Info("Starting webhook trigger listener", "addr", webhookListenAddr)
			if err := http.ListenAndServe(webhookListenAddr, mux); err != nil {
				setupLog.Error(err, "Webhook listener failed")
			}
		}()
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

		// Register SQL tool if environment has database credentials
		if env != nil {
			sqlDatabases := buildSQLDatabases(env)
			if len(sqlDatabases) > 0 {
				sqlTool := tools.NewSQLTool(sqlDatabases)
				reg.Register(sqlTool)
				setupLog.Info("SQL tool registered",
					"agent", agent.Name,
					"databaseCount", len(sqlDatabases))
			}
		}

		return reg, nil
	}

	// --- v0.7.0: Wire orphaned packages (must be before RunConfigFactory) ---

	// Approval manager (singleton — creates ApprovalRequest CRDs, polls for decisions)
	approvalMgr := approval.NewManager(mgr.GetClient(), ctrl.Log.WithName("approval"))
	setupLog.Info("Approval manager initialised")

	// Event bus (publish/consume AgentEvent CRDs for inter-agent coordination)
	eventBus := events.NewBus(mgr.GetClient(), ctrl.Log.WithName("events"))
	setupLog.Info("Event bus initialised")

	// State manager (per-agent persistent key-value via AgentState CRDs)
	stateMgr := state.NewManager(mgr.GetClient(), ctrl.Log.WithName("state"))
	setupLog.Info("State manager initialised")

	// A2A router (agent-to-agent task delegation via AgentEvent CRDs)
	a2aRouter := a2a.NewRouter(mgr.GetClient(), "agents")
	setupLog.Info("A2A router initialised")

	// Notification router (Slack/Telegram/Email/Webhook severity-based routing)
	notifyRouter := buildNotificationRouter(ctrl.Log.WithName("notify"))
	if notifyRouter != nil {
		setupLog.Info("Notification router initialised")
	} else {
		setupLog.Info("No notification channels configured — notifications disabled")
	}

	_ = eventBus // Available for future use in runner post-run hooks

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

		// Pre-run connectivity check
		if resolvedEnv != nil && resolvedEnv.Connectivity != nil {
			connMgr := connectivitypkg.NewManager(setupLog.WithName("connectivity"))
			if err := connMgr.PreRunCheck(context.Background(), resolvedEnv.Connectivity, resolvedEnv.Endpoints); err != nil {
				setupLog.Error(err, "connectivity pre-run check failed", "agent", agent.Name)
				// Non-fatal: agent may still work with available endpoints
			}
		}

		// Create Vault credential manager if environment has Vault config
		var credMgr *vaultpkg.CredentialManager
		if resolvedEnv != nil {
			vaultCfg := getVaultConfigFromEnv(resolvedEnv, mgr.GetClient(), agent.Namespace)
			if vaultCfg != nil {
				vc, err := vaultpkg.NewClient(*vaultCfg)
				if err != nil {
					setupLog.Error(err, "failed to create Vault client", "agent", agent.Name)
				} else {
					if err := vc.Authenticate(context.Background()); err != nil {
						setupLog.Error(err, "failed to authenticate to Vault", "agent", agent.Name)
					} else {
						credMgr = vaultpkg.NewCredentialManager(vc)
						setupLog.Info("Vault credential manager created", "agent", agent.Name)
					}
				}
			}
		}

		// Request dynamic credentials from Vault if configured
		if credMgr != nil && resolvedEnv != nil {
			requestVaultSSHCredentials(context.Background(), credMgr, resolvedEnv, setupLog)
			requestVaultDBCredentials(context.Background(), credMgr, resolvedEnv, setupLog)
		}

		reg, err := toolRegistryFactory(agent, resolvedEnv)
		if err != nil {
			return cfg, fmt.Errorf("tool registry factory: %w", err)
		}

		// --- v0.7.0: Register state, A2A, and DNS tools ---

		// State tools — agent can remember things across runs
		reg.Register(state.NewStateGetTool(stateMgr, agent.Name, agent.Namespace))
		reg.Register(state.NewStateSetTool(stateMgr, agent.Name, agent.Namespace, ""))
		reg.Register(state.NewStateDeleteTool(stateMgr, agent.Name, agent.Namespace))

		// A2A tools — agent can delegate tasks to other agents
		reg.Register(a2a.NewDelegateTaskTool(a2aRouter, agent.Name))
		reg.Register(a2a.NewCheckTasksTool(a2aRouter, agent.Name))

		cfg.ToolRegistry = reg

		// --- v0.7.0: Wire approval manager ---
		if agent.Spec.Guardrails.ApprovalMode != "" && agent.Spec.Guardrails.ApprovalMode != "none" {
			cfg.ApprovalManager = approvalMgr
		}

		// --- v0.7.0: Wire notification delivery as post-run callback ---
		var cleanups []func(ctx context.Context) []error

		// Vault credential cleanup
		if credMgr != nil {
			cleanups = append(cleanups, credMgr.Cleanup)
		}

		// Notification delivery after run completion
		if notifyRouter != nil {
			cleanups = append(cleanups, func(ctx context.Context) []error {
				// Notifications are fired by the runner's finalizeRun via NotifyFunc
				// (see RunConfig.NotifyFunc below) — this is just a placeholder
				// to show the wiring is in place.
				return nil
			})
		}

		if len(cleanups) > 0 {
			cfg.Cleanup = func(ctx context.Context) []error {
				var allErrs []error
				for _, fn := range cleanups {
					if errs := fn(ctx); len(errs) > 0 {
						allErrs = append(allErrs, errs...)
					}
				}
				return allErrs
			}
		}

		// Wire notification function — called after run finalization with the completed run
		if notifyRouter != nil {
			cfg.NotifyFunc = func(ctx context.Context, agent *corev1alpha1.LegatorAgent, run *corev1alpha1.LegatorRun) {
				// Determine severity from run result
				severity := "info"
				if run.Status.Phase == corev1alpha1.RunPhaseFailed {
					severity = "warning"
				}
				if run.Status.Guardrails != nil && run.Status.Guardrails.EscalationsTriggered > 0 {
					severity = "critical"
				}

				msg := notify.Message{
					AgentName: agent.Name,
					RunName:   run.Name,
					Severity:  severity,
					Title:     fmt.Sprintf("Run %s: %s", run.Status.Phase, run.Name),
					Body:      truncateReport(run.Status.Report, 1000),
					Timestamp: time.Now(),
				}

				if errs := notifyRouter.Notify(ctx, msg); len(errs) > 0 {
					for _, e := range errs {
						setupLog.Error(e, "notification delivery failed",
							"agent", agent.Name, "run", run.Name)
					}
				}
			}
		}

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
		OnReconcile: func(agent *corev1alpha1.LegatorAgent) {
			sched.RegisterWebhookTriggers(agent)
		},
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
	if err := (&controller.UserPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "UserPolicy")
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

	// --- v0.8.0: Wire Headscale inventory sync loop (optional) ---
	var headscaleSync *inventory.HeadscaleSync
	if headscaleAPIURL != "" && headscaleAPIKey != "" {
		headscaleSync = inventory.NewHeadscaleSync(inventory.HeadscaleSyncConfig{
			BaseURL:      headscaleAPIURL,
			APIKey:       headscaleAPIKey,
			SyncInterval: headscaleSyncInterval,
		}, ctrl.Log)

		if err := mgr.Add(headscaleSync); err != nil {
			setupLog.Error(err, "Failed to add Headscale sync loop")
			os.Exit(1)
		}
		setupLog.Info("Headscale inventory sync registered",
			"url", headscaleAPIURL,
			"interval", headscaleSyncInterval.String(),
		)
	} else {
		setupLog.Info("Headscale inventory sync disabled",
			"reason", "headscale-api-url or headscale-api-key not set",
		)
	}

	// --- v0.9.0: Wire anomaly detection baseline pipeline ---
	anomalyDetector := anomaly.NewDetector(mgr.GetClient(), anomaly.Config{
		Namespace:             "agents",
		ScanInterval:          anomalyScanInterval,
		Lookback:              anomalyLookback,
		FrequencyWindow:       anomalyFrequencyWindow,
		FrequencyThreshold:    anomalyFrequencyThreshold,
		ScopeSpikeMultiplier:  anomalyScopeSpikeMultiplier,
		MinScopeSpikeDelta:    anomalyMinScopeSpikeDelta,
		TargetDriftMinSamples: anomalyTargetDriftMinSamples,
	}, ctrl.Log)
	if err := mgr.Add(anomalyDetector); err != nil {
		setupLog.Error(err, "Failed to add anomaly detector")
		os.Exit(1)
	}
	setupLog.Info("Anomaly detection baseline registered",
		"interval", anomalyScanInterval.String(),
		"lookback", anomalyLookback.String(),
		"frequencyWindow", anomalyFrequencyWindow.String(),
		"frequencyThreshold", anomalyFrequencyThreshold,
	)

	// --- v0.8.0: Wire Legator API server ---
	apiEnabled := apiListenAddr != "" && apiListenAddr != "0"
	if apiEnabled {
		apiSrv := api.NewServer(api.ServerConfig{
			ListenAddr: apiListenAddr,
			OIDC: apiauth.OIDCConfig{
				IssuerURL:   apiOIDCIssuer,
				Audience:    apiOIDCAudience,
				BypassPaths: []string{"/healthz"},
			},
			Policies:  buildAPIPolicies(apiAdminGroup, apiOperatorGroup, apiViewerGroup),
			Inventory: headscaleSync,
		}, mgr.GetClient(), ctrl.Log)

		// Register as a controller-runtime Runnable so it starts/stops with the manager
		if err := mgr.Add(apiSrv); err != nil {
			setupLog.Error(err, "Failed to add API server to manager")
			os.Exit(1)
		}
		setupLog.Info("API server registered", "addr", apiListenAddr)
	}

	// --- v0.9.0 P3.1: Telegram-first ChatOps MVP ---
	if chatopsTelegramBotToken != "" {
		if !apiEnabled {
			setupLog.Error(errors.New("api disabled"), "Telegram ChatOps requires API server", "api-bind-address", apiListenAddr)
			os.Exit(1)
		}

		bindings, err := chatops.ParseBindingsJSON(chatopsTelegramBindings)
		if err != nil {
			setupLog.Error(err, "Failed to parse Telegram ChatOps bindings")
			os.Exit(1)
		}
		if len(bindings) == 0 {
			setupLog.Error(errors.New("no bindings"), "Telegram ChatOps requires at least one chat binding")
			os.Exit(1)
		}

		chatopsAPIBaseURL := chatopsTelegramAPIBaseURL
		if chatopsAPIBaseURL == "" {
			chatopsAPIBaseURL = inferLocalAPIBaseURL(apiListenAddr)
		}

		chatopsBot, err := chatops.NewTelegramBot(chatops.TelegramConfig{
			BotToken:        chatopsTelegramBotToken,
			APIBaseURL:      chatopsAPIBaseURL,
			APIIssuer:       apiOIDCIssuer,
			APIAudience:     apiOIDCAudience,
			PollInterval:    chatopsTelegramPollInterval,
			LongPollTimeout: chatopsTelegramLongPollTimeout,
			ConfirmationTTL: chatopsTelegramConfirmationTTL,
			UserBindings:    bindings,
		}, ctrl.Log)
		if err != nil {
			setupLog.Error(err, "Failed to initialize Telegram ChatOps bot")
			os.Exit(1)
		}

		if err := mgr.Add(chatopsBot); err != nil {
			setupLog.Error(err, "Failed to add Telegram ChatOps bot to manager")
			os.Exit(1)
		}
		setupLog.Info("Telegram ChatOps bot registered",
			"apiBaseURL", chatopsAPIBaseURL,
			"bindings", len(bindings),
			"confirmationTTL", chatopsTelegramConfirmationTTL.String(),
		)
	} else {
		setupLog.Info("Telegram ChatOps bot disabled", "reason", "chatops-telegram-bot-token not set")
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

// getVaultConfigFromEnv extracts Vault client config from a resolved environment.
// Returns nil if the environment has no Vault configuration.
func getVaultConfigFromEnv(env *resolver.ResolvedEnvironment, c client.Client, namespace string) *vaultpkg.Config {
	if env == nil || env.VaultConfig == nil {
		return nil
	}

	vc := env.VaultConfig
	cfg := &vaultpkg.Config{
		Address:     vc.Address,
		K8sAuthRole: vc.K8sAuthRole,
		K8sAuthPath: vc.K8sAuthPath,
	}

	// Resolve Vault token from Secret if using token auth
	if vc.AuthMethod == "token" && vc.TokenSecretRef != "" {
		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), client.ObjectKey{
			Name:      vc.TokenSecretRef,
			Namespace: namespace,
		}, secret); err == nil {
			if token, ok := secret.Data["token"]; ok {
				cfg.Token = string(token)
			}
		}
	}

	return cfg
}

// requestVaultSSHCredentials checks for vault-ssh-ca credential types in the environment
// and requests ephemeral SSH certificates from Vault. The resulting credentials are
// injected back into the resolved environment's credential map so buildSSHCredentials
// can pick them up as normal SSH credentials.
func requestVaultSSHCredentials(
	ctx context.Context,
	credMgr *vaultpkg.CredentialManager,
	env *resolver.ResolvedEnvironment,
	log logr.Logger,
) {
	if env.RawCredentials == nil {
		return
	}

	for name, credRef := range env.RawCredentials {
		if credRef.Type != "vault-ssh-ca" || credRef.Vault == nil {
			continue
		}

		// Get the host and user from the existing credential data
		credData := env.Credentials[name]
		if credData == nil {
			credData = make(map[string]string)
		}

		user := credData["user"]
		if user == "" {
			user = credData["username"]
		}
		if user == "" {
			user = "root" // Vault SSH CA will enforce allowed principals
		}

		sshCreds, err := credMgr.RequestSSHCredentials(ctx, vaultpkg.SSHCARequest{
			Mount: credRef.Vault.Mount,
			Role:  credRef.Vault.Role,
			User:  user,
			TTL:   credRef.Vault.TTL,
		})
		if err != nil {
			log.Error(err, "failed to request Vault SSH credentials",
				"credential", name,
				"mount", credRef.Vault.Mount,
				"role", credRef.Vault.Role)
			continue
		}

		// Inject the ephemeral credentials back into the resolved environment
		if env.Credentials[name] == nil {
			env.Credentials[name] = make(map[string]string)
		}
		env.Credentials[name]["private-key"] = sshCreds.PrivateKey
		env.Credentials[name]["certificate"] = sshCreds.Certificate
		env.Credentials[name]["user"] = sshCreds.User

		log.Info("Vault SSH credentials injected",
			"credential", name,
			"user", sshCreds.User,
			"mount", credRef.Vault.Mount)
	}
}

// requestVaultDBCredentials checks for vault-database credential types in the environment
// and requests ephemeral database credentials from Vault. The resulting credentials are
// injected back into the resolved environment's credential map so buildSQLDatabases
// can pick them up as normal database credentials.
func requestVaultDBCredentials(
	ctx context.Context,
	credMgr *vaultpkg.CredentialManager,
	env *resolver.ResolvedEnvironment,
	log logr.Logger,
) {
	if env.RawCredentials == nil {
		return
	}

	for name, credRef := range env.RawCredentials {
		if credRef.Type != "vault-database" || credRef.Vault == nil {
			continue
		}

		dbCreds, err := credMgr.RequestDBCredentials(ctx, credRef.Vault.Mount, credRef.Vault.Role)
		if err != nil {
			log.Error(err, "failed to request Vault database credentials",
				"credential", name,
				"mount", credRef.Vault.Mount,
				"role", credRef.Vault.Role)
			continue
		}

		// Inject the ephemeral credentials back into the resolved environment
		if env.Credentials[name] == nil {
			env.Credentials[name] = make(map[string]string)
		}
		env.Credentials[name]["username"] = dbCreds.Username
		env.Credentials[name]["password"] = dbCreds.Password

		log.Info("Vault database credentials injected",
			"credential", name,
			"username", dbCreds.Username,
			"mount", credRef.Vault.Mount,
			"role", credRef.Vault.Role)
	}
}

// buildSQLDatabases extracts database configuration from the resolved environment.
// Looks for credentials with keys: driver, host, port, dbname, and either username/password
// or Vault-injected credentials.
func buildSQLDatabases(env *resolver.ResolvedEnvironment) map[string]*tools.SQLDatabase {
	dbs := make(map[string]*tools.SQLDatabase)

	for name, data := range env.Credentials {
		driver := data["driver"]
		if driver == "" {
			continue // Not a database credential
		}

		host := data["host"]
		if host == "" {
			continue
		}

		port := data["port"]
		dbname := data["dbname"]
		if dbname == "" {
			dbname = data["database"]
		}

		username := data["username"]
		if username == "" {
			username = data["user"]
		}
		password := data["password"]

		// Must have auth
		if username == "" {
			continue
		}

		// Build DSN based on driver
		var dsn string
		switch driver {
		case "postgres", "postgresql":
			sslmode := data["sslmode"]
			if sslmode == "" {
				sslmode = "require"
			}
			if port == "" {
				port = "5432"
			}
			dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
				host, port, username, password, dbname, sslmode)
		case "mysql":
			if port == "" {
				port = "3306"
			}
			tls := data["tls"]
			if tls == "" {
				tls = "preferred"
			}
			dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?tls=%s&parseTime=true",
				username, password, host, port, dbname, tls)
		default:
			continue // Unsupported driver
		}

		db := &tools.SQLDatabase{
			Driver: driver,
			DSN:    dsn,
		}

		// Normalise driver name for database/sql
		if db.Driver == "postgresql" {
			db.Driver = "postgres"
		}

		dbs[name] = db
	}

	return dbs
}

// buildNotificationRouter creates a notification Router from environment variables.
// Returns nil if no channels are configured.
func buildNotificationRouter(log logr.Logger) *notify.Router {
	var routes notify.SeverityRoute
	hasChannels := false

	// Slack
	if url := os.Getenv("LEGATOR_NOTIFY_SLACK_WEBHOOK"); url != "" {
		ch := notify.NewSlackChannel(url, os.Getenv("LEGATOR_NOTIFY_SLACK_CHANNEL"))
		routes.Info = append(routes.Info, ch)
		routes.Warning = append(routes.Warning, ch)
		routes.Critical = append(routes.Critical, ch)
		hasChannels = true
	}

	// Telegram
	botToken := os.Getenv("LEGATOR_NOTIFY_TELEGRAM_TOKEN")
	chatID := os.Getenv("LEGATOR_NOTIFY_TELEGRAM_CHAT_ID")
	if botToken != "" && chatID != "" {
		ch := notify.NewTelegramChannel(botToken, chatID)
		routes.Info = append(routes.Info, ch)
		routes.Warning = append(routes.Warning, ch)
		routes.Critical = append(routes.Critical, ch)
		hasChannels = true
	}

	// Generic webhook
	if url := os.Getenv("LEGATOR_NOTIFY_WEBHOOK_URL"); url != "" {
		ch := notify.NewWebhookChannel(url, nil)
		routes.Info = append(routes.Info, ch)
		routes.Warning = append(routes.Warning, ch)
		routes.Critical = append(routes.Critical, ch)
		hasChannels = true
	}

	if !hasChannels {
		return nil
	}

	// Rate limiter: default 100/hour/agent
	maxPerHour := 100
	limiter := notify.NewRateLimiter(maxPerHour)

	return notify.NewRouter(routes, limiter, log)
}

// truncateReport shortens a report string to maxLen, adding "..." if truncated.
func truncateReport(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// buildAPIPolicies constructs RBAC policies from OIDC group names.
func buildAPIPolicies(adminGroup, operatorGroup, viewerGroup string) []apirbac.UserPolicy {
	return []apirbac.UserPolicy{
		{
			Name:     "admin-group",
			Subjects: []apirbac.SubjectMatcher{{Claim: "groups", Value: adminGroup}},
			Role:     apirbac.RoleAdmin,
		},
		{
			Name:     "operator-group",
			Subjects: []apirbac.SubjectMatcher{{Claim: "groups", Value: operatorGroup}},
			Role:     apirbac.RoleOperator,
		},
		{
			Name:     "viewer-group",
			Subjects: []apirbac.SubjectMatcher{{Claim: "groups", Value: viewerGroup}},
			Role:     apirbac.RoleViewer,
		},
	}
}

// inferLocalAPIBaseURL converts an API bind address (e.g. :8090) into a local HTTP URL.
func inferLocalAPIBaseURL(bindAddr string) string {
	bindAddr = strings.TrimSpace(bindAddr)
	if strings.HasPrefix(bindAddr, "http://") || strings.HasPrefix(bindAddr, "https://") {
		return bindAddr
	}
	if strings.HasPrefix(bindAddr, ":") {
		return "http://127.0.0.1" + bindAddr
	}
	if strings.HasPrefix(bindAddr, "0.0.0.0:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(bindAddr, "0.0.0.0:")
	}
	if strings.HasPrefix(bindAddr, "[::]:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(bindAddr, "[::]:")
	}
	if strings.Contains(bindAddr, ":") {
		return "http://" + bindAddr
	}
	return "http://127.0.0.1:8090"
}

// envOrDefault reads an environment variable, returning a default if empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

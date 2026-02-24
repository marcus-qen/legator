/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/dashboard"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		listenAddr   string
		namespace    string
		apiBaseURL   string
		oidcIssuer   string
		oidcClientID string
		oidcSecret   string
		oidcRedirect string
	)

	defaultAPIBaseURL := os.Getenv("DASHBOARD_API_URL")
	if defaultAPIBaseURL == "" {
		defaultAPIBaseURL = os.Getenv("LEGATOR_API_URL")
	}
	if defaultAPIBaseURL == "" {
		defaultAPIBaseURL = "http://127.0.0.1:8090"
	}

	flag.StringVar(&listenAddr, "listen", ":8080", "Dashboard listen address")
	flag.StringVar(&namespace, "namespace", "", "Filter to specific namespace (empty = all)")
	flag.StringVar(&apiBaseURL, "api-base-url", defaultAPIBaseURL, "Legator API base URL for dashboard mutations and cockpit data")
	flag.StringVar(&oidcIssuer, "oidc-issuer", os.Getenv("OIDC_ISSUER"), "OIDC issuer URL")
	flag.StringVar(&oidcClientID, "oidc-client-id", os.Getenv("OIDC_CLIENT_ID"), "OIDC client ID")
	flag.StringVar(&oidcSecret, "oidc-client-secret", os.Getenv("OIDC_CLIENT_SECRET"), "OIDC client secret")
	flag.StringVar(&oidcRedirect, "oidc-redirect-url", os.Getenv("OIDC_REDIRECT_URL"), "OIDC redirect URL")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("dashboard")

	// Create Kubernetes client
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get kubeconfig: %v\n", err)
		os.Exit(1)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create client: %v\n", err)
		os.Exit(1)
	}

	dashConfig := dashboard.Config{
		ListenAddr:       listenAddr,
		Namespace:        namespace,
		APIBaseURL:       apiBaseURL,
		OIDCIssuer:       oidcIssuer,
		OIDCClientID:     oidcClientID,
		OIDCClientSecret: oidcSecret,
		OIDCRedirectURL:  oidcRedirect,
	}

	srv, err := dashboard.NewServer(k8sClient, dashConfig, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create dashboard server: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	log.Info("Starting Legator Dashboard",
		"addr", listenAddr,
		"namespace", namespace,
		"apiBaseURL", apiBaseURL)

	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Dashboard server error: %v\n", err)
		os.Exit(1)
	}
}

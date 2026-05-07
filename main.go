package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"promql-proxy/internal/cache"
	"promql-proxy/internal/labeler"
	"promql-proxy/internal/rbac"

	"github.com/prometheus-community/prom-label-proxy/injectproxy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	var (
		listenAddr   = flag.String("listen-address", ":8080", "Address for the proxy server")
		upstream     = flag.String("upstream", "", "Upstream Mimir URL (required)")
		headerName   = flag.String("header-name", "X-Forwarded-User", "HTTP header containing the username")
		groupsHeader = flag.String("groups-header", "X-Forwarded-Groups", "HTTP header containing comma-separated groups")
		cacheTTL     = flag.Duration("cache-ttl", 60*time.Second, "TTL for the namespace RBAC cache")
		rbacResource = flag.String("rbac-resource", "pods", "Kubernetes resource for the RBAC check")
		rbacAPIGroup = flag.String("rbac-api-group", "metrics.k8s.io", "Kubernetes API group for the RBAC check")
		rbacVerb     = flag.String("rbac-verb", "get", "Kubernetes verb for the RBAC check")
		metricsAddr  = flag.String("metrics-address", ":9090", "Address for metrics and health endpoints")
		kubeconfig   = flag.String("kubeconfig", "", "Path to kubeconfig (out-of-cluster development only)")

		// Upstream auth
		upstreamBearerTokenFile = flag.String("upstream-bearer-token-file", "", "Path to file containing bearer token for upstream")
		upstreamBasicAuthUser   = flag.String("upstream-basic-auth-username", "", "Username for upstream basic auth")
		upstreamBasicAuthPwFile = flag.String("upstream-basic-auth-password-file", "", "Path to file containing password for upstream basic auth")

		// Upstream TLS
		upstreamCAFile   = flag.String("upstream-tls-ca-file", "", "Path to CA certificate for upstream TLS verification")
		upstreamCertFile = flag.String("upstream-tls-cert-file", "", "Path to client certificate for upstream mTLS")
		upstreamKeyFile  = flag.String("upstream-tls-key-file", "", "Path to client key for upstream mTLS")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if *upstream == "" {
		logger.Error("--upstream is required")
		os.Exit(1)
	}

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		logger.Error("invalid upstream URL", "error", err)
		os.Exit(1)
	}

	// Configure upstream TLS
	if *upstreamCAFile != "" || *upstreamCertFile != "" {
		tlsConfig, err := buildUpstreamTLSConfig(*upstreamCAFile, *upstreamCertFile, *upstreamKeyFile)
		if err != nil {
			logger.Error("failed to configure upstream TLS", "error", err)
			os.Exit(1)
		}
		http.DefaultTransport.(*http.Transport).TLSClientConfig = tlsConfig
		logger.Info("upstream TLS configured")
	}

	// Kubernetes client
	var k8sConfig *rest.Config
	if *kubeconfig != "" {
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		k8sConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		logger.Error("failed to create Kubernetes config", "error", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		logger.Error("failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	// Components
	cacheCtx, cacheCancel := context.WithCancel(context.Background())
	defer cacheCancel()
	nsCache := cache.New(cacheCtx, *cacheTTL)
	checker := rbac.NewChecker(clientset, rbac.Config{
		Resource: *rbacResource,
		APIGroup: *rbacAPIGroup,
		Verb:     *rbacVerb,
	})
	rbacLabeler := labeler.NewRBACLabeler(*headerName, *groupsHeader, checker, nsCache, logger)

	// prom-label-proxy routes
	reg := prometheus.NewRegistry()
	routes, err := injectproxy.NewRoutes(
		upstreamURL,
		"namespace",
		rbacLabeler,
		injectproxy.WithEnabledLabelsAPI(),
		injectproxy.WithPrometheusRegistry(reg),
		injectproxy.WithErrorOnReplace(),
	)
	if err != nil {
		logger.Error("failed to create routes", "error", err)
		os.Exit(1)
	}

	// Upstream auth
	upstreamAuthHeader, err := buildUpstreamAuthHeader(*upstreamBearerTokenFile, *upstreamBasicAuthUser, *upstreamBasicAuthPwFile)
	if err != nil {
		logger.Error("failed to configure upstream auth", "error", err)
		os.Exit(1)
	}

	// Wrap routes with upstream auth injection
	var handler http.Handler = routes
	if upstreamAuthHeader != "" {
		logger.Info("upstream auth configured")
		handler = withUpstreamAuth(routes, upstreamAuthHeader)
	}

	// Metrics / health server
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	metricsMux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	metricsServer := &http.Server{
		Addr:         *metricsAddr,
		Handler:      metricsMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	proxyServer := &http.Server{
		Addr:         *listenAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		logger.Info("starting metrics server", "address", *metricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics server failed: %w", err)
		}
	}()

	go func() {
		logger.Info("starting promql-proxy",
			"address", *listenAddr,
			"upstream", *upstream,
			"header", *headerName,
			"cache_ttl", cacheTTL.String(),
		)
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("proxy server failed: %w", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("received signal", "signal", sig.String())
	case err := <-errCh:
		logger.Error("server error", "error", err)
	}

	logger.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = proxyServer.Shutdown(shutdownCtx)
	_ = metricsServer.Shutdown(shutdownCtx)
}

// withUpstreamAuth wraps a handler to inject an Authorization header on every
// request before it is forwarded by the reverse proxy.
func withUpstreamAuth(next http.Handler, authHeader string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", authHeader)
		next.ServeHTTP(w, r)
	})
}

// buildUpstreamAuthHeader constructs the Authorization header value from the
// provided flags. Returns an empty string if no auth is configured.
func buildUpstreamAuthHeader(bearerTokenFile, basicUser, basicPwFile string) (string, error) {
	if bearerTokenFile != "" && basicUser != "" {
		return "", fmt.Errorf("cannot specify both --upstream-bearer-token-file and --upstream-basic-auth-username")
	}

	if bearerTokenFile != "" {
		token, err := os.ReadFile(bearerTokenFile)
		if err != nil {
			return "", fmt.Errorf("reading bearer token file: %w", err)
		}
		return "Bearer " + strings.TrimSpace(string(token)), nil
	}

	if basicUser != "" {
		if basicPwFile == "" {
			return "", fmt.Errorf("--upstream-basic-auth-password-file is required when --upstream-basic-auth-username is set")
		}
		pw, err := os.ReadFile(basicPwFile)
		if err != nil {
			return "", fmt.Errorf("reading basic auth password file: %w", err)
		}
		creds := basicUser + ":" + strings.TrimSpace(string(pw))
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds)), nil
	}

	return "", nil
}

// buildUpstreamTLSConfig constructs a tls.Config from the provided CA and
// client certificate files.
func buildUpstreamTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	tlsConfig := &tls.Config{}

	if caFile != "" {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
		}
		tlsConfig.RootCAs = pool
	}

	if certFile != "" {
		if keyFile == "" {
			return nil, fmt.Errorf("--upstream-tls-key-file is required when --upstream-tls-cert-file is set")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

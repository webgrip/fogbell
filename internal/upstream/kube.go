package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

const serviceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"

// Kube reads the Kubernetes API as the pod's ServiceAccount.
type Kube struct {
	Base      string
	TokenFile string
	HTTP      *http.Client
}

// InCluster is the API as the pod's ServiceAccount; nil outside a cluster.
func InCluster() (*Kube, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, nil
	}
	if port == "" {
		port = "443"
	}
	ca, err := os.ReadFile(serviceAccount + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("serviceaccount ca.crt holds no certificate")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &Kube{
		Base:      "https://" + host + ":" + port,
		TokenFile: serviceAccount + "/token",
		HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
	}, nil
}

// Get reads a path. The token is read on every call: the projected token rotates under the pod.
func (k *Kube) Get(ctx context.Context, path string, out any) error {
	headers := map[string]string{}
	if k.TokenFile != "" {
		token, err := os.ReadFile(k.TokenFile)
		if err != nil {
			return err
		}
		headers["Authorization"] = "Bearer " + strings.TrimSpace(string(token))
	}
	return do(ctx, k.HTTP, http.MethodGet, join(k.Base, path), headers, nil, out)
}

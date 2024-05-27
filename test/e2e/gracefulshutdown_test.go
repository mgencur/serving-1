//go:build e2e
// +build e2e

/*
Copyright 2024 The Knative Authors

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

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pkgtest "knative.dev/pkg/test"
	"knative.dev/pkg/test/spoof"
	"knative.dev/serving/test"
	v1test "knative.dev/serving/test/v1"
)

const (
	kourierGatewayNamespace = "knative-serving-ingress"
	kourierGatewayLabel     = "app=3scale-kourier-gateway"
)

func TestGracefulShutdown(t *testing.T) {
	t.Parallel()
	clients := test.Setup(t)

	names := test.ResourceNames{
		Service: test.ObjectNameForTest(t),
		Image:   test.Timeout,
	}

	test.EnsureTearDown(t, clients, &names)

	gatewayNs := kourierGatewayNamespace
	if gatewayNsOverride := os.Getenv("GATEWAY_NAMESPACE_OVERRIDE"); gatewayNsOverride != "" {
		gatewayNs = gatewayNsOverride
	}

	// Align with the default DRAIN_TIME_SECONDS for Kourier gateway
	drainTime := 15 * time.Second

	t.Log("Creating a new Service ")
	resources, err := v1test.CreateServiceReady(t, clients, &names)
	if err != nil {
		t.Fatal("Failed to create Service:", err)
	}

	serviceURL := resources.Service.Status.URL.URL()

	t.Log("Probing to force at least one pod", serviceURL)
	if _, err := pkgtest.CheckEndpointState(
		context.Background(),
		clients.KubeClient,
		t.Logf,
		serviceURL,
		spoof.IsOneOfStatusCodes(http.StatusOK, http.StatusGatewayTimeout),
		"CheckSuccessfulResponse",
		test.ServingFlags.ResolvableDomain,
		test.AddRootCAtoTransport(context.Background(), t.Logf, clients, test.ServingFlags.HTTPS),
		spoof.WithHeader(test.ServingFlags.RequestHeader())); err != nil {
		t.Fatalf("Error probing %s: %v", serviceURL, err)
	}

	tests := []struct {
		name            string
		requestDuration time.Duration
		wantStatusCode  int
	}{
		{
			name:            fmt.Sprintf("do a request taking slightly less than the drain time: %s", drainTime),
			requestDuration: drainTime - (3 * time.Second),
			wantStatusCode:  http.StatusOK,
		},
		{
			name:            fmt.Sprintf("do a request taking slightly more than the drain time: %s", drainTime),
			requestDuration: drainTime + (3 * time.Second),
			// Additional info: The activator logs this error:
			//stream.go:305: E 08:35:24.827 activator-666d49f874-55njc [activator] [serving-tests/graceful-shutdown-gifjsfix-00001] error reverse proxying request; sockstat: sockets: used 19
			//TCP: inuse 8 orphan 7 tw 14 alloc 581 mem 0
			//UDP: inuse 0 mem 258
			//UDPLITE: inuse 0
			//RAW: inuse 0
			//FRAG: inuse 0 memory 0
			//err=context canceled
			wantStatusCode: http.StatusBadGateway,
		},
	}

	g := new(errgroup.Group)
	var statusCodes sync.Map

	// Run all requests asynchronously at the same time, and collect the results in statusCodes map
	for i := range tests {
		test := tests[i]

		g.Go(func() error {
			statusCode, err := sendRequestAndReturnStatus(t, clients, serviceURL, test.requestDuration, 0)
			statusCodes.Store(test.name, statusCode)
			return err
		})
	}

	// Ensures the requests sent by the goroutines above are in-flight
	time.Sleep(1 * time.Second)

	// Retrieve and delete all gateway pods
	gatewayPods, err := clients.KubeClient.CoreV1().Pods(gatewayNs).List(context.Background(), metav1.ListOptions{
		LabelSelector: kourierGatewayLabel,
	})
	if err != nil {
		t.Fatal("Failed to get Gateway pods:", err)
	}

	for _, gatewayPod := range gatewayPods.Items {
		if err := clients.KubeClient.CoreV1().Pods(gatewayNs).Delete(context.Background(), gatewayPod.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("Failed to delete pod %s: %v", gatewayPod.Name, err)
		}
	}

	// Wait until we get responses from the asynchronous requests
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		statusCode, _ := statusCodes.Load(test.name)

		if statusCode.(int) != test.wantStatusCode {
			t.Fatalf("%s has failed: expected %d, got %s", test.name, test.wantStatusCode, statusCode)
		}
	}
}

// sendRequests send a request to "endpoint", returns error if unexpected response code, nil otherwise.
func sendRequestAndReturnStatus(t *testing.T, clients *test.Clients, endpoint *url.URL,
	initialSleep, sleep time.Duration) (int, error) {
	client, err := pkgtest.NewSpoofingClient(context.Background(), clients.KubeClient, t.Logf, endpoint.Hostname(), test.ServingFlags.ResolvableDomain, test.AddRootCAtoTransport(context.Background(), t.Logf, clients, test.ServingFlags.HTTPS))
	if err != nil {
		return 0, fmt.Errorf("error creating Spoofing client: %w", err)
	}

	start := time.Now()
	defer func() {
		t.Logf("URL: %v, initialSleep: %v, sleep: %v, request elapsed %v ms", endpoint, initialSleep, sleep,
			time.Since(start).Milliseconds())
	}()
	u, _ := url.Parse(endpoint.String())
	q := u.Query()
	q.Set("initialTimeout", fmt.Sprint(initialSleep.Milliseconds()))
	q.Set("timeout", fmt.Sprint(sleep.Milliseconds()))
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create new HTTP request: %w", err)
	}
	spoof.WithHeader(test.ServingFlags.RequestHeader())(req)

	resp, err := client.Do(req)

	return resp.StatusCode, nil
}

// +build e2e

/*
Copyright 2020 The Knative Authors

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

package ha

import (
	"log"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"
	pkgTest "knative.dev/pkg/test"
	"knative.dev/serving/test"
	"knative.dev/serving/test/e2e"
)

const (
	kourierGatewayDeployment = "3scale-kourier-gateway"
	kourierGatewayLabel      = "app=3scale-kourier-gateway"
	kourierService           = "kourier"
)

func TestKourierGatewayHA(t *testing.T) {
	clients := e2e.Setup(t)

	if err := waitForDeploymentScale(clients, kourierGatewayDeployment, ingressNamespace, haReplicas); err != nil {
		t.Fatalf("Deployment %s not scaled to %d: %v", kourierGatewayDeployment, haReplicas, err)
	}

	// Create a service that we will continually probe during kourier restart.
	names, resources := createPizzaPlanetService(t)
	test.CleanupOnInterrupt(func() { test.TearDown(clients, names) })
	defer test.TearDown(clients, names)

	prober := test.RunRouteProber(log.Printf, clients, resources.Service.Status.URL.URL())
	defer assertSLO(t, prober)

	url := resources.Service.Status.URL.URL()
	spoofingClient, err := pkgTest.NewSpoofingClient(
		clients.KubeClient,
		t.Logf,
		url.Hostname(),
		test.ServingFlags.ResolvableDomain)

	if err != nil {
		t.Fatal("Error creating spoofing client:", err)
	}

	pods, err := clients.KubeClient.Kube.CoreV1().Pods(ingressNamespace).List(metav1.ListOptions{
		LabelSelector: kourierGatewayLabel,
	})
	if err != nil {
		t.Fatal("Failed to get Gateway pods:", err)
	}
	gatewayPod := pods.Items[0].Name

	origEndpoints, err := getPublicEndpointsForService(t, clients, kourierService, ingressNamespace)
	if err != nil {
		t.Fatalf("Unable to get public endpoints for revision %s: %v", resources.Revision.Name, err)
	}

	clients.KubeClient.Kube.CoreV1().Pods(ingressNamespace).Delete(gatewayPod, &metav1.DeleteOptions{
		GracePeriodSeconds: ptr.Int64(0),
	})

	// Wait for the killed gateway to disappear from the kourier endpoints
	if err := waitForChangedPublicEndpointsForService(t, clients, kourierService, ingressNamespace, origEndpoints); err != nil {
		t.Fatal("Failed to wait for the service to update its endpoints:", err)
	}

	// Assert the kourier gateway at the first possible moment after the killed pod disappears from its endpoints.
	assertServiceWorksNow(t, clients, spoofingClient, names, url, test.PizzaPlanetText1)

	if err := waitForPodDeleted(t, clients, gatewayPod, ingressNamespace); err != nil {
		t.Fatalf("Did not observe %s to actually be deleted: %v", gatewayPod, err)
	}
	if err := waitForDeploymentScale(clients, kourierGatewayDeployment, ingressNamespace, haReplicas); err != nil {
		t.Fatalf("Deployment %s failed to scale up: %v", kourierGatewayDeployment, err)
	}

	pods, err = clients.KubeClient.Kube.CoreV1().Pods(ingressNamespace).List(metav1.ListOptions{
		LabelSelector: kourierGatewayLabel,
	})
	if err != nil {
		t.Fatal("Failed to get Gateway pods:", err)
	}

	// Sort the pods according to creation timestamp so that we can kill the oldest one. We want to
	// gradually kill both gateway pods that were started at the beginning.
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp) })

	gatewayPod = pods.Items[0].Name // Stop the oldest gateway pod remaining.

	origEndpoints, err = getPublicEndpointsForService(t, clients, kourierService, ingressNamespace)
	if err != nil {
		t.Fatalf("Unable to get public endpoints for revision %s: %v", resources.Revision.Name, err)
	}

	clients.KubeClient.Kube.CoreV1().Pods(ingressNamespace).Delete(gatewayPod, &metav1.DeleteOptions{
		GracePeriodSeconds: ptr.Int64(0),
	})

	if err := waitForPodDeleted(t, clients, gatewayPod, ingressNamespace); err != nil {
		t.Fatalf("Did not observe %s to actually be deleted: %v", gatewayPod, err)
	}
	if err := waitForDeploymentScale(clients, kourierGatewayDeployment, ingressNamespace, haReplicas); err != nil {
		t.Fatalf("Deployment %s failed to scale up: %v", kourierGatewayDeployment, err)
	}
	// Wait for the killed pod to disappear from the kourier endpoints
	if err := waitForChangedPublicEndpointsForService(t, clients, kourierService, ingressNamespace, origEndpoints); err != nil {
		t.Fatal("Failed to wait for the service to update its endpoints:", err)
	}

	// Assert the kourier gateway at the first possible moment after the killed pod disappears from its endpoints.
	assertServiceWorksNow(t, clients, spoofingClient, names, url, test.PizzaPlanetText1)
}

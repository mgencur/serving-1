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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"
	"knative.dev/serving/test"
	"knative.dev/serving/test/e2e"
)

const (
	ingressNamespace         = "knative-serving-ingress"
	kourierHPALease          = "kourier"
	kourierControlDeployment = "3scale-kourier-control"
	kourierControlLabel      = "app=3scale-kourier-control"
)

func TestKourierControlHA(t *testing.T) {
	clients := e2e.Setup(t)

	if err := waitForDeploymentScale(clients, kourierControlDeployment, ingressNamespace, haReplicas); err != nil {
		t.Fatalf("Deployment %s not scaled to %d: %v", kourierControlDeployment, haReplicas, err)
	}

	leaderController, err := getLeader(t, clients, kourierHPALease, ingressNamespace)
	if err != nil {
		t.Fatalf("Failed to get leader: %v", err)
	}

	// Create a service that we will continually probe during kourier restart.
	names, resources := createPizzaPlanetService(t)
	test.CleanupOnInterrupt(func() { test.TearDown(clients, names) })
	defer test.TearDown(clients, names)

	clients.KubeClient.Kube.CoreV1().Pods(ingressNamespace).Delete(leaderController, &metav1.DeleteOptions{
		GracePeriodSeconds: ptr.Int64(0),
	})

	if err := waitForPodDeleted(t, clients, leaderController, ingressNamespace); err != nil {
		t.Fatalf("Did not observe %s to actually be deleted: %v", leaderController, err)
	}

	// Make sure a new leader has been elected
	if _, err = getLeader(t, clients, kourierHPALease, ingressNamespace); err != nil {
		t.Fatalf("Failed to find new leader: %v", err)
	}

	assertServiceEventuallyWorks(t, clients, names, resources.Service.Status.URL.URL(), test.PizzaPlanetText1)

	// Verify that after changing the leader we can still create a new kservice
	service2Names, _ := createPizzaPlanetService(t)
	test.CleanupOnInterrupt(func() { test.TearDown(clients, service2Names) })
	test.TearDown(clients, service2Names)

	//TODO: what else to check?
}

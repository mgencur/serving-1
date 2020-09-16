/*
Copyright 2019 The Knative Authors

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

package v1

import (
	"context"
	"fmt"
	"golang.org/x/sync/errgroup"
	"knative.dev/pkg/pool"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	pkgTest "knative.dev/pkg/test"
	"knative.dev/pkg/test/spoof"
	"knative.dev/serving/pkg/apis/serving"
	v1 "knative.dev/serving/pkg/apis/serving/v1"
	"knative.dev/serving/test"
	v1test "knative.dev/serving/test/v1"
)

func waitForExpectedResponse(t pkgTest.TLegacy, clients *test.Clients, url *url.URL, expectedResponse string) error {
	client, err := pkgTest.NewSpoofingClient(clients.KubeClient, t.Logf, url.Hostname(), test.ServingFlags.ResolvableDomain, test.AddRootCAtoTransport(t.Logf, clients, test.ServingFlags.Https))
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, url.String(), nil)
	if err != nil {
		return err
	}
	_, err = client.Poll(req, v1test.RetryingRouteInconsistency(pkgTest.MatchesAllOf(pkgTest.IsStatusOK, pkgTest.EventuallyMatchesBody(expectedResponse))))
	return err
}

type trafficObjectives struct {
	url               *url.URL
	minSuccesses      int
	expectedResponses []string
}

type requestCtx struct {
	client *spoof.SpoofingClient
	url    *url.URL
}

// indexedResponse holds the index and body of the response for the given requested url.
type indexedResponse struct {
	index int
	url   *url.URL
	body  string
}

type indexedResponses []indexedResponse

func validateDomains(t pkgTest.TLegacy, clients *test.Clients, baseDomain *url.URL,
	baseExpected, trafficTargets, targetsExpected []string) error {
	subdomains := make([]*url.URL, 0, len(trafficTargets))
	for _, target := range trafficTargets {
		subdomain, _ := url.Parse(baseDomain.String())
		subdomain.Host = target + "-" + baseDomain.Host
		subdomains = append(subdomains, subdomain)
	}

	g, _ := errgroup.WithContext(context.Background())

	// We don't have a good way to check if the route is updated so we will wait until a subdomain has
	// started returning at least one expected result to key that we should validate percentage splits.
	// In order for tests to succeed reliably, we need to make sure that all domains succeed.
	for _, resp := range baseExpected {
		// Check for each of the responses we expect from the base domain.
		resp := resp
		g.Go(func() error {
			t.Log("Waiting for route to update", baseDomain)
			return waitForExpectedResponse(t, clients, baseDomain, resp)
		})
	}
	for i, s := range subdomains {
		i, s := i, s
		g.Go(func() error {
			t.Log("Waiting for route to update", s)
			return waitForExpectedResponse(t, clients, s, targetsExpected[i])
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("error with initial domain probing: %w", err)
	}

	// Holds expected response objectives for all domains.
	expectedTraffic := make([]trafficObjectives, 0, len(trafficTargets)+1 /* one for the base domain*/)

	minBasePercentage := test.MinSplitPercentage
	if len(baseExpected) == 1 {
		minBasePercentage = test.MinDirectPercentage
	}
	expectedTraffic = append(expectedTraffic,
		trafficObjectives{
			url:               baseDomain,
			minSuccesses:      int(math.Floor(test.NumRequests * minBasePercentage)),
			expectedResponses: baseExpected,
		},
	)

	for i, subdomain := range subdomains {
		i, subdomain := i, subdomain
		expectedTraffic = append(expectedTraffic,
			trafficObjectives{
				url:               subdomain,
				minSuccesses:      int(math.Floor(test.NumRequests * test.MinDirectPercentage)),
				expectedResponses: []string{targetsExpected[i]},
			},
		)
	}

	return checkDistribution(t, clients, expectedTraffic)
}

func checkDistribution(t pkgTest.TLegacy, clients *test.Clients, expectedTraffic []trafficObjectives) error {
	// requestPool holds all requests that will be performed via thread pool.
	var requestPool []*requestCtx
	for _, traffic := range expectedTraffic {
		client, err := pkgTest.NewSpoofingClient(clients.KubeClient,
			t.Logf,
			traffic.url.Hostname(),
			test.ServingFlags.ResolvableDomain,
			test.AddRootCAtoTransport(t.Logf, clients, test.ServingFlags.Https),
		)
		if err != nil {
			return err
		}
		ctx := requestCtx{
			client: client,
			url:    traffic.url,
		}
		// Produce the target requests for this url.
		for i := 0; i < test.NumRequests; i++ {
			requestPool = append(requestPool, &ctx)
		}
	}

	wg := pool.New(8)
	resultCh := make(chan indexedResponse, len(requestPool))

	for i, requestCtx := range requestPool {
		i, requestCtx := i, requestCtx
		wg.Go(func() error {
			req, err := http.NewRequest(http.MethodGet, requestCtx.url.String(), nil)
			if err != nil {
				return err
			}
			resp, err := requestCtx.client.Do(req)
			if err != nil {
				return err
			}
			resultCh <- indexedResponse{
				index: i,
				url:   requestCtx.url,
				body:  string(resp.Body),
			}
			return nil
		})
	}

	if err := wg.Wait(); err != nil {
		return fmt.Errorf("error checking routing distribution: %w", err)
	}
	close(resultCh)

	responses := make(map[*url.URL]indexedResponses)

	// Register responses for each url separately.
	for r := range resultCh {
		responses[r.url] = append(responses[r.url], r)
	}

	// Validate responses for each url.
	for _, traffic := range expectedTraffic {
		checkResponses(t, test.NumRequests, traffic.minSuccesses, traffic.url.Hostname(), traffic.expectedResponses, responses[traffic.url])
	}
	return nil
}

// checkResponses verifies that each "expectedResponse" is present in "actualResponses" at least "min" times.
func checkResponses(t pkgTest.TLegacy, num int, min int, domain string, expectedResponses []string, actualResponses indexedResponses) error {
	// counts maps the expected response body to the number of matching requests we saw.
	counts := make(map[string]int)
	// badCounts maps the unexpected response body to the number of matching requests we saw.
	badCounts := make(map[string]int)

	// counts := eval(
	//   SELECT body, count(*) AS total
	//   FROM $actualResponses
	//   WHERE body IN $expectedResponses
	//   GROUP BY body
	// )
	for _, ar := range actualResponses {
		expected := false
		for _, er := range expectedResponses {
			if strings.Contains(ar.body, er) {
				counts[er]++
				expected = true
			}
		}
		if !expected {
			badCounts[ar.body]++
			t.Logf("For domain %s: got unexpected response for request %d", domain, ar.index)
		}
	}
	// Print unexpected responses for debugging purposes
	for badResponse, count := range badCounts {
		t.Logf("For domain %s: saw unexpected response %q %d times.", domain, badResponse, count)
	}
	// Verify that we saw each entry in "expectedResponses" at least "min" times.
	// check(SELECT body FROM $counts WHERE total < $min)
	totalMatches := 0
	for _, er := range expectedResponses {
		count := counts[er]
		if count < min {
			return fmt.Errorf("domain %s failed: want at least %d, got %d for response %q", domain, min, count, er)
		}
		t.Logf("For domain %s: wanted at least %d, got %d requests.", domain, min, count)
		totalMatches += count
	}
	// Verify that the total expected responses match the number of requests made.
	if totalMatches < num {
		return fmt.Errorf("domain %s: saw expected responses %d times, wanted %d", domain, totalMatches, num)
	}
	// If we made it here, the implementation conforms. Congratulations!
	return nil
}

// Validates service health and vended content match for a runLatest Service.
// The checks in this method should be able to be performed at any point in a
// runLatest Service's lifecycle so long as the service is in a "Ready" state.
func validateDataPlane(t pkgTest.TLegacy, clients *test.Clients, names test.ResourceNames, expectedText string) error {
	t.Log("Checking that the endpoint vends the expected text:", expectedText)
	_, err := pkgTest.WaitForEndpointState(
		clients.KubeClient,
		t.Logf,
		names.URL,
		v1test.RetryingRouteInconsistency(pkgTest.MatchesAllOf(pkgTest.IsStatusOK, pkgTest.EventuallyMatchesBody(expectedText))),
		"WaitForEndpointToServeText",
		test.ServingFlags.ResolvableDomain,
		test.AddRootCAtoTransport(t.Logf, clients, test.ServingFlags.Https))
	if err != nil {
		return fmt.Errorf("the endpoint for Route %s at %s didn't serve the expected text %q: %w", names.Route, names.URL, expectedText, err)
	}

	return nil
}

// Validates the state of Configuration, Revision, and Route objects for a runLatest Service.
// The checks in this method should be able to be performed at any point in a
// runLatest Service's lifecycle so long as the service is in a "Ready" state.
func validateControlPlane(t pkgTest.T, clients *test.Clients, names test.ResourceNames, expectedGeneration string) error {
	t.Log("Checking to ensure Revision is in desired state with", "generation", expectedGeneration)
	err := v1test.CheckRevisionState(clients.ServingClient, names.Revision, func(r *v1.Revision) (bool, error) {
		if ready, err := v1test.IsRevisionReady(r); !ready {
			return false, fmt.Errorf("revision %s did not become ready to serve traffic: %w", names.Revision, err)
		}
		if r.Status.DeprecatedImageDigest == "" {
			return false, fmt.Errorf("imageDigest not present for revision %s", names.Revision)
		}
		if validDigest, err := validateImageDigest(names.Image, r.Status.DeprecatedImageDigest); !validDigest {
			return false, fmt.Errorf("imageDigest %s is not valid for imageName %s: %w", r.Status.DeprecatedImageDigest, names.Image, err)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	err = v1test.CheckRevisionState(clients.ServingClient, names.Revision, v1test.IsRevisionAtExpectedGeneration(expectedGeneration))
	if err != nil {
		return fmt.Errorf("revision %s did not have an expected annotation with generation %s: %w", names.Revision, expectedGeneration, err)
	}

	t.Log("Checking to ensure Configuration is in desired state.")
	err = v1test.CheckConfigurationState(clients.ServingClient, names.Config, func(c *v1.Configuration) (bool, error) {
		if c.Status.LatestCreatedRevisionName != names.Revision {
			return false, fmt.Errorf("Configuration(%s).LatestCreatedRevisionName = %q, want %q",
				names.Config, c.Status.LatestCreatedRevisionName, names.Revision)
		}
		if c.Status.LatestReadyRevisionName != names.Revision {
			return false, fmt.Errorf("Configuration(%s).LatestReadyRevisionName = %q, want %q",
				names.Config, c.Status.LatestReadyRevisionName, names.Revision)
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	t.Log("Checking to ensure Route is in desired state with", "generation", expectedGeneration)
	err = v1test.CheckRouteState(clients.ServingClient, names.Route, v1test.AllRouteTrafficAtRevision(names))
	if err != nil {
		return fmt.Errorf("the Route %s was not updated to route traffic to the Revision %s: %w", names.Route, names.Revision, err)
	}

	return nil
}

// Validates labels on Revision, Configuration, and Route objects when created by a Service
// see spec here: https://github.com/knative/serving/blob/master/docs/spec/spec.md#revision
func validateLabelsPropagation(t pkgTest.T, objects v1test.ResourceObjects, names test.ResourceNames) error {
	t.Log("Validate Labels on Revision Object")
	revision := objects.Revision

	if revision.Labels["serving.knative.dev/configuration"] != names.Config {
		return fmt.Errorf("expect Confguration name in Revision label %q but got %q ", names.Config, revision.Labels["serving.knative.dev/configuration"])
	}
	if revision.Labels["serving.knative.dev/service"] != names.Service {
		return fmt.Errorf("expect Service name in Revision label %q but got %q ", names.Service, revision.Labels["serving.knative.dev/service"])
	}

	t.Log("Validate Labels on Configuration Object")
	config := objects.Config
	if config.Labels["serving.knative.dev/service"] != names.Service {
		return fmt.Errorf("expect Service name in Configuration label %q but got %q ", names.Service, config.Labels["serving.knative.dev/service"])
	}
	if config.Labels["serving.knative.dev/route"] != names.Route {
		return fmt.Errorf("expect Route name in Configuration label %q but got %q ", names.Route, config.Labels["serving.knative.dev/route"])
	}

	t.Log("Validate Labels on Route Object")
	route := objects.Route
	if route.Labels["serving.knative.dev/service"] != names.Service {
		return fmt.Errorf("expect Service name in Route label %q but got %q ", names.Service, route.Labels["serving.knative.dev/service"])
	}
	return nil
}

func validateAnnotations(objs *v1test.ResourceObjects, extraKeys ...string) error {
	// This checks whether the annotations are set on the resources that
	// expect them to have.
	// List of issues listing annotations that we check: #1642.

	anns := objs.Service.GetAnnotations()
	for _, a := range append([]string{serving.CreatorAnnotation, serving.UpdaterAnnotation}, extraKeys...) {
		if got := anns[a]; got == "" {
			return fmt.Errorf("service expected %s annotation to be set, but was empty", a)
		}
	}
	anns = objs.Route.GetAnnotations()
	for _, a := range append([]string{serving.CreatorAnnotation, serving.UpdaterAnnotation}, extraKeys...) {
		if got := anns[a]; got == "" {
			return fmt.Errorf("route expected %s annotation to be set, but was empty", a)
		}
	}
	anns = objs.Config.GetAnnotations()
	for _, a := range append([]string{serving.CreatorAnnotation, serving.UpdaterAnnotation}, extraKeys...) {
		if got := anns[a]; got == "" {
			return fmt.Errorf("config expected %s annotation to be set, but was empty", a)
		}
	}
	return nil
}

func validateReleaseServiceShape(objs *v1test.ResourceObjects) error {
	// Traffic should be routed to the lastest created revision.
	if got, want := objs.Service.Status.Traffic[0].RevisionName, objs.Config.Status.LatestReadyRevisionName; got != want {
		return fmt.Errorf("Status.Traffic[0].RevisionsName = %s, want: %s", got, want)
	}
	return nil
}

func validateImageDigest(imageName string, imageDigest string) (bool, error) {
	ref, err := name.ParseReference(pkgTest.ImagePath(imageName))
	if err != nil {
		return false, err
	}

	digest, err := name.NewDigest(imageDigest)
	if err != nil {
		return false, err
	}

	return ref.Context().String() == digest.Context().String(), nil
}

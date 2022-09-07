#This makefile is used by ci-operator

CGO_ENABLED=0
GOOS=linux
CORE_IMAGES=./cmd/activator ./cmd/autoscaler ./cmd/autoscaler-hpa ./cmd/controller ./cmd/queue ./cmd/webhook ./vendor/knative.dev/pkg/apiextensions/storageversion/cmd/migrate ./cmd/domain-mapping ./cmd/domain-mapping-webhook
TEST_IMAGES=$(shell find ./test/test_images ./test/test_images/multicontainer -mindepth 1 -maxdepth 1 -type d)
TEST_IMAGE_TAG=latest
DOCKER_REPO_OVERRIDE=
KO_FLAGS=
BRANCH=
TEST=
IMAGE=

# Guess location of openshift/release repo. NOTE: override this if it is not correct.
OPENSHIFT=${CURDIR}/../../github.com/openshift/release

install:
	for img in $(CORE_IMAGES); do \
		go install -tags="disable_gcp,disable_aws,disable_azure" $$img ; \
	done
.PHONY: install

test-install:
	for img in $(TEST_IMAGES); do \
		go install $$img ; \
	done
.PHONY: test-install

test-e2e:
	./openshift/e2e-tests.sh
.PHONY: test-e2e

test-e2e-tls:
	ENABLE_INTERNAL_TLS="true" ./openshift/e2e-tests.sh
.PHONY: test-e2e-tls

test-images:
	for img in $(TEST_IMAGES); do \
		KO_DOCKER_REPO=$(DOCKER_REPO_OVERRIDE) ko resolve --tags=$(TEST_IMAGE_TAG) $(KO_FLAGS) -RBf $$img ; \
	done
.PHONY: test-images

test-image-single:
	KO_DOCKER_REPO=$(DOCKER_REPO_OVERRIDE) ko resolve --tags=$(TEST_IMAGE_TAG) $(KO_FLAGS) -RBf test/test_images/$(IMAGE)
.PHONY: test-image-single

# Run make DOCKER_REPO_OVERRIDE=<your_repo> test-e2e-local if test images are available
# in the given repository. Make sure you first build and push them there by running `make test-images`.
# Run make BRANCH=<ci_promotion_name> test-e2e-local if test images from the latest CI
# build for this branch should be used. Example: `make BRANCH=knative-v0.13.2 test-e2e-local`.
# If neither DOCKER_REPO_OVERRIDE nor BRANCH are defined the tests will use test images
# from the last nightly build.
# If TEST is defined then only the single test will be run.
test-e2e-local:
	./openshift/e2e-tests-local.sh $(TEST)
.PHONY: test-e2e-local

# Generate Dockerfiles for core and test images used by ci-operator. The files need to be committed manually.
generate-dockerfiles:
	./openshift/ci-operator/generate-dockerfiles.sh openshift/ci-operator/knative-images $(CORE_IMAGES)
	./openshift/ci-operator/generate-dockerfiles.sh openshift/ci-operator/knative-test-images $(TEST_IMAGES)
.PHONY: generate-dockerfiles

# Generates a ci-operator configuration for a specific branch.
generate-ci-config:
	./openshift/ci-operator/generate-ci-config.sh $(BRANCH) > ci-operator-config.yaml
.PHONY: generate-ci-config

# Generate an aggregated knative yaml file with replaced image references
generate-release:
	./openshift/release/generate-release.sh $(RELEASE)
.PHONY: generate-release

# Update CI configuration in the $(OPENSHIFT) directory.
# NOTE: Makes changes outside this repository.
update-ci:
	sh ./openshift/ci-operator/update-ci.sh $(OPENSHIFT) $(CORE_IMAGES)

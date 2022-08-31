#!/bin/bash

branch=${1-'knative-v0.6.0'}
openshift=${2-'4.3'}
promotion_disabled=${3-false}
generate_continuous=${4-false}
internal_tls_enabled=${5-false}

if [[ "$branch" == "knative-next" ]]; then
  promotion_name="knative-nightly"
  generate_continuous=false
else
  promotion_name="$branch.0"
fi

core_images=$(find ./openshift/ci-operator/knative-images -mindepth 1 -maxdepth 1 -type d | LC_COLLATE=posix sort)
exclude_images="-not -name multicontainer -not -name initcontainers"
test_images=$(find ./openshift/ci-operator/knative-test-images -mindepth 1 -maxdepth 1 -type d $exclude_images | LC_COLLATE=posix sort)

function generate_image_dependencies {
  for img in $core_images; do
    image_base=knative-serving-$(basename $img)
    to_image=$(echo ${image_base//[_.]/-})
    to_image=$(echo ${to_image//v0/upgrade-v0})
    to_image=$(echo ${to_image//migrate/storage-version-migration})
    image_env=$(echo ${to_image//-/_})
    image_env=$(echo ${image_env^^})
    cat <<EOF
      - env: $image_env
        name: $to_image
EOF
  done

  for img in $test_images; do
    image_base=knative-serving-test-$(basename $img)
    to_image=$(echo ${image_base//_/-})
    image_env=$(echo ${to_image//-/_})
    image_env=$(echo ${image_env^^})
    cat <<EOF
      - env: $image_env
        name: $to_image
EOF
  done
}

function generate_cron_expression {
  if [[ "$branch" == "knative-v0.25.3" ]]; then
    echo '0 1 * * 1-5'
  elif [[ "$branch" == "knative-v0.26" ]]; then
    echo '0 3 * * 1-5'
  elif [[ "$branch" == "knative-v1.0" ]]; then
    echo '0 5 * * 1-5'
  elif [[ "$branch" == "knative-v1.1" ]]; then
    echo '0 7 * * 1-5'
  elif [[ "$branch" == "knative-v1.2" ]]; then
    echo '0 9 * * 1-5'
  elif [[ "$branch" == "knative-v1.3" ]]; then
    echo '0 11 * * 1-5'
  elif [[ "$branch" == "knative-v1.4" ]]; then
    echo '0 11 * * 1-5'
  elif [[ "$branch" == "knative-v1.5" ]]; then
    echo '0 13 * * 1-5'
  elif [[ "$branch" == "knative-v1.6" ]]; then
    echo '0 15 * * 1-5'
  elif [[ "$branch" == "knative-v1.7" ]]; then
    echo '0 17 * * 1-5'
  elif [[ "$branch" == "knative-v1.8" ]]; then
    echo '0 19 * * 1-5'
  fi
}

function print_single_test {
  local name=${1}
  local commands=${2}
  local cluster_profile=${3}
  local do_claim=${4}
  local workflow=${5}
  local cron=${6}


  cat <<EOF
- as: ${name}
  steps:
    test:
    - as: test
      cli: latest
      commands: ${commands}
      dependencies:
$image_deps
      from: src
      resources:
        requests:
          cpu: 100m
      timeout: 4h0m0s
    workflow: ${workflow}
EOF

if [[ -n "$cluster_profile" ]]; then
 cat <<EOF
    cluster_profile: ${cluster_profile}
EOF
fi

if [[ "$do_claim" == true ]]; then
cat <<EOF
  cluster_claim:
    architecture: amd64
    cloud: aws
    owner: openshift-ci
    product: ocp
    timeout: 1h0m0s
    version: "$openshift"
EOF
fi

if [[ -n "$cron" ]]; then
 cat <<EOF
  cron: ${cron}
EOF
fi

}

function print_base_images {
  cat <<EOF
base_images:
  base:
    name: "$openshift"
    namespace: ocp
    tag: base
EOF
}

function print_build_root {
  cat <<EOF
build_root:
  project_image:
    dockerfile_path: openshift/ci-operator/build-image/Dockerfile
canonical_go_repository: knative.dev/serving
binary_build_commands: make install
test_binary_build_commands: make test-install
EOF
}

function print_tests {
  cat <<EOF
tests:
EOF

  cron="$(generate_cron_expression)"

  print_single_test    "e2e-aws-ocp-${openshift//./}"             "make test-e2e"         "" "true" "generic-claim" ""

  if [[ "$internal_tls_enabled" == true ]]; then
    print_single_test "e2e-aws-ocp-${openshift//./}-continuous"  "make test-e2e-tls"          "" "true" "generic-claim" "${cron}"
  elif [[ "$generate_continuous" == true ]]; then
    print_single_test "e2e-aws-ocp-${openshift//./}-continuous"  "make test-e2e"          "" "true" "generic-claim" "${cron}"
  fi

}

function print_releases {
  cat <<EOF
releases:
  initial:
    integration:
      name: '$openshift'
      namespace: ocp
  latest:
    integration:
      include_built_images: true
      name: '$openshift'
      namespace: ocp
EOF
}

function print_promotion {
  cat <<EOF
promotion:
  additional_images:
    knative-serving-src: src
  disabled: $promotion_disabled
  cluster: https://api.ci.openshift.org
  namespace: openshift
  name: $promotion_name
EOF
}

function print_resources {
  cat <<EOF
resources:
  '*':
    limits:
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 2Gi
  'bin':
    limits:
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 2Gi
EOF
}

function print_images {
  cat <<EOF
images:
EOF

for img in $core_images; do
  image_base=$(basename $img)
  to_image=$(echo ${image_base//[_.]/-})
  to_image=$(echo ${to_image//v0/upgrade-v0})
  to_image=$(echo ${to_image//migrate/storage-version-migration})
  cat <<EOF
- dockerfile_path: openshift/ci-operator/knative-images/$image_base/Dockerfile
  from: base
  inputs:
    bin:
      paths:
      - destination_dir: .
        source_path: /go/bin/$image_base
  to: knative-serving-$to_image
EOF
done

for img in $test_images; do
  image_base=$(basename $img)
  to_image=$(echo ${image_base//_/-})
  cat <<EOF
- dockerfile_path: openshift/ci-operator/knative-test-images/$image_base/Dockerfile
  from: base
  inputs:
    test-bin:
      paths:
      - destination_dir: .
        source_path: /go/bin/$image_base
  to: knative-serving-test-$to_image
EOF
done
}

image_deps="$(generate_image_dependencies)"

print_base_images
print_build_root
print_tests
print_releases
print_promotion
print_resources
print_images

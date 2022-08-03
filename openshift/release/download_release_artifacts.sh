#!/usr/bin/env bash

# Usage example: ./download_release_artifacts.sh v1.4.0

set -Eeuo pipefail

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"

manifest_path="${SCRIPT_DIR}/manifest-patches"
artifacts_path="${SCRIPT_DIR}/artifacts"
mkdir -p "${manifest_path}"
mkdir -p "${artifacts_path}"
# These files could in theory change from release to release, though their names should
# be fairly stable.
serving_files=(serving-crds serving-core serving-hpa serving-post-install-jobs)

function download_serving {
  component=$1
  version=$2
  shift
  shift

  files=("$@")

  component_dir="${artifacts_path}"
  release_suffix="${version%?}0"
  target_dir="${component_dir}"
  rm -r "$component_dir"
  mkdir -p "$target_dir"

  for (( i=0; i<${#files[@]}; i++ ));
  do
    index=$(( i+1 ))
    file="${files[$i]}.yaml"
    target_file="$target_dir/$index-$file"

    url="https://github.com/knative/$component/releases/download/knative-$release_suffix/$file"
    wget --no-check-certificate "$url" -O "$target_file"
  done
}

download_serving serving "$1" "${serving_files[@]}"

# Drop namespace from manifest.
git apply "${manifest_path}/001-serving-namespace-deletion.patch"

# Extra role for downstream, so that users can get the autoscaling CM to fetch defaults.
git apply "${manifest_path}/002-openshift-serving-role.patch"

# TODO: Remove this once upstream fixed https://github.com/knative/operator/issues/376.
# See also https://issues.redhat.com/browse/SRVKS-670.
git apply "${manifest_path}/003-serving-pdb.patch"

# Add internal-tls patch. The backport will conflict with the downloaded manifest.
git apply "${manifest_path}/004-internal-tls.patch"

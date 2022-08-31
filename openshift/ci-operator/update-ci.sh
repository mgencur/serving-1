#!/bin/bash
# A script that will update the mapping file in github.com/openshift/release

set -e

source "$(dirname "$0")/../tui-functions.sh"

readonly TMPDIR=$(mktemp -d knativeServingPeriodicReporterXXXX -p /tmp/)

fail() { echo; echo "$*"; exit 1; }

cat >> "$TMPDIR"/reporterConfig <<EOF
  reporter_config:
    slack:
      channel: '#knative-serving-ci'
      job_states_to_report:
      - success
      - failure
      - error
      report_template: '{{if eq .Status.State "success"}} :rainbow: Job *{{.Spec.Job}}* ended with *{{.Status.State}}*. <{{.Status.URL}}|View logs> :rainbow: {{else}} :volcano: Job *{{.Spec.Job}}* ended with *{{.Status.State}}*. <{{.Status.URL}}|View logs> :volcano: {{end}}'
EOF

# Deduce branch name and X.Y.Z version.
BRANCH=$(git rev-parse --abbrev-ref HEAD)
VERSION=$(echo $BRANCH | sed -E 's/^.*(v[0-9]+\.[0-9]+|next)|.*/\1/')
test -n "$VERSION" || fail "'$BRANCH' is not a release branch"

# Set up variables for important locations in the openshift/release repo.
OPENSHIFT=$(realpath "$1"); shift
test -d "$OPENSHIFT/.git" || fail "'$OPENSHIFT' is not a git repo"
CONFIGDIR=$OPENSHIFT/ci-operator/config/openshift/knative-serving
test -d "$CONFIGDIR" || fail "'$CONFIGDIR' is not a directory"
PERIODIC_CONFIGDIR=$OPENSHIFT/ci-operator/jobs/openshift/knative-serving
test -d "$PERIODIC_CONFIGDIR" || fail "'$PERIODIC_CONFIGDIR' is not a directory"

# Generate CI config files
stage "Generating CI config files"
CONFIG=$CONFIGDIR/openshift-knative-serving-release-$VERSION
PERIODIC_CONFIG=$PERIODIC_CONFIGDIR/openshift-knative-serving-release-$VERSION-periodics.yaml
CURDIR=$(dirname $0)

# $1=branch $2=openshift $3=promotion_disabled $4=generate_continuous $5=internal_tls_enabled(optional)
$CURDIR/generate-ci-config.sh knative-$VERSION 4.6 true false > ${CONFIG}__46.yaml
$CURDIR/generate-ci-config.sh knative-$VERSION 4.7 true false > ${CONFIG}__47.yaml
$CURDIR/generate-ci-config.sh knative-$VERSION 4.8 true false > ${CONFIG}__48.yaml
$CURDIR/generate-ci-config.sh knative-$VERSION 4.9 true false > ${CONFIG}__49.yaml
$CURDIR/generate-ci-config.sh knative-$VERSION 4.10 false true > ${CONFIG}__410.yaml

# Append missing lines to the mirror file.
if [[ "$VERSION" != "next" ]]; then
  stage "Syncing mirror file"
  VER=$(echo $VERSION | sed 's/\./_/;s/\.[0-9]\+$//') # X_Y form of version
  MIRROR="$OPENSHIFT/core-services/image-mirroring/knative/mapping_knative_${VER}_quay"
  [ -n "$(tail -c1 $MIRROR)" ] && echo >> $MIRROR # Make sure there's a newline
  exclude_images="-not -name multicontainer -not -name initcontainers"
  test_images=$(find ./openshift/ci-operator/knative-test-images -mindepth 1 -maxdepth 1 -type d $exclude_images | LC_COLLATE=posix sort)
  for IMAGE in $test_images; do
      NAME=knative-serving-test-$(basename $IMAGE | sed 's/_/-/' | sed 's/_/-/' | sed 's/[_.]/-/' | sed 's/[_.]/-/' | sed 's/v0/upgrade-v0/')

      step "Adding $NAME to mirror file as $VERSION tag"
      LINE="registry.ci.openshift.org/openshift/knative-$VERSION.0:$NAME quay.io/openshift-knative/${NAME/knative-serving-test-/}:$VERSION"
      # Add $LINE if not already present
      grep -q "^$LINE\$" $MIRROR || echo "$LINE"  >> $MIRROR

      VER=$(echo $VER | sed 's/\_/./')
      step "Adding $NAME to mirror file as $VER tag"
      LINE="registry.ci.openshift.org/openshift/knative-$VERSION.0:$NAME quay.io/openshift-knative/${NAME/knative-serving-test-/}:$VER"
      # Add $LINE if not already present
      grep -q "^$LINE\$" $MIRROR || echo "$LINE"  >> $MIRROR
  done
else
  stage "Syncing mirror file"
  MIRROR="$OPENSHIFT/core-services/image-mirroring/knative/mapping_knative_nightly_quay"
  [ -n "$(tail -c1 $MIRROR)" ] && echo >> $MIRROR # Make sure there's a newline
  test_images=$(find ./openshift/ci-operator/knative-test-images -mindepth 1 -maxdepth 1 -type d | LC_COLLATE=posix sort)
  for IMAGE in $test_images; do
      NAME=knative-serving-test-$(basename $IMAGE | sed 's/_/-/' | sed 's/_/-/' | sed 's/[_.]/-/' | sed 's/[_.]/-/' | sed 's/v0/upgrade-v0/')
      step "Adding $NAME to mirror file as latest tag"
      LINE="registry.ci.openshift.org/openshift/knative-nightly:$NAME quay.io/openshift-knative/${NAME/knative-serving-test-/}:latest"
      # Add $LINE if not already present
      grep -q "^$LINE\$" $MIRROR || echo "$LINE"  >> $MIRROR
  done
fi
# Switch to openshift/release to generate PROW files
cd $OPENSHIFT
stage "Generating PROW job in $OPENSHIFT"
make jobs
stage "Generating ci-operator-config in $OPENSHIFT"
make ci-operator-config
RERUN_MAKE=false
# We have to do this manually, see: https://docs.ci.openshift.org/docs/how-tos/notification/
if [[ "$VERSION" != "next" ]]; then
  stage "Adding reporter_config to periodics"
  # These version MUST match the ocp version we used above
  for OCP_VERSION in 49 410; do
    JOB="periodic-ci-openshift-knative-serving-release-${VERSION}-${OCP_VERSION}-e2e-aws-ocp-${OCP_VERSION}-continuous"  
    if [[ $(sed -n "/  name: $JOB/ r $TMPDIR/reporterConfig" "$PERIODIC_CONFIG") ]]; then
      sed -i "/  name: $JOB/ r $TMPDIR/reporterConfig" "$PERIODIC_CONFIG"
      RERUN_MAKE=true
      step "Updating job $JOB - Done."
    else
      step "Skip updating job $JOB - probably generate_continuous is not enabled."
    fi
  done
fi

if [[ "$RERUN_MAKE" == "true" ]]; then
  # One last run to format any manual changes to the jobs
  stage "Generating PROW job in $OPENSHIFT"
  step "Running make job again to format any manually added configuration"
  make jobs
fi

stage "Summary"
GIT_OUTPUT=$(git ls-files --modified)
if [[ -n "${GIT_OUTPUT}" ]]; then
  step "Modified files in $OPENSHIFT"
  git ls-files --modified
fi
GIT_OUTPUT=$(git ls-files --others --exclude-standard)
if [[ -n "${GIT_OUTPUT}" ]]; then
  step "New files in $OPENSHIFT"
  git ls-files --others --exclude-standard 
fi
stage_warn "Commit changes to $OPENSHIFT and create a PR"

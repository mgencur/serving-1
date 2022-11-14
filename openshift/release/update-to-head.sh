#!/usr/bin/env bash

# Synchs the REPO_BRANCH branch to main and then triggers CI
# Usage: update-to-head.sh

set -e
REPO_NAME="serving"
REPO_OWNER_NAME="openshift-knative"
REPO_BRANCH="release-next"
REPO_BRANCH_CI="${REPO_BRANCH}-ci"

# Check if there's an upstream release we need to mirror downstream
openshift/release/mirror-upstream-branches.sh

# Reset release-next to upstream/main.
git fetch upstream main
git checkout upstream/main -B ${REPO_BRANCH}

# Update openshift's main and take all needed files from there.
git fetch openshift main
git checkout openshift/main openshift OWNERS_ALIASES OWNERS Makefile
# Apply patches .
git apply openshift/patches/*
git add .
git commit -am ":fire: Apply carried patches."

# Revert the autoscaling API version change.
git revert 974d19d03644dff46b097a15efb4d3d7167765ad

# Revert the autoscaling API version change in webhook resource.
git revert a6a18b857be4f9e03a5bc4e196ea8450ff68828e

make generate-dockerfiles
make RELEASE=ci generate-release
git add openshift OWNERS_ALIASES OWNERS Makefile
git commit -m ":open_file_folder: Update openshift specific files."

git push -f openshift ${REPO_BRANCH}

# Trigger CI
git checkout ${REPO_BRANCH} -B ${REPO_BRANCH_CI}
date > ci
git add ci
git commit -m ":robot: Triggering CI on branch '${REPO_BRANCH}' after synching to upstream/main"
git push -f openshift ${REPO_BRANCH_CI}

if hash hub 2>/dev/null; then
   # Test if there is already a sync PR in 
   COUNT=$(hub api -H "Accept: application/vnd.github.v3+json" repos/${REPO_OWNER_NAME}/${REPO_NAME}/pulls --flat \
    | grep -c ":robot: Triggering CI on branch '${REPO_BRANCH}' after synching to upstream/main") || true
   if [ "$COUNT" = "0" ]; then
      hub pull-request --no-edit -l "kind/sync-fork-to-upstream" -b ${REPO_OWNER_NAME}/${REPO_NAME}:${REPO_BRANCH} -h ${REPO_OWNER_NAME}/${REPO_NAME}:${REPO_BRANCH_CI}
   fi
else
   echo "hub (https://github.com/github/hub) is not installed, so you'll need to create a PR manually."
fi

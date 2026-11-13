#!/bin/bash
# Builds the images of the lab from the develop branches in src/ with the tags the develop of mcloud (../mcloud,
# .env.original) and mcloud-agent (src/mcloud-agent, versions.yml) expect; the logs go to logs/build-<repository>.log,
# the result of every build to logs/build-status.log.
cd "$(dirname "$0")/src" || exit 1
logs="$(mkdir -p ../logs && cd ../logs && pwd)"

# tag_of <file> <name>: the value of NAME=value (.env) or NAME: value (YAML) of a file
tag_of() {
  sed -n -E "s/^$2[=:][[:space:]]*\"?([^\"[:space:]]+)\"?.*/\1/p" "$1" 2> /dev/null | head -1
}

# build_image <repository> <tag>: make build of the repository, its result appended to build-status.log; fails with it
build_image() {
  if (cd "$1" && make build VERSION="$2" > "${logs}/build-$1.log" 2>&1); then
    echo "OK $1:$2" >> "${logs}/build-status.log"
  else
    echo "FAIL $1:$2" >> "${logs}/build-status.log"
    return 1
  fi
}

stf_tag="$(tag_of ../mcloud/.env.original TAG_STF)"
grid_tag="$(tag_of ../mcloud/.env.original TAG_SELENIUM_HUB)"
device_tag="$(tag_of mcloud-agent/versions.yml DEVICE_VERSION)"
appium_tag="$(tag_of mcloud-agent/versions.yml APPIUM_VERSION)"
android_tag="$(tag_of mcloud-agent/versions.yml ANDROID_CONNECTOR_VERSION)"
ios_tag="$(tag_of mcloud-agent/versions.yml IOS_CONNECTOR_VERSION)"
if [[ -z "$stf_tag" || -z "$grid_tag" || -z "$device_tag" || -z "$appium_tag" || -z "$android_tag" || -z "$ios_tag" ]]; then
  echo "the tags are not found in ../mcloud/.env.original and src/mcloud-agent/versions.yml" >&2
  exit 1
fi
# mcloud-device is built FROM the stf image of its Dockerfile: the local one when the tags are the same
if ! grep -q "^FROM public.ecr.aws/zebrunner/stf:${stf_tag}\$" mcloud-device/Dockerfile; then
  echo "mcloud-device is not built FROM stf:${stf_tag}: it takes another STF than the one of mcloud" >&2
fi

: > "${logs}/build-status.log"
status=0
(build_image stf "$stf_tag" && build_image mcloud-device "$device_tag") &
pids=($!)
build_image mcloud-grid "$grid_tag" &
pids+=($!)
build_image appium "$appium_tag" &
pids+=($!)
build_image mcloud-android-connector "$android_tag" &
pids+=($!)
build_image mcloud-ios-connector "$ios_tag" &
pids+=($!)
for pid in "${pids[@]}"; do
  wait "$pid" || status=1
done
echo DONE >> "${logs}/build-status.log"
exit "$status"

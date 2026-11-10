#!/bin/bash
# Builds the images of the lab from the develop branches in src/ with the tags the develop of mcloud and mcloud-agent
# expect; the logs go to logs/build-<repository>.log, the result of every build to logs/build-status.log.
cd "$(dirname "$0")/src" || exit 1
logs="$(mkdir -p ../logs && cd ../logs && pwd)"

# build_image <repository> <tag>: make build of the repository, its result appended to build-status.log
build_image() {
  if (cd "$1" && make build VERSION="$2" > "${logs}/build-$1.log" 2>&1); then
    echo "OK $1:$2" >> "${logs}/build-status.log"
  else
    echo "FAIL $1:$2" >> "${logs}/build-status.log"
  fi
}

: > "${logs}/build-status.log"
# mcloud-device is built FROM the stf image
(build_image stf 3.1 && build_image mcloud-device 3.1) &
build_image mcloud-grid 3.1 &
build_image appium 3.0 &
build_image mcloud-android-connector 2.0 &
build_image mcloud-ios-connector 2.0 &
wait
echo DONE >> "${logs}/build-status.log"

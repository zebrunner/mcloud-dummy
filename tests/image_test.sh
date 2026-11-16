#!/bin/bash
# Checks of the docker image: build, contents, configuration, the startup with the example devices and the exit codes.
# IMAGE overrides the tag of the tested image (it is built when missing, REBUILD=true builds it anyway).
source "$(dirname "$0")/lib.sh"
cd "$REPO" || exit 1

IMAGE="${IMAGE:-mcloud-dummy:test}"
PREFIX="mcloud-dummy-test-$$"

require_cmd docker
docker info > /dev/null 2>&1 || skip "requires a running docker daemon"

# cleanup: removes the containers of the startup checks
# called by the EXIT trap of lib.sh
# shellcheck disable=SC2329
cleanup() {
  docker rm --force "${PREFIX}-run" "${PREFIX}-control" > /dev/null 2>&1
}

# in_container <command...>: runs the command in the started container
in_container() {
  docker exec "${PREFIX}-run" "$@" 2>&1
}

# wait_healthy <seconds> [container]: waits for the healthcheck of a started container, the one of the startup checks
# by default
wait_healthy() {
  local i
  for ((i = 0; i < $1; i++)); do
    [[ "$(docker inspect "${2:-${PREFIX}-run}" --format '{{.State.Health.Status}}' 2> /dev/null)" == "healthy" ]] && return 0
    sleep 1
  done
  return 1
}

section "build"
if [[ "${REBUILD:-false}" != "true" ]] && docker image inspect "$IMAGE" > /dev/null 2>&1; then
  skip_check "image builds" "$IMAGE exists, REBUILD=true builds it again"
elif docker build --progress=plain --provenance=false --tag "$IMAGE" . > "${WORK}/build.log" 2>&1; then
  pass "image builds"
else
  fail "image builds" "docker build failed" "$(tail -100 "${WORK}/build.log")"
  finish
fi

section "configuration"
config="$(docker image inspect "$IMAGE" --format '{{json .Config}}')"
check_contains "entrypoint" '"Entrypoint":["/opt/zebrunner/mcloud-dummy"]' "$config"
check_contains "the example devices are the default configuration" '"DUMMY_CONFIG=/opt/zebrunner/devices.yml"' "$config"
check_contains "the healthcheck is the program" '"/opt/zebrunner/mcloud-dummy","-healthcheck"' "$config"
check_contains "the control address is the one of the devices file by default" '"DUMMY_CONTROL="' "$config"
check_contains "the image is labeled Apache-2.0" '"org.opencontainers.image.licenses":"Apache-2.0"' "$config"

section "exit codes"
out="$(docker run --rm -e LOGGER_LEVEL=LOUD "$IMAGE" 2>&1)"
check "an invalid LOGGER_LEVEL exits with 0" "0" "$?"
check_contains "the invalid LOGGER_LEVEL is logged" "LOGGER_LEVEL must be" "$out"
out="$(docker run --rm -e DUMMY_CONFIG=/nonexistent.yml "$IMAGE" 2>&1)"
check "a missing configuration exits with 0" "0" "$?"
check_contains "the missing configuration is logged" "invalid configuration" "$out"
out="$(docker run --rm -e DUMMY_CONTROL=:15555 "$IMAGE" 2>&1)"
check "a control port of a device exits with 0" "0" "$?"
check_contains "the control port of a device is logged" "the port 15555 is the port of" "$out"

section "startup"
if docker run -d --name "${PREFIX}-run" -e DUMMY_HOST=dummy "$IMAGE" > /dev/null 2>&1 && wait_healthy 60; then
  pass "the container with the example devices is healthy"
else
  fail "the container with the example devices is healthy" "not healthy" "$(docker logs "${PREFIX}-run" 2>&1 | tail -20)"
  finish
fi
devices="$(in_container wget -qO- http://127.0.0.1:8800/api/devices)"
check "the example devices are started" "5" "$(grep -o '"serial"' <<< "$devices" | wc -l | tr -d ' ')"
check_contains "an Android device answers on its adb port" "dummy:15555" "$devices"
check_contains "WebDriverAgent answers" '"ready":true' "$(in_container wget -qO- http://127.0.0.1:18100/status)"
check "the screen is a JPEG" "ffd8ff" "$(in_container sh -c 'wget -qO- http://127.0.0.1:8800/api/devices/dummy-android-001/screen | head -c 3 | od -An -tx1' | tr -d ' \n')"
logs="$(docker logs "${PREFIX}-run" 2>&1)"
check_contains "the devices are logged" 'msg="android device" serial=dummy-android-001' "$logs"
check_not_contains "no error is logged" "level=ERROR" "$logs"
state="$(in_container wget -qO- --post-data '{"battery":7,"charging":false,"network":"none"}' http://127.0.0.1:8800/api/devices/dummy-android-001/state)"
check_contains "the state API changes the battery" '"level": 7' "$state"
check_contains "the state API changes the network" '"network": "none"' "$state"
check_contains "the change of the state is an event" "7% charging=false" "$(in_container wget -qO- http://127.0.0.1:8800/api/devices/dummy-android-001/events)"

section "control of the devices file"
printf -- '---\ncontrol: ":8900"\ndevices:\n  - platform: android\n' > "${WORK}/control.yml"
if docker create --name "${PREFIX}-control" "$IMAGE" > /dev/null 2>&1 &&
  docker cp "${WORK}/control.yml" "${PREFIX}-control:/opt/zebrunner/devices.yml" > /dev/null 2>&1 &&
  docker start "${PREFIX}-control" > /dev/null 2>&1 && wait_healthy 60 "${PREFIX}-control"; then
  pass "the healthcheck asks the control port of the devices file"
else
  fail "the healthcheck asks the control port of the devices file" "not healthy" "$(docker logs "${PREFIX}-control" 2>&1 | tail -20)"
fi
check "the control API listens on the port of the devices file" "ok" "$(docker exec "${PREFIX}-control" wget -qO- http://127.0.0.1:8900/healthz 2>&1)"

section "fleets"
out="$(docker run --rm -v "$REPO/examples/fleet.yml:/w/fleet.yml:ro" "$IMAGE" farm-devices -config /w/fleet.yml -host dummy 2>&1)"
check "farm-devices lists the devices of the fleet (the time zones are in the image)" "19" "$(grep -c -- '- id:' <<< "$out")"
check_contains "farm-devices makes iOS proxy devices" "connection: proxy" "$out"
out="$(docker run --rm "$IMAGE" clone 2>&1)"
check "clone without STF_TOKEN exits with 2" "2" "$?"

finish

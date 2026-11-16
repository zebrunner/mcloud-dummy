# The lab of mcloud-dummy

The stand mcloud-dummy was verified on: an MCloud server and a node of dummy devices on one Linux host, every image built from the develop branches, the node enrolled and deployed with the tooling of mcloud-agent. It runs next to the real devices of the host without touching them: its own folder, its own list of devices (`MCLOUD_DEVICES` of the rendered settings of `zebrunner-farm`), no root.

```text
~/tools/az/mcloud-dummy-lab
|-- src/            the develop branches: stf, mcloud-grid, mcloud-device, appium, the connectors, mcloud-agent
|-- build-all.sh    builds their images with the tags the develop of mcloud and mcloud-agent expect
|-- mcloud/         the MCloud server (https://192.168.30.2:8443, a self-signed certificate)
|-- agent/          a copy of mcloud-agent: its settings (join) and the devices, lab-render.yml
|-- farm/           zebrunner-farm, its settings and mcloud-devices.txt rendered by lab-render.yml
|-- mcloud-dummy/   this repository, lab/dummy.yml the first 5 devices, lab/fleet.yml the varied fleet
`-- logs/
```

1. Build the images: `./build-all.sh` (`logs/build-*.log`, the result of every build in `logs/build-status.log`; it fails when a build failed, mcloud-device is built only after its STF). The tags are the ones of `TAG_STF` and `TAG_SELENIUM_HUB` of `mcloud/.env.original` and of `src/mcloud-agent/versions.yml` (`<version>-develop-latest` on develop). The pinned Alpine packages of mcloud-ios-connector were removed from the Alpine mirrors on 2026-11-10, its build fails: pull the image CI published of develop instead, `docker pull public.ecr.aws/zebrunner/mcloud-ios-connector:2.0-develop-latest`.
2. The server: clone mcloud, put a certificate of the LAN address into `configuration/stf-proxy/ssl/`, write `AGENTS_ADDRESS=<LAN address>` into `.env` and run the setup:

   ```bash
   printf "https\n192.168.30.2\n8443\ny\nadmin\nadmin@zebrunner.com\ny\nmock\nn\n" | ./zebrunner.sh setup
   ./zebrunner.sh start
   ```

   Sign in with the mock sign-in (`/auth/mock/`, then `/auth/api/v1/mock` with the admin), create an access token (`POST /api/v1/user/accessTokens?title=lab`), set it as `STF_TOKEN` of `variables.env`, `./zebrunner.sh restart`.
3. The node: `./zebrunner.sh node-invite dummy-node 192.168.30.2` on the server; in `agent/` (a copy of mcloud-agent): `cp config/settings.yml.original config/settings.yml` with `WDA_FILE: /dev/null`, `cp config/devices.yml.original config/devices.yml` with the devices of the lab (`android-sim` with `address: mcloud-dummy:<port>`, `proxy` with `address: mcloud-dummy`; `farm-devices` writes them, see below), then the join string into `./zebrunner.sh join`.
4. Render the farm without root in `agent/`: `zebrunner-farm`, its settings `zebrunner-farm.env` (from `versions.yml` and `config/`, `MCLOUD_DEVICES` the devices of the lab) and `mcloud-devices.txt`:

   ```bash
   ansible-playbook -i localhost, lab-render.yml -e lab_farm=$(cd ../farm && pwd)
   ```

5. Start the dummies and their containers:

   ```bash
   docker run -d --name mcloud-dummy --network infra --restart unless-stopped -p 192.168.30.2:8800:8800 \
     -e DUMMY_HOST=mcloud-dummy -v $PWD/mcloud-dummy/lab/dummy.yml:/opt/zebrunner/devices.yml:ro mcloud-dummy:dev
   ZEBRUNNER_FARM_ENV=$PWD/farm/zebrunner-farm.env ./farm/zebrunner-farm start
   ```

6. Check: `./zebrunner.sh nodes check dummy-node` on the server (the devices present, their screens reachable), `python3 mcloud-dummy/tests/e2e.py https://192.168.30.2:8443/mcloud/wd/hub all`, `python3 mcloud-dummy/tests/screen.py 192.168.30.2 <screen port>`.

The varied fleet (`lab/fleet.yml`: 8 Android and 4 iOS devices of the models of the demo farm, made by `mcloud-dummy clone`): stop the devices of the lab with `ZEBRUNNER_FARM_ENV=$PWD/farm/zebrunner-farm.env ./farm/zebrunner-farm down`, write their list into `config/devices.yml` of the agent, render the farm again (step 4) and start the dummies with `fleet.yml`:

```bash
docker run --rm -u $(id -u) -v $PWD/mcloud-dummy/lab/fleet.yml:/w/fleet.yml:ro -v $PWD/agent/config:/v \
  mcloud-dummy:dev farm-devices -config /w/fleet.yml -host mcloud-dummy -port-start 8600 -vars /v/devices.yml
```

What the lab verified (2026-11-10): the five dummies present and ready in STF (identity, display, battery, phone), their screens through the STF screen websocket (minicap and MJPEG), Appium sessions of both platforms through the grid, the remote debug of STF (`adb connect` to the STF bridge, shell and input through it), a reboot requested by STF and the recovery of the containers, the `unauthorized` device (the connector stops with 0), the redeploy of the dummies (all the containers recover), 200 dummies in one container. The fleet: the 12 devices present and ready in STF with the screens, the densities, the rotations, the battery levels and states and the networks (WIFI, MOBILE, none) of their configuration; a change of `POST /api/state` seen by STF within seconds; Appium sessions of both platforms; the UiAutomator2 contract (64 of 64) on a device of the cellular network; mcloud-grid ignores a device below 15% of battery for an hour (`STF_DEVICE_LOW_BATTERY_IGNORE_TIMEOUT`), also after it is charged again.

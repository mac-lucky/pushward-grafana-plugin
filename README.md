# PushWard for Grafana

[![CI](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml/badge.svg)](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml)

A Grafana App plugin that turns Grafana alerts into PushWard Live Activities on your iPhone: a live timeline sparkline on the Lock Screen and Dynamic Island that updates while an alert is firing and closes out when it resolves. It can also poll PromQL on a schedule and publish the results as PushWard iOS Home and Lock Screen widgets, so it replaces the standalone `pushward-grafana` container.

What it is: the in-Grafana setup and management layer for PushWard. One click wires up a webhook contact point, validates your key, and (with the backend) builds the timeline inside Grafana, so there is no separate container to run and no separate Prometheus scrape config for the bridge to read alert history. It reuses your existing Grafana datasource.

What it isn't: a native contact-point type. Grafana hardcodes those in core, so no third party can add one; every integration, including Grafana's own OnCall, delivers over a webhook contact point. This plugin makes that setup first-class, it does not replace the webhook.

## Features

- Connect wizard: one click creates the PushWard webhook contact point (no manual URL or header copying) and a scoped service-account token.
- Embedded timeline bridge: the Go backend queries your Grafana datasource for the alert's metric history and pushes a live timeline Live Activity. No `pushward-grafana` container required.
- Widget engine: declare `value`, `progress`, `status`, `gauge`, or `stat_list` widgets in the config; the backend polls each on its own interval (`on_change` or `always` mode, multi-series fan-out) and publishes them to your iOS widgets.
- Management: see current Live Activities and a recent delivery log, send a test notification, or fire a test timeline.
- Alert-rule links: "View in PushWard" on alert rules and instances.

## Requirements

- Grafana 12.3 or newer (unified alerting plus app-plugin IAM service accounts).
- A PushWard account and an `hlk_` integration key from the [PushWard iOS app](https://apps.apple.com/app/id6759689999). The `notifications` capability is needed for test notifications; the `widgets` capability is needed to publish widgets. Docs: <https://pushward.app/docs/integrations/grafana>.
- A Prometheus or VictoriaMetrics datasource in Grafana, for the timeline history and widget queries.

## Install

The plugin is not in the Grafana catalog yet, so it ships unsigned. However you install it, you have to allowlist its id (`pushward-alerts-app`) or Grafana will refuse to load it. Unsigned plugins do not load on Grafana Cloud, so this is self-hosted only for now.

### Bare metal or a bind-mounted plugins directory

Download the ZIP from the [latest release](https://github.com/mac-lucky/pushward-grafana-plugin/releases/latest), unzip it into your Grafana plugins directory (default `/var/lib/grafana/plugins`), and allowlist it:

```ini
# grafana.ini
[plugins]
allow_loading_unsigned_plugins = pushward-alerts-app
```

Restart Grafana.

### Docker or Kubernetes

Set env vars instead of editing `grafana.ini`. An in-container `grafana.ini` edit is lost the next time the container is recreated (image pull, `docker compose up`, a redeploy); the env var is not. This bites people, so prefer it for anything containerized:

```bash
GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=pushward-alerts-app

# Optional: let Grafana download and install the plugin on startup (Grafana 11.5+),
# instead of dropping the unzipped files into the plugins volume yourself.
GF_PLUGINS_PREINSTALL=pushward-alerts-app@0.3.0@https://github.com/mac-lucky/pushward-grafana-plugin/releases/download/v0.3.0/pushward-alerts-app-0.3.0.zip
```

Bump the version in both the tag and the file name to match the release you want. Without `PREINSTALL`, mount the unzipped plugin into the plugins volume and keep only the allowlist var.

Then enable the app under Administration > Plugins > PushWard and open its Configuration page.

## Configure

1. Open Administration > Plugins > PushWard > Configuration.
2. Paste your `hlk_` key and pick the datasource to read history from.
3. Run Connect. It creates the webhook contact point and a scoped viewer service-account token, and routes your alerts to it.
4. Optional: tune severity mapping, history window, and poll interval, and declare widgets. Fire a test timeline to confirm the whole path before you depend on it.

Turn on **Also send a push notification** if you want a normal banner / Lock Screen push alongside the timeline Live Activity: one active notification when an alert starts firing and a passive one when it resolves. The Live Activity is quiet by design, so this is the switch to flip when you want an alert to actively interrupt. It is off by default and applies to every alert routed to the PushWard contact point.

## Delivery metrics

The backend counts what it delivers: alerts received, activities created, pushes sent, and delivery errors. The Overview page shows the current values. Nothing to configure, and no datasource involved.

The counters reset when Grafana restarts the plugin's backend process. If you want history rather than a live count, they are also exported in Prometheus format at `/metrics/plugins/pushward-alerts-app` as `pushward_alerts_received_total`, `pushward_activities_created_total`, `pushward_pushes_sent_total`, and `pushward_errors_total`, so you can point an existing scrape job at them. That is optional and nothing in the plugin depends on it.

## Provision (GitOps)

Everything the Configuration page and Connect wizard set can live in git and be provisioned from files, so a fresh Grafana comes up already wired. This repo ships an example under `provisioning/plugins/`.

- App settings via `provisioning/plugins/apps.yaml`: an `apps:` block with `jsonData` for the config and the `widgets` array, and `secureJsonData.apiKey` for the `hlk_` key.
- Datasource via `provisioning/datasources/*.yaml`.
- The webhook contact point via `provisioning/alerting/*.yaml`.

Two caveats. Provisioning writes the settings row but does not run the plugin's first-run backend logic, so after a deploy hit `GET /api/plugins/pushward-alerts-app/resources/healthz` once to spin the backend up. And provisioned settings re-apply on every restart, so the file wins over any later UI edit. Keep the key out of git with `$__env{VAR}` interpolation in `secureJsonData`.

## Signing

Shipping unsigned is the reason every install needs the allowlist step above. There are two ways to drop it, both in [`DISTRIBUTION.md`](./DISTRIBUTION.md):

- Private signature: self-service, no Grafana review. It signs the build for specific instance root URLs, and those instances then load it without the allowlist. It is pinned to the URL, so you re-sign when the URL changes and it does nothing for other users.
- Community catalog signature (later): Grafana reviews the plugin once, then CI signs each release with your own access-policy token. Users install from the catalog with no manual steps, and it is the only path that works on Grafana Cloud.

## Develop

```bash
bun install
bun run dev                  # watch-build frontend
mage -v build:linuxARM64     # build backend (rerun after pkg/ edits)
docker compose up            # dev Grafana at http://localhost:3000
```

The backend loops the alert webhook back into its own `/resources/webhook` so the timeline is built inside Grafana, where the datasource is reachable. It imports the shared PushWard wire contract from `pushward-integrations` rather than redefining the JSON, because the snake_case REST vs camelCase APNs split is the number-one source of contract bugs. See [`DISTRIBUTION.md`](./DISTRIBUTION.md) for the signing and release flow.

## License

Apache-2.0.

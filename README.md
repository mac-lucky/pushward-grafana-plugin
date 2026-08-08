# PushWard for Grafana

[![CI](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml/badge.svg)](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml)
[![Website](https://img.shields.io/badge/pushward.app-5B4FE5?logo=safari&logoColor=white)](https://pushward.app)
[![App Store](https://img.shields.io/badge/App_Store-Download-0D96F6?logo=apple&logoColor=white)](https://apps.apple.com/app/id6759689999)

A Grafana App plugin that turns Grafana alerts into PushWard Live Activities on your iPhone: a live timeline sparkline on the Lock Screen and Dynamic Island that updates while an alert is firing and closes out when it resolves. It can also poll PromQL on a schedule and publish the results as PushWard iOS Home and Lock Screen widgets, so it replaces the standalone `pushward-grafana` container.

What it is: the in-Grafana setup and management layer for PushWard. One click wires up a webhook contact point, validates your key, and (with the backend) builds the timeline inside Grafana, so there is no separate container to run and no separate Prometheus scrape config for the bridge to read alert history. It reuses your existing Grafana datasource.

What it isn't: a native contact-point type. Grafana hardcodes those in core, so no third party can add one; every integration, including Grafana's own OnCall, delivers over a webhook contact point. This plugin makes that setup first-class, it does not replace the webhook.

## Features

- Connect wizard: one click creates the PushWard webhook contact point (no manual URL or header copying) and a scoped service-account token.
- Embedded timeline bridge: the Go backend queries your Grafana datasource for the alert's metric history and pushes a live timeline Live Activity. No `pushward-grafana` container required.
- Widget engine: declare `value`, `progress`, `status`, `gauge`, `stat_list`, `trend` or `countdown` widgets in the config; the backend polls each on its own interval (`on_change` or `always` mode, multi-series fan-out) and publishes them to your iOS widgets.
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
GF_PLUGINS_PREINSTALL=pushward-alerts-app@0.5.0@https://github.com/mac-lucky/pushward-grafana-plugin/releases/download/v0.5.0/pushward-alerts-app-0.5.0.zip
```

Bump the version in both the tag and the file name to match the release you want. Without `PREINSTALL`, mount the unzipped plugin into the plugins volume and keep only the allowlist var.

Then enable the app under Administration > Plugins and data > Plugins > PushWard and open its Configuration page.

## Configure

1. Open Administration > Plugins and data > Plugins > PushWard > Configuration.
2. Paste your `hlk_` key and pick the datasource to read history from.
3. Run Connect. It creates the webhook contact point and a scoped viewer service-account token, and routes your alerts to it.
4. Optional: tune severity mapping, history window, and poll interval, and declare widgets. Fire a test timeline to confirm the whole path before you depend on it.

Turn on **Also send a push notification** if you want a normal banner / Lock Screen push alongside the timeline Live Activity, one when an alert starts firing and one when it resolves. The Live Activity is quiet by design, so this is the switch to flip when you want an alert to actively interrupt. Pick a **Notification priority** for those pushes: Silent (quiet, Lock Screen only), Normal (a regular alert), or Critical (breaks through Focus and silent mode, if your PushWard account has the critical-alert entitlement, otherwise it falls back to time-sensitive). The priority applies to both the firing and resolved push. It is off by default and applies to every alert routed to the PushWard contact point.

## Widgets

Each entry in the `widgets` array is one query published as an iOS Home or Lock Screen widget. A slug, a query and a template is the whole minimum; everything below is optional.

`trend` draws a sparkline. The plugin keeps the last 48 readings of a widget in memory and sends them alongside the current value, so the chart comes from the poll history rather than a range query. At a 60s interval it fills in 48 minutes. The widget shows up on the second poll, not the first, because two samples is the least the server accepts for a line. Bounds behave differently here than on the other templates: `content.min_value` and `content.max_value` are optional chart limits, and without them the chart auto-scales. Trend needs `query` rather than `query_all` - there is one sample buffer per widget, so a fan-out has nowhere to keep per-series history.

A repeated reading is only recorded once a minute, or once per poll interval if that is longer. Without that, a widget polling every 5s would flush 48 minutes of real history out of the buffer in four and leave you looking at 48 identical dots. A reading that actually moves is always recorded immediately.

`countdown` is the only template with no query. Set `content.end_date` to an RFC 3339 timestamp, optionally `content.start_date` and `content.expired_text`, and the phone counts down on its own with no further pushes. Nothing polls it, so `stale_after` is rejected on a countdown.

`stale_after` is how many seconds after the last update iOS starts dimming a widget as out of date (60 to 604800, blank for never). Setting it also arms a heartbeat: the last published content is re-sent every `max(30s, stale_after/2)`, which the server records as a touch rather than a push, so a metric that sits flat for hours does not make the widget look dead and does not cost you a notification either. The window has to be at least three times the poll interval. The heartbeat fires on a poll tick, so the longest gap between two refreshes is half the window plus one interval; at three the gap stays comfortably inside the window, whereas at two it would land right on the edge and dim the widget once a cycle.

Any template can render its subtitle as a live timer via `content.subtitle_timer` (`{"date": "<RFC 3339>", "style": "timer"` or `"relative"}`), and a `stat_list` row takes the same object as a per-row `timer`. The static `subtitle` and row `value` stay behind them as the fallback. No form fields for these yet, so set them in the JSON view.

`battery`, `schedule` and `flow` exist on the server but not here. Each needs several independent readings in one payload - per-device charge levels, an hourly price curve, the four sides of an energy flow - which one query per widget cannot express. Use the REST API or the Home Assistant integration for those.

### Before you add a trend or countdown widget

These two templates need the PushWard iOS app **1.6.0 or newer**, and the failure on an older build is not a graceful one. The app decodes its widget list in one pass, so a single entry using a template it does not know makes **the entire widget list unavailable** - not just the new widget. Every other widget on that device stops rendering with it.

The app cannot recover on its own, and there is no in-app way to delete the widget that caused it. The fix is to remove the widget from this plugin's config and delete it through the API (`DELETE /widgets/{slug}`), or to update the app.

So: update the app on every device signed into the account before adding one, and remember that includes anything you do not check often, like a spare iPad or a family member's phone. The plugin does not block the choice, because it has no way to see what app version your devices run.

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

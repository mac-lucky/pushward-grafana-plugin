# PushWard for Grafana

[![CI](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml/badge.svg)](https://github.com/mac-lucky/pushward-grafana-plugin/actions/workflows/ci.yml)

A Grafana **App plugin** that turns Grafana alerts into rich **PushWard** Live Activities on your iPhone — a live timeline sparkline on the Lock Screen and Dynamic Island that updates while an alert is firing and closes out when it resolves.

> **What it is:** the in-Grafana setup + management layer for PushWard. One-click wires up a webhook contact point, validates your key, and (with a backend) renders the timeline **inside Grafana** — no separate container and no extra Prometheus config (it reuses your existing Grafana datasource).
>
> **What it isn't:** a native contact-point type. Grafana hardcodes those in core, so no third party can add one — every integration (including Grafana's own OnCall) delivers via a **webhook** contact point. This plugin makes that setup first-class; it doesn't replace the webhook.

## Features

- **Connect wizard** — one click creates the PushWard webhook contact point (no manual URL/header copying) and a scoped service-account token.
- **Embedded timeline bridge** — the Go backend queries your Grafana datasource for the alert's metric history and pushes a live timeline Live Activity; no `pushward-grafana` container required.
- **Configuration** — paste your PushWard `hlk_` key, pick the datasource, tune severity mapping / history window / poll interval.
- **Management** — see current Live Activities and a recent delivery log; send a test notification or fire a test timeline.
- **Alert-rule actions** — "View in PushWard" links on alert rules and instances.

## Requirements

- Grafana **≥ 12.3** (unified alerting + app plugin IAM service accounts).
- A **PushWard** account and an `hlk_` integration key with the `notifications` capability — get one in the [PushWard iOS app](https://apps.apple.com/app/id6759689999). Docs: <https://pushward.app/docs/integrations/grafana>.
- A Prometheus/VictoriaMetrics datasource in Grafana (for the timeline history).

## Install

**Self-hosted (now):** download the release ZIP, unzip into your Grafana plugins directory, and allowlist the unsigned plugin:

```ini
# grafana.ini
[plugins]
allow_loading_unsigned_plugins = pushward-alerts-app
```

Then enable **Administration → Plugins → PushWard** and open its **Configuration** page. Full instructions and the (future) signed-catalog / Grafana Cloud path: [`DISTRIBUTION.md`](./DISTRIBUTION.md).

## Develop

```bash
bun install
bun run dev                  # watch-build frontend
mage -v build:linuxARM64     # build backend (rerun after pkg/ edits)
docker compose up            # dev Grafana → http://localhost:3000
```

See [`CLAUDE.md`](./CLAUDE.md) for architecture (the embedded-bridge self-loop, the contract-lock against the PushWard wire format) and the full command set.

## License

Apache-2.0.

# Distribution & Signing

PushWard ships this plugin in two tracks: **unsigned for self-hosted now**, and the **official Grafana catalog (Community signature) later** for universal install + Grafana Cloud.

## 1. Self-hosted now (unsigned)

A release build is an unsigned ZIP (`pushward-alerts-app-<version>.zip`) attached to the GitHub Release.

**Install (manual):**
```bash
# Unzip into the Grafana plugins directory (default /var/lib/grafana/plugins)
unzip pushward-alerts-app-<version>.zip -d /var/lib/grafana/plugins/
```

**Allow the unsigned plugin** — Grafana refuses to load unsigned plugins unless explicitly allowlisted:

`grafana.ini`:
```ini
[plugins]
allow_loading_unsigned_plugins = pushward-alerts-app
```

Docker / Kubernetes env var equivalent:
```
GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=pushward-alerts-app
```

Restart Grafana, then enable the app under **Administration → Plugins → PushWard → Enable**.

> Unsigned plugins do **not** load on **Grafana Cloud**. Cloud requires the signed catalog build (track 2).

## 2. Official catalog / Community signature (later)

1. Register a grafana.com org (the plugin id prefix — `pushward` — must match the org slug).
2. Submit at grafana.com → My Plugins → Submit New Plugin (ZIP URL + source URL + SHA1). Grafana's Plugins team reviews (~2–6 weeks) and assigns a Community signature level.
3. Once approved, CI signs every release. The signed build installs via the catalog / `grafana-cli plugins install pushward-alerts-app` and loads on self-hosted **and** Cloud with no allowlist.

**Enable signing in CI** (`.github/workflows/release.yml`):
- Uncomment the `with: policy_token:` lines on the `grafana/plugin-actions/build-plugin` step.
- Add the repo secret `GRAFANA_ACCESS_POLICY_TOKEN` (generate per
  https://grafana.com/developers/plugin-tools/publish-a-plugin/sign-a-plugin#generate-an-access-policy-token).

**Private signing** (pin to specific self-hosted instances, no catalog review) is also possible:
```bash
export GRAFANA_ACCESS_POLICY_TOKEN=<token>
bun run sign -- --rootUrls https://grafana.example.com
```

## Backend binaries

The backend ships per-OS/arch binaries (`gpx_alerts_<os>_<arch>`). CI cross-compiles via mage (`mage -v buildAll`); the release action packages them into the ZIP. Required targets: `linux/amd64`, `linux/arm64` (Grafana Cloud + most self-hosted), plus `darwin`/`windows` for local dev convenience.

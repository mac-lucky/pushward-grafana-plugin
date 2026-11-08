# Distribution & Signing

PushWard ships this plugin **unsigned for self-hosted Grafana**. A listing in the **official Grafana catalog** would add one-click install and Grafana Cloud, but it needs a paid Commercial signature (section 2).

## 1. Self-hosted now (unsigned)

A release build is an unsigned ZIP (`pushward-alerts-app-<version>.zip`) attached to the GitHub Release.

**Install (manual):**
```bash
# Unzip into the Grafana plugins directory (default /var/lib/grafana/plugins)
unzip pushward-alerts-app-<version>.zip -d /var/lib/grafana/plugins/
```

**Allow the unsigned plugin**: Grafana refuses to load unsigned plugins unless explicitly allowlisted:

`grafana.ini`:
```ini
[plugins]
allow_loading_unsigned_plugins = pushward-alerts-app
```

Docker / Kubernetes env var equivalent:
```
GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=pushward-alerts-app
```

Restart Grafana, then enable the app under **Administration > Plugins > PushWard > Enable**.

> Unsigned plugins do **not** load on **Grafana Cloud**. Cloud users can send alerts through the [relay webhook](https://pushward.app/docs/integrations/relay) with no plugin.

## 2. Official catalog / Commercial signature (not started)

Grafana's [plugin policy](https://grafana.com/legal/plugins/) limits the Community signature to plugins "not affiliated with any commercial endeavor", and PushWard is a commercial service. A catalog listing therefore needs a Commercial signature, which requires a paid Commercial Plugin Subscription. The price is not published: ask integrations@grafana.com.

1. Agree the subscription with Grafana Labs.
2. Register a grafana.com org (the plugin id prefix, `pushward`, must match the org slug).
3. Submit at grafana.com > My Plugins > Submit New Plugin (ZIP URL + source URL + SHA1). Grafana's Plugins team reviews it.
4. Once approved, CI signs every release. The signed build installs via the catalog / `grafana-cli plugins install pushward-alerts-app` and loads on self-hosted **and** Cloud with no allowlist.

**Enable signing in CI** (`.github/workflows/release.yml`):
- Uncomment the `with: policy_token:` lines on the `grafana/plugin-actions/build-plugin` step.
- Add the repo secret `GRAFANA_ACCESS_POLICY_TOKEN` (generate per
  https://grafana.com/developers/plugin-tools/publish-a-plugin/sign-a-plugin#generate-an-access-policy-token).

**Private signing** (pin to your own self-hosted instance, no catalog review) is also possible, but the policy does not allow sharing a privately signed plugin with anyone else:
```bash
export GRAFANA_ACCESS_POLICY_TOKEN=<token>
bun run sign -- --rootUrls https://grafana.example.com
```

## Backend binaries

The backend ships per-OS/arch binaries (`gpx_alerts_<os>_<arch>`). CI cross-compiles via mage (`mage -v buildAll`); the release action packages them into the ZIP. Required targets: `linux/amd64`, `linux/arm64` (Grafana Cloud + most self-hosted), plus `darwin`/`windows` for local dev convenience.

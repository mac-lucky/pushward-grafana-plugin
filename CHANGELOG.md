# Changelog

## 0.7.0

- Two new widget templates. `trend` draws a sparkline from the plugin's own rolling buffer of the last 48 polls, so you get a chart without a range query; it appears after the second poll and takes optional `min_value` / `max_value` as chart bounds. `countdown` has no query at all - give it an `end_date` and the phone counts down on its own. Both need the PushWard iOS app 1.6.0 or newer. Update every device on the account first: an older app cannot decode the new templates, and one entry it cannot decode makes its entire widget list unavailable until the widget is deleted through the API.
- New `stale_after` field (60 to 604800 seconds) for how long iOS waits before dimming a widget as out of date. Setting it also starts a heartbeat that re-sends the last published content at half that interval, which the server records as a touch rather than a push, so a metric that sits flat for hours no longer makes the widget look dead and costs you no notifications. It must be at least three times the poll interval.
- A widget subtitle, or any `stat_list` row, can render as a live timer that ticks on the device instead of on a poll. JSON view only for now.
- `battery`, `schedule` and `flow` widgets are listed with proper names and icons on the Widgets page now, though the plugin still cannot create them - one query per widget cannot supply the several readings they need.

## 0.6.0

- Optionally send a normal push notification alongside the timeline Live Activity when an alert fires and resolves, with a Silent / Normal / Critical priority. Silent is a quiet Lock Screen entry, Normal alerts as usual, and Critical breaks through Focus and silent mode (needs the critical-alert entitlement on your PushWard account, otherwise it is delivered as time-sensitive). Off by default; the priority applies to both the firing and resolved push.

## 0.5.0

- Delivery counters (alerts received, activities created, pushes sent, errors) now show on the Overview page with no setup. They were previously registered in a way the plugin SDK never exported, so nothing surfaced them.
- The same counters are exported in Prometheus format at `/metrics/plugins/pushward-alerts-app` if you want to keep history across restarts.
- Removed the bundled `PushWard Delivery` dashboard. Its panels needed a Prometheus scrape job against Grafana's own metrics endpoint, so it showed "No data" out of the box; the Overview page replaces it.

## 0.3.0

- Silence a firing alert's rule from the Activities page (uses your Grafana session).
- End a running Live Activity from the Activities page.
- Alert-rule action links: open the matching activities, or pre-fill the pushward_query annotation for a rule.
- Form-based widget editor alongside the raw-JSON view.

## 0.2.0

Initial release.

# Changelog

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

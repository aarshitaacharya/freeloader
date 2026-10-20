#!/usr/bin/env python3
"""Generates deploy/k8s/monitoring/freeloader-dashboard.json. Edit here, then run:
    python3 tools/dashboard/gen.py
Every query uses a metric the services actually export (grep `Name:` in */internal/metrics)."""
import json, pathlib

DS = {"type": "prometheus", "uid": "prometheus"}
panels, y, pid = [], 0, 0


def row(title):
    global y, pid
    pid += 1
    panels.append({"id": pid, "type": "row", "title": title, "collapsed": False,
                   "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}})
    y += 1


def panel(title, exprs, x, w, unit="short", kind="timeseries", desc="", h=8, stack=False):
    """exprs: list of (promql, legend)."""
    global pid
    pid += 1
    p = {"id": pid, "type": kind, "title": title, "description": desc, "datasource": DS,
         "gridPos": {"h": h, "w": w, "x": x, "y": y},
         "targets": [{"datasource": DS, "expr": e, "legendFormat": l, "refId": chr(65 + i)}
                     for i, (e, l) in enumerate(exprs)],
         "fieldConfig": {"defaults": {"unit": unit, "custom": {"lineWidth": 1, "fillOpacity": 10,
                         "stacking": {"mode": "normal" if stack else "none"}}}, "overrides": []},
         "options": {"legend": {"displayMode": "list", "placement": "bottom"},
                     "tooltip": {"mode": "multi"}}}
    if kind == "stat":
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "value"}
    panels.append(p)


def done_row(h=8):
    global y
    y += h


G = 'job="gateway"'
row("Traffic")
panel("Requests/s by status", [('sum by (status) (rate(fl_gateway_requests_total[1m]))', "{{status}}")], 0, 8, "reqps", stack=True)
panel("Requests/s by alias", [('sum by (alias) (rate(fl_gateway_requests_total[1m]))', "{{alias}}")], 8, 8, "reqps", stack=True)
panel("Response cache hit ratio", [('sum(rate(fl_gateway_cache_hits_total[5m])) / sum(rate(fl_gateway_requests_total[5m]))', "hit ratio")], 16, 4, "percentunit", "stat")
panel("Rate-limited/s", [('sum by (app) (rate(fl_gateway_ratelimited_total[1m]))', "{{app}}")], 20, 4, "reqps")
done_row()

row("Latency and providers")
panel("Latency p50 / p95 / p99", [
    ('histogram_quantile(0.5, sum by (le) (rate(fl_gateway_request_seconds_bucket[2m])))', "p50"),
    ('histogram_quantile(0.95, sum by (le) (rate(fl_gateway_request_seconds_bucket[2m])))', "p95"),
    ('histogram_quantile(0.99, sum by (le) (rate(fl_gateway_request_seconds_bucket[2m])))', "p99")], 0, 8, "s",
    desc="End-to-end, including cache hits and failover attempts.")
panel("p95 latency by answering provider", [('histogram_quantile(0.95, sum by (le, provider) (rate(fl_gateway_request_seconds_bucket[2m])))', "{{provider}}")], 8, 8, "s",
      desc='provider="cache" is a response-cache hit.')
panel("Upstream errors/s by provider and code", [('sum by (provider, code) (rate(fl_gateway_upstream_errors_total[1m]))', "{{provider}} {{code}}")], 16, 8, "ops",
      desc="code 0 = network error or timeout.")
done_row()
panel("Failovers/s (from → to)", [('sum by (from, to) (rate(fl_gateway_failovers_total[1m]))', "{{from}} → {{to}}")], 0, 12, "ops")
panel("Gateway replicas up", [('count(up{job="gateway"} == 1)', "up")], 12, 4, "short", "stat")
panel("Gateway cache errors/s (fail-open)", [('sum(rate(fl_gateway_cache_errors_total[1m]))', "errors")], 16, 8, "ops",
      desc="fl-cache unreachable: the gateway keeps serving without limits.")
done_row()

row("Quota (fl-guard)")
panel("Requests today by provider", [('max by (provider) (fl_guard_provider_requests_today)', "{{provider}}")], 0, 8, "short",
      desc="Counted by the guard from usage events. Reset time is the provider's own midnight.")
panel("Requests today by model", [('max by (provider, model) (fl_guard_model_requests_today)', "{{provider}}/{{model}}")], 8, 8, "short")
panel("Exhausted flags (1 = skipped by the router)", [
    ('max by (provider) (fl_guard_exhausted)', "{{provider}}"),
    ('max by (provider, model) (fl_guard_model_exhausted)', "{{provider}}/{{model}}")], 16, 8, "short")
done_row()

row("Usage pipeline (events → ledger)")
panel("Events buffered / dropped (gateway)", [
    ('sum(fl_gateway_events_buffered)', "buffered"),
    ('sum(increase(fl_gateway_events_dropped_total[5m]))', "dropped (5m)")], 0, 8, "short",
    desc="Dropped = events lost while the broker was unavailable (ADR 0006).")
panel("Ledger events/s and duplicates/s", [
    ('sum(rate(fl_ledger_events_total[1m]))', "newly written"),
    ('sum(rate(fl_ledger_duplicates_total[1m]))', "duplicates ignored")], 8, 8, "ops")
panel("Consumer lag by partition", [('max by (partition) (fl_ledger_consumer_lag)', "p{{partition}}")], 16, 8, "short")
done_row()
panel("Ledger batch time p95", [('histogram_quantile(0.95, sum by (le) (rate(fl_ledger_batch_seconds_bucket[5m])))', "p95")], 0, 8, "s")
panel("Ledger DB / invalid errors", [
    ('sum(increase(fl_ledger_db_errors_total[5m]))', "db errors (5m)"),
    ('sum(increase(fl_ledger_invalid_total[5m]))', "invalid records (5m)")], 8, 8, "short")
panel("Guard events/s and duplicates/s", [
    ('sum(rate(fl_guard_events_total[1m]))', "counted"),
    ('sum(rate(fl_guard_duplicates_total[1m]))', "duplicates")], 16, 8, "ops")
done_row()

row("fl-cache")
panel("Commands/s by command", [('sum by (cmd) (rate(fl_cache_commands_total[1m]))', "{{cmd}}")], 0, 8, "ops", stack=True)
panel("Command latency p99 (excludes network)", [('histogram_quantile(0.99, sum by (le) (rate(fl_cache_command_seconds_bucket[2m])))', "p99")], 8, 8, "s")
panel("Keys / connections", [('fl_cache_keys', "keys"), ('fl_cache_connections', "connections")], 16, 4, "short")
panel("Evictions + expired /s", [('rate(fl_cache_evictions_total[1m])', "evicted"), ('rate(fl_cache_expired_total[1m])', "expired")], 20, 4, "ops")
done_row()

dash = {"uid": "freeloader", "title": "Freeloader", "tags": ["freeloader"], "timezone": "browser",
        "schemaVersion": 39, "version": 1, "refresh": "10s", "time": {"from": "now-15m", "to": "now"},
        "panels": panels, "templating": {"list": []}, "annotations": {"list": []}}
out = pathlib.Path(__file__).resolve().parents[2] / "deploy/k8s/monitoring/freeloader-dashboard.json"
out.write_text(json.dumps(dash, indent=1) + "\n")
print("wrote", out)

# README media

The GIFs in `docs/img/` are recorded from real runs, then assembled here. Nothing is mocked up: the browser
frames are Playwright screenshots of the running panel and Grafana, and the one terminal frame is drawn from
the real `curl` response captured during the run.

Needs the kind stack up (`make kind-up-mock`, `make kind-monitoring`), a port-forward to Grafana
(`make kind-grafana`), Node with Playwright (`npm i playwright && npx playwright install chromium`) and
Python with Pillow (`pip install pillow`).

```bash
# panel demo: creates an app named <name> (pick one not used before), sends 40 requests, screenshots each step
ADMIN=$(kubectl --context kind-freeloader -n freeloader get secret freeloader-secrets -o jsonpath='{.data.ADMIN_TOKEN}' | base64 -d)
node tools/readme-media/panel-frames.js "$ADMIN" /tmp/panel-frames doc-search
python3 tools/readme-media/assemble.py /tmp/panel-frames/frames.json docs/img/panel-demo.gif --width 960

# grafana drill: needs a load test running in parallel, and deletes a gateway pod 7 captures in
#   (cd gateway && go run ./cmd/loadgen --key <key> --duration 100s --rps 40)
GRAFANA_PW=$(kubectl --context kind-freeloader -n monitoring get secret grafana-admin -o jsonpath='{.data.password}' | base64 -d)
node tools/readme-media/grafana-frames.js "$GRAFANA_PW" /tmp/grafana-frames
python3 tools/readme-media/assemble.py /tmp/grafana-frames/frames.json docs/img/grafana-drill.gif --width 1000
```

Captions are written into the capture scripts. The Grafana capture is a time-lapse (one frame every 3.5 s,
shown for 0.7 s), and its captions say what the run actually showed.

"""Weighted traffic splitter — a LOCAL DEVELOPMENT AID, not part of the solution.

This is a stand-in for the NGINX ingress controller, which in the real system
(Phase 4) performs weighted routing between the stable and canary ReplicaSets
under the control of an Argo Rollouts `Rollout` manifest. Argo Rollouts sets the
weight and advances it automatically; this script only lets a human set it by
hand, so the gateway dashboard can be exercised before a cluster exists.

Nothing here should be cited as part of the thesis implementation — it exists
purely so Phase 1 can be demonstrated and debugged locally, and it is replaced
wholesale by Argo Rollouts + NGINX.

The canary weight is re-read from WEIGHT_FILE on every request so it can be
changed live, without restarting anything and without interrupting the
dashboard's rolling window.
"""

import os
import random
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STABLE_URL = os.environ.get("STABLE_URL", "http://localhost:8081")
CANARY_URL = os.environ.get("CANARY_URL", "http://localhost:8083")
WEIGHT_FILE = os.environ.get("WEIGHT_FILE", ".run/canary-weight")
PORT = int(os.environ.get("PORT", "8090"))

TIMEOUT_SECONDS = 10


def canary_weight():
    """Percentage of traffic to send to the canary, re-read per request."""
    try:
        with open(WEIGHT_FILE) as fh:
            return max(0.0, min(100.0, float(fh.read().strip())))
    except (OSError, ValueError):
        # A missing or malformed weight means "no canary" — the safe direction.
        return 0.0


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        target = CANARY_URL if random.random() * 100 < canary_weight() else STABLE_URL
        try:
            with urllib.request.urlopen(target + self.path, timeout=TIMEOUT_SECONDS) as resp:
                body, status = resp.read(), resp.status
                ctype = resp.headers.get("Content-Type", "application/json")
        except urllib.error.HTTPError as exc:
            # A 500 from a faulty canary is a real answer and must be passed
            # through verbatim: the body still names the version that failed.
            body, status = exc.read(), exc.code
            ctype = exc.headers.get("Content-Type", "application/json")
        except Exception:
            body, status, ctype = b'{"error":"splitter: backend unreachable"}', 502, "application/json"

        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass  # Silence per-request logging; the services do their own.


if __name__ == "__main__":
    print(f"splitter on :{PORT} — stable={STABLE_URL} canary={CANARY_URL}", flush=True)
    print(f"weight read live from {WEIGHT_FILE}", flush=True)
    ThreadingHTTPServer(("", PORT), Handler).serve_forever()

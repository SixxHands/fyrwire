from __future__ import annotations

import json
import subprocess
import urllib.error
import urllib.request

from .models import FyrWireError

class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


_RED = 0xE06C6C
_GREEN = 0x6EDC96
_AMBER = 0xF0BE5A


class Notifier:
    def __init__(self, webhook: str | None = None, command: str | None = None,
                 after_failures: int = 2, timeout: int = 8, log=print):
        self.webhook = webhook
        self.command = command
        self.after_failures = max(1, int(after_failures))
        self.timeout = int(timeout)
        self.log = log

    # Formats for Discord, Slack, or a plain webhook. Redirects are refused on purpose so nobody bounces our token somewhere funny.
    def _post(self, title: str, body: str, colour: int) -> None:
        if not self.webhook:
            return
        if "discord.com/api/webhooks" in self.webhook:
            payload = {"embeds": [{"title": title, "description": body,
                                   "color": colour}]}
        elif "hooks.slack.com" in self.webhook:
            payload = {"text": f"*{title}*\n{body}"}
        else:
            payload = {"title": title, "text": body}
        req = urllib.request.Request(
            self.webhook, data=json.dumps(payload).encode(), method="POST",
            headers={"Content-Type": "application/json"})
        opener = urllib.request.build_opener(_NoRedirect)
        with opener.open(req, timeout=self.timeout) as r:
            if r.status >= 300:
                raise urllib.error.HTTPError(
                    self.webhook, r.status,
                    "the alert was NOT delivered", r.headers, None)

    def _run(self, title: str, body: str) -> None:
        if not self.command:
            return
        cmd = self.command.format(title=title, body=body.replace("\n", " "))
        r = subprocess.run(cmd, shell=True, timeout=self.timeout,
                           capture_output=True, text=True)
        if r.returncode != 0:
            first = (r.stderr or "").strip().splitlines()
            detail = f" — {first[0][:200]}" if first else ""
            raise FyrWireError(
                f"alert command exited {r.returncode}{detail}")

    def send(self, title: str, body: str, colour: int = _AMBER) -> None:
        for fn in (lambda: self._post(title, body, colour),
                   lambda: self._run(title, body)):
            try:
                fn()
            except (urllib.error.URLError, urllib.error.HTTPError, OSError,
                    subprocess.SubprocessError, TimeoutError,
                    FyrWireError) as e:
                self.log(f"alert delivery failed: {e}")

    def evaluate(self, report: dict, state, node_name: str) -> list[str]:
        sent: list[str] = []
        failing = not report.get("ok")

        if failing:
            n = state.consecutive_failures
            if n >= self.after_failures and not getattr(state, "alerted_down", False):
                kept = report.get("kept") or {}
                ports = kept.get("ports") or {}
                detail = ", ".join(f"{k}:{v}" for k, v in sorted(ports.items()))
                self.send(
                    f"{node_name} — cannot reach the gateway",
                    f"{n} consecutive failures.\n"
                    f"`{report.get('error', 'unknown error')}`\n\n"
                    f"Leaving DNS pointed at the last known good endpoint"
                    + (f" ({detail})." if detail else "."),
                    _RED)
                state.alerted_down = True
                sent.append("down")
            return sent

        if getattr(state, "alerted_down", False):
            self.send(f"{node_name} — recovered",
                      f"Resolving again at `{report.get('host', '?')}`.", _GREEN)
            state.alerted_down = False
            sent.append("recovered")

        skipped = set(report.get("skipped") or [])
        reasons = report.get("reasons") or {}
        known = set(getattr(state, "alerted_services", []) or [])
        for key in sorted(skipped - known):
            why = reasons.get(key) or "the gateway granted it no port"
            self.send(f"{node_name} — {key} is not being published",
                      f"{why}.\n\nIts record is unchanged; the other services "
                      f"are fine.", _AMBER)
            sent.append(f"service-down:{key}")
        for key in sorted(known - skipped):
            self.send(f"{node_name} — {key} is back",
                      "It resolved again and its record has been updated.",
                      _GREEN)
            sent.append(f"service-up:{key}")
        state.alerted_services = sorted(skipped)

        return sent


def from_config(cfg: dict, log=print) -> Notifier | None:
    a = cfg.get("alerts") or {}
    if not (a.get("webhook") or a.get("command")):
        return None
    return Notifier(webhook=a.get("webhook"), command=a.get("command"),
                    after_failures=int(a.get("after_failures", 2)), log=log)

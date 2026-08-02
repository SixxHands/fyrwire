from __future__ import annotations

import asyncio
import io
import os
import socket
import sys
import tempfile
import threading
import time
import tomllib
import unittest
import urllib.error
from unittest import mock

from fyrwire.agent import Agent, AgentState
from fyrwire import config as config_mod
from fyrwire import providers
from fyrwire.models import (FyrWireError, HttpsRecordData, NodePlanData, EndpointData,
                            Service, SrvRecordData)
from fyrwire.providers import CloudflareProvider
from fyrwire.relay import RelayManager
from fyrwire.tui import panel, vis


class FakeSource:
    name = "fake"

    def __init__(self, *results):
        self.results = list(results)
        self.calls = 0

    def resolve(self, requests):
        r = self.results[min(self.calls, len(self.results) - 1)]
        self.calls += 1
        if isinstance(r, Exception):
            raise r
        return r


class FakeProvider:
    name = "fake"

    def __init__(self):
        self.a_calls: list[tuple] = []
        self.srv_calls: list[tuple] = []

    def ensure_a(self, name, ip, ttl):
        self.a_calls.append((name, ip))
        return f"set A {name}"

    def ensure_srv(self, rec):
        self.srv_calls.append((rec.name, rec.port))
        return f"set SRV {rec.name}"


def plan(*services, ttl=60):
    return NodePlanData("node.example.com", tuple(services), ttl=ttl)


MC = Service("minecraft", 25565, "_minecraft._tcp.example.com")
VH = Service("valheim", 2456, "_valheim._udp.example.com", "udp")


def echo_backend(tag: bytes) -> int:
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 0))
    s.listen(8)
    port = s.getsockname()[1]

    def serve():
        while True:
            try:
                c, _ = s.accept()
            except OSError:
                return
            threading.Thread(
                target=lambda k=c: (k.sendall(tag + b":" + k.recv(64)), k.close()),
                daemon=True).start()

    threading.Thread(target=serve, daemon=True).start()
    return port


def free_port() -> int:
    p = socket.socket()
    p.bind(("127.0.0.1", 0))
    n = p.getsockname()[1]
    p.close()
    return n


def roundtrip(port: int, msg: bytes = b"x") -> bytes:
    c = socket.create_connection(("127.0.0.1", port), timeout=5)
    c.sendall(msg)
    data = c.recv(128)
    c.close()
    return data


class Base(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.state = os.path.join(self.dir, "state.json")

    def agent(self, source, provider, p=None, **kw):
        return Agent(source, provider, p or plan(MC), self.state,
                     log=lambda *_: None, **kw)


class TestUpdate(Base):
    def test_publishes_only_on_change(self):
        src = FakeSource(EndpointData("1.1.1.1", {"minecraft": 100}),
                         EndpointData("1.1.1.1", {"minecraft": 100}),
                         EndpointData("1.1.1.1", {"minecraft": 200}))
        prov = FakeProvider()
        a = self.agent(src, prov)
        a.tick(); a.tick(); a.tick()
        self.assertEqual([p for _, p in prov.srv_calls], [100, 200])
        self.assertEqual(len(prov.a_calls), 1, "address unchanged: publish A once")

    def test_resolve_failure_never_clears_dns(self):
        prov = FakeProvider()
        self.agent(FakeSource(EndpointData("1.1.1.1", {"minecraft": 100})),
                   prov).tick()
        prov2 = FakeProvider()
        report = self.agent(FakeSource(FyrWireError("tunnel down")), prov2).tick()
        self.assertFalse(report["ok"])
        self.assertEqual(prov2.a_calls, [])
        self.assertEqual(prov2.srv_calls, [])
        self.assertEqual(report["kept"]["ports"]["minecraft"], 100)

    def test_one_service_failing_is_not_node_failing(self):
        p = plan(MC, VH)
        self.agent(FakeSource(EndpointData("1.1.1.1", {"minecraft": 100, "valheim": 200})),
                   FakeProvider(), p).tick()
        prov = FakeProvider()
        report = self.agent(
            FakeSource(EndpointData("1.1.1.1", {"valheim": 999}, partial={"minecraft"})),
            prov, p).tick()
        self.assertTrue(report["ok"])
        self.assertEqual(report["skipped"], ["minecraft"])
        self.assertEqual(prov.srv_calls, [("_valheim._udp.example.com", 999)])

    def test_address_move_touches_a_not_srv(self):
        self.agent(FakeSource(EndpointData("1.1.1.1", {"minecraft": 100})),
                   FakeProvider()).tick()
        prov = FakeProvider()
        self.agent(FakeSource(EndpointData("2.2.2.2", {"minecraft": 100})), prov).tick()
        self.assertEqual(prov.a_calls, [("node.example.com", "2.2.2.2")])
        self.assertEqual(prov.srv_calls, [],
                         "an SRV targets the A name, not an address")

    def test_state_survives_restart(self):
        self.agent(FakeSource(EndpointData("1.1.1.1", {"minecraft": 100})),
                   FakeProvider()).tick()
        self.assertEqual(AgentState.load(self.state).last_ports["minecraft"], 100)


class TestRelay(unittest.TestCase):
    def setUp(self):
        self.rm = RelayManager(log=lambda *_: None)
        self.addCleanup(self.rm.shutdown)

    def test_forwards_traffic(self):
        back = echo_backend(b"MC")
        front = free_port()
        self.rm.sync({"mc": ("127.0.0.1", front, "127.0.0.1", back)})
        self.assertEqual(roundtrip(front), b"MC:x")

    def test_many_services_route_independently(self):
        specs, expect = {}, {}
        for tag in (b"MC", b"VH", b"WEB"):
            f = free_port()
            specs[tag.decode()] = ("127.0.0.1", f, "127.0.0.1", echo_backend(tag))
            expect[f] = tag + b":x"
        self.rm.sync(specs)
        for front, want in expect.items():
            self.assertEqual(roundtrip(front), want)

    def test_unchanged_listener_is_not_rebound(self):
        back, front = echo_backend(b"MC"), free_port()
        spec = {"mc": ("127.0.0.1", front, "127.0.0.1", back)}
        self.rm.sync(spec)
        self.assertEqual(self.rm.sync(spec), [],
                         "rebinding a working relay drops live connections")

    def test_rotation_rebinds_only_that_service(self):
        a_back, b_back = echo_backend(b"A"), echo_backend(b"B")
        a_front, b_front = free_port(), free_port()
        self.rm.sync({"a": ("127.0.0.1", a_front, "127.0.0.1", a_back),
                      "b": ("127.0.0.1", b_front, "127.0.0.1", b_back)})
        b_new = free_port()
        changes = self.rm.sync({"a": ("127.0.0.1", a_front, "127.0.0.1", a_back),
                                "b": ("127.0.0.1", b_new, "127.0.0.1", b_back)})
        self.assertEqual(len(changes), 1)
        self.assertIn("b", changes[0])
        self.assertEqual(roundtrip(a_front), b"A:x")
        self.assertEqual(roundtrip(b_new), b"B:x")

    def test_bind_failure_spares_the_others(self):
        good_back, good_front = echo_backend(b"OK"), free_port()
        taken = socket.socket()
        taken.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        taken.bind(("127.0.0.1", 0))
        taken.listen(1)
        self.addCleanup(taken.close)
        changes = self.rm.sync({
            "good": ("127.0.0.1", good_front, "127.0.0.1", good_back),
            "bad": ("127.0.0.1", taken.getsockname()[1], "127.0.0.1", good_back),
        })
        self.assertTrue(any("FAILED" in c for c in changes))
        self.assertEqual(roundtrip(good_front), b"OK:x")
        self.assertNotIn("bad", self.rm.stats())

    def test_removal_stops_listening(self):
        back, front = echo_backend(b"MC"), free_port()
        self.rm.sync({"mc": ("127.0.0.1", front, "127.0.0.1", back)})
        self.rm.sync({})
        with self.assertRaises(OSError):
            roundtrip(front)


class TestIntegration(Base):
    def test_dns_and_relays_come_from_one_resolve(self):
        mc_back, vh_back = echo_backend(b"MC"), echo_backend(b"VH")
        mc_front, vh_front = free_port(), free_port()
        p = plan(Service("minecraft", mc_back, "_minecraft._tcp.example.com"),
                 Service("valheim", vh_back, "_valheim._tcp.example.com"))
        prov = FakeProvider()
        a = self.agent(
            FakeSource(EndpointData("1.1.1.1", {"minecraft": mc_front,
                                              "valheim": vh_front})),
            prov, p, relay=True, bind_host="127.0.0.1")
        self.addCleanup(a.relays.shutdown)
        report = a.tick()

        self.assertTrue(report["ok"])
        self.assertEqual(sorted(port for _, port in prov.srv_calls),
                         sorted([mc_front, vh_front]))
        self.assertEqual(roundtrip(mc_front), b"MC:x")
        self.assertEqual(roundtrip(vh_front), b"VH:x")

    def test_udp_service_is_not_relayed(self):
        p = plan(MC, VH)
        a = self.agent(
            FakeSource(EndpointData("1.1.1.1", {"minecraft": free_port(),
                                              "valheim": free_port()})),
            FakeProvider(), p, relay=True, bind_host="127.0.0.1")
        self.addCleanup(a.relays.shutdown)
        a.tick()
        self.assertNotIn("valheim", a.relays.stats(),
                         "UDP needs session tracking, not a TCP listener")


class TestRemove(unittest.TestCase):

    def _provider(self, existing_comment):
        from fyrwire.providers import CloudflareProvider

        class Fake(CloudflareProvider):
            def __init__(self):
                self.token, self.zone, self.timeout = "t", "z", 5
                self._ids = {}
                self.deleted = []

            def _find(self, rtype, name):
                return "rec123", {"id": "rec123", "comment": existing_comment}

            def _call(self, method, path, body=None):
                if method == "DELETE":
                    self.deleted.append(path)
                return {"success": True, "result": {}}

        return Fake()

    def test_deletes_a_record_it_owns(self):
        p = self._provider("managed by fyrwire")
        self.assertIn("deleted", p.delete("SRV", "_mc._tcp.example.com"))
        self.assertEqual(len(p.deleted), 1)

    def test_refuses_to_delete_a_record_it_does_not_own(self):
        p = self._provider("hand-made, do not touch")
        with self.assertRaises(FyrWireError):
            p.delete("SRV", "_mc._tcp.example.com")
        self.assertEqual(p.deleted, [], "must not delete an untagged record")

    def test_absent_record_is_not_an_error(self):
        from fyrwire.providers import CloudflareProvider

        class Fake(CloudflareProvider):
            def __init__(self):
                self.token, self.zone, self.timeout = "t", "z", 5
                self._ids = {}

            def _find(self, rtype, name):
                return None, None

        self.assertIsNone(Fake().delete("SRV", "_gone._tcp.example.com"))


class TestConfigEdit(unittest.TestCase):
    CONFIG = '''[source]
type = "static"
host = "1.2.3.4"

[provider]
type = "cloudflare"
api_token = "x"
zone_id = "z"

[node]
a_name = "node.example.com"

[[service]]
name          = "minecraft"
internal_port = 25565
srv_name      = "_minecraft._tcp.example.com"

[[service]]
name          = "valheim"
internal_port = 2456
srv_name      = "_valheim._udp.example.com"
'''

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, "fyrwire.toml")
        with open(self.path, "w") as fh:
            fh.write(self.CONFIG)

    def test_removing_one_leaves_the_other_intact(self):
        from fyrwire.cli import build_plan, load_config
        from fyrwire.setup import remove_service

        rc = remove_service(self.path, "valheim", keep_dns=True, yes=True)
        self.assertEqual(rc, 0)
        left = build_plan(load_config(self.path, resolve_secrets=False))
        self.assertEqual([s.key for s in left.services], ["minecraft"])
        self.assertEqual(left.services[0].internal_port, 25565,
                         "the surviving service must be untouched")

    def test_unknown_service_is_rejected(self):
        from fyrwire.setup import remove_service
        self.assertEqual(remove_service(self.path, "nope", keep_dns=True, yes=True), 1)
        with open(self.path) as fh:
            self.assertIn("valheim", fh.read(), "config must not be modified")


class RecordingNotifier:

    def __init__(self, after_failures=2):
        from fyrwire.notify import Notifier
        self.sent = []
        self.n = Notifier(webhook=None, command=None, after_failures=after_failures,
                          log=lambda *_: None)
        self.n.send = lambda title, body, colour=0: self.sent.append(title)

    def evaluate(self, report, state, node):
        return self.n.evaluate(report, state, node)


class TestAlerting(Base):
    def _agent(self, source, notifier, p=None):
        return Agent(source, FakeProvider(), p or plan(MC), self.state,
                     log=lambda *_: None, notifier=notifier)

    def test_single_failure_does_not_alert(self):
        n = RecordingNotifier(after_failures=2)
        self._agent(FakeSource(FyrWireError("blip")), n).tick()
        self.assertEqual(n.sent, [])

    def test_alerts_once_after_the_threshold(self):
        n = RecordingNotifier(after_failures=2)
        src = FakeSource(FyrWireError("down"))
        for _ in range(5):
            self._agent(src, n).tick()
        self.assertEqual(len(n.sent), 1, "one alert per episode, not per cycle")
        self.assertIn("cannot reach", n.sent[0])

    def test_recovery_alerts_immediately(self):
        n = RecordingNotifier(after_failures=2)
        for _ in range(2):
            self._agent(FakeSource(FyrWireError("down")), n).tick()
        self.assertEqual(len(n.sent), 1)
        self._agent(FakeSource(EndpointData("1.1.1.1", {"minecraft": 100})), n).tick()
        self.assertEqual(len(n.sent), 2)
        self.assertIn("recovered", n.sent[1])

    def test_per_service_transitions(self):
        p = plan(MC, VH)
        n = RecordingNotifier()
        self._agent(FakeSource(
            EndpointData("1.1.1.1", {"valheim": 200}, partial={"minecraft"})), n, p).tick()
        self.assertTrue(any("minecraft" in t for t in n.sent))
        n.sent.clear()
        self._agent(FakeSource(
            EndpointData("1.1.1.1", {"minecraft": 100, "valheim": 200})), n, p).tick()
        self.assertTrue(any("back" in t for t in n.sent))

    def test_broken_channel_cannot_stop_reconciling(self):
        class Exploding:
            def evaluate(self, *a, **k):
                raise RuntimeError("webhook exploded")
        report = self._agent(
            FakeSource(EndpointData("1.1.1.1", {"minecraft": 100})), Exploding()).tick()
        self.assertTrue(report["ok"], "a broken notifier must not fail the tick")


class TestHostInfo(unittest.TestCase):

    def test_local_address_toward_needs_no_subprocess(self):
        import fyrwire.hostinfo as h
        called = []
        real = h.subprocess.run
        h.subprocess.run = lambda *a, **k: called.append(a) or real(*a, **k)
        try:
            addr = h.local_address_toward("1.1.1.1")
        finally:
            h.subprocess.run = real
        self.assertIsNotNone(addr, "routing lookup should always resolve")
        self.assertEqual(called, [], "must not shell out for the routing answer")

    def test_subnet_router(self):
        from fyrwire.hostinfo import subnet_router
        self.assertEqual(subnet_router("10.2.0.2"), "10.2.0.1")
        self.assertEqual(subnet_router("192.168.4.77"), "192.168.4.1")
        self.assertIsNone(subnet_router("not-an-address"))

    def test_gateway_candidates_work_without_interface_enumeration(self):
        from fyrwire import detect, hostinfo

        class NoIfaces(hostinfo.IHostBackend):
            name = "stub"
            def listening_ports(self): return []
            def interfaces(self): return []

        real = hostinfo.backend
        hostinfo.backend = lambda: NoIfaces()
        try:
            cands = detect.gateway_candidates()
        finally:
            hostinfo.backend = real
        self.assertTrue(cands, "routing-table candidate must survive alone")
        self.assertIn("route toward the internet", cands[0][1])

    def test_unknown_platform_degrades_instead_of_crashing(self):
        from fyrwire.hostinfo import FallbackBackend
        b = FallbackBackend()
        self.assertEqual(b.listening_ports(), [])
        self.assertIn("manually", b.unsupported_reason())

    def test_mac_and_windows_parsers_handle_real_output(self):
        from fyrwire.hostinfo import MacBackend, WindowsBackend
        import fyrwire.hostinfo as h

        LSOF = ("COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n"
                "java    52001 bob   45u  IPv6 0x1234      0t0  TCP *:25565 (LISTEN)\n"
                "Plex     6013 bob   12u  IPv4 0x5678      0t0  TCP 127.0.0.1:32400 (LISTEN)\n")
        NETSTAT = ("\nActive Connections\n\n"
                   "  Proto  Local Address          Foreign Address        State           PID\n"
                   "  TCP    0.0.0.0:25565          0.0.0.0:0              LISTENING       4321\n"
                   "  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       900\n")
        real = h._run
        try:
            h._run = lambda cmd, timeout=6: LSOF
            self.assertEqual(MacBackend().listening_ports(), [(25565, "java"), (32400, "Plex")])
            h._run = lambda cmd, timeout=6: NETSTAT
            self.assertEqual(WindowsBackend().listening_ports(), [(25565, "")],
                             "port 135 is below 1024 and must be filtered")
        finally:
            h._run = real


class TestPresets(unittest.TestCase):

    def test_srv_record_implies_a_client_that_reads_it(self):
        from fyrwire import presets
        bad = [p.key for p in presets.listing()
               if p.record == "srv" and p.follows not in ("srv", "srv-setup")]
        self.assertEqual(bad, [], "publishing SRV that nothing reads is a lie")

    def test_srv_name_refuses_presets_with_no_srv_record(self):
        from fyrwire import presets
        web = presets.get("web")
        with self.assertRaises(ValueError):
            web.srv_name("example.com")

    def test_srv_names_are_well_formed(self):
        from fyrwire import presets
        placeholders = []
        for p in presets.listing():
            if p.record != "srv":
                continue
            if not p.service:
                placeholders.append(p.key)
                with self.assertRaises(ValueError):
                    p.srv_name("example.com")
                continue
            name = p.srv_name("example.com")
            self.assertRegex(name, r"^_[a-z0-9-]+\._(tcp|udp)\.example\.com$",
                             f"{p.key} produced {name}")
        self.assertEqual(placeholders, ["custom-srv"])

    def test_custom_label_is_validated_where_it_is_entered(self):
        from fyrwire import presets
        c = presets.get("custom-srv")
        self.assertEqual(c.with_service("mything", "udp").srv_name("example.com"),
                         "_mything._udp.example.com")
        for bad in ("_minecraft", "my.thing", "a--b", "12345", "", "-lead", "trail-"):
            with self.assertRaises(ValueError, msg=f"accepted {bad!r}"):
                c.with_service(bad)

    def test_web_is_not_advertised_as_automatic(self):
        from fyrwire import presets
        self.assertEqual(presets.get("web").follows, "manual")


class TestPanels(unittest.TestCase):
    def test_geometry_is_exact(self):
        for width in (60, 80, 100, 132):
            for status in ("", "LIVE", "DEGRADED x12"):
                lines = panel("fyrwire://node", ["a", "bb"], status, width)
                for line in lines:
                    self.assertEqual(vis(line), width,
                                     f"width={width} status={status!r}")


if __name__ == "__main__":
    unittest.main()


class FakeDesec(providers.DesecProvider):

    def __init__(self, min_ttl=60, **kw):
        existing = kw.pop("existing", {})
        super().__init__(token="t", domain="example.com", **kw)
        self.calls = []
        self.existing = existing
        self._minimum_ttl = min_ttl
        self._min_ttl_at = time.monotonic()

    def _call(self, method, url, body=None):
        self.calls.append((method, url, body))
        if method == "GET":
            return self.existing.get(url)
        return None


class TestDesecProvider(unittest.TestCase):
    def test_urls_always_end_in_a_slash(self):
        p = FakeDesec()
        p.ensure_a("node.example.com", "1.2.3.4", 60)
        for _, url, _ in p.calls:
            self.assertTrue(url.endswith("/"), url)

    def test_apex_is_at_sign_in_urls_but_empty_in_json(self):
        p = FakeDesec()
        p.ensure_a("example.com", "1.2.3.4", 60)
        get_url = [u for m, u, _ in p.calls if m == "GET"][0]
        self.assertIn("/rrsets/@/A/", get_url)
        body = [b for m, _, b in p.calls if m == "PATCH"][0]
        self.assertEqual(body[0]["subname"], "")

    def test_writes_go_to_the_collection_not_the_detail_endpoint(self):
        p = FakeDesec()
        p.ensure_a("node.example.com", "1.2.3.4", 60)
        patch_url = [u for m, u, _ in p.calls if m == "PATCH"][0]
        self.assertTrue(patch_url.endswith("/rrsets/"), patch_url)

    def test_write_body_is_a_list(self):
        p = FakeDesec()
        p.ensure_a("node.example.com", "1.2.3.4", 60)
        body = [b for m, _, b in p.calls if m == "PATCH"][0]
        self.assertIsInstance(body, list)

    def test_srv_target_gets_a_trailing_dot(self):
        p = FakeDesec()
        p.ensure_srv(SrvRecordData("_minecraft._tcp.play.example.com",
                               "node.example.com", 51471, 0, 5, 60))
        body = [b for m, _, b in p.calls if m == "PATCH"][0]
        self.assertEqual(body[0]["records"], ["0 5 51471 node.example.com."])

    def test_no_write_when_the_record_already_matches(self):
        url = ("https://desec.io/api/v1/domains/example.com/rrsets/node/A/")
        p = FakeDesec(existing={url: {"ttl": 60, "records": ["1.2.3.4"]}})
        self.assertIsNone(p.ensure_a("node.example.com", "1.2.3.4", 60))
        self.assertEqual([m for m, _, _ in p.calls], ["GET"])

    def test_case_difference_alone_is_not_a_change(self):
        url = ("https://desec.io/api/v1/domains/example.com/rrsets/"
               "_minecraft._tcp.play/SRV/")
        p = FakeDesec(existing={url: {"ttl": 60,
                                      "records": ["0 5 51471 Node.Example.com."]}})
        self.assertIsNone(p.ensure_srv(SrvRecordData(
            "_minecraft._tcp.play.example.com", "node.example.com",
            51471, 0, 5, 60)))

    def test_refuses_a_name_outside_the_domain(self):
        p = FakeDesec()
        with self.assertRaises(FyrWireError) as cm:
            p.ensure_a("node.elsewhere.org", "1.2.3.4", 60)
        self.assertIn("not inside", str(cm.exception))

    def test_delete_without_a_witness_is_refused(self):
        p = FakeDesec()
        with self.assertRaises(FyrWireError) as cm:
            p.delete("SRV", "_minecraft._tcp.play.example.com")
        self.assertIn("witness", str(cm.exception))

    def test_delete_refuses_a_record_pointing_elsewhere(self):
        url = ("https://desec.io/api/v1/domains/example.com/rrsets/"
               "_minecraft._tcp.play/SRV/")
        p = FakeDesec(existing={url: {"ttl": 60,
                                      "records": ["0 5 25565 someone-else.net."]}})
        with self.assertRaises(FyrWireError):
            p.delete("SRV", "_minecraft._tcp.play.example.com",
                     expect="node.example.com")
        self.assertNotIn("DELETE", [m for m, _, _ in p.calls])

    def test_delete_proceeds_when_the_witness_matches(self):
        url = ("https://desec.io/api/v1/domains/example.com/rrsets/"
               "_minecraft._tcp.play/SRV/")
        p = FakeDesec(existing={url: {"ttl": 60,
                                      "records": ["0 5 51471 node.example.com."]}})
        self.assertIsNotNone(p.delete("SRV", "_minecraft._tcp.play.example.com",
                                      expect="node.example.com"))
        self.assertIn("DELETE", [m for m, _, _ in p.calls])

    def test_delete_of_an_absent_record_is_a_no_op(self):
        p = FakeDesec()
        self.assertIsNone(p.delete("SRV", "_minecraft._tcp.play.example.com",
                                   expect="node.example.com"))

    def test_ttl_below_the_floor_is_refused_with_the_number_in_it(self):
        p = FakeDesec(min_ttl=3600)
        with self.assertRaises(FyrWireError) as cm:
            p.ensure_a("node.example.com", "1.2.3.4", 60)
        msg = str(cm.exception)
        self.assertIn("3600", msg)
        self.assertIn("exception", msg)


class FakeCloudflare(CloudflareProvider):
    def __init__(self):
        self.token, self.zone, self.timeout = "t", "z", 5
        self._ids = {}
        self.records = []
        self.deleted = []

    def _find(self, rtype, name):
        for r in self.records:
            if r["type"] == rtype and r["name"] == name:
                return r["id"], r
        return None, None

    def _call(self, method, path, body=None):
        if method == "DELETE":
            self.deleted.append(path)
        return {"success": True, "result": {}}


class TestOwnershipWitness(unittest.TestCase):

    def test_cloudflare_witness_is_checked_in_addition_to_the_tag(self):
        cf = FakeCloudflare()
        cf.records = [{"id": "1", "type": "SRV",
                       "name": "_minecraft._tcp.play.example.com",
                       "comment": CloudflareProvider.TAG,
                       "data": {"target": "somewhere-else.net"}}]
        with self.assertRaises(FyrWireError):
            cf.delete("SRV", "_minecraft._tcp.play.example.com",
                      expect="node.example.com")

    def test_every_provider_accepts_the_witness_argument(self):
        import inspect
        for name in providers.available():
            sig = inspect.signature(providers._REGISTRY[name].delete)
            self.assertIn("expect", sig.parameters, name)


class _FakeTime:

    def __init__(self, t=1000.0):
        self.t = t

    def __call__(self):
        return self.t

    def advance(self, seconds):
        self.t += seconds


def udp_echo_backend(tag: bytes):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    seen = []

    def serve():
        while True:
            try:
                data, addr = s.recvfrom(65535)
            except OSError:
                return
            seen.append(addr)
            try:
                s.sendto(tag + b":" + data, addr)
            except OSError:
                return

    threading.Thread(target=serve, daemon=True).start()
    return port, seen, s


def udp_roundtrip(port: int, payload: bytes, timeout=2.0):
    c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    c.settimeout(timeout)
    try:
        c.connect(("127.0.0.1", port))
        c.send(payload)
        return c.recv(65535)
    finally:
        c.close()


class TestUdpRelay(unittest.TestCase):
    def setUp(self):
        self.mgr = RelayManager()
        self.closers = []

    def tearDown(self):
        self.mgr.shutdown()
        for s in self.closers:
            try:
                s.close()
            except OSError:
                pass

    def backend(self, tag=b"BE"):
        port, seen, sock = udp_echo_backend(tag)
        self.closers.append(sock)
        return port, seen

    def test_datagram_round_trips(self):
        be, _ = self.backend(b"one")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.assertEqual(udp_roundtrip(front, b"hello"), b"one:hello")

    def test_two_clients_get_separate_upstream_sockets(self):
        be, seen = self.backend(b"x")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.assertEqual(udp_roundtrip(front, b"aaa"), b"x:aaa")
        self.assertEqual(udp_roundtrip(front, b"bbb"), b"x:bbb")
        self.assertEqual(len({p for _, p in seen}), 2,
                         "each client must reach the backend from its own port")

    def test_reply_source_port_is_the_granted_port(self):
        be, _ = self.backend(b"src")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        c.settimeout(2.0)
        try:
            c.sendto(b"q", ("127.0.0.1", front))
            data, addr = c.recvfrom(65535)
        finally:
            c.close()
        self.assertEqual(data, b"src:q")
        self.assertEqual(addr[1], front,
                         "a reply from any other port is dropped by any "
                         "client behind a NAT")

    def test_back_to_back_datagrams_open_exactly_one_session(self):
        be, seen = self.backend(b"r")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        c.connect(("127.0.0.1", front))
        c.send(b"1")
        c.send(b"2")
        c.settimeout(2.0)
        c.recv(65535)
        c.recv(65535)
        c.close()
        self.assertEqual(len({p for _, p in seen}), 1,
                         "one client must not open two upstream sockets")

    def test_unchanged_listener_is_not_rebound(self):
        be, _ = self.backend()
        front = free_port()
        spec = {"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")}
        self.mgr.sync(spec)
        self.assertEqual(self.mgr.sync(spec), [],
                         "a rebind would destroy every live session")

    def test_protocol_is_part_of_the_signature(self):
        from fyrwire.relay import _Listener, _UdpListener
        tcp = _Listener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        udp = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        self.assertNotEqual(tcp.signature, udp.signature)
        self.assertIn("tcp", tcp.signature)
        self.assertIn("udp", udp.signature)

    def test_protocol_change_rebinds_the_listener(self):
        be, _ = self.backend()
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "tcp")})
        self.assertEqual(self.mgr.stats()["a"]["proto"], "tcp")

    def test_stop_releases_the_fd_before_it_returns(self):
        from fyrwire.relay import _UdpListener
        be, _ = self.backend(b"z")
        front = free_port()

        async def scenario():
            first = _UdpListener("a", "127.0.0.1", front, "127.0.0.1", be)
            await first.start()
            await first.stop()
            second = _UdpListener("a", "127.0.0.1", front, "127.0.0.1", be)
            await second.start()
            await second.stop()

        asyncio.run(scenario())

    def test_rotation_through_the_manager_rebinds_and_still_serves(self):
        be, _ = self.backend(b"z")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.mgr.sync({})
        changes = self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.assertTrue(any("FAILED" not in c for c in changes), changes)
        self.assertEqual(udp_roundtrip(front, b"again"), b"z:again")

    def test_rotating_one_port_leaves_the_other_alone(self):
        be1, _ = self.backend(b"p")
        be2, _ = self.backend(b"q")
        f1, f2 = free_port(), free_port()
        self.mgr.sync({"a": ("127.0.0.1", f1, "127.0.0.1", be1, "udp"),
                       "b": ("127.0.0.1", f2, "127.0.0.1", be2, "udp")})
        f1b = free_port()
        changes = self.mgr.sync({"a": ("127.0.0.1", f1b, "127.0.0.1", be1, "udp"),
                                 "b": ("127.0.0.1", f2, "127.0.0.1", be2, "udp")})
        self.assertEqual(len(changes), 1, changes)
        self.assertEqual(udp_roundtrip(f2, b"still"), b"q:still")

    def test_removal_stops_listening(self):
        be, _ = self.backend()
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        self.mgr.sync({})
        self.assertNotIn("a", self.mgr.stats())
        with self.assertRaises((socket.timeout, TimeoutError, ConnectionError, OSError)):
            udp_roundtrip(front, b"anyone", timeout=0.4)

    def test_zero_length_datagram_creates_no_session(self):
        be, seen = self.backend()
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        c.sendto(b"", ("127.0.0.1", front))
        c.close()
        lis = self.mgr._listeners["a"]
        for _ in range(20):
            if lis.total:
                break
            time.sleep(0.02)
        self.assertEqual(lis.total, 0)
        self.assertEqual(seen, [])

    def test_max_datagram_round_trips_without_truncation(self):
        be, _ = self.backend(b"big")
        front = free_port()
        self.mgr.sync({"a": ("127.0.0.1", front, "127.0.0.1", be, "udp")})
        payload = b"y" * (65507 - 4)
        self.assertEqual(udp_roundtrip(front, payload, timeout=4.0),
                         b"big:" + payload)

    def test_stats_shape_for_both_protocols(self):
        be, _ = self.backend()
        tcp_be = echo_backend(b"t")
        f1, f2 = free_port(), free_port()
        self.mgr.sync({"u": ("127.0.0.1", f1, "127.0.0.1", be, "udp"),
                       "t": ("127.0.0.1", f2, "127.0.0.1", tcp_be, "tcp")})
        st = self.mgr.stats()
        self.assertEqual(st["u"]["proto"], "udp")
        self.assertEqual(st["t"]["proto"], "tcp")
        self.assertIn("sessions", st["u"])
        self.assertIn("active", st["t"])

    def test_bind_failure_on_one_key_spares_the_others(self):
        be, _ = self.backend(b"ok")
        blocker = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        blocker.bind(("127.0.0.1", 0))
        taken = blocker.getsockname()[1]
        self.closers.append(blocker)
        good = free_port()
        changes = self.mgr.sync({
            "bad": ("127.0.0.1", taken, "127.0.0.1", be, "udp"),
            "good": ("127.0.0.1", good, "127.0.0.1", be, "udp")})
        self.assertTrue(any("FAILED" in c for c in changes), changes)
        self.assertNotIn("bad", self.mgr.stats())
        self.assertEqual(udp_roundtrip(good, b"fine"), b"ok:fine")


class TestUdpSessionPolicy(unittest.TestCase):

    def _listener(self, **kw):
        from fyrwire.relay import _UdpListener
        clock = _FakeTime()
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2,
                           now=clock, **kw)
        lis.backend = ("127.0.0.1", 2)
        lis.loop = None
        return lis, clock

    def _fake_session(self, lis, addr):
        from fyrwire.relay import _UdpSession
        s = _UdpSession(client=addr, sock=socket.socket(socket.AF_INET,
                                                       socket.SOCK_DGRAM),
                        last_rx=lis.now(), last_tx=lis.now())
        lis.sessions[addr] = s
        lis.per_ip[addr[0]] = lis.per_ip.get(addr[0], 0) + 1
        lis.total += 1
        return s

    def test_unreplied_session_expires_on_the_short_timer(self):
        lis, clock = self._listener()
        self._fake_session(lis, ("1.2.3.4", 5000))
        clock.advance(31)
        self.assertEqual(lis.reap_once(), 1)

    def test_replied_session_survives_the_short_timer(self):
        lis, clock = self._listener()
        s = self._fake_session(lis, ("1.2.3.4", 5000))
        s.replied = True
        clock.advance(31)
        self.assertEqual(lis.reap_once(), 0)
        clock.advance(181)
        self.assertEqual(lis.reap_once(), 1)

    def test_backend_only_traffic_keeps_a_session_alive(self):
        lis, clock = self._listener()
        s = self._fake_session(lis, ("1.2.3.4", 5000))
        s.replied = True
        for _ in range(10):
            clock.advance(60)
            s.last_tx = clock()
            self.assertEqual(lis.reap_once(), 0)

    def test_session_table_cap_drops_rather_than_queues(self):
        lis, _ = self._listener(max_sessions=2)
        lis.max_sessions = 2
        for i in range(2):
            self._fake_session(lis, ("1.2.3.4", 5000 + i))
        lis.on_client(b"x", ("1.2.3.4", 9999))
        self.assertEqual(len(lis.sessions), 2)
        self.assertEqual(lis.dropped, 1)

    def test_per_ip_cap_stops_one_source_taking_the_table(self):
        lis, _ = self._listener(max_per_ip=2)
        for i in range(2):
            self._fake_session(lis, ("9.9.9.9", 6000 + i))
        lis.on_client(b"x", ("9.9.9.9", 7000))
        self.assertEqual(lis.dropped, 1)
        self.assertEqual(len(lis.sessions), 2)

    def test_new_session_rate_limit_is_a_token_bucket(self):
        lis, clock = self._listener(new_per_sec=2)
        lis.max_sessions = 100
        self.assertTrue(lis._allow_new())
        self.assertTrue(lis._allow_new())
        self.assertFalse(lis._allow_new(), "bucket must empty")
        clock.advance(1.0)
        self.assertTrue(lis._allow_new(), "and refill over time")

    def test_eviction_keeps_the_per_ip_index_correct(self):
        lis, _ = self._listener()
        s = self._fake_session(lis, ("5.5.5.5", 1234))
        lis._evict(s, "test")
        self.assertNotIn("5.5.5.5", lis.per_ip)
        self.assertEqual(lis.sessions, {})

    def test_max_sessions_is_clamped_to_real_host_limits(self):
        from fyrwire.relay import _UdpListener
        self.assertLess(_UdpListener._clamp(10 ** 9), 10 ** 9,
                        "the host's own limits must bound the table")
        self.assertGreaterEqual(_UdpListener._clamp(10 ** 9), 1)


BASE_CFG = '''
[source]
type = "natpmp"
gateway = "10.2.0.1"
[provider]
type = "cloudflare"
api_token = "env:T"
zone_id = "z"
[node]
a_name = "node.example.com"
ttl = 60
{node}
[[service]]
name = "mc"
internal_port = 25565
srv_name = "_minecraft._tcp.example.com"
{svc}
'''


def cfg_text(node="", svc="", top=""):
    return top + "\n" + BASE_CFG.format(node=node, svc=svc)


class TestConfigSchema(unittest.TestCase):
    def load(self, text):
        from fyrwire import config
        return config.validate(tomllib.loads(text))

    def rejects(self, text, *expect):
        with self.assertRaises(FyrWireError) as cm:
            self.load(text)
        msg = str(cm.exception)
        for e in expect:
            self.assertIn(e, msg)
        return msg

    def test_a_valid_config_loads(self):
        self.assertIn("node", self.load(cfg_text()))

    def test_typo_in_a_node_key_is_refused_with_a_suggestion(self):
        self.rejects(cfg_text(node="relayy = true"), "relayy", "did you mean")

    def test_typo_in_a_service_key_is_refused(self):
        self.rejects(cfg_text(svc="internl_port = 1"), "internal_port")

    def test_typo_in_a_section_name_is_refused(self):
        self.rejects(cfg_text() + '\n[alerst]\nwebhook = "x"\n', "alerst", "alerts")

    def test_typo_in_a_provider_key_is_refused(self):
        self.rejects(cfg_text().replace('zone_id = "z"', 'zoneid = "z"'), "zoneid")

    def test_typo_in_a_udp_key_is_refused(self):
        self.rejects(cfg_text(node="relay = true\nrelay_udp = true\n"
                                   "[node.udp]\nidle_neww = 5"), "idle_new")

    def test_unknown_provider_type_is_refused_with_a_suggestion(self):
        self.rejects(cfg_text().replace('"cloudflare"', '"clouflare"'),
                     "did you mean", "cloudflare")

    def test_missing_required_source_key_names_it(self):
        self.rejects(cfg_text().replace('gateway = "10.2.0.1"', ""), "gateway")

    def test_missing_required_service_key_names_it(self):
        self.rejects(cfg_text().replace(
            'srv_name = "_minecraft._tcp.example.com"', ""), "srv_name")

    def test_bool_and_int_are_not_interchangeable(self):
        self.rejects(cfg_text(node="relay = 1"), "true or false")
        self.rejects(cfg_text() + "\n[agent]\ninterval = true\n", "boolean")

    def test_enum_values_are_checked(self):
        self.rejects(cfg_text(svc='protocol = "sctp"'), "tcp, udp")
        self.rejects(cfg_text(svc='record = "txt"'), "srv")
        self.load(cfg_text(svc='protocol = "udp"'))

    def test_relay_udp_without_relay_is_refused(self):
        self.rejects(cfg_text(node="relay_udp = true"), "relay_udp", "relay")

    def test_single_bracket_service_is_diagnosed(self):
        self.rejects(cfg_text().replace("[[service]]", "[service]"),
                     "double brackets")

    def test_a_newer_schema_version_is_refused_not_guessed(self):
        self.rejects(cfg_text(top="[fyrwire]\nversion = 99"), "version 99", "1")

    def test_the_current_schema_version_is_accepted(self):
        self.load(cfg_text(top=f"[fyrwire]\nversion = {config_mod.SCHEMA_VERSION}"))

    def test_a_config_with_no_version_is_treated_as_version_one(self):
        self.assertNotIn("fyrwire", self.load(cfg_text()))

    def test_env_secrets_are_not_type_checked_as_their_final_value(self):
        self.load(cfg_text().replace('zone_id = "z"', 'zone_id = "env:ZONE"'))

    def test_the_desec_provider_validates_too(self):
        self.load(cfg_text().replace(
            'type = "cloudflare"\napi_token = "env:T"\nzone_id = "z"',
            'type = "desec"\ntoken = "env:T"\ndomain = "example.com"'))

    def test_schema_is_derived_from_constructors_so_it_cannot_drift(self):
        from fyrwire import config, providers
        keys = config._class_keys(providers.DesecProvider)
        self.assertEqual(set(keys), {"token", "domain", "timeout"})
        self.assertTrue(keys["token"][1], "token has no default, so it is required")
        self.assertFalse(keys["timeout"][1], "timeout has a default")

    def test_starter_config_passes_its_own_validator(self):
        from fyrwire import cli
        self.load(cli.STARTER)


def _clean_env(**extra):
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("XDG_", "FYRWIRE_", "APPDATA", "LOCALAPPDATA"))}
    env.update(extra)
    return env


class TestPlatformPaths(unittest.TestCase):
    def under(self, system, **env):
        from fyrwire import paths
        return mock.patch("platform.system", return_value=system), \
            mock.patch.dict(os.environ, _clean_env(**env), clear=True), paths

    def test_linux_uses_xdg_layout(self):
        sysp, envp, paths = self.under("Linux")
        with sysp, envp:
            self.assertTrue(paths.config_file().endswith(
                "/.config/fyrwire/fyrwire.toml"))
            self.assertTrue(paths.state_file().endswith(
                "/.local/state/fyrwire/state.json"))

    def test_macos_uses_application_support(self):
        sysp, envp, paths = self.under("Darwin")
        with sysp, envp:
            self.assertIn("Library/Application Support/FyrWire",
                          paths.config_file())

    def test_windows_config_roams_but_state_does_not(self):
        sysp, envp, paths = self.under(
            "Windows", APPDATA=r"C:\U\AppData\Roaming",
            LOCALAPPDATA=r"C:\U\AppData\Local")
        with sysp, envp:
            self.assertIn("Roaming", paths.config_file())
            self.assertIn("Local", paths.state_file())
            self.assertNotIn("Roaming", paths.state_file())

    def test_windows_falls_back_when_appdata_is_unset(self):
        sysp, envp, paths = self.under("Windows")
        with sysp, envp:
            self.assertIn("AppData", paths.config_file())

    def test_an_explicit_xdg_setting_wins_on_every_platform(self):
        for system in ("Linux", "Darwin", "Windows"):
            sysp, envp, paths = self.under(system, XDG_CONFIG_HOME="/custom")
            with sysp, envp:
                self.assertEqual(paths.config_file(),
                                 "/custom/fyrwire/fyrwire.toml", system)

    def test_explicit_env_overrides_beat_everything(self):
        sysp, envp, paths = self.under("Linux", FYRWIRE_CONFIG="/exact/path.toml")
        with sysp, envp:
            self.assertEqual(paths.config_file(), "/exact/path.toml")

    def test_legacy_locations_are_offered_for_migration_only_when_they_differ(self):
        from fyrwire import paths
        sysp, envp, _ = self.under("Linux")
        with sysp, envp:
            self.assertEqual(paths.legacy_locations(), [],
                             "on Linux the layout is unchanged, so nothing to migrate")
        sysp, envp, _ = self.under("Darwin")
        with sysp, envp:
            self.assertTrue(paths.legacy_locations(),
                            "a macOS user upgrading has files in the old place")


class TestPlatformHonesty(unittest.TestCase):

    def test_only_linux_is_marked_verified(self):
        from fyrwire import hostinfo
        self.assertTrue(hostinfo.LinuxBackend.verified)
        for cls in (hostinfo.MacBackend, hostinfo.WindowsBackend,
                    hostinfo.FallbackBackend):
            self.assertFalse(cls.verified, cls.__name__)

    def test_unverified_backends_say_so_in_health(self):
        from fyrwire import hostinfo
        ok, why = hostinfo.MacBackend().health()
        self.assertTrue(ok, "unverified is not the same as broken")
        self.assertIn("never been run", why)

    def test_unknown_platform_explains_what_still_works(self):
        from fyrwire import hostinfo
        ok, why = hostinfo.FallbackBackend().health()
        self.assertFalse(ok)
        self.assertIn("manually", why)

    def test_backend_selection_covers_every_platform_name(self):
        from fyrwire import hostinfo
        for system, want in [("Linux", "linux"), ("Darwin", "macos"),
                             ("Windows", "windows"), ("Haiku", "unknown")]:
            with mock.patch("platform.system", return_value=system):
                self.assertEqual(hostinfo.backend().name, want)

    def test_local_address_toward_sends_no_packets_and_still_answers(self):
        from fyrwire import hostinfo
        addr = hostinfo.local_address_toward("1.1.1.1")
        self.assertIsNotNone(addr)
        self.assertEqual(len(addr.split(".")), 4)


class TestServiceInstallers(unittest.TestCase):
    CFG = "/home/u/.config/fyrwire/fyrwire.toml"

    def test_systemd_unit_survives_a_space_in_the_path(self):
        from fyrwire import install
        line = [l for l in install.systemd_unit("/home/a b/cfg.toml").splitlines()
                if l.startswith("ExecStart")][0]
        self.assertIn('"/home/a b/cfg.toml"', line)

    def test_systemd_unit_escapes_percent(self):
        from fyrwire import install
        line = [l for l in install.systemd_unit("/home/50%/x.toml").splitlines()
                if l.startswith("ExecStart")][0]
        self.assertIn("50%%", line)

    def test_systemd_unit_does_not_embed_the_token(self):
        from fyrwire import install
        unit = install.systemd_unit(self.CFG)
        self.assertIn("EnvironmentFile", unit)
        self.assertNotIn("FYRWIRE_CF_TOKEN=sk", unit)

    def test_launchd_plist_is_well_formed_xml(self):
        from fyrwire import install
        import xml.dom.minidom
        doc = xml.dom.minidom.parseString(install.launchd_plist(self.CFG))
        self.assertEqual(doc.documentElement.tagName, "plist")

    def test_launchd_plist_escapes_xml_metacharacters(self):
        from fyrwire import install
        import xml.dom.minidom
        doc = xml.dom.minidom.parseString(
            install.launchd_plist("/home/a&b/<c>.toml"))
        strings = [n.firstChild.data for n in doc.getElementsByTagName("string")
                   if n.firstChild]
        self.assertIn("/home/a&b/<c>.toml", strings)

    def test_schtasks_passes_the_command_as_one_argument(self):
        from fyrwire import install
        cmd = install.schtasks_command(r"C:\Users\a b\cfg.toml")
        i = cmd.index("/TR")
        self.assertIn("fyrwire", cmd[i + 1])
        self.assertIn('"C:\\Users\\a b\\cfg.toml"', cmd[i + 1])

    def test_schtasks_registers_a_logon_task_not_a_system_service(self):
        from fyrwire import install
        cmd = install.schtasks_command(self.CFG)
        self.assertIn("ONLOGON", cmd)
        self.assertNotIn("/RU", cmd)

    def test_launcher_uses_the_running_interpreter(self):
        from fyrwire import install
        self.assertEqual(install._launcher(self.CFG)[0], sys.executable)

    def test_unknown_platform_refuses_to_install_but_says_what_works(self):
        from fyrwire import install
        with mock.patch("platform.system", return_value="Haiku"):
            with self.assertRaises(FyrWireError) as cm:
                install.install(self.CFG)
        self.assertIn("still runs", str(cm.exception))


class TestUdpFailureHandling(unittest.TestCase):

    def setUp(self):
        self.closers = []

    def tearDown(self):
        for s in self.closers:
            try:
                s.close()
            except OSError:
                pass

    def _dead_backend(self):
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
        s.close()
        return port

    def test_a_refused_backend_evicts_the_session_immediately(self):
        from fyrwire.relay import _UdpListener
        be = self._dead_backend()

        async def scenario():
            lis = _UdpListener("k", "127.0.0.1", 0, "127.0.0.1", be)
            await lis.start()
            port = lis.transport.get_extra_info("socket").getsockname()[1]
            c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            c.connect(("127.0.0.1", port))
            c.send(b"hello")
            await asyncio.sleep(0.25)
            c.close()
            result = (len(lis.sessions), lis.upstream_errors)
            await lis.stop()
            return result

        sessions, errors = asyncio.run(scenario())
        self.assertEqual(sessions, 0, "a refused session must not linger")
        self.assertGreaterEqual(errors, 1, "and must be counted")

    def test_a_session_evicted_mid_attach_does_not_steal_the_next_fd(self):
        from fyrwire.relay import _UdpListener
        be = self._dead_backend()

        async def scenario():
            lis = _UdpListener("k", "127.0.0.1", 0, "127.0.0.1", be)
            await lis.start()
            port = lis.transport.get_extra_info("socket").getsockname()[1]
            clients = []
            for i in range(4):
                c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                c.connect(("127.0.0.1", port))
                c.send(b"a")
                c.send(b"b")
                clients.append(c)
                await asyncio.sleep(0.05)
            await asyncio.sleep(0.3)
            deaf = [s for s in lis.sessions.values()
                    if s.transport is None and not s.attaching]
            for c in clients:
                c.close()
            await lis.stop()
            return deaf

        self.assertEqual(asyncio.run(scenario()), [],
                         "no session may survive with no transport")

    def test_the_rate_limiter_is_actually_wired_into_the_datagram_path(self):
        from fyrwire.relay import _UdpListener
        clock = _FakeTime()
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2,
                           new_per_sec=2, now=clock)
        lis.backend = ("127.0.0.1", 2)
        lis.loop = None
        lis.max_sessions = 100
        opened = []
        lis._open = lambda addr: opened.append(addr)
        for i in range(5):
            lis.on_client(b"x", ("1.2.3.4", 6000 + i))
        self.assertEqual(len(opened), 2, "the bucket holds 2")
        self.assertEqual(lis.dropped, 3)

    def test_a_reply_for_an_evicted_session_is_dropped(self):
        from fyrwire.relay import _UdpListener, _UdpSession
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        sent = []
        lis.transport = type("T", (), {"sendto": lambda self, d, a: sent.append(a)})()
        stale = _UdpSession(client=("1.2.3.4", 5000),
                            sock=socket.socket(socket.AF_INET, socket.SOCK_DGRAM))
        self.closers.append(stale.sock)
        lis.on_backend(stale, b"late")
        self.assertEqual(sent, [], "a reply for a session we dropped goes nowhere")

    def test_byte_counters_are_cumulative_not_a_live_sum(self):
        from fyrwire.relay import _UdpListener, _UdpSession
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        lis.backend = ("127.0.0.1", 2)
        lis.loop = None
        s = _UdpSession(client=("1.2.3.4", 5000),
                        sock=socket.socket(socket.AF_INET, socket.SOCK_DGRAM))
        self.closers.append(s.sock)
        lis.sessions[s.client] = s
        lis.c2s_bytes = 5000
        lis._evict(s, "test")
        self.assertEqual(lis.stats()["c2s_bytes"], 5000)

    def test_send_failures_are_not_counted_as_refused_sessions(self):
        from fyrwire.relay import _UdpListener
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        st = lis.stats()
        self.assertIn("send_dropped", st)
        self.assertIn("sessions_dropped", st)
        self.assertIn("listener_errors", st)

    def test_stats_is_safe_while_the_session_table_churns(self):
        from fyrwire.relay import _UdpListener, _UdpSession
        lis = _UdpListener("k", "127.0.0.1", 1, "127.0.0.1", 2)
        socks = []
        stop = threading.Event()
        errors = []

        def churn():
            i = 0
            while not stop.is_set():
                key = ("1.2.3.4", 5000 + (i % 64))
                sk = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                socks.append(sk)
                lis.sessions[key] = _UdpSession(client=key, sock=sk)
                if i % 3 == 0 and lis.sessions:
                    lis.sessions.pop(next(iter(lis.sessions)), None)
                i += 1

        t = threading.Thread(target=churn, daemon=True)
        t.start()
        try:
            for _ in range(20000):
                try:
                    lis.stats()
                except RuntimeError as e:
                    errors.append(e)
                    break
        finally:
            stop.set()
            t.join(timeout=2)
            for sk in socks:
                try:
                    sk.close()
                except OSError:
                    pass
        self.assertEqual(errors, [], "stats() must not iterate the live table")

    def test_a_failed_rebind_leaves_no_stale_listener_in_stats(self):
        mgr = RelayManager()
        try:
            be = self._dead_backend()
            first = free_port()
            mgr.sync({"a": ("127.0.0.1", first, "127.0.0.1", be, "udp")})
            blocker = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            blocker.bind(("127.0.0.1", 0))
            taken = blocker.getsockname()[1]
            self.closers.append(blocker)
            changes = mgr.sync({"a": ("127.0.0.1", taken, "127.0.0.1", be, "udp")})
            self.assertTrue(any("FAILED" in c for c in changes), changes)
            self.assertNotIn("a", mgr.stats(),
                             "a listener that failed to rebind is not live")
        finally:
            mgr.shutdown()


class TestDesecFailureHandling(unittest.TestCase):

    def provider(self, responder, min_ttl=60):
        p = providers.DesecProvider(token="t", domain="example.com")
        p._minimum_ttl = min_ttl
        p._min_ttl_at = time.monotonic()
        p.calls = []

        def _call(method, url, body=None):
            p.calls.append((method, url, body))
            return responder(method, url, body)

        p._call = _call
        return p

    def test_a_404_on_a_write_is_not_reported_as_success(self):
        p = providers.DesecProvider(token="t", domain="example.com")
        p._minimum_ttl, p._min_ttl_at = 60, time.monotonic()
        seen = []

        def raise404(req, timeout=None):
            raise urllib.error.HTTPError(
                req.full_url, 404, "Not Found", {}, io.BytesIO(b'{"detail":"gone"}'))

        with mock.patch("urllib.request.urlopen", raise404):
            self.assertIsNone(p._read("SRV", "_mc._tcp.example.com"))
            with self.assertRaises(FyrWireError):
                p._write([{"subname": "x", "type": "A", "ttl": 60,
                           "records": ["1.2.3.4"]}])
        del seen

    def test_a_multi_record_rrset_is_never_deleted(self):
        p = self.provider(lambda m, u, b: {
            "ttl": 60,
            "records": ["0 5 51471 node.example.com.",
                        "10 0 25565 backup.example.com."]})
        with self.assertRaises(FyrWireError) as cm:
            p.delete("SRV", "_mc._tcp.play.example.com", expect="node.example.com")
        self.assertIn("2 records", str(cm.exception))
        self.assertNotIn("DELETE", [m for m, _, _ in p.calls])

    def test_the_witness_checks_the_target_not_any_field(self):
        p = self.provider(lambda m, u, b: {
            "ttl": 60, "records": ["0 5 51471 somewhere-else.net."]})
        with self.assertRaises(FyrWireError):
            p.delete("SRV", "_mc._tcp.play.example.com", expect="0")
        self.assertNotIn("DELETE", [m for m, _, _ in p.calls])

    def test_https_body_is_exactly_right(self):
        p = self.provider(lambda m, u, b: None)
        p.ensure_https(HttpsRecordData(name="play.example.com",
                                   target="node.example.com", port=51471,
                                   priority=1, ttl=60))
        body = [b for m, _, b in p.calls if m == "PATCH"][0]
        self.assertEqual(body[0]["records"],
                         ["1 node.example.com. port=51471"])

    def test_https_reordered_and_quoted_params_are_not_a_change(self):
        url = "https://desec.io/api/v1/domains/example.com/rrsets/play/HTTPS/"
        p = self.provider(lambda m, u, b: (
            {"ttl": 60, "records": ['1 node.example.com. port="51471"']}
            if m == "GET" else None))
        self.assertIsNone(p.ensure_https(HttpsRecordData(
            name="play.example.com", target="node.example.com",
            port=51471, priority=1, ttl=60)))
        del url

    def test_an_ip_literal_target_is_refused(self):
        p = self.provider(lambda m, u, b: None)
        with self.assertRaises(FyrWireError) as cm:
            p.ensure_srv(SrvRecordData("_mc._tcp.example.com", "203.0.113.5",
                                   51471, 0, 5, 60))
        self.assertIn("hostname", str(cm.exception))

    def test_an_underscore_in_the_target_is_refused(self):
        p = self.provider(lambda m, u, b: None)
        with self.assertRaises(FyrWireError):
            p.ensure_srv(SrvRecordData("_mc._tcp.example.com", "node_1.example.com",
                                   51471, 0, 5, 60))

    def test_auth_scheme_is_token_not_bearer(self):
        captured = {}

        def fake(req, timeout=None):
            captured["auth"] = req.get_header("Authorization")
            raise urllib.error.HTTPError(req.full_url, 401, "no", {},
                                         io.BytesIO(b'{"detail":"x"}'))

        p = providers.DesecProvider(token="secret", domain="example.com")
        with mock.patch("urllib.request.urlopen", fake):
            with self.assertRaises(FyrWireError):
                p._call("GET", p._url("domains", "example.com"))
        self.assertEqual(captured["auth"], "Token secret")

    def test_429_becomes_rate_limited_and_carries_retry_after(self):
        def fake(req, timeout=None):
            raise urllib.error.HTTPError(
                req.full_url, 429, "slow down", {"Retry-After": "7"},
                io.BytesIO(b'{"detail":"throttled"}'))

        p = providers.DesecProvider(token="t", domain="example.com")
        with mock.patch("urllib.request.urlopen", fake):
            with self.assertRaises(providers.RateLimited) as cm:
                p._call("GET", p._url("domains", "example.com"))
        self.assertEqual(cm.exception.retry_after, 7.0)

    def test_transient_server_errors_are_retryable_not_fatal(self):
        for code, want_retryable in ((503, True), (500, True), (409, True),
                                     (403, False), (401, False), (400, False)):
            def fake(req, timeout=None, _c=code):
                raise urllib.error.HTTPError(req.full_url, _c, "x", {},
                                             io.BytesIO(b'{"detail":"y"}'))

            p = providers.DesecProvider(token="t", domain="example.com")
            with mock.patch("urllib.request.urlopen", fake):
                with self.assertRaises(FyrWireError) as cm:
                    p._call("GET", p._url("domains", "example.com"))
            got = isinstance(cm.exception, providers.RateLimited)
            self.assertEqual(got, want_retryable, f"HTTP {code}")

    def test_error_bodies_are_flattened_into_a_sentence(self):
        self.assertIn("Ensure this value",
                      providers._flatten_desec_error(
                          '{"ttl":["Ensure this value is >= 3600."]}'))
        self.assertIn("ttl:", providers._flatten_desec_error(
            '{"ttl":["bad"]}'))
        self.assertEqual("plain", providers._flatten_desec_error("plain"))

    def test_the_ttl_floor_is_re_read_rather_than_frozen_for_the_process(self):
        calls = []
        p = self.provider(lambda m, u, b: (calls.append(u) or
                                           {"minimum_ttl": 3600}))
        p._minimum_ttl, p._min_ttl_at = 3600, time.monotonic()
        p.minimum_ttl()
        self.assertEqual(len(calls), 0, "fresh, so no refetch")
        p._min_ttl_at = time.monotonic() - 99999
        p.minimum_ttl()
        self.assertEqual(len(calls), 1, "stale, so refetched")

    def test_a_missing_minimum_ttl_is_refused_rather_than_guessed(self):
        p = self.provider(lambda m, u, b: {"name": "example.com"})
        p._minimum_ttl = None
        with self.assertRaises(FyrWireError) as cm:
            p.minimum_ttl()
        self.assertIn("Refusing to guess", str(cm.exception))

    def test_the_cloudflare_witness_ignores_case(self):
        cf = FakeCloudflare()
        cf.records = [{"id": "1", "type": "SRV", "name": "s.example.com",
                       "comment": CloudflareProvider.TAG,
                       "data": {"target": "Node.Example.COM"}}]
        self.assertIsNotNone(
            cf.delete("SRV", "s.example.com", expect="node.example.com"))

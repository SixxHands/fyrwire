from __future__ import annotations

import os
import socket
import tempfile
import threading
import unittest

from fyrwire.agent import Agent, AgentState
from fyrwire import providers
from fyrwire.models import (FyrWireError, NodePlanData, EndpointData, Service,
                            SrvRecordData)
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

    def __init__(self, **kw):
        existing = kw.pop("existing", {})
        super().__init__(token="t", domain="example.com", **kw)
        self.calls = []
        self.existing = existing
        self._minimum_ttl = 60

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
        p = FakeDesec()
        p._minimum_ttl = 3600
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

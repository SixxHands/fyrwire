from __future__ import annotations

import os
import socket
import tempfile
import threading
import unittest

from fyrwire.agent import Agent, AgentState
from fyrwire.models import FyrWireError, NodePlanData, EndpointData, Service
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

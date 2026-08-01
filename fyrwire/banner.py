from __future__ import annotations

import os
import sys

HEIGHT = 6

_GLYPHS: dict[str, tuple[str, ...]] = {
    "F": ("███████╗", "██╔════╝", "█████╗  ", "██╔══╝  ", "██║     ", "╚═╝     "),
    "Y": ("██╗   ██╗", "╚██╗ ██╔╝", " ╚████╔╝ ", "  ╚██╔╝  ", "   ██║   ", "   ╚═╝   "),
    "R": ("██████╗ ", "██╔══██╗", "██████╔╝", "██╔══██╗", "██║  ██║", "╚═╝  ╚═╝"),
    "W": ("██╗    ██╗", "██║    ██║", "██║ █╗ ██║", "██║███╗██║", "╚███╔███╔╝", " ╚══╝╚══╝ "),
    "I": ("██╗", "██║", "██║", "██║", "██║", "╚═╝"),
    "E": ("███████╗", "██╔════╝", "█████╗  ", "██╔══╝  ", "███████╗", "╚══════╝"),
    " ": ("  ", "  ", "  ", "  ", "  ", "  "),
}

_RAMP = ((255, 138, 60), (255, 106, 58), (243, 78, 62), (214, 58, 74),
         (176, 46, 88), (132, 40, 96))


def _colour_ok(stream) -> bool:
    if os.environ.get("NO_COLOR") is not None:
        return False
    if os.environ.get("FYRWIRE_NO_COLOR"):
        return False
    return hasattr(stream, "isatty") and stream.isatty()


def render(text: str = "FyrWire", colour: bool | None = None, stream=None) -> str:
    stream = stream or sys.stdout
    if colour is None:
        colour = _colour_ok(stream)

    rows = [""] * HEIGHT
    for ch in text.upper():
        g = _GLYPHS.get(ch)
        if g is None:
            continue
        for i in range(HEIGHT):
            rows[i] += g[i]

    if not colour:
        return "\n".join(rows)

    out = []
    for i, row in enumerate(rows):
        r, g, b = _RAMP[i % len(_RAMP)]
        out.append(f"\033[38;2;{r};{g};{b}m{row}\033[0m")
    return "\n".join(out)


TAGLINE = "Keep your connections... connected!"


def width_of(text: str = "FyrWire") -> int:
    return sum(len(_GLYPHS[c][0]) for c in text.upper() if c in _GLYPHS)


def tagline(text: str = TAGLINE, under: str = "FyrWire") -> str:
    pad = max(0, (width_of(under) - len(text)) // 2)
    return " " * pad + text


if __name__ == "__main__":
    print(render())
    print(tagline())

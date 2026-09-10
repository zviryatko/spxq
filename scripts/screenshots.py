#!/usr/bin/env python3
"""Capture the real spxq TUI, then frame its terminal cells for the README.

All report data is synthetic. Requires Pillow, pyte, a POSIX PTY, and a font.
"""
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import sqlite3
import struct
import subprocess
import tempfile
import termios
import time

import pyte
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "docs/assets"
COLS, ROWS = 110, 24
CELL_W, CELL_H = 12, 24
MARGIN, PAD, BAR = 56, 28, 64


def font_path():
    override = os.getenv("SPXQ_SCREENSHOT_FONT")
    if override:
        return override
    candidates = [
        "/usr/share/fonts/dejavu-sans-mono-fonts/DejaVuSansMono.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
        "/usr/share/fonts/adobe-source-code-pro-fonts/SourceCodePro-Medium.otf",
        "/System/Library/Fonts/Menlo.ttc",
    ]
    for path in candidates:
        if Path(path).exists():
            return path
    if shutil.which("fc-match"):
        return subprocess.check_output(["fc-match", "monospace", "-f", "%{file}"], text=True)
    raise RuntimeError("Set SPXQ_SCREENSHOT_FONT to a monospace font file")


FONT = ImageFont.truetype(font_path(), 19)
TITLE_FONT = ImageFont.truetype(font_path(), 17)
ANSI = {
    "black": "000000", "red": "cd0000", "green": "00cd00", "brown": "cdcd00",
    "blue": "0000ee", "magenta": "cd00cd", "cyan": "00cdcd", "white": "e5e5e5",
    "brightblack": "7f7f7f", "brightred": "ff0000", "brightgreen": "00ff00",
    "brightbrown": "ffff00", "brightblue": "5c5cff", "brightmagenta": "ff00ff",
    "brightcyan": "00ffff", "brightwhite": "ffffff",
}


def color(value, default):
    return "#" + ANSI.get(value, default if value == "default" else value)


def capture(args, keys=()):
    screen = pyte.Screen(COLS, ROWS)
    stream = pyte.ByteStream(screen)
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(ROOT)
        os.environ.update(TERM="xterm-256color", COLORTERM="truecolor", TZ="UTC")
        os.execv(str(ROOT / "spxq"), [str(ROOT / "spxq"), *args])
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    def drain(seconds):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if select.select([fd], [], [], 0.04)[0]:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                stream.feed(data)

    try:
        drain(2.5)  # Allow terminal capability negotiation to time out.
        for key in keys:
            os.write(fd, key)
            drain(0.15)
        # Render before q restores the terminal's original screen.
        result = screen
        os.write(fd, b"q")
        for _ in range(30):
            done, status = os.waitpid(pid, os.WNOHANG)
            if done:
                if status != 0:
                    raise RuntimeError(f"spxq exited with status {status}")
                pid = None
                return result
            time.sleep(0.03)
        raise RuntimeError("spxq did not exit")
    finally:
        os.close(fd)
        if pid:
            os.kill(pid, signal.SIGTERM)
            os.waitpid(pid, 0)


def render(screen, name, title, light=False):
    width = COLS * CELL_W + PAD * 2
    height = ROWS * CELL_H + PAD * 2 + BAR
    canvas = Image.new("RGB", (width + MARGIN * 2, height + MARGIN * 2))
    draw = ImageDraw.Draw(canvas)
    # Quiet teal gradient around the actual terminal, with a soft window shadow.
    for y in range(canvas.height):
        t = y / canvas.height
        rgb = tuple(int(a + (b - a) * t) for a, b in zip((32, 67, 73), (13, 27, 37)))
        draw.line((0, y, canvas.width, y), fill=rgb)
    shadow = Image.new("RGBA", canvas.size)
    ImageDraw.Draw(shadow).rounded_rectangle(
        (MARGIN, MARGIN + 14, MARGIN + width, MARGIN + height + 14),
        radius=22, fill=(0, 0, 0, 130),
    )
    canvas = Image.alpha_composite(canvas.convert("RGBA"), shadow.filter(ImageFilter.GaussianBlur(18)))
    draw = ImageDraw.Draw(canvas)
    bg = "#fafafa" if light else "#000000"
    chrome = "#edf0f3" if light else "#15232e"
    muted = "#526477" if light else "#9fbbca"
    draw.rounded_rectangle((MARGIN, MARGIN, MARGIN + width, MARGIN + height), radius=20, fill=bg)
    draw.rounded_rectangle((MARGIN, MARGIN, MARGIN + width, MARGIN + BAR + 20), radius=20, fill=chrome)
    draw.rectangle((MARGIN, MARGIN + BAR, MARGIN + width, MARGIN + BAR + 20), fill=bg)
    for i, fill in enumerate(("#f58b79", "#f0c779", "#74d9b7")):
        x, y = MARGIN + 29 + i * 25, MARGIN + BAR // 2
        draw.ellipse((x - 6, y - 6, x + 6, y + 6), fill=fill)
    draw.text((MARGIN + width // 2, MARGIN + BAR // 2), title, font=TITLE_FONT, anchor="mm", fill=muted)
    x0, y0 = MARGIN + PAD, MARGIN + BAR + PAD
    for y in range(ROWS):
        for x in range(COLS):
            ch = screen.buffer[y][x]
            fg = color(ch.fg, "1f2937" if light else "ffffff")
            cell_bg = color(ch.bg, "fafafa" if light else "000000")
            if ch.reverse:
                fg, cell_bg = cell_bg, fg
            px, py = x0 + x * CELL_W, y0 + y * CELL_H
            draw.rectangle((px, py, px + CELL_W - 1, py + CELL_H - 1), fill=cell_bg)
            if ch.data.strip():
                draw.text((px, py), ch.data, font=FONT, fill=fg)
    canvas.convert("RGB").save(OUT / name, optimize=True)


with tempfile.TemporaryDirectory(prefix="spxq-screenshots-") as tmp:
    tmp = Path(tmp)
    database = tmp / "demo.db"
    subprocess.run([str(ROOT / "spxq"), "import", str(ROOT / "examples/demo.json"), "-o", str(database)], check=True)
    con = sqlite3.connect(database)
    catalog = con.execute("SELECT n.id FROM nodes n JOIN functions f ON f.id=n.function_id WHERE f.name=?", (r"App\Catalog\CatalogService::load",)).fetchone()[0]
    con.close()
    args = ["view", str(database), "--root", str(catalog), "--limit", "5"]
    render(capture(args, [b"\x1b[B", b"\x1b[C"]), "terminal-dark.png", "spxq / catalog request / wall time")
    render(capture(args + ["--theme", "light"], [b"m"]), "terminal-light.png", "spxq / catalog request / memory", light=True)
    reports = tmp / "reports"
    reports.mkdir()
    base = json.loads((ROOT / "examples/demo.json").read_text())
    for i, (host, route, task) in enumerate([
        ("shop-web-01", "/products/field-jacket", "product.show"),
        ("shop-web-02", "/search?q=camping", "search.index"),
        ("shop-web-01", "/cart", "cart.show"),
        ("shop-worker-01", "bin/console catalog:reindex", "catalog.reindex"),
        ("shop-web-03", "/checkout", "checkout.create"),
        ("shop-worker-02", "bin/console cache:warmup", "cache.warmup"),
        ("shop-web-02", "/collections/outdoors", "collection.show"),
        ("shop-web-01", "/health", "health.check"),
    ]):
        m = dict(base, key=f"demo-{i}", host_name=host, exec_ts=base["exec_ts"] - i * 93,
                 custom_metadata_str=json.dumps({"route": task}, separators=(",", ":")))
        if route.startswith("bin/"):
            m.update(cli=1, cli_command_line=route)
        else:
            m.update(http_request_uri=route)
        (reports / f"demo-{i}.json").write_text(json.dumps(m))
        (reports / f"demo-{i}.txt.gz").symlink_to(ROOT / "examples/demo.txt.gz")
    render(capture(["view", "--dir", str(reports)]), "terminal-reports.png", "spxq / report library / synthetic demo")
print(f"Saved 3 real TUI captures to {OUT}")

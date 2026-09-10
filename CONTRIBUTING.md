# Contributing

Bug reports and small, focused pull requests are welcome. For parser issues,
include a minimal synthetic trace, the metric list, and the exact error. Avoid
posting production profiles, request URLs, credentials, or private function names.

## Development

Go 1.23+ and a C compiler are required. SQLite is bundled by the CGO driver.

```sh
go build -o spxq .
go test ./...
go vet ./...
gofmt -w *.go
./spxq import examples/demo.json -o /tmp/spxq-demo.db
./spxq view /tmp/spxq-demo.db
```

Keep parser and aggregation changes covered by small deterministic tests. Preserve
signed and fractional metric values and recursion semantics. Keep report imports
streaming and avoid adding raw event storage or network activity to the viewer.

## Documentation images

The committed screenshots come from the real terminal UI, running against the
synthetic fixture in `examples/`. Regenerate them after changing the UI:

```sh
python3 scripts/generate-demo.py
python3 -m venv .venv
.venv/bin/pip install -r scripts/screenshots-requirements.txt
go build -o spxq .
.venv/bin/python scripts/screenshots.py
```

The screenshot script requires a POSIX PTY and a monospace TrueType/OpenType font.
Set `SPXQ_SCREENSHOT_FONT` to a font file if automatic discovery fails.

## Releases

The `Build & release` workflow tests and builds native binaries on Linux amd64,
Linux arm64, macOS amd64, and macOS arm64. Linux artifacts use musl and static
linking; macOS artifacts use the system libraries. A `v*` tag publishes all four
archives plus `checksums.txt` only after every build passes.

```sh
git tag v0.2.0
git push origin v0.2.0
```

The first release is `v0.1.0`. GitHub Actions must be enabled with permission to
write repository contents for the release job. Pull requests receive read-only
permissions. Releases include this project's license and third-party notices.

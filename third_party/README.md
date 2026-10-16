# Dependency notices

`licenses/` contains upstream notices for the dependencies linked into the Linux
and macOS binaries, generated with `go-licenses v2.0.1`. `modules.txt` records the
module graph; it also includes indirect/test-only modules that may not be linked.
These notices do not change the license of the upstream projects.

Eclipse Paho is dual licensed under EPL-2.0 and EDL-1.0. This distribution uses
the EDL-1.0 option and includes the original LICENSE and `edl-v10` texts. The
scanner identifies the first license only; upstream sample source and test keys
are deliberately excluded from the notices bundle. Notices are copied from upstream module sources.

Regenerate after dependency changes (Go, Python 3 and go-licenses required):

```sh
GOBIN="$PWD/.local/tools" go install github.com/google/go-licenses/v2@v2.0.1
GO_LICENSES="$PWD/.local/tools/go-licenses" bash scripts/licenses.sh
```

Review the generated diff, including scanner warnings about assembly/native code.
The Go runtime license is copied from the compiler used for each binary/container
build. The chart includes the project's own license; it ships no Go binaries.

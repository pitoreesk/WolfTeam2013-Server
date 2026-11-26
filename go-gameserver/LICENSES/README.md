# License materials

The project-authored source is licensed under the repository's root
[`LICENSE.md`](../../LICENSE.md). Compiled server distributions also contain
or depend on the following components:

| Component | Version | License |
|---|---:|---|
| Go standard library | Go 1.25 | `go-BSD-3-Clause.txt` |
| `github.com/jackc/pgx/v5` | `v5.9.2` | `pgx-MIT.txt` |
| `github.com/jackc/pgpassfile` | `v1.0.0` | `pgpassfile-MIT.txt` |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | `pgservicefile-MIT.txt` |
| `github.com/jackc/puddle/v2` | `v2.2.2` | `puddle-MIT.txt` |
| `golang.org/x/crypto` | `v0.41.0` | `go-BSD-3-Clause.txt` |
| `golang.org/x/sync` | `v0.17.0` | `go-BSD-3-Clause.txt` |
| `golang.org/x/text` | `v0.29.0` | `go-BSD-3-Clause.txt` |

`predecessor-MIT.txt` preserves the license notice for the MIT-licensed
implementation consulted during the project's development history.

These files must accompany distributed server binaries and container images.
Their inclusion does not change the license of project-authored source.

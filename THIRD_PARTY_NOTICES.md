# Third-party notices

laug's own files are under the Apache License, Version 2.0 (`LICENSE`). The files the next two sections
list are not: each keeps its upstream MIT License, whose text follows its table.

## Vendored from temporalio/samples-server

These files come from https://github.com/temporalio/samples-server at commit f811a03 (2026-09-16),
vendored on 2026-10-07. The first lines of each file name the source. They are used under the MIT
License below, copied from the upstream `LICENSE` at that commit.

| File in laug | Upstream path | Changed |
| --- | --- | --- |
| `cells/compose/cell.yml` | `compose/docker-compose-multirole.yaml` | rewritten for a pool; a source and license header added; the header and the file's comments say what changed |
| `cells/compose/nginx.conf` | `compose/deployment/nginx/nginx.conf` | a source and license header added at the top; otherwise unchanged |
| `cells/compose/scripts/setup-postgres.sh` | `compose/scripts/setup-postgres.sh` | a source and license header added; the two snippet markers removed |
| `cells/compose/scripts/create-namespace.sh` | `compose/scripts/create-namespace.sh` | a source and license header added; one typo fixed (`MAX_ATTdMPTS`) |
| `cells/compose/dynamicconfig/laug.yaml` | `compose/dynamicconfig/development-sql.yaml` | the first two keys only, with laug's comments; the rest of the file is laug's |
| `load/testdata/laug.yaml` | `compose/dynamicconfig/development-sql.yaml` | a test copy of `cells/compose/dynamicconfig/laug.yaml` with the same two keys; the rest of the file is laug's |

```
The MIT License

Copyright (c) 2020 Temporal Technologies Inc.  All rights reserved.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

## Vendored from temporalio/temporal

This file comes from https://github.com/temporalio/temporal at tag v1.31.0, read from the Go
module go.temporal.io/server@v1.31.0 and vendored on 2026-10-14. It is the config template the
temporalio/server:1.31.0 image embeds; `laug cell fi-template-check` confirms the image's binary holds
it byte for byte. Its header names the source. It is used under the MIT License below, copied from
the module's `LICENSE` at that tag.

| File in laug | Upstream path | Changed |
| --- | --- | --- |
| `cells/compose/config/laug-server.yaml` | `common/config/config_template_embedded.yaml` | a source and license header after the first line, and one block in the postgres12 branch that renders `faultInjection` from `LAUG_FI_*`; both fenced by `laug-begin` and `laug-end`, and `laug cell fi-template-check` diffs the rest against upstream |

```
The MIT License

Copyright (c) 2025 Temporal Technologies Inc.  All rights reserved.

Copyright (c) 2020 Uber Technologies, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

## Fetched at run time, not shipped

laug does not redistribute these. `laug upstream` clones Omes into `.upstream/`, `laug cell up` pulls the
images, and the go command downloads the Go modules. `bin/obs up` fetches four release tarballs, Prometheus,
Grafana, the OpenTelemetry Collector and Jaeger, into the stack's state directory outside the checkout, and unpacks
each only when its size and sha256 match its line in `_obs/versions.txt`. `make fc-image` fetches Firecracker and its
jailer, the guest kernel and the guest's base rootfs, an Ubuntu 24.04 squashfs, into `FC_DIR` (`/opt/fc` unless set).
It then installs the guest's packages with apt and pulls the cell's images (`cells/rootfs/build-image.sh`).
[SECURITY.md](SECURITY.md#outbound-connections) says which of these are checked against a pinned sha256, and how.
Each comes with its own license.
"Image label" means the `org.opencontainers.image.licenses` label read with `docker image inspect` on the dev
host, a Linux VM with 24 vCPU and 47 GiB running Docker; a label is not the license text.

| Component | License | Source of that answer |
| --- | --- | --- |
| temporalio/omes, commit f0de206 | MIT, Copyright (c) 2023 Temporal Technologies Inc. | its `LICENSE`, read in `.upstream/omes` |
| go.temporal.io/sdk v1.49.0 (`go.mod`) | MIT, Copyright (c) 2020 Temporal Technologies Inc. | its `LICENSE`, read in the Go module cache |
| go.temporal.io/server v1.31.0 and v1.32.1, downloaded by `laug cell fi-template-check` to compare the template, not built | MIT, Copyright (c) 2025 Temporal Technologies Inc. and (c) 2020 Uber Technologies, Inc. | its `LICENSE`, read in the Go module cache; the two files are byte for byte the same |
| go.temporal.io/sdk/contrib/opentelemetry v0.8.1 (`go.mod`), the traces of `laug gate run -workflow` | MIT, Copyright (c) 2020 Temporal Technologies Inc. and (c) 2020 Uber Technologies, Inc. | its `LICENSE`, read in the Go module cache |
| go.opentelemetry.io/otel, otel/sdk, otel/trace, otel/exporters/otlp/otlptrace and otlptrace/otlptracehttp v1.44.0 (`go.mod`) | Apache-2.0, and BSD-3-Clause for the part from Go (Copyright 2009 The Go Authors), one LICENSE file | each one's `LICENSE`, read in the Go module cache: the same file in all five, sha256 1ae07514be1d7bb33f0698f8d91fb51b8b9fe1463157ec1c72081a49b9bc6f40, the Apache-2.0 text to line 201 and the BSD-3-Clause text from line 205 |
| the other 51 of the 58 modules whose files `go.sum` hashes (its lines without `/go.mod`) | each module's own: 15 under MIT, 15 under BSD-3-Clause, 14 under Apache-2.0, 2 under the otel LICENSE of the row above (otel/metric, otel/sdk/metric), 2 under BSD-2-Clause, 1 under ISC and 1 under MIT and Apache-2.0 (gopkg.in/yaml.v3); github.com/nexus-rpc/nexus-proto-annotations v0.1.0, compiled into `bin/laug` through go.temporal.io/api, ships no license file | each one's license file, read in the Go module cache |
| temporalio/server and temporalio/admin-tools 1.31.0 (the default) and 1.32.1 (`SERVER_TAG=1.32.1`), temporalio/ui 2.49.1 | MIT | image label |
| ghcr.io/shopify/toxiproxy:2.12.0 | MIT | image label |
| elasticsearch:7.17.27, pulled only with `VISIBILITY=es` | Elastic License 2.0 | image label, and the image's `/usr/share/elasticsearch/LICENSE.txt` |
| postgres:16 | PostgreSQL License | no label; the image's `/usr/share/doc/postgresql-16/copyright` |
| nginx:1.22.1 | BSD-2-Clause | no label; the image's `/usr/share/doc/nginx/copyright` |
| prom/prometheus:v2.53.0 | Apache-2.0 | no label; the image's `/LICENSE` |
| grafana/grafana:11.1.0 | AGPL-3.0 | no label; the image's `/usr/share/grafana/LICENSE` |
| prometheus 2.53.0, linux-amd64 release tarball, for the observability stack (`_obs/versions.txt`) | Apache-2.0 | its `LICENSE`, read in the unpacked tree, the same file as its image's `/LICENSE` |
| grafana 11.1.0, linux-amd64 release tarball, for the observability stack | AGPL-3.0 | its `LICENSE`, read on GitHub 2026-10-17 and in the unpacked tree, the same file as its image's `/usr/share/grafana/LICENSE` |
| OpenTelemetry Collector, core distribution (`otelcol`) 0.162.0, linux-amd64 release tarball, for the observability stack | Apache-2.0 | open-telemetry/opentelemetry-collector's `LICENSE`, read on GitHub 2026-10-17 and in its Go module go.opentelemetry.io/collector v0.162.0, which the binary names; the tarball holds a README and the binary, no license file |
| Jaeger 2.21.0, linux-amd64 release tarball, for the observability stack | Apache-2.0 | jaegertracing/jaeger's `LICENSE`, read on GitHub 2026-10-17 and in its Go module github.com/jaegertracing/jaeger at the version the `jaeger` binary names; the tarball holds the binaries `jaeger` and `example-hotrod`, no license file |
| Firecracker v1.17.0 and its jailer, x86_64 release tarball, for the microVM cells (`cells/rootfs/fc-lib.sh`) | Apache-2.0, with parts from Chromium OS under BSD-3-Clause; its `NOTICE` says the bundle includes libseccomp, under LGPL-2.1, used at build time to make the cBPF bytecode shipped with Firecracker | the tarball's `LICENSE`, `NOTICE` and `THIRD-PARTY` |
| the guest kernel, Linux 6.18.51 as Firecracker's CI built it, for the microVM cells | GPL-2.0 WITH Linux-syscall-note | Linux's `COPYING`, read at the stable tree's tag v6.18.51 on kernel.org; `make fc-image` fetches the kernel alone, with no license file |
| the guest's base rootfs, Ubuntu 24.04 from the same CI build, and the packages `make fc-image` adds to it with apt, unpinned: docker.io, docker-compose-v2, iptables, jq, ca-certificates and curl, with what they depend on | each package's own | not checked one by one; the CI rootfs holds no `/usr/share/doc` and an empty `/var/lib/dpkg`, and its `/etc/dpkg/dpkg.cfg.d/excludes` drops `/usr/share/doc/*` but keeps `/usr/share/doc/*/copyright` |
| yq v4.54.1 (mikefarah/yq), the build host's own binary, which `make fc-image` copies into the guest for `cells/shards-ready.sh` (`cells/rootfs/build-image.sh`:126) | MIT, Copyright (c) 2017 Mike Farah | its `LICENSE`, read in the Go module cache at v4.54.1 |

Elasticsearch 7.17 and Grafana 11 are not under a permissive license. Neither is the guest kernel, under GPL-2.0, nor
the libseccomp that Firecracker's release bundle includes, under LGPL-2.1. laug only runs their unmodified images,
Firecracker's unmodified release and Grafana's unmodified release tarball (in the observability stack), and boots the
kernel as Firecracker's CI built it. The cell image `make fc-image` writes holds the rootfs's packages and the cell's
images, Grafana's among them. Read their terms before you redistribute or host any of them, that image included.

## temporalio/dashboards, none ported yet

No file in laug comes from https://github.com/temporalio/dashboards yet. The observability stack's
`laug · cell` board (`_obs/stack/cell.json`) ports none of its panels: its queries are written
from the server's metric names, and its description says so. A commit that ports a panel adds a row
here for each file, as in the sections above, and the repository's `LICENSE` text at the commit it
ports from. The board's description then credits the repository.

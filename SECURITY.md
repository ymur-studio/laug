# Security

laug is a lab for testing Temporal server releases on a machine you control. Nothing in it has auth: not the pool's
API, not a cell, not the Temporal CLI dev server the orchestrator runs, not the observability stack. So by default
every listener laug opens binds to 127.0.0.1 or to an address only this host reaches. The three SDK metrics listeners
below are the exception. With Docker Engine 28 or later, only this host reaches a compose cell's ports on 127.0.0.1. An
older engine lets other machines on the same network reach them.<!-- source: Docker Engine 28.0.0 release notes, Port
publishing in bridge networks: neighbor hosts could connect to ports mapped on a loopback address, moby/moby#49325 -->
Each default here is read from the file in the table's last column.

## Listeners

| Listener | Default bind | Wider with | File |
| --- | --- | --- | --- |
| `laug pool serve`: the pool's API, which creates and destroys cells on request | 127.0.0.1:7700 | nothing: `-listen` refuses a host that is not loopback | `internal/pool/pool.go`, `cmd/laug/pool.go` |
| A compose cell: frontend 7233, UI 8080, Prometheus 9090, Grafana 3000, Toxiproxy API 8474; a pool cell adds 10 times its block to each, blocks 0 to 16, so its frontend stays below 7400 | 127.0.0.1, as `BIND_ADDR` | `-bind all` on `laug cell up` or `laug pool serve`: 0.0.0.0, every interface; any other value is refused | `cells/compose/cell.yml`, `cmd/laug/cell.go`, `cmd/laug/pool.go` |
| A Firecracker cell: the same five ports, on 0.0.0.0 inside the guest | `10.254.<block>.2`, the far end of a veth pair in the cell's own network namespace | nothing: `-bind` is refused for microVM cells | `cells/backend-firecracker.sh`, `cmd/laug/pool.go` |
| `laug orchestrator server`, the orchestrator's dev server from the Temporal CLI: frontend 7400, HTTP API 7401, metrics 7402, UI 8400 | 127.0.0.1 | nothing: `-orchestrator` refuses another host | `cmd/laug/orchestrator.go` |
| `laug health loop`: the HealthCheck worker's SDK metrics | :9091, every interface | | `internal/health/check.go` |
| `laug load`, `laug fault`, `laug raun` and `laug candidate`: the Omes worker's and client's SDK metrics | 0.0.0.0:9092 and 0.0.0.0:9093 | | `load/run-omes.sh`, `faults/lib.sh`, `faults/candidate.sh` |
| `laug gate run`: the gate's own pool, then each item's Omes metrics | 127.0.0.1:7700; 127.0.0.1:19100 to 19115, a pair an item, 8 items at most | nothing: `GATE_POOL_LISTEN` must be `127.0.0.1:<port>` | `gate/run.sh`, `gate/lib.sh` |
| `laug gate control`: a gate on fakes, with its own pool | 127.0.0.1, a free port | | `cmd/laug/gatecontrol.go` |
| `bin/obs up`, the stack: cellstat 7710, follow 7720, Prometheus 7730, Grafana 7740, the Collector 7751 to 7753, Jaeger 7760 to 7763 | 127.0.0.1 | nothing: `make obs-test` refuses a config address that is not 127.0.0.1, a port with no host or an address from the environment | `_obs/obs.sh`, `_obs/` |
| `bin/obs view`: a store on 7730 and Grafana on 7740 | 127.0.0.1 | | `_obs/obs.sh` |
| `bin/obs usage record -listen` and `bin/obs follow -listen`: one scrape target each | `127.0.0.1:<port>` | nothing: another host is refused | `_obs/cellstat/main.go`, `_obs/follow/main.go` |
| `make obs-test`: a canned exposition on 7769, a Prometheus on 7768, the store under test on 7730 | 127.0.0.1 | | `_obs/obs.sh`, `_obs/follow/fakecell_test.go` |
| `make test`: the harness's fake pools | 127.0.0.1: 7700, or a free port from 17790 to 17989 | | `load/testdata/harness-test.sh` |

The dev server also opens 3 ports of its own on 127.0.0.1, in the host's ephemeral range. On the dev host, a Linux VM
with 24 vCPU and 47 GiB running Docker, no flag pinned them and they changed between two starts
(`docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt`, section 2). Any local user can start a gate on
the dev server. The orchestrator's worker then runs `go run ./cmd/omes` in the directory the gate's input names, with
the plan it names (`internal/orchestrator/input.go`, `load/run-omes.sh`). So run the dev server only on a
single-user host. `laug orchestrator worker`, `laug health check`, `laug health record` and `laug health timer` open no
listener. `make docs`, the renderer, writes files, and nothing serves them.

## What each one can do

- The pool's API leases, resets and releases cells. It starts and removes compose projects or microVMs. Root starts
  each microVM, through Firecracker's jailer unless `FC_JAIL=0`. Its POST /vmfault lets any local user throttle a
  leased microVM cell's drive, just as any local user can already release a cell. The Firecracker backend passes a
  drive's rate limiter to the VMM and refuses any other field, path_on_host among them. The compose backend answers
  POST /vmfault with an error. `laug pool down -remove-cells` is host-wide, not scoped to this checkout: it stops
  every gate's pool this user may signal and runs `docker compose down -v` on every `laug-<8 hex>` project, whoever
  started it (`cells/pool-down.sh:23`, `:25-29`).
- The pool's API answers 415 to a request with a body whose Content-Type is not `application/json`, and 403 to a
  request whose Host header is not a loopback name (`newMux` in `internal/pool/pool.go`). Without the first, a page
  open in a browser on this host could POST a lease as text/plain. Without the second, a page that rebinds its DNS
  name to 127.0.0.1 could read the reply.<!-- source: the Fetch standard's CORS-safelisted request-header (a text/plain
  POST needs no preflight) and its forbidden request-header names (a page cannot set Host); no browser run -->
- The Toxiproxy API can slow or cut a cell's connection to its Postgres.
- A cell's Grafana lets anyone in as Admin, with its login form off (`cells/compose/cell.yml`). The stack's Grafana
  lets anyone in as Viewer, and its boards are provisioned read-only. Its admin password sits in a mode 600 file in the
  stack's state directory, outside the checkout (`_obs/grafana/grafana.ini`, `_obs/obs.sh`).
- Postgres (user and password `temporal`) and Toxiproxy's proxy to it on 15432 (`faults/toxiproxy.json`) publish no
  port. Only the cell's own network reaches them, and, for a compose cell, this host, at the container's address on
  the cell's bridge network.<!-- source: Docker docs, bridge networks -->
- The server roles' own ports publish nothing either, and the same two reach them: the cell's own network and, for a
  compose cell, this host. They are gRPC 7234 (history), 7235 (matching) and 7239 (the worker), HTTP 7243, and
  membership ports in 6933 to 6939 (the 1.31.0 image's template, `cells/compose/config/laug-server.yaml`). The two
  frontends add gRPC 7237 and 7236, and the roles serve metrics on 8000 to 8004 (`cells/compose/cell.yml`).
- The three SDK metrics listeners serve metrics only. They listen on every interface so that a cell's Prometheus
  reaches them at `host.docker.internal`. The cell maps that name to `host-gateway` (`cells/compose/cell.yml`), which
  Docker Engine on Linux resolves to the host's address on Docker's default bridge, not to loopback.<!-- source:
  Docker docs, `host-gateway` --> Binding them to that address alone is untested.

## Firecracker and root

`sudo bin/laug pool serve -backend firecracker` runs the whole laug binary as root, and with it the backend scripts from
your checkout. Run it only on a host where you already have root. `sudo bin/laug pool down -backend firecracker` stops
it. Nothing on this path has auth either: any local user who reaches the pool's API can lease, reset, release or
throttle a microVM cell.

A cell publishes nothing on a host interface. Its five ports answer at `10.254.<block>.2`, the far end of a veth pair
in the cell's own network namespace, so only this host reaches them. Deny rules in the iptables chains FORWARD and
DOCKER-USER drop anything forwarded to or from a cell's link, so containers and other machines cannot reach a cell.
From another machine, use `ssh -L`. A guest can start a connection to one place only: the host's SDK metrics ports
9091 to 9093, for its Prometheus. These rules cover pool cells only. `make fc-image`, `make fc-snapshot` and
`make fc-restore` boot one guest on tap0, and no deny rule covers it. `net_up` in `cells/rootfs/fc-lib.sh` turns on
IP forwarding, adds two FORWARD rules that accept tap0's traffic to the uplink and its replies, and a MASQUERADE rule
for the guest's subnet. Nothing turns forwarding off or removes the rules or tap0 when the target ends.

Faults reach a guest's containers over ssh as root, with a key the backend writes, mode 0600, for the user who ran
sudo. Every ssh to a guest checks the host key the image build recorded. The build makes new host keys and fails if
the new key is the base rootfs's. Every cell is restored from one image's snapshot, so the check refuses another
machine at a cell's address but cannot tell one cell from another.

By default (`FC_JAIL=1`) each VMM starts through Firecracker's jailer (`jail_argv` in `cells/rootfs/fc-lib.sh`), as
uid 20000 plus the cell's block and gid 20000. It has no effective, permitted or inheritable capabilities. It runs with
no_new_privs set and a seccomp filter on every thread. Its capability bounding set is left full. It is
chrooted, in network, mount and pid namespaces of its own, and in a cgroup that counts its CPU and memory and caps
neither. Root still runs the pool, the backend and the jailer, so the jail is a second layer. It limits what a guest
that escaped into its VMM could reach, not what a local user can do through the pool's API. Every cell shares gid
20000, which may read the snapshot's vm.snap and vm.mem. That differs from Firecracker's production host setup guide,
which asks that the files a VMM uses be its own user's and group's.<!-- source: prod-host-setup.md --> Nothing caps a
cell's vCPU threads against its neighbors'. With `FC_JAIL=0` each VMM runs as root in network and mount namespaces
of its own, with no chroot, no uid drop and no cgroup of its own. Firecracker's default seccomp filter stays, since
laug passes no `--no-seccomp`.<!-- source: Firecracker's prod-host-setup.md, which calls the default filters its
most restrictive; no run read an unjailed VMM's seccomp -->

In boot 9 (unjailed) and boot 10 (jailed), 11 of 11 isolation checks of the network and the disk held in each
(`cells/results/2026-10-29-boot9.txt`, P6c; `docs/tables/records.jsonl#cells/results/2026-10-29-boot10.txt`, J-P6c):
[RECORDS, section 12](docs/RECORDS.md#12-boot-9) says what boot 9's record holds and how to run its restores again,
and [CAPABILITIES, The pool](docs/CAPABILITIES.md#the-pool) lists the jail's checks and their records. No
check has run an hour into a lease, and none tests the jail from inside a VMM.

## Outbound connections

- `laug orchestrator worker -otlp` and `laug gate run -workflow -otlp` send traces to `127.0.0.1:<port>`, the stack's
  Collector on 7751. They refuse any other address, and without `-otlp` they send none
  (`internal/orchestrator/trace.go`).
- `_obs/jaeger.yaml` turns off the traces Jaeger's query server would send to 127.0.0.1:4317.
  `_obs/grafana/grafana.ini` points Grafana's grafana.com addresses at Grafana itself. `make obs-test` fails when
  either setting is undone.
- What laug fetches: `laug upstream` clones Omes at f0de206. `bin/obs up` fetches four releases and checks each one's
  size and sha256 (`_obs/versions.txt`). The first `laug cell up` pulls the cell's 8 images
  (`cells/results/2026-10-23-boot1.txt`, A1). `make build` fetches Go's modules, and with `GOTOOLCHAIN=auto` the Go
  version go.mod names when the installed one is older. `make fc-image` fetches Firecracker, its CI guest kernel and a
  base rootfs, each checked against a sha256 pinned in `cells/rootfs/fc-lib.sh`, then the guest's packages and the
  cell's images (`cells/rootfs/build-image.sh`). Firecracker's pin must equal the sha256 its release publishes, or
  the build stops. The kernel's and the rootfs's pins are trust on first use: each is the sum of one download, and
  the build compares them to nothing else (`cells/rootfs/build-image.sh`).

## A host firewall

A host firewall does not stand in for auth. Docker inserts its published-port rules ahead of ufw's. So under
`-bind all` a cell's ports are reachable from every network the host is on, whatever ufw says.<!-- source: Docker
docs, packet filtering and firewalls --> Before you use `-bind all`, put your own auth in front of the cell's ports.

## Reporting a vulnerability

Report a vulnerability through GitHub's private vulnerability reporting on the repository, not in
a public issue. I read these reports myself, and you will have a first answer within seven days. This page gives no
email address for reports.

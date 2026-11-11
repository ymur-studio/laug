#!/usr/bin/env bash
# build-image.sh: build the laug cell image for Firecracker. Linux x86_64 with /dev/kvm, as root (sudo make fc-image).
# Output: $FC_DIR/image/cell.ext4, a 16 GiB sparse ext4 with Docker, Compose, iptables (legacy), jq, yq, the cell's
# compose files and its 8 images loaded, stamped in /etc/laug-image. Firecracker, its CI guest kernel and the base
# rootfs are fetched once, at the versions pinned in fc-lib.sh.
set -euo pipefail
. "$(dirname "$0")/fc-lib.sh"
need_root_kvm
mkdir -p "$FC_DIR"; vm_lock; need_no_pool; cd "$FC_DIR"
t_all=$(now)

fetch() { # fetch <url> <file> <sha256>: only a complete download whose sum is the pinned one gets the final name
  curl -fsSL -o "$2.part" "$1"
  ( sum_check "$2.part" "$3" ) || { rm -f "$2.part"; exit 2; }
  mv "$2.part" "$2"
}

# 1. Firecracker and its jailer, the guest kernel, the base rootfs and a guest key (docs/getting-started.md at
# $FC_VERSION). Each download is checked against its sum in fc-lib.sh, and the tarball against the release's
# .sha256.txt too. The tarball holds both binaries; it is fetched again when either is missing or not $FC_VERSION, so
# an $FC_DIR from before the jailer gains it. `jailer --version` prints "Jailer v<version>" (v1.17.0
# src/jailer/src/main.rs), so the second field of each first line is the version.
have="$([[ -x firecracker ]] && ./firecracker --version | head -1 | awk '{print $2}' || true)"
have_jailer="$([[ -x jailer ]] && ./jailer --version | head -1 | awk '{print $2}' || true)"
if [[ "$have" != "$FC_VERSION" || "$have_jailer" != "$FC_VERSION" ]]; then
  rel="https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-x86_64.tgz"
  # The release's checksum file, beside the tarball: "<sha256>  <file name>" (the v1.17.0 release assets).
  pub="$(curl -fsSL "$rel.sha256.txt" | awk 'NR==1{print $1}')" || pub=""
  [[ "$pub" == "$FC_SHA256_TGZ" ]] \
    || { echo "the release's firecracker-$FC_VERSION-x86_64.tgz.sha256.txt says ${pub:-nothing}, fc-lib.sh pins $FC_SHA256_TGZ; refusing both" >&2; exit 2; }
  fetch "$rel" fc.tgz "$FC_SHA256_TGZ"
  tar -xzf fc.tgz && cp "release-$FC_VERSION-x86_64/firecracker-$FC_VERSION-x86_64" firecracker \
    && cp "release-$FC_VERSION-x86_64/jailer-$FC_VERSION-x86_64" jailer && rm -f fc.tgz
fi
base="$FC_DIR/${FC_CI##*/}-ubuntu-24.04.squashfs"
# On disk already: checked against the pin before use.
if [[ -f "$KERNEL_PATH" ]]; then
  ( sum_check "$KERNEL_PATH" "$FC_SHA256_KERNEL" ) \
    || { echo "remove $KERNEL_PATH and run make fc-image again, or set FC_SHA256_KERNEL for another kernel" >&2; exit 2; }
else fetch "$S3/$FC_CI/x86_64/$FC_KERNEL" "$KERNEL_PATH" "$FC_SHA256_KERNEL"; fi
if [[ -f "$base" ]]; then
  ( sum_check "$base" "$FC_SHA256_ROOTFS" ) \
    || { echo "remove $base and run make fc-image again, or set FC_SHA256_ROOTFS for another rootfs" >&2; exit 2; }
else fetch "$S3/$FC_CI/x86_64/ubuntu-24.04.squashfs" "$base" "$FC_SHA256_ROOTFS"; fi
[[ -f "$KEY" ]] || ssh-keygen -q -t ed25519 -f "$KEY" -N ""
fc_version="$(./firecracker --version | head -1 | awk '{print $2}')"
jailer_version="$(./jailer --version | head -1 | awk '{print $2}')"
echo "firecracker $fc_version, jailer $jailer_version, kernel $FC_KERNEL, base $FC_CI ubuntu-24.04"

# The guest's yq runs shards-ready.sh, so it must be mikefarah yq v4 as a real binary, not a snap launcher.
yq_host="$(readlink -f "$(command -v yq)")"
[[ "$(basename "$yq_host")" != snap ]] && "$yq_host" --version 2>&1 | grep -qE 'mikefarah.* v4\.' \
  || { echo "root's yq ($yq_host) is not mikefarah yq v4 as a binary; put one in /usr/local/bin" >&2; exit 2; }

# 2. The images the cell uses, read from cell.yml (Elasticsearch is behind a profile and not included). SERVER_TAG is
# unset so the image bakes cell.yml's default server tag, the one snapshot.sh's compose up in the guest resolves; the
# Makefile refuses fc-image with another tag. The compose call alone listed the 1.31.0 images on the dev host;
# boot 9's build on the lab host baked 8 images (cells/results/2026-10-29-boot9.txt:99, :108-109).
images="$(cd "$LAUG_ROOT/cells/compose" && env -u SERVER_TAG ENABLE_ES=false docker compose -f cell.yml config --images | sort -u)"
for i in $images; do docker image inspect "$i" >/dev/null 2>&1 || docker pull -q "$i"; done
echo "images: $(wc -w <<<"$images")"

# 3. The root filesystem. Never delete while anything is mounted under the build dir: a bind-mounted /dev would
# take the host's devices with it. Mounts are matched as a fixed-string prefix of findmnt's canonical paths, and
# rm stays on one filesystem whatever happens.
B="$FC_DIR/build"
under_b() { findmnt -rn -o TARGET | awk -v p="$B/" 'index($0, p) == 1'; }
unmount_all() { local m; for m in $(under_b | sort -r); do umount "$m"; done; }
unmount_all
if [[ -n "$(under_b)" ]]; then echo "mounts remain under $B; refusing to delete" >&2; exit 1; fi
rm -rf --one-file-system "$B"; mkdir -p "$B"; cd "$B"
unsquashfs -q -d root "$base" >/dev/null
# The CI rootfs is stripped: apt needs /tmp and its cache directories.
mkdir -p root/tmp root/var/tmp root/var/cache/apt/archives/partial root/var/lib/apt/lists/partial root/var/log/apt
chmod 1777 root/tmp root/var/tmp
cp /etc/resolv.conf root/etc/resolv.conf
trap unmount_all EXIT
for d in proc sys dev dev/pts; do mount --bind "/$d" "root/$d"; mount --make-rslave "root/$d"; done
# The chroot gets its own environment (env -i), only what apt needs, so no host variable reaches the guest. The lab
# batch exports TMPDIR as a directory under its output directory, which the guest root lacks. Run by the batch,
# apt-get install failed twice in 15 s, and "dpkg returned an error code (1)" was all it showed. Run by hand as root
# with a plain environment, the build passed (on the lab host). In an ubuntu:24.04 container on the dev host
# (not the CI rootfs), a TMPDIR naming a missing directory failed ca-certificates' post-installation script, its
# mktemp, with the same dpkg line; with TMPDIR unset, or under this env -i, the install passed. apt's output went to
# /dev/null then. Now it goes to $FC_DIR/chroot.log, kept after the build. A pass prints the recipe's summary line, a
# failure the log's last 40 lines.
# --norc: env -i drops SHLVL, and a bash with a socket on stdin then reads /etc/bash.bashrc as if rshd ran it; -u
# printed "PS1: unbound variable" from it (seen on the dev host, outside a chroot). --norc skips the file.
# The guest's sshd host keys: two builds on the lab host recorded one fingerprint (cells/results/2026-10-29-boot8.txt,
# I): the CI rootfs ships its own (RSA, ECDSA and ED25519 in the pinned squashfs's /etc/ssh, seen on the lab host; the
# build prints the base's fingerprint below). The recipe deletes them and ssh-keygen -A makes each default type again in
# /etc/ssh (seen on the dev host with -f and a prefix, OpenSSH 9.6p1, and in the image built on the lab host: RSA, ECDSA
# and ED25519, private keys 0600), so each build has keys of its own and the first boot below records them. On the lab
# host, boot 10's build made a key whose fingerprint differs from the base rootfs's
# (cells/results/2026-10-29-boot10.txt, I).
base_key="$(ssh-keygen -lf root/etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null | awk '{print $2}')" || base_key=""
log="$FC_DIR/chroot.log"
# shellcheck disable=SC2016  # the recipe expands inside the chroot, on purpose
env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/root DEBIAN_FRONTEND=noninteractive LANG=C \
  chroot root /bin/bash --norc -euo pipefail -c '
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends docker.io docker-compose-v2 iptables jq ca-certificates curl
  # The guest kernel has NFT_MASQ unset (its embedded config), so the image uses iptables-legacy (nft was not tried).
  update-alternatives --set iptables /usr/sbin/iptables-legacy
  update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
  systemctl enable docker || true
  rm -f /etc/ssh/ssh_host_*
  ssh-keygen -A
  apt-get clean
  echo "in the image: docker.io $(dpkg-query -W -f="\${Version}" docker.io), compose $(dpkg-query -W -f="\${Version}" docker-compose-v2), $(iptables --version)"' \
  >"$log" 2>&1 || { e=$?; echo "the image chroot failed, exit $e; the last 40 lines of $log:" >&2; tail -n 40 "$log" >&2; exit "$e"; }
grep -m1 '^in the image: ' "$log" || { echo "the image chroot passed but wrote no summary line; see $log" >&2; exit 1; }
# ssh-keygen -A exits 0 when it cannot write a key (seen on the dev host, OpenSSH 9.6p1), so the recipe's -e misses a
# failed key: the new key is checked here, and against the base rootfs's.
[[ -s root/etc/ssh/ssh_host_ed25519_key && -s root/etc/ssh/ssh_host_ed25519_key.pub ]] || { echo "the image chroot made no ed25519 host key; see $log" >&2; exit 1; }
new_key="$(ssh-keygen -lf root/etc/ssh/ssh_host_ed25519_key.pub | awk '{print $2}')"
[[ -n "$new_key" && "$new_key" != "$base_key" ]] || { echo "the guest's ed25519 host key is still the base rootfs's, $base_key; see $log" >&2; exit 1; }
echo "guest host key made: $new_key; the base rootfs's: ${base_key:-none}"
# The recipe replaces only /etc/ssh/ssh_host_*: a HostKey line naming another file would keep a key it did not make.
# Ubuntu's stock sshd_config has its HostKey lines commented out, and sshd_config.d is empty (the pinned squashfs,
# seen on the lab host).
bad_hk="$(grep -rhsiE '^[[:space:]]*HostKey([[:space:]]|=)' root/etc/ssh/sshd_config root/etc/ssh/sshd_config.d \
  | grep -viE '^[[:space:]]*HostKey([[:space:]]+|[[:space:]]*=[[:space:]]*)/etc/ssh/ssh_host_[a-z0-9]+_key[[:space:]]*$' || true)"
[[ -z "$bad_hk" ]] || { echo "the image's sshd config names a host key outside /etc/ssh/ssh_host_*: $bad_hk" >&2; exit 1; }
unmount_all; trap - EXIT
install -m 0755 "$yq_host" root/usr/local/bin/yq
# The guest kernel has no iptables raw table (CONFIG_IP_NF_RAW unset), which Docker 29 uses (seen: 29.1.3; 28 and
# later per Docker's release notes) for the DROP rules that block direct routing to container addresses.
# This dockerd build honors DOCKER_INSECURE_NO_IPTABLES_RAW (seen in the binary; moby v28.5.2's bridge driver,
# rawRulesDisabled) and skips those rules. The guest's only network is a private tap to the host, so the protection
# guards nothing here.
mkdir -p root/etc/systemd/system/docker.service.d
printf '[Service]\nEnvironment=DOCKER_INSECURE_NO_IPTABLES_RAW=1\n' > root/etc/systemd/system/docker.service.d/laug.conf
mkdir -p root/root/.ssh; cp "$KEY.pub" root/root/.ssh/authorized_keys; chmod 600 root/root/.ssh/authorized_keys
# cell.yml mounts a file and a directory from outside cells/ (../../faults/toxiproxy.json, ../../dashboards/
# provisioning). A missing bind-mount source becomes an empty directory, and toxiproxy then has no postgres proxy.
mkdir -p root/opt/laug/cells root/opt/laug/faults root/opt/laug/dashboards
cp -r "$LAUG_ROOT/cells/compose" root/opt/laug/cells/; cp "$LAUG_ROOT/cells/shards-ready.sh" root/opt/laug/cells/
cp "$LAUG_ROOT/faults/toxiproxy.json" root/opt/laug/faults/; cp -r "$LAUG_ROOT/dashboards/provisioning" root/opt/laug/dashboards/
# shellcheck disable=SC2086  # one argument per image on purpose
docker save $images -o root/opt/laug/images.tar
{ echo "laug cell image"; echo "built $(date -u +%FT%TZ)"; echo "firecracker $fc_version, base $FC_CI ubuntu-24.04, kernel $FC_KERNEL"
  echo "cell.yml sha256 $(sha256sum "$LAUG_ROOT/cells/compose/cell.yml" | cut -c1-16)"
  echo "fi.yml sha256 $(sha256sum "$LAUG_ROOT/cells/compose/fi.yml" | cut -c1-16)"
  echo "config/laug-server.yaml sha256 $(sha256sum "$LAUG_ROOT/cells/compose/config/laug-server.yaml" | cut -c1-16)"; echo "images:"; echo "$images"; } > root/etc/laug-image
# Built under a temp name and renamed only when the first boot below succeeds, so a failed build never leaves an
# image that fc-snapshot would use.
img="$FC_DIR/image/cell.ext4"; new="$img.new"
mkdir -p "$FC_DIR/image"; rm -f "$new"
truncate -s 16G "$new"; mkfs.ext4 -q -d root -F "$new"
cd "$FC_DIR"; rm -rf --one-file-system "$B"

# 4. First boot: load the images into the guest's Docker so they live in the image. Fails closed: the tar is
# removed only after every image is present, and a missing one fails the build. ssh records the guest's host key, the
# one this build made in step 3, on first contact (accept-new) in the UserKnownHostsFile given, here a file of its
# own (ssh_config(5), StrictHostKeyChecking); every later ssh to a guest checks that key (SSHO in fc-lib.sh).
rm -f "$KNOWN_HOSTS.new"; : > "$KNOWN_HOSTS.new"
SSHO=(-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$KNOWN_HOSTS.new" -o ConnectTimeout=2 -o LogLevel=ERROR -i "$KEY")
net_up; t0=$(now); vm_boot "$new" 4 4096
wait_ssh || { echo "no ssh to the guest; see $FC_DIR/vmm.log and $FC_DIR/fc.stdout" >&2; vm_kill; exit 1; }
echo "first boot: ssh after $(since "$t0") s"
imglist="$(tr '\n' ' ' <<<"$images")"   # one line: a newline inside the remote for-list is a syntax error
if ! gssh "set -e; for i in \$(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 0.5; done; docker info >/dev/null
  t=\$(date +%s); docker load -q -i /opt/laug/images.tar >/dev/null
  for i in $imglist; do docker image inspect \"\$i\" >/dev/null; done
  rm -f /opt/laug/images.tar
  echo \"docker load \$(( \$(date +%s) - t )) s, \$(docker images -q | wc -l) images, \$(docker info --format '{{.Driver}}, cgroup v{{.CgroupVersion}}')\"; sync"; then
  echo "loading images in the guest failed; the image is not usable" >&2; vm_kill; exit 1
fi
vm_stop
# The line ssh wrote, its host field (the guest's tap address, or its hash under HashKnownHosts) made *, any address: the
# file is read only as the guest's known_hosts, and a pool cell answers on $FC_NET.<b>.2 (sshd(8), SSH_KNOWN_HOSTS
# FILE FORMAT).
awk 'NF >= 3 && $1 !~ /^[#@]/ && !n { $1 = "*"; print; n++ } END { exit !n }' "$KNOWN_HOSTS.new" > "$KNOWN_HOSTS.tmp" \
  || { echo "ssh recorded no host key for the guest in $KNOWN_HOSTS.new; the image is not usable" >&2; rm -f "$KNOWN_HOSTS.tmp"; exit 1; }
install -m 0644 "$KNOWN_HOSTS.tmp" "$KNOWN_HOSTS"; rm -f "$KNOWN_HOSTS.tmp" "$KNOWN_HOSTS.new"
echo "guest host key: $(awk '{print $2}' "$KNOWN_HOSTS" | paste -sd ' '), in $KNOWN_HOSTS"
mv "$new" "$img"
echo "laug · cell image $img, $(du -h "$img" | cut -f1) on disk, built in $(since "$t_all") s"

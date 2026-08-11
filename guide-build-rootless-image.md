# Build & Push buildkitd image (rootful + rootless)

## How it works

Running `docker buildx build --target <stage>` triggers a chain of Dockerfile stages. Here is what happens end-to-end.

### 1. Dockerfile stage dependency chain

```
golatest (golang:alpine)
  └── gobuild-base          # apk: clang, lld, musl-dev, git, make
        ├── buildkit-version  # extracts git tag/commit → /tmp/.ldflags, /tmp/.version
        ├── buildkitd         # go build → /usr/bin/buildkitd  (static, CGO_ENABLED=0)
        ├── buildctl          # go build → /usr/bin/buildctl   (static, CGO_ENABLED=0)
        ├── runc              # go build → /usr/bin/runc        (static, CGO_ENABLED=1)
        └── rootlesskit       # go build → /rootlesskit         (static, CGO_ENABLED=0)

binaries-linux  (scratch)
  ├── runc       → /buildkit-runc
  ├── buildkitd  → /buildkitd
  └── buildctl   → /buildctl
  (+ binfmt QEMU helpers + CNI plugins)

buildkit-export  (alpine)
  ├── apk: fuse3, git, openssh, openssl, pigz, xz, iptables, ip6tables, util-linux-misc
  └── COPY buildctl-daemonless.sh → /usr/bin/

buildkit-linux  (from buildkit-export)    ← --target for rootful
  ├── COPY binaries/ → /usr/bin/   (buildkitd, buildctl, runc, ...)
  └── ENTRYPOINT ["buildkitd-entrypoint"]

rootless  (alpine)                         ← --target for rootless
  ├── apk: fuse3, fuse-overlayfs, openssh, shadow-uidmap, ...
  ├── COPY rootlesskit  → /usr/bin/rootlesskit
  ├── COPY binaries/    → /usr/bin/   (buildkitd, buildctl, runc, ...)
  ├── USER 1000:1000
  └── ENTRYPOINT ["rootlesskit", "buildkitd"]
```

### 2. Cross-compilation with `xx`

The Dockerfile uses [tonistiigi/xx](https://github.com/tonistiigi/xx) to cross-compile all Go binaries for `$TARGETPLATFORM` while the build itself runs on `$BUILDPLATFORM`. An `amd64` host produces `linux/arm64` Go binaries natively without QEMU at compile time. QEMU is only needed for the final `rootless` stage where `apk add` actually runs Alpine binaries for the target architecture.

### 3. Version stamping

`buildkit-version` mounts the `.git` directory (enabled by `BUILDKIT_CONTEXT_KEEP_GIT_DIR=1`) and writes `-ldflags` with `version.Version`, `version.Revision`, and `version.Package` into `/tmp/.ldflags`. Both `buildkitd` and `buildctl` stages mount this file at link time so `buildkitd --version` prints the correct git tag.

### 4. Why `oci-mediatypes=false` is required for SWR

Buildx ≥ 0.10 defaults to OCI media types for all output:

| Schema | Manifest type | Index (multi-arch) type |
|---|---|---|
| OCI (Buildx default) | `application/vnd.oci.image.manifest.v1+json` | `application/vnd.oci.image.index.v1+json` |
| Docker v2 (SWR-compatible) | `application/vnd.docker.distribution.manifest.v2+json` | `application/vnd.docker.distribution.manifest.list.v2+json` |

Huawei SWR accepts single OCI manifests but rejects OCI Index (multi-arch manifest lists), returning `Invalid image, fail to parse 'manifest.json'`. Passing `oci-mediatypes=false` in `--output` forces Buildx to emit Docker v2 media types, which SWR handles correctly.

---

## Prerequisites

- Docker with Buildx
- SWR login done beforehand

```bash
docker login swr.cn-southwest-2.myhuaweicloud.com
```

---

## Step 0: Create the builder

The default `docker` driver cannot build multi-arch images or push multi-arch manifests. Always use a `docker-container` driver builder.

Many environments (VMs, containers with restricted capabilities) cannot create veth pairs for bridge networks, causing:

```
failed to add the host (vethXXX) <=> sandbox (vethYYY) pair interfaces: operation not supported
```

`--driver-opt network=host` runs the BuildKit container on the host network directly, bypassing veth/bridge creation entirely.

```bash
docker buildx rm mp-builder 2>/dev/null || true

docker buildx create \
  --name mp-builder \
  --driver docker-container \
  --driver-opt network=host \
  --use

docker buildx inspect --bootstrap
# Status: running
# BuildKit version: v0.31.x
# Platforms: linux/amd64, linux/amd64/v2, linux/amd64/v3, linux/386
```

---

## Option A: Rootful amd64 only (no QEMU needed)

```bash
cd buildkit-proxy

SWR_TAG=swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy

docker buildx build \
  --target buildkit-linux \
  --platform linux/amd64 \
  --tag "${SWR_TAG}" \
  --provenance=false \
  --sbom=false \
  --output "type=image,push=true,oci-mediatypes=false" \
  .
```

---

## Option B: Rootful multi-arch amd64 + arm64 (QEMU required)

### 1. Register QEMU for arm64

```bash
docker run --privileged --rm tonistiigi/binfmt --install arm64

# Confirm arm64 is now listed
docker buildx inspect mp-builder
# Platforms: linux/amd64, linux/amd64/v2, linux/amd64/v3, linux/386, linux/arm64
```

### 2. Build and push

```bash
cd buildkit-proxy

SWR_TAG=swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy

docker buildx build \
  --target buildkit-linux \
  --platform linux/amd64,linux/arm64 \
  --tag "${SWR_TAG}" \
  --provenance=false \
  --sbom=false \
  --output "type=image,push=true,oci-mediatypes=false" \
  .
```

---

## Option C: Rootless amd64 only (no QEMU needed)

```bash
cd buildkit-proxy

SWR_TAG=swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy-rootless

docker buildx build \
  --target rootless \
  --platform linux/amd64 \
  --tag "${SWR_TAG}" \
  --provenance=false \
  --sbom=false \
  --output "type=image,push=true,oci-mediatypes=false" \
  .
```

---

## Option D: Rootless multi-arch amd64 + arm64 (QEMU required)

### 1. Register QEMU for arm64

```bash
docker run --privileged --rm tonistiigi/binfmt --install arm64

# Confirm arm64 is now listed
docker buildx inspect mp-builder
# Platforms: linux/amd64, linux/amd64/v2, linux/amd64/v3, linux/386, linux/arm64
```

### 2. Build and push

```bash
cd buildkit-proxy

SWR_TAG=swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy-rootless

docker buildx build \
  --target rootless \
  --platform linux/amd64,linux/arm64 \
  --tag "${SWR_TAG}" \
  --provenance=false \
  --sbom=false \
  --output "type=image,push=true,oci-mediatypes=false" \
  .
```

---

## Target comparison

| Target | User | Entrypoint | Requires privileged |
|---|---|---|---|
| `buildkit-linux` | root (uid 0) | `buildkitd-entrypoint` | Yes (for OCI worker) |
| `rootless` | user 1000:1000 | `rootlesskit buildkitd` | No (uses `fuse-overlayfs`) |

**Flag reference:**

| Flag | Why |
|---|---|
| `--target buildkit-linux` | Stop at rootful stage (root user + `ENTRYPOINT ["buildkitd-entrypoint"]`) |
| `--target rootless` | Stop at rootless stage (rootlesskit + user 1000:1000) |
| `--platform linux/amd64,linux/arm64` | Build for both architectures and combine into a multi-arch manifest list |
| `--provenance=false --sbom=false` | Disable SLSA provenance and SBOM attestation manifests that Buildx attaches by default — these are OCI-only and cause SWR parse errors |
| `--output "type=image,push=true,oci-mediatypes=false"` | Push directly to the registry using Docker v2 media types instead of OCI; `oci-mediatypes=false` is the key flag that makes the multi-arch manifest list parseable by SWR |

---

## Verify

```bash
# Check the manifest list in the registry (rootful)
docker buildx imagetools inspect \
  swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy

# Check the manifest list in the registry (rootless)
docker buildx imagetools inspect \
  swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy-rootless

# Pull and run the arm64 image (rootful)
docker run --rm --platform linux/arm64 \
  swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy \
  buildkitd --version

# Pull and run the arm64 image (rootless)
docker run --rm --platform linux/arm64 \
  --user 1000:1000 \
  swr.cn-southwest-2.myhuaweicloud.com/modelfoundry/moby/buildkit:v0.31.x-proxy-rootless \
  buildkitd --version
```

---

## Troubleshooting

| Problem | Fix |
|---|---|
| `operation not supported` on veth/bridge | Recreate builder with `--driver-opt network=host` |
| `Invalid image, fail to parse 'manifest.json'` | Add `oci-mediatypes=false` to `--output`; SWR cannot parse OCI Index format |
| `exporting attestation manifest` still appears | Add `--provenance=false --sbom=false` explicitly |
| Only `linux/amd64` in builder platforms | Register QEMU: `docker run --privileged --rm tonistiigi/binfmt --install arm64` |
| `exec format error` running the image | Wrong arch loaded; verify with `docker inspect --format '{{.Architecture}}'` |
| Slow GitHub downloads during build | Unset `HTTP_PROXY`/`HTTPS_PROXY` if squid CA is not trusted in the build env, or pass squid CA via `--build-arg` |

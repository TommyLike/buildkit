# Security Threat Model — buildkitd for Open-Source CI

## Attack surface

BuildKit serves an open-source repo (`vllm-ascend`) whose workflows run on self-hosted ARC runners in Kubernetes. Anyone can fork the repo and submit a PR — workflows execute automatically via `pull_request` trigger.

Three daemon processes run with elevated privileges:

| Component | Privilege | Reason |
|---|---|---|
| buildkitd (rootful) | `CAP_SYS_ADMIN`, `CAP_SYS_CHROOT`, `CAP_SYS_PTRACE`, seccomp `Unconfined`, AppArmor `Unconfined` | Overlayfs mounts, runc sandboxes |
| squid proxy | root (needed for port 3128) | SSL-bump MITM, DNS resolution |
| buildkitd (rootless) | rootlesskit + fuse-overlayfs, uid 1000 | Container builds without elevated capabilities |

## Attack chain: PR from fork → buildkitd

```
1. Attacker forks vllm-ascend
2. Attacker creates PR modifying .github/workflows/build.yml:
     run: buildctl --addr "${BUILDKITD_ADDR}" build --frontend dockerfile.v0 ...
3. PR triggers workflow on ARC self-hosted runner
4. Runner pod spawns in gy006 cluster
5. Vault sidecar injects ca.pem, cert.pem, key.pem → /home/user/.docker/
6. Pod has network access to buildkitd-service.buildkitd:1234 (mTLS)
7. BUILDKITD_ADDR is pre-set in pod env
8. buildctl connects to buildkitd with valid mTLS certs
9. Attacker's Dockerfile executes via buildkitd
```

## What each layer protects against

| Layer | Blocks fork PR? | Note |
|---|---|---|
| Cluster network boundary | ✅ | buildkitd Service is cluster-internal only |
| mTLS (buildkitd) | ❌ | Attacker inherits Vault-injected certs from runner pod |
| `pull_request` trigger secrets isolation | ❌ | Vault authenticates via SA token, not GitHub `secrets.*` |
| `runc` sandbox per `RUN` step | ✅ | Dockerfile processes run in isolated OCI containers |
| buildkitd root + `SYS_ADMIN` | ➖ | runc 0day = node root. Rootless buildkitd would contain this |
| Dockerfile visibility | ✅ | Base images, Dockerfile instructions are public |

## Capability risk breakdown

### Why each capability is needed

| Cap | Needed for | Extraneous risk |
|---|---|---|
| `SYS_ADMIN` | `mount(overlayfs)` per `RUN` step | Mount host `/` → `chroot` → modify any host file. Load kernel modules. Manipulate namespaces to escape into host PID/netns. Create device nodes. |
| `SYS_CHROOT` | `runc` pivots root for sandboxed `RUN` steps | Skip `pivot_root` → chroot into arbitrary mount points if paired with `SYS_ADMIN` |
| `SYS_PTRACE` | `runc` process signaling and OOM handling | Read/write memory of ANY host process. Dump Vault-injected secrets from sidecar or other containers |

### seccomp `Unconfined`

Default seccomp profile blocks ~300 syscalls: `mount`, `umount`, `kexec_load`, `init_module`, `bpf`, `reboot`. `Unconfined` allows all of them:

- Write kernel memory via `/dev/mem`
- Load kernel modules (`init_module`, `finit_module`)
- Reboot the node (`reboot`)
- Load eBPF programs (`bpf`)

### AppArmor `Unconfined`

Default AppArmor profiles restrict filesystem paths and operations per workload. `Unconfined` removes all filesystem, mount, and operation restrictions.

## Escalation scenarios

### Scenario 1: runc container escape (e.g., CVE-2024-21626)

```
Attacker Dockerfile:
  RUN exploit → escapes runc sandbox → process runs as root in buildkitd container
  buildkitd has SYS_ADMIN → mounts host / → writes to host filesystem → node root
```

**Rootless mitigates this:** even after runc escape, attacker is uid 1000 inside user namespace with no capability to mount or load modules.

### Scenario 2: buildkitd daemon exploit (gRPC handler)

```
buildkitd gRPC handler crash/overflow → attacker controls buildkitd process (root, SYS_ADMIN)
→ immediate node root
```

**Rootless mitigates this:** exploit yields uid 1000 in user namespace, no elevated capabilities.

### Scenario 3: Host filesystem access via snapshot/Dockerfile tricks

```
Dockerfile:
  COPY --from=/ /host /out
  or Dockerfile symlink traversal in --mount
```

Partial protection by runc sandbox, but buildkitd's snapshotter and COPY implementation may have gaps. BuildKit has had historical CVE-2021-41089, CVE-2022-40730 for similar issues.

## Known CVEs in the ecosystem

| CVE | Class | Rootful risk | Rootless mitigation |
|---|---|---|---|
| CVE-2024-21626 | runc WORKDIR escape | Node root | User namespace isolation |
| CVE-2019-5736 | runc host binary overwrite | Node root | No write access to host runc |
| CVE-2022-23648 | containerd snapshots | Node filesystem access | Limited by user namespace |
| CVE-2024-23651 | BuildKit symlink in COPY | Host filesystem read | Limited by user namespace |
| CVE-2021-41089 | BuildKit --mount=type=bind | Host filesystem | Limited by user namespace |

## Mitigations

### Option 1: Restrict PR triggers (quickest, no infra change)

```yaml
# .github/workflows/build.yml
jobs:
  build-with-buildkitd:
    if: github.event.pull_request.head.repo.full_name == github.repository
    # OR
    if: github.event.pull_request.author_association == 'MEMBER' || github.event.pull_request.author_association == 'OWNER'
    steps:
      - run: buildctl --addr "${BUILDKITD_ADDR}" build ...

  build-with-docker:
    if: github.event.pull_request.head.repo.full_name != github.repository
    steps:
      - run: docker build .   # no privileged daemon, uses pod's docker socket or DinD
```

- ✅ Blocks untrusted PRs from buildkitd
- ✅ Zero infra change
- ❌ Fork PRs lose buildkitd caching/provenance features

### Option 2: Rootless buildkitd for PR builds

Run two buildkitd instances:
- **rootful** → trusted builds (main branch, internal PRs, releases)
- **rootless** → untrusted fork PRs (network-isolated on separate node pool)

```yaml
- name: Build
  run: |
    if [[ "$IS_TRUSTED" == "true" ]]; then
      export BUILDKITD_ADDR=tcp://buildkitd-trusted.buildkitd:1234
    else
      export BUILDKITD_ADDR=tcp://buildkitd-untrusted.buildkitd:1234
    fi
    buildctl --addr "${BUILDKITD_ADDR}" build ...
```

- ✅ Full isolation, no capability escalation on untrusted path
- ❌ Double infrastructure, 10-20% FUSE overhead on PR builds

### Option 3: Accept risk (if node is isolated)

If the buildkitd node:
- Is in a separate node pool, not shared with production
- Has no cluster secrets mounted beyond buildkitd mTLS certs
- Network policy blocks egress to production services
- The only valuable assets are build caches and CI artifacts

Then the practical risk is low — runc/buildkitd 0days are rare and typically targeted at multi-tenant environments. A CI node with nothing but build artifacts is a low-value target.

## Recommendation

For `vllm-ascend` (open-source, anyone can submit PRs):

**Option 1 (restrict triggers)** for immediate deployment, **Option 2 (rootless for PRs)** as medium-term hardening.

The `CAP_SYS_ADMIN` + `Unconfined` combination exposes the node to any runc or buildkitd kernel escape 0day. These are rare but have demonstrated impact (CVE-2024-21626 affected every Kubernetes cluster running untrusted workloads). Rootless buildkitd eliminates this class of vulnerability entirely by running buildkitd itself in a user namespace.

# Build proxy network

BuildKit can run exec network traffic through a BuildKit-owned HTTP(S) proxy.

The proxy network is controlled by solve or daemon configuration. Once proxy is
enabled for a build, BuildKit applies it to every exec that has network access.
The exec network mode controls how the proxy itself reaches the network.

## Enabling proxy networking

Enable proxy networking for a single build with `buildctl build`:

```bash
buildctl build --proxy-network ...
```

Enable proxy networking by default in `buildkitd.toml`:

```toml
proxyNetwork = true
```

Or enable it with the daemon flag:

```bash
buildkitd --proxy-network
```

The per-build setting and the daemon setting both enable the same runtime proxy
behavior. Frontends do not set proxy networking per operation.

## Network mode behavior

The exec network mode describes the network access requested by the build step.
With proxy networking enabled, BuildKit uses that mode to choose the proxy
egress path.

| Exec network mode | Proxy behavior |
| --- | --- |
| default sandbox | Proxy is injected. The proxy reaches the network through a bridge/CNI-style namespace, not through the `buildkitd` host namespace. This is intended for normal internet access and does not expose host loopback services. |
| host | Proxy is injected. The proxy reaches the network from the `buildkitd` host namespace. This requires the normal `network.host` entitlement on both the daemon and solve request. |
| none | Proxy is not injected. The exec has no network access. |

For example, a Dockerfile step using the default network can fetch external
URLs through the proxy:

```dockerfile
RUN wget -O- https://example.com/
```

A step that needs to access a service on the `buildkitd` host network must
request host networking:

```dockerfile
RUN --network=host wget -O- http://127.0.0.1:8080/
```

The build also needs the host network entitlement:

```bash
buildkitd --proxy-network --allow-insecure-entitlement network.host
buildctl build --proxy-network --allow network.host ...
```

`RUN --network=none` remains fully offline:

```dockerfile
RUN --network=none wget -O- https://example.com/
```

## What BuildKit injects

For proxied execs, BuildKit injects proxy environment variables into the exec
process:

```text
HTTP_PROXY
HTTPS_PROXY
http_proxy
https_proxy
NO_PROXY
no_proxy
```

`NO_PROXY` includes local loopback names and addresses, so direct localhost
traffic from the process is not sent through the proxy by default. If the
process clears `NO_PROXY`, localhost requests are sent to the proxy and then
handled according to the exec network mode.

BuildKit also injects a generated CA certificate into common Linux trust bundle
locations for the duration of the exec. This lets HTTPS requests using the
system trust store pass through the BuildKit proxy.

## Request capture and logs

The proxy records network requests made by exec steps. Build output includes a
summary like:

```text
proxy network requests:
- GET https://example.com/ -> 200
- GET http://127.0.0.1:8080/ -> 502
```

Successful GET responses can be captured as build materials. When provenance is
requested, these captured materials are added to the provenance dependency list
with proxy network metadata. If a GET request cannot be captured completely,
BuildKit can report it as an incomplete proxy material in provenance metadata.

## Source policy

Proxy requests use BuildKit source policy checks. The proxy evaluates
proxy-visible HTTP(S) requests as synthesized HTTP source ops, so policy can be
used to allow, deny, or convert matching proxy GET URLs. Conversion currently
applies to GET requests where only the URL is changed.

This keeps proxy network access aligned with the same policy model used for
other BuildKit sources.

## Upstream forward proxy (caching proxy)

BuildKit's internal MITM proxy can be configured to forward all requests
through an upstream forward proxy, such as a Squid caching proxy. This enables
cluster-level caching while preserving BuildKit's request capture, policy, and
provenance features.

Configure an upstream proxy in `buildkitd.toml`:

```toml
proxyNetwork = true

[proxy]
  upstreamURL = "http://squid.internal:3128"
```

When `upstreamURL` is set, the internal MITM proxy sends all outgoing requests
through the specified upstream proxy instead of connecting directly. The build
containers are unaware of the upstream proxy — they interact only with the
internal MITM proxy as usual.

### HTTPS upstream proxy

If the upstream proxy uses HTTPS (e.g., `https://squid.internal:3128`), provide
the CA certificate used to verify the upstream proxy's TLS certificate:

```toml
[proxy]
  upstreamURL = "https://squid.internal:3128"
  upstreamCACert = "/etc/buildkit/squid-ca.pem"
```

### How it works

With upstream proxy configured, the request flow is:

```
Container → Internal MITM proxy → Upstream Squid → Internet
```

- The internal MITM proxy handles HTTP/HTTPS interception, policy evaluation,
  and request capture.
- Forwarded requests are sent through the upstream proxy's `Proxy` transport,
  allowing the upstream proxy to cache responses.
- HTTPS requests are decrypted by the internal MITM proxy before being forwarded
  to the upstream proxy, so the upstream proxy can inspect and cache HTTP-level
  content.
- The internal proxy's CA certificate is still injected into build containers;
  the upstream proxy's CA certificate is only used by the internal proxy's
  transport when connecting to an HTTPS upstream proxy.

## Scope and limitations

The proxy network feature currently applies to exec traffic. It does not replace
image resolver, Git, HTTP source, or other non-exec fetch paths.

The current implementation is Linux-focused. Rootless workers also have the
usual rootless networking limitations, where worker networking may behave like
host networking.

Applications that ignore `HTTP_PROXY` and `HTTPS_PROXY`, use custom trust
stores, or open raw TCP connections cannot bypass the proxy. That traffic is
blocked instead of being captured.

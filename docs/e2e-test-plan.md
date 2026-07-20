# 端到端测试方案：上游代理（Squid）验证

## 测试结果摘要

| 验证项 | 结果 |
|--------|------|
| buildkitd 启动 (proxy config 解析) | ✅ 通过 |
| HTTP 请求经过 Squid | ✅ TCP_MISS/200 GET http://example.com/ |
| HTTPS 请求经过 Squid | ✅ TCP_TUNNEL/200 CONNECT example.com:443 |
| proxy network requests 记录 | ✅ 构建输出含请求记录 |
| 执行时间 | 2026-07-20 10:17 CST |

## 测试环境

| 项目 | 值 |
|------|-----|
| 机器 | `tommylikehu@ecs-develop-for-husheng-linux` |
| 内核 | Linux 6.8.0 (x86_64) |
| Go | 1.25.9 (`/usr/local/go/bin/go`) |
| runc | 1.3.5 |
| Docker | 29.5.3 |
| 工作目录 | `/home/tommylikehu/workspace/buildkit` |
| 分支 | `feature/upstream-proxy-config` |
| Commit | `970bc60b9` |

## 测试架构

```
宿主机 (Linux)
├── squid 容器 (docker, ubuntu/squid:latest)
│   IP: 172.17.0.2
│   端口: 3128
│   access.log: /var/log/squid/access.log
│
└── buildkitd (原生进程, root 运行)
    --oci-worker-net=host
    --allow-insecure-entitlement network.host
    proxy.upstreamURL = "http://172.17.0.2:3128"

请求链路:
Container(RUN wget) → 内部 MITM 代理 → Squid(3128) → Internet
```

## 执行步骤

### Step 1: 编译 ✅

```bash
export PATH=$PATH:/usr/local/go/bin
cd /home/tommylikehu/workspace/buildkit
make binaries
```
产物: `./bin/build/buildkitd` (77M), `./bin/build/buildctl` (33M)

### Step 2: 启动 Squid ✅

```bash
docker run -d --name buildkit-squid-test \
  -p 3128:3128 \
  ubuntu/squid:latest

# Squid IP: 172.17.0.2
```

### Step 3: 创建 buildkitd 配置 ✅

```bash
mkdir -p /tmp/buildkit-test/state /tmp/buildkit-test/context

cat > /tmp/buildkit-test/buildkitd.toml << EOF
root = "/tmp/buildkit-test/state"
proxyNetwork = true

[proxy]
  upstreamURL = "http://172.17.0.2:3128"
EOF
```

### Step 4: 启动 buildkitd ✅

```bash
sudo ./bin/build/buildkitd \
  --config /tmp/buildkit-test/buildkitd.toml \
  --oci-worker=true \
  --oci-worker-net=host \
  --oci-worker-binary=./bin/build/buildkit-runc \
  --allow-insecure-entitlement network.host \
  --root /tmp/buildkit-test/state \
  --addr unix:///tmp/buildkit-test/buildkitd.sock &
```

启动日志:
```
found worker "0wrdzwjim5fqekvjg9vzxkyg1", labels=[...network:host...]
running server on /tmp/buildkit-test/buildkitd.sock
```

### Step 5: 测试 Dockerfile ✅

```dockerfile
FROM alpine:latest
RUN wget -q -O- http://example.com
RUN wget -q -O- https://example.com
```

### Step 6: 执行构建 ✅

```bash
sudo ./bin/build/buildctl \
  --addr unix:///tmp/buildkit-test/buildkitd.sock \
  build --proxy-network \
  --frontend dockerfile.v0 \
  --local context=/tmp/buildkit-test/context \
  --local dockerfile=/tmp/buildkit-test/context \
  --output type=image,name=buildkit-proxy-test:latest,push=false
```

构建输出:
```
#5 [2/3] RUN wget -q -O- http://example.com
#5 0.109 <!doctype html>...Example Domain...
#5 0.118 proxy network requests:
#5 0.118 - GET http://example.com/ -> 200

#6 [3/3] RUN wget -q -O- https://example.com
#6 0.084 <!doctype html>...Example Domain...
#6 0.088 proxy network requests:
#6 0.088 - GET https://example.com/ -> 200
```

### Step 7: 验证 Squid 日志 ✅

```bash
docker exec buildkit-squid-test cat /var/log/squid/access.log
```

Squid access.log 输出:
```
TCP_MISS/200  947 GET  http://example.com/     - HIER_DIRECT/104.20.23.154
TCP_TUNNEL/200 6551 CONNECT example.com:443    - HIER_DIRECT/104.20.23.154
```

| 请求 | Squid 记录 | 说明 |
|------|-----------|------|
| HTTP example.com | `TCP_MISS/200 GET http://example.com/` | 代理转发，首次未命中缓存 |
| HTTPS example.com | `TCP_TUNNEL/200 CONNECT example.com:443` | CONNECT 隧道，MITM 代理解密后转发 |

### Step 8: 清理 ✅

```bash
sudo pkill buildkitd
pkill https-proxy
docker rm -f buildkit-squid-test
rm -rf /tmp/buildkit-test
```

---

## 第二轮测试：HTTPS upstream + CA cert

此轮测试专门验证 `upstreamCACert` 配置的完整代码路径。

### 架构

```
宿主机
├── Go HTTPS forward proxy (localhost:3129)
│   证书: squid-san.crt (CA 签发, SAN: localhost)
│   记录所有请求到日志
│
└── buildkitd
    upstreamURL = "https://localhost:3129"
    upstreamCACert = "/tmp/buildkit-test/certs/ca.pem"
```

### Step A1: 生成 CA + 代理证书 ✅

```bash
openssl genrsa -out ca.key 2048
openssl req -new -x509 -days 365 -key ca.key -out ca.pem -subj "/CN=BuildKit Test CA"
openssl genrsa -out squid.key 2048
openssl req -new -key squid.key -out squid.csr -config san.cnf  # SAN: localhost, 127.0.0.1
openssl x509 -req -days 365 -in squid.csr -CA ca.pem -CAkey ca.key \
  -set_serial 01 -out squid.crt -extfile san.cnf -extensions v3_req
```

### Step A2: 编译并启动 HTTPS 代理 ✅

Go forward proxy 代码处理 HTTP GET 转发和 HTTPS CONNECT 隧道，监听 TLS 端口 3129。

### Step A3: 启动 buildkitd (HTTPS upstream) ✅

```toml
[proxy]
  upstreamURL = "https://localhost:3129"
  upstreamCACert = "/tmp/buildkit-test/certs/ca.pem"
```

启动日志无 error，CA 文件解析成功，daemon 正常运行。

### Step A4: 执行构建 ✅

HTTP 和 HTTPS 两个 RUN 指令均成功，返回 example.com HTML 内容。

### Step A5: 验证代理日志 ✅

```
REQUEST: GET http://example.com/ (Host: example.com)
GET OK: http://example.com/ -> 200 (-1 bytes)
GET DONE: http://example.com/ copied 559 bytes

REQUEST: CONNECT //example.com:443 (Host: example.com:443)
CONNECT OK: example.com:443
CONNECT DONE client->dest: 1782 bytes
CONNECT DONE dest->client: 6534 bytes
```

## 验证结论

全部 8 项验证通过：

| # | 验证项 | 结果 | 证据 |
|---|--------|------|------|
| 1 | buildkitd 启动 (HTTP upstream) | ✅ | `running server on /tmp/buildkit-test/buildkitd.sock` |
| 2 | HTTP → Squid | ✅ | `TCP_MISS/200 GET http://example.com/` |
| 3 | HTTPS (MITM) → Squid | ✅ | `TCP_TUNNEL/200 CONNECT example.com:443` |
| 4 | Proxy 请求记录 | ✅ | 构建输出含 `proxy network requests` |
| 5 | buildkitd 启动 (HTTPS upstream + CA cert) | ✅ | daemon 正常启动，CA 文件解析成功 |
| 6 | HTTP → HTTPS Proxy (TLS) | ✅ | `GET http://example.com/ -> 200 (559 bytes)` |
| 7 | HTTPS MITM → HTTPS Proxy via CONNECT | ✅ | `CONNECT example.com:443 OK` / `client->dest: 1782 bytes` |
| 8 | TLS 握手 (CA cert 校验) | ✅ | self-signed cert → CA → transport trust → tunnel established |

### 第二轮测试：HTTPS upstream + CA cert 详细结果

**测试配置**:
```toml
root = "/tmp/buildkit-test/state"
proxyNetwork = true

[proxy]
  upstreamURL = "https://localhost:3129"
  upstreamCACert = "/tmp/buildkit-test/certs/ca.pem"
```

**HTTPS 代理日志** (Go forward proxy, TLS 端口 3129):
```
REQUEST: GET http://example.com/ (Host: example.com)
GET OK: http://example.com/ -> 200 (-1 bytes)
GET DONE: http://example.com/ copied 559 bytes

REQUEST: CONNECT //example.com:443 (Host: example.com:443)
CONNECT OK: example.com:443
CONNECT DONE client->dest: 1782 bytes
CONNECT DONE dest->client: 6534 bytes
```

**证书链**: `BuildKit Test CA (ca.pem)` → `squid-proxy (squid-san.crt, SAN: localhost, 127.0.0.1)`

**验证的代码路径**:
- `newProxyTransport()` 中 `upstreamCACert != ""` 分支 → `os.ReadFile` + `x509.SystemCertPool()` + `AppendCertsFromPEM` + `TLSClientConfig.RootCAs` 设置
- transport TLS 握手使用自定义 RootCAs 验证自签证书 → 成功
- HTTP 请求：transport → Proxy 函数返回 `https://localhost:3129` → TLS 连接 → 代理转发
- HTTPS 请求：transport → CONNECT 隧道 via HTTPS 代理 → TLS with target

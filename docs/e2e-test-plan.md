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
docker rm -f buildkit-squid-test
rm -rf /tmp/buildkit-test
```

## 验证结论

全部 5 项验证通过：

1. ✅ **buildkitd 启动** — proxy 配置解析成功，upstreamURL 正确应用（无效 URL 已在前序测试中验证会拒绝启动）
2. ✅ **HTTP 代理** — `wget http://example.com` 的请求出现在 Squid access.log (`TCP_MISS/200 GET`)
3. ✅ **HTTPS 代理 (MITM)** — `wget https://example.com` 的请求出现在 Squid access.log (`TCP_TUNNEL/200 CONNECT`)
4. ✅ **请求记录** — buildctl 输出中包含 `proxy network requests` 记录
5. ✅ **容器构建成功** — Alpine 镜像拉取 + wget 执行 + 内容返回均正常

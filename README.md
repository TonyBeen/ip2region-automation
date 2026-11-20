# ip2region automation & query service

本项目是一个集成了 **数据库自动化定时更新** 与 **Go 高性能离线 IP 查询微服务** 的一体化解决方案：
1. **自动更新器**：定时拉取 `fa1seut0pia/ip2region-xdb` 最新 Release，下载 IPv4/IPv6 双栈 xdb，校验 SHA-256 和文件大小，通过原子软链无缝切换版本。
2. **Go 高性能查询服务**：直接基于官方 `lionsoul2014/ip2region` (v3.19.0) 子模块实现，微秒级响应，支持高并发，提供类似 `ifconfig.me` / `ip.sb` 的 IP 查询 API，支持 **Nginx 前缀反代（默认 `/ifconfig`）** 与 **零停机热重载**。
3. **开箱即用 Docker 部署**：基于多阶段构建的轻量级镜像（< 70MB），一键拉起服务与自动同步守护进程。

---

## 目录结构

```text
.
├── cmd/
│   └── server/             # Go 高性能 HTTP 查询服务源码
│       ├── main.go
│       └── main_test.go
├── seed/                   # 仓库内置的最新双栈 xdb 离线种子（开箱即用，无需等待下载）
├── submodules/
│   └── ip2region/          # 官方 lionsoul2014/ip2region 子模块 (tag: v3.19.0)
├── nginx/
│   └── ifconfig.conf       # Nginx /ifconfig 前缀反向代理配置示例
├── scripts/
│   ├── update_seed.py      # 一键更新本地 seed/ 离线种子数据库到上游最新版本
│   ├── deploy.sh           # Linux 宿主机部署脚本
│   ├── cleanup.sh          # 历史版本与临时目录安全清理脚本
│   └── entrypoint.sh       # Docker 容器启动入口
├── systemd/                # systemd timer & service 配置
├── updater.py              # 数据下载、校验、原子切换与回滚核心
├── Dockerfile              # Docker 多阶段构建文件
├── docker-compose.yml      # Docker Compose 编排文件
└── go.mod                  # Go 模块定义（本地指向子模块）
```

---

## 快速开始 (Docker 部署)

推荐使用 Docker / Docker Compose 进行一键部署：

```bash
# 1. 递归克隆包含子模块的仓库（若已克隆，执行 git submodule update --init --recursive）
git submodule update --init --recursive

# 2. 启动服务
docker compose up -d
```

服务启动后：
- 自动检测并拉取最新的 IPv4 与 IPv6 数据库。
- 启动 Go 高性能查询服务（默认端口 `28080`，前缀路径 `/ifconfig`）。
- 后台每 6 小时自动检查一次数据库更新，更新成功后通过 `/ifconfig/reload` 自动零停机热重载索引。

---

## Nginx 反向代理配置 (/ifconfig)

参考 [nginx/ifconfig.conf](file:///home/eular/VSCode/ip2region-automation/nginx/ifconfig.conf)：

```nginx
upstream ip2region_backend {
    server 127.0.0.1:28080;
    keepalive 32;
}

server {
    listen 80;
    server_name your-domain.com;

    # 推荐配置：带 /ifconfig 前缀转发
    location /ifconfig {
        proxy_pass http://ip2region_backend;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Prefix /ifconfig;

        # 开启 HTTP 1.1 keepalive 提升并发响应
        proxy_http_version 1.1;
        proxy_set_header Connection "";

        proxy_connect_timeout 5s;
        proxy_read_timeout 10s;
    }
}
```

> **注意**：服务内置路径自适应机制，无论是直接反向代理 `proxy_pass http://.../ifconfig;` 还是剥离前缀 `proxy_pass http://.../;`，均可无缝兼容。

---

## API 查询接口说明

所有接口均位于配置的前缀路径下（默认为 `/ifconfig`）：

### 1. 查询访问者自身 IP（默认返回格式化 JSON）

- **直接访问（默认返回 JSON，携带发送请求的公网 IP）**：
  ```bash
  curl http://your-domain.com/ifconfig
  ```
  返回示例：
  ```json
  {
    "ip": "114.114.114.114",
    "country": "中国",
    "province": "江苏省",
    "city": "南京市",
    "isp": "114DNS",
    "code": "CN",
    "region": "中国|江苏省|南京市|114DNS|CN"
  }
  ```

- **仅获取纯 IP 文本（方便 Shell 脚本提取）**：
  ```bash
  curl http://your-domain.com/ifconfig?format=ip
  # 输出: 114.114.114.114
  ```

### 2. 查询指定 IPv4 或 IPv6 地址

- **路径方式（默认返回 JSON）**：
  ```bash
  # 查询 IPv4
  curl http://your-domain.com/ifconfig/1.1.1.1

  # 查询 IPv6
  curl http://your-domain.com/ifconfig/240e:3b7:3272:d8d0::1
  ```

- **参数方式**：
  ```bash
  curl "http://your-domain.com/ifconfig?ip=1.1.1.1"
  ```

### 3. 批量并发查询多个 IP（单次请求并发查询多个 IP）

支持 GET 逗号分隔传参，以及 POST 提交 JSON 数组或每行一个 IP：

- **GET 参数方式（逗号分隔多个 IP，支持 IPv4 与 IPv6 混查）**：
  ```bash
  curl "http://your-domain.com/ifconfig?ips=1.1.1.1,8.8.8.8,240e:3b7:3272:d8d0::1"
  ```

- **POST JSON 数组（推荐）**：
  ```bash
  curl -X POST "http://your-domain.com/ifconfig/batch" \
    -H "Content-Type: application/json" \
    -d '["1.1.1.1", "8.8.8.8", "114.114.114.114"]'
  ```

- **POST 对象或文本行**：
  ```bash
  # 支持 {"ips": [...]}
  curl -X POST "http://your-domain.com/ifconfig" \
    -d '{"ips": ["1.1.1.1", "8.8.8.8"]}'

  # 或通过文本文件一行一个 IP 提交
  curl -X POST "http://your-domain.com/ifconfig" --data-binary @ips.txt
  ```

返回示例（后端多协程并发处理后返回结果列表）：
```json
[
  {
    "ip": "1.1.1.1",
    "country": "Australia",
    "province": "Queensland",
    "city": "Brisbane",
    "code": "AU",
    "region": "Australia|Queensland|Brisbane|0|AU"
  },
  {
    "ip": "8.8.8.8",
    "country": "United States",
    "province": "California",
    "isp": "Google LLC",
    "code": "US",
    "region": "United States|California|0|Google LLC|US"
  }
]
```

### 4. 健康检查与状态

```bash
curl http://your-domain.com/ifconfig/healthz
```
返回示例：
```json
{
  "loaded": true,
  "loaded_at": "2026-11-20T14:38:53+08:00",
  "prefix": "/ifconfig",
  "status": "ok",
  "v4_enabled": true,
  "v6_enabled": true,
  "manifest": {
    "version": "2026.11.18",
    "source": "fa1seut0pia/ip2region-xdb"
  }
}
```

### 5. 手动触发热重载 (Hot Reload)

```bash
curl -X POST http://your-domain.com/ifconfig/reload
```
服务在接收到请求后会原子切换内存索引，旧连接查询不受影响，平滑过渡。

---

## 环境变量配置

在 `docker-compose.yml` 或宿主机环境中支持以下配置项：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PATH_PREFIX` | `/ifconfig` | 服务的前缀路径，留空或 `/` 则挂载在根路径 |
| `PORT` | `28080` | HTTP 监听端口 |
| `IP2REGION_DATA_DIR` | `/var/lib/ip2region` | 数据库存储路径 |
| `CACHE_POLICY` | `vindex` | 缓存策略：`vindex`（512KB 向量索引，极低内存）、`content`（全内存，极速） |
| `SEARCHERS` | `50` | 连接池/检索并发实例数 |
| `UPDATE_INTERVAL` | `6h` | 自动检查上游更新的周期（如 `6h`、`12h`） |
| `RELOAD_TOKEN` | 空 | 可选的重载密钥，配置后重载需携带 `X-Reload-Token` 头 |

---

## 宿主机直接运行 / 本地开发

### 编译与运行 Go 查询服务

```bash
# 编译
go build -o bin/ip2region-server ./cmd/server

# 运行（前缀为 /ifconfig）
IP2REGION_DATA_DIR=/var/lib/ip2region PATH_PREFIX=/ifconfig PORT=28080 ./bin/ip2region-server
```

### 单元测试

```bash
go test -v ./cmd/server
```

### 手动执行数据更新器

```bash
python3 updater.py --data-dir /var/lib/ip2region --reload-command 'curl -s -X POST http://127.0.0.1:28080/ifconfig/reload'
```

### 清理旧版本

```bash
# 默认保留当前版本和最近两个历史版本
sudo bash scripts/cleanup.sh --keep 2 --yes
```

### 一键更新仓库自带的离线 xdb 种子数据库

本项目自带 `seed/` 离线种子，随代码一同分发。当后续希望更新仓库自带的兜底数据库时，只需执行：

```bash
python3 scripts/update_seed.py
```

脚本会自动检测 `fa1seut0pia/ip2region-xdb` 的最新发布，下载并校验双栈 xdb 数据库与哈希清单，完成后即可直接提交代码。

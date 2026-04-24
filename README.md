# baka-web-file-server

一个轻量、无状态的自托管文件服务器。基于 HTTPS + JWT 鉴权，提供 RPC 风格的 API，支持文件上传、远程 URL 下载和进度追踪。附带开箱即用的 Web 界面，部署简单。

类似 filebrowser，但完全独立实现。后端由我完成，前端约 40% 由 AI 生成。

服务端内置 CORS 中间件（如需启用，取消 `program.go` 中间件链的注释），方便自行编写前端或 CLI 工具。

## 功能

- 文件浏览、上传、下载
- 远程 URL 下载到服务器
- 分片上传 + xxhash 完整性校验
- JWT 登录鉴权 + Token 自动续签
- 并发远程下载任务管理（支持取消和进度追踪）
- 内置 Web UI（编译时嵌入二进制，也支持外部目录）

## 预览

![主页](assets/index.jpeg)
![文件浏览](assets/browser.jpeg)
![下载任务](assets/download.jpeg)

## 快速开始

```bash
./bakaWFS
```

首次运行会在当前目录生成 `config.yaml` 和 `users.yaml`，编辑后重新启动即可。

### 首次配置

**必须修改的字段：**

```yaml
secret: "替换为随机字符串"   # JWT 签名密钥
```

**TLS 证书（可选）：**

```yaml
cert_path: "certificate.crt"
key_path:  "private.key"
```

启用 TLS 前请准备好证书。未启用 TLS 时，**不要将服务暴露在公网**。

## 配置说明

`config.yaml`：

```yaml
address: "0.0.0.0"
https_port: 443          # 设为 -1 关闭该协议。两者同时开启时 HTTP 自动重定向到 HTTPS
http_port:  80
secret: ""
cert_path: "certificate.crt"
key_path:  "private.key"
file_dir:   "files"      # 文件存储根目录
html_dir:   "built-in"   # 设为 "built-in" 使用内置前端；也可指定外部目录（加载其中的 index.html）
temp_dir:   ".uploads"   # 临时目录（分片上传、远程下载暂存）
users_file: "users.yaml"
download_workers: 2       # 并发远程下载 worker 数
```

`users.yaml`：

```yaml
users:
  - username: "baka"
    password: "bakabaka"
```

## API

| 方法 | 路径 | 说明 | 鉴权 |
|------|------|------|------|
| POST | `/login` | 登录，返回 JWT | 否 |
| POST | `/verify` | 验证并续签 Token | 是 |
| GET  | `/list` | 获取文件目录树 | 否 |
| GET  | `/files/*` | 下载文件（支持 Range / 断点续传） | 否 |
| POST | `/upload` | 上传文件（整体上传） | 是 |
| POST | `/upload/chunk` | 上传单个分片 | 是 |
| POST | `/upload/merge` | 合并分片 | 是 |
| POST | `/remote-upload` | 从 URL 下载文件到服务器 | 是 |
| GET  | `/progress` | 查看远程下载进度 | 是 |
| POST | `/cancel` | 取消远程下载任务 | 是 |

鉴权接口需在 Header 中携带 `Authorization: Bearer <token>`。

详细 API 文档见 [bakaWFS API](bakaWFS_API.md)。

## 项目结构

```
.
├── program.go           # 主入口
├── embed.go             # 嵌入前端静态文件
├── windows-terminal.go  # Windows 终端色彩适配
├── linux-terminal.go    # Linux/macOS 终端输出
├── config.yaml
├── users.yaml
├── internal/
│   ├── auth/            # JWT 逻辑
│   ├── config/          # 配置加载与校验
│   ├── dto/             # 数据结构
│   ├── fileutil/        # 文件工具函数（含 xxhash 校验）
│   ├── handler/         # HTTP handler 与中间件
│   └── task/            # 远程下载任务管理
├── files/               # 文件存储目录
├── html/                # 前端静态文件（编译时嵌入二进制）
└── .uploads/            # 临时目录，启动时自动清理
```

## 依赖

| 依赖 | 说明 |
|------|------|
| [golang-jwt](https://github.com/golang-jwt/jwt) | JWT 认证 |
| [xxhash](https://github.com/cespare/xxhash) | 分片上传的客户端（Wasm）+ 服务端双重文件完整性校验 |
| [go-colorable](https://github.com/mattn/go-colorable) | 旧版 Windows CMD 终端色彩回退适配 |

## License

MIT License © 2026 Zhang Feng

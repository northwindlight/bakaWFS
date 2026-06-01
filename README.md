# baka-web-file-server · 漫画版（manga）

> **⚠️ 本分支会改写原始图片文件。** 漫画版为了缩略图缓存,会把内容哈希写入图片自身的元数据（PNG `tEXt` chunk / JPEG `APP9` 段）。改写是原子的、不改变图像像素,但文件字节和修改时间会变,且不可逆。**不接受请用 [`main`](../../tree/main)。**

---

一个轻量、无状态的自托管文件服务器。基于 HTTPS + JWT,提供 RPC 风格的 API,支持文件上传、远程 URL 下载和进度追踪。附带开箱即用的 Web 界面,部署简单。

类似 filebrowser,但完全独立实现。后端由我完成,前端约 40% 由 AI 生成。

漫画版在主线全部能力之上,叠加了针对漫画浏览的缩略图与渐进式加载。

主线（通用文件服务器,**不碰原图**）见 [`main`](../../tree/main) 分支。

## 功能

- 文件浏览、上传、下载
- 文件管理：重命名/移动、复制（原子操作）、删除、新建文件夹
- 远程 URL 下载到服务器
- 分片上传 + xxhash 完整性校验
- JWT 登录鉴权 + Token 自动续签
- 并发远程下载任务管理（支持取消和进度追踪）
- 内置 Web UI（编译时嵌入二进制,也支持外部目录）
- 鉴权模式：开启后所有接口（含浏览/下载）强制登录,前端打开即显示登录界面
- 跨平台服务封装：一条命令注册为系统服务并自启动（Linux systemd / Windows SCM / macOS launchd）
- **列表缩略图** — `POST /thumbs` 批量拉取目录所有图片的 96px 缩略图,二进制流,列表直接显示预览
- **渐进加载** — 阅读器先显示 600px 中图,后台拉原图无缝替换;预加载相邻页
- **内容寻址缓存** — 缩略图以图像数据 xxhash64 为 key,文件移动/改名后缓存自动命中
- **批量预生成** — `go run ./cmd/pregenthumbs` 一次性生成全部缩略图

### 图片元数据

图片文件不是一整块二进制。PNG 由名为 chunk 的分段组成,其中有 `IDAT`（像素压缩流）和 `tEXt`（文本备注）;JPEG 同样,`SOS` 之后是像素扫描数据,`APPn` 段是应用自定义区。**看图器只解码像素流,元数据段直接跳过**,所以在 `tEXt` 或 `APPn` 里塞一条文字,画面纹丝不动。

漫画版利用这个特性:往图片自身的元数据区写入一条哈希值。文件还是标准 PNG / JPEG,所有看图器正常打开。

### 它会改什么

第一次访问某张图时（点开阅读器、浏览含图片的目录、或跑 `pregenthumbs` 批量预生成）：

1. 算图像像素数据的 xxhash64
2. 在文件头部插入一条元数据:PNG 是 `tEXt` chunk 放在 `IHDR` 之后,JPEG 是 `APP9` 段放在 `SOI` 之后,内容为 `bakawfs-xxh` + 16 位 hex 哈希,共约 30 字节
3. 文件增加几十字节、mtime 更新,**像素不变**（嵌入前后的解码结果逐像素一致）
4. 之后任何一次访问,只读文件头几 KB 取出哈希,不碰全文件

改动是原子的（临时文件 + rename,中断不毁原图）、幂等的（已有该 chunk 不再重复插入）。

### 这算破坏原图吗

如果你把"原图"理解为**一个完整的容器文件**——是的,字节变了。如果你把"原图"理解为**画面的像素内容**——没有,一个像素都没变。

图片文件天生是个双层结构:图像数据(你看到的画面)和元数据(给软件看的备注)。两者独立解码,互不干扰。相机写入 EXIF、PS 写入版权信息、你右键"属性"填备注——和本程序做的事完全一样:往容器里多挂了一条标签,画面纹丝不动。前面验证过的逐像素一致,就是证据。

真正破坏原图的行为是**重压缩**(JPEG 重新编码丢失细节)或**缩放裁切**(像素被修改)。本程序既不重压缩也不改画面,本质上只是在图片容器里留下了自己来过的记号。

### 缩略图缓存

**无状态原则。** bakaWFS 唯一的真理来源是文件系统——`/list` 实时 `os.ReadDir`,配置和用户是 YAML,鉴权是自包含的 JWT（服务端不记会话）,写操作排队但不维护操作日志之外的持久状态。全程没有两个需要互相同步的数据源。

如果为缩略图引入 hash→文件 映射表（SQLite、索引文件、哪怕是 `.thumbcache/.index/` 目录）,就第一次造出了第二个数据源——文件移动后映射要更新,删图后记录要清理,缓存丢失后映射和文件要核对。打破了程序的无状态根基。

**把哈希嵌在图片自身的元数据区,哈希是文件的一部分,不是额外的状态。** 文件移动哈希跟着走,删图哈希一起消失,不存在同步问题。

**为什么用内容哈希。** 缓存名是文件内容的 xxhash64。文件移动、改名——图像数据没变,哈希不变,缓存命中。

**为什么快。** 哈希嵌在文件头部（PNG `tEXt` 在 `IHDR` 后、JPEG `APP9` 在 `SOI` 后,都位于前几 KB）,第二次直接读头部,零全文件 I/O。

**为什么重算不变。** 哈希只覆盖图像像素流,元数据区不在被哈希的范围内,嵌入后重算结果一致,可校验。

## 预览

![主目录](assets/main-dir.png)
![文件操作面板](assets/file-ops.png)
![图片预览](assets/image-preview.png)
![上传管理](assets/upload-mgmt.png)

## 快速开始

```bash
./bakaWFS
```

首次运行会在当前目录生成 `config.yaml` 和 `users.yaml`,编辑后重新启动即可。

### 注册为系统服务

```bash
./bakaWFS install   # 安装并设为自启动
./bakaWFS start     # 立即启动服务
./bakaWFS stop      # 停止服务
./bakaWFS status    # 查看运行状态
./bakaWFS uninstall # 卸载服务
```

服务工作目录固定为二进制所在目录,`config.yaml` 与二进制放在同一目录即可。Linux 上需要 root 权限执行 install/uninstall。

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

启用 TLS 前请准备好证书。未启用 TLS 时,**不要将服务暴露在公网**。

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
html_dir:   "built-in"   # "built-in"=内置前端 / 外部目录路径 / 留空禁用前端（纯 API 模式）
temp_dir:   ".uploads"   # 临时目录（分片上传、远程下载暂存）
users_file: "users.yaml"
download_workers: 2       # 并发远程下载 worker 数
audit_log: ""             # 审计日志路径,留空关闭
cors_enabled: false       # 是否启用 CORS 跨域支持
auth_mode: false          # 鉴权模式：true = 所有接口需登录；false = 开放模式（仅写操作需登录）
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
| GET  | `/api/config` | 获取服务器配置（如 auth_mode） | 否 |
| POST | `/login` | 登录,返回 JWT | 否 |
| POST | `/verify` | 验证并续签 Token | 是 |
| GET  | `/list` | 获取文件目录树 | auth_mode 时需要 |
| GET  | `/files/*` | 下载文件（支持 Range,强制 Content-Disposition: attachment） | auth_mode 时需要 |
| GET  | `/thumb/<path>?size=` | 单图缩略图（list=96px / mid=600px） | 同 `/files` |
| POST | `/thumbs` | 批量缩略图,二进制流,body `{"paths":[...]}` | 同 `/files` |
| POST | `/upload` | 上传文件（整体上传） | 是 |
| POST | `/upload/chunk` | 上传单个分片 | 是 |
| POST | `/upload/merge` | 合并分片 | 是 |
| POST | `/remote-upload` | 从 URL 下载文件到服务器 | 是 |
| GET  | `/progress` | 查看远程下载进度 | 是 |
| POST | `/cancel` | 取消远程下载任务 | 是 |
| POST | `/delete` | 删除文件或目录 | 是 |
| POST | `/rename` | 重命名 / 移动文件或目录 | 是 |
| POST | `/copy` | 复制文件或目录 | 是 |
| POST | `/mkdir` | 新建文件夹 | 是 |

鉴权接口需在 Header 中携带 `Authorization: Bearer <token>`。`/thumbs` 返回自描述二进制流：`[uint32 数量]{[uint16 路径长][路径][uint32 图长][JPEG]}`。

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
├── cmd/
│   └── pregenthumbs/    # 批量预生成缩略图并嵌入哈希
├── internal/
│   ├── auth/            # JWT 逻辑
│   ├── config/          # 配置加载与校验
│   ├── fileops/         # 串行化文件变更队列 + 审计日志
│   ├── fileutil/        # 文件工具函数（含 xxhash 校验、目录树）
│   ├── handler/         # HTTP handler 与中间件
│   ├── task/            # 远程下载任务管理
│   └── thumb/           # 缩略图生成 + 哈希元数据读写
├── files/               # 文件存储目录
├── html/                # 前端静态文件（编译时嵌入二进制）
├── .uploads/            # 临时目录,启动时自动清理
└── .thumbcache/         # 缩略图缓存目录
```

## 依赖

| 依赖 | 说明 |
|------|------|
| [golang-jwt](https://github.com/golang-jwt/jwt) | JWT 认证 |
| [xxhash](https://github.com/cespare/xxhash) | 分片上传 + 缩略图缓存 key（客户端 Wasm + 服务端双重校验） |
| [go-colorable](https://github.com/mattn/go-colorable) | 旧版 Windows CMD 终端色彩回退适配 |
| [kardianos/service](https://github.com/kardianos/service) | 跨平台系统服务封装（systemd / SCM / launchd） |
| [x/image](https://golang.org/x/image) | 缩略图缩放与 WebP 解码 |

## License

MIT License © 2026 Zhang Feng

# bakaWFS API 文档

鉴权接口需在 Header 中携带 `Authorization: Bearer <token>`。

---

## 接口总览

| 方法 | 路径 | 说明 | 鉴权 |
|------|------|------|------|
| POST | `/login` | 登录，返回 JWT | 否 |
| POST | `/verify` | 验证并续签 token | 是 |
| GET | `/list` | 获取文件目录树 | 否 |
| GET | `/files/*` | 下载文件 | 否 |
| POST | `/upload` | 上传文件（整体） | 是 |
| POST | `/remote-upload` | 从 URL 下载文件到服务器 | 是 |
| GET | `/progress` | 查看远程下载进度 | 是 |
| POST | `/cancel` | 取消远程下载任务 | 是 |
| POST | `/delete` | 删除文件或目录 | 是 |
| POST | `/rename` | 重命名/移动文件或目录 | 是 |
| POST | `/copy` | 复制文件或目录 | 是 |
| POST | `/mkdir` | 新建文件夹 | 是 |
| POST | `/upload/chunk` | 上传单个分片 | 是 |
| POST | `/upload/merge` | 合并分片 | 是 |

---

## POST `/login`

用户登录，验证用户名和密码，返回 JWT token。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Body (JSON) | `username` | string | 是 | 用户名 |
| Body (JSON) | `password` | string | 是 | 密码 |

### 正确回复

| 字段 | 类型 | 说明 |
|------|------|------|
| `username` | string | 用户名 |
| `token` | string | JWT token |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体格式错误 | 400 | Bad Request |
| 用户名或密码错误 | 401 | Unauthorized |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/verify`

验证当前 token 是否有效，并返回续签后的新 token。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |

### 正确回复

| 字段 | 类型 | 说明 |
|------|------|------|
| `username` | string | 用户名 |
| `token` | string | 续签后的新 JWT token |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 缺少 token | 401 | Unauthorized: missing token |
| token 无效或已过期 | 401 | Unauthorized: invalid or expired token |
| 方法不是 POST | 405 | Method Not Allowed |

---

## GET `/list`

扫描服务器文件目录，返回完整目录树结构（JSON）。

### 请求参数

无。

### 正确回复

返回目录树对象（递归结构）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 文件或目录名 |
| `isDir` | bool | 是否为目录 |
| `size` | int64 | 文件大小（字节），目录为 0 |
| `children` | array | 子节点列表（仅目录有） |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 目录扫描失败 | 500 | Internal Server Error |
| 方法不是 GET | 405 | Method Not Allowed |

---

## GET `/files/*`

直接下载指定路径的文件，支持 Range 请求。路径为文件相对于服务器根目录的路径。

**示例：** `GET /files/videos/demo.mp4`

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Path | `*` | string | 是 | 文件相对路径 |
| Header | `Range` | string | 否 | 范围请求，如 `bytes=0-1023` |

### 正确回复

文件二进制内容，支持 206 Partial Content。

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 文件不存在 | 404 | Not Found |
| 方法不是 GET | 405 | Method Not Allowed |

---

## POST `/upload`

整体上传一个文件到服务器。文件大小限制 10GB。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Header | `X-Upload-Filename` | string | 是 | URL 编码的目标文件路径（相对路径） |
| Header | `Content-Length` | int | 是 | 文件字节数 |
| Body | — | binary | 是 | 文件二进制内容 |

### 正确回复

| 状态码 | 说明 |
|--------|------|
| 204 | 上传成功，无响应体 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 缺少必要 Header | 400 | Bad Request: Missing upload headers |
| 文件大小超过 10GB | 400 | Bad Request: File size exceeds limit |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 实际大小与 Content-Length 不符 | 400 | Bad Request: Size mismatch |
| 文件已存在 | 409 | Conflict: File already exists |
| 服务器写入失败 | 500 | Internal Server Error |

---

## POST `/remote-upload`

提交一个远程下载任务，服务器异步从指定 URL 下载文件。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `url` | string | 是 | 远程文件 URL |
| Body (JSON) | `filename` | string | 是 | 保存到服务器的目标文件路径（相对路径） |

### 正确回复

| 状态码 | 说明 |
|--------|------|
| 202 | 任务已接受，异步执行，无响应体 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体格式错误 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 文件已存在 | 409 | Conflict: File already exists |
| 下载队列已满 | 503 | Service Unavailable |

---

## GET `/progress`

查询当前所有远程下载任务的进度。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |

### 正确回复

返回以文件名为 key 的 JSON 对象，每个任务包含：

| 字段 | 类型 | 说明 |
|------|------|------|
| `username` | string | 发起任务的用户 |
| `downloadSize` | int64 | 已下载字节数 |
| `expectedSize` | int64 | 文件总字节数（未知时为 -1） |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 方法不是 GET | 405 | Method Not Allowed |

---

## POST `/cancel`

取消一个正在进行的远程下载任务。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `filename` | string | 是 | 要取消的任务文件名 |

### 正确回复

| 状态码 | 说明 |
|--------|------|
| 204 | 取消成功，无响应体 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体格式错误或 filename 为空 | 400 | Bad Request |
| 任务不存在 | 404 | Not Found: No such task in progress |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/delete`

删除指定文件或目录（递归删除）。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `path` | string | 是 | 要删除的文件/目录路径（相对路径） |

### 正确回复

| 状态码 | 说明 |
|--------|------|
| 204 | 删除成功，无响应体 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体字段缺失 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 目标不存在 | 404 | Not Found |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/rename`

重命名或移动文件/目录。源路径和目标路径不同时为移动操作。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `path` | string | 是 | 源文件/目录路径（相对路径） |
| Body (JSON) | `dst` | string | 是 | 目标路径（相对路径） |

### 正确回复

| 字段 | 类型 | 说明 |
|------|------|------|
| `path` | string | 最终落盘路径（自动续号处理冲突后的实际文件名） |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体字段缺失 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 源文件不存在 | 404 | Not Found |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/copy`

复制文件或目录到目标位置。操作先在临时目录完成拷贝，再通过串行队列原子落盘，目标路径冲突时自动续号。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `path` | string | 是 | 源文件/目录路径（相对路径） |
| Body (JSON) | `dst` | string | 是 | 目标路径（相对路径） |

### 正确回复

| 字段 | 类型 | 说明 |
|------|------|------|
| `path` | string | 最终落盘路径（自动续号处理冲突后的实际文件名） |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体字段缺失 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 源文件不存在 | 404 | Not Found |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/mkdir`

在指定路径创建新文件夹。路径冲突时自动续号。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `path` | string | 是 | 要创建的文件夹路径（相对路径） |

### 正确回复

| 字段 | 类型 | 说明 |
|------|------|------|
| `path` | string | 最终创建的文件夹路径 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体字段缺失 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 方法不是 POST | 405 | Method Not Allowed |

---

## POST `/upload/chunk`

上传单个分片，分片序号从 0 开始，幂等（重传自动覆盖）。单片大小上限 500MB。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Header | `X-Upload-Filename` | string | 是 | URL 编码的目标文件路径 |
| Header | `X-Chunk-Index` | int | 是 | 分片序号，从 0 开始 |
| Header | `Content-Length` | int | 是 | 本片字节数 |
| Body | — | binary | 是 | 分片二进制内容 |

### 正确回复

| 状态码 | 说明 |
|--------|------|
| 204 | 分片接收成功，无响应体 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 缺少必要 Header | 400 | Bad Request: Missing headers |
| 分片序号非法 | 400 | Bad Request: Invalid X-Chunk-Index |
| 分片大小超过 500MB | 400 | Bad Request: Chunk too large |
| 实际大小与 Content-Length 不符 | 400 | Bad Request: Size mismatch |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 目标文件已存在 | 409 | Conflict: File already exists |

---

## POST `/upload/merge`

所有分片上传完毕后，触发合并并校验整体 xxhash。

### 请求参数

| 位置 | 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|------|
| Header | `Authorization` | string | 是 | `Bearer <token>` |
| Body (JSON) | `filename` | string | 是 | 目标文件路径（相对路径） |
| Body (JSON) | `hash` | string | 是 | 完整文件的 xxhash 字符串 |
| Body (JSON) | `total` | int | 是 | 分片总数 |

### 正确回复

| 状态码 | Body | 说明 |
|--------|------|------|
| 204 | 无 | 合并成功 |
| 202 | `{"missing": [0, 2, ...]}` | 存在缺失分片，返回缺失序号列表，需补传 |

### 错误回复

| 场景 | 状态码 | 说明 |
|------|--------|------|
| 请求体字段缺失 | 400 | Bad Request |
| 路径包含非法字符 | 400 | Bad Request: Forbidden path |
| 目标文件已存在 | 409 | Conflict: File already exists |
| xxhash 不匹配（已清理所有分片，需整体重传） | 422 | `{"error": "hash mismatch"}` |
| 合并或移动失败 | 500 | Internal Server Error |
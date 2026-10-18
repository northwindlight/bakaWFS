# bakaWFS 维护约定

- 普通文件管理和漫画模式共用同一条 main；manga 分支仅留历史，公共修复不再双线 cherry-pick。
- `manga_mode` 默认 false。普通模式不能生成缩略图或向原图写元数据；前端从 `/api/config` 读取能力，不按分支猜测。
- 文件系统是唯一状态来源。删除只以同盘 rename 提交，物理清理在文件区外后台完成；禁止失败时递归删除源路径。
- 前端源文件在 html/，Tailwind 输入在 ui/。修改模板/依赖后 `npm ci && npm run build:ui`，连同 html/tailwind.css、html/vendor/ 的可公开分发产物与许可证一起提交；不复制本地私有字体和部署配置。
- 验证：`go test ./...`、`go vet ./...`、`npm test`；race 在支持的主机运行。浏览器回归只用临时目录，不读取真实 config.yaml/users.yaml。

# vendor (前端第三方资源)

本目录存放随信令服务器二进制一起 `go:embed` 嵌入的第三方前端库，**运行时不依赖外网/CDN**。

需要的文件（由仓库根目录的 `fetch-xterm.sh` / `fetch-xterm.bat` 自动下载，构建脚本会在编译前调用）：

- `xterm.min.js`            (xterm@5.3.0)
- `xterm.min.css`           (xterm@5.3.0)
- `xterm-addon-fit.min.js`  (xterm-addon-fit@0.8.0)

手动下载：

```bash
# 在仓库根目录执行
./fetch-xterm.sh        # Linux/macOS
fetch-xterm.bat         # Windows
```

> 这些文件不需要提交到版本库；只要在 `go build` 之前存在即可被嵌入。

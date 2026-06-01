// pregenthumbs 一次性预生成所有图片的 list + mid 两级缩略图缓存。
// 必须在 bakaWFS 项目根目录运行（与服务相同的工作目录），
// 这样源路径字符串与服务一致，缓存 key 才能命中。
//
//	go run ./cmd/pregenthumbs
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"bakaWFS/internal/fileutil"
	"bakaWFS/internal/thumb"
)

func main() {
	const (
		dirPath  = "files"       // 与 config.yaml 的 file_dir 一致
		cacheDir = ".thumbcache" // 与服务一致
	)

	gen, err := thumb.New(cacheDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	// 复用服务端同款遍历（ScanDir，/list 接口也用它，已正确处理软链目录），
	// 保证收集到的相对路径与服务侧的源路径字符串完全一致。
	root, err := fileutil.ScanDir(dirPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan file_dir:", err)
		os.Exit(1)
	}
	var paths []string
	var walk func(node *fileutil.Node, prefix string)
	walk = func(node *fileutil.Node, prefix string) {
		for _, c := range node.Children {
			rel := filepath.Join(prefix, c.Name)
			if c.Type == "dir" {
				walk(c, rel)
			} else if thumb.Supported(c.Name) {
				paths = append(paths, rel)
			}
		}
	}
	// ScanDir 返回的根节点 Name 是 dirPath 的 base，children 相对它，
	// 而服务侧源路径是 filepath.Join(dirPath, 相对路径)，所以前缀用 dirPath。
	walk(root, dirPath)
	fmt.Printf("发现 %d 张图片，开始生成 list + mid 缓存...\n", len(paths))

	// 限并发，给正在运行的正式服务留出 CPU
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan string, workers*2)
	var done int64
	var failed int64
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				okList := true
				if _, err := gen.Get(p, thumb.SizeList); err != nil {
					okList = false
				}
				if _, err := gen.Get(p, thumb.SizeMid); err != nil {
					okList = false
				}
				n := atomic.AddInt64(&done, 1)
				if !okList {
					atomic.AddInt64(&failed, 1)
				}
				if n%100 == 0 {
					fmt.Printf("  进度 %d/%d（失败 %d）\n", n, len(paths), atomic.LoadInt64(&failed))
				}
			}
		}()
	}

	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("完成：共 %d 张，失败 %d 张，缓存目录 %s\n",
		len(paths), atomic.LoadInt64(&failed), cacheDir)
}

package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// 两级尺寸：list 列表清晰小图；mid 中图（慢网下可直接看清漫画，也作阅读器占位的上一级）。
type Size int

const (
	SizeList Size = iota // 96px 宽，列表缩略图（适配 2x 屏的 48px 显示）
	SizeMid              // 600px 宽，中等画质
)

// 各尺寸的宽度与 JPEG 质量。
var sizeSpec = map[Size]struct {
	width   int
	quality int
	tag     string // 缓存 key 后缀，区分不同尺寸
}{
	SizeList: {width: 96, quality: 78, tag: "list"},
	SizeMid:  {width: 600, quality: 80, tag: "mid"},
}

// ParseSize 把前端传入的 size 参数转成枚举，未知值回退 list。
func ParseSize(s string) Size {
	if s == "mid" {
		return SizeMid
	}
	return SizeList
}

// maxConcurrentDecode 同时进行的图片解码数上限。单张解码最坏占 ~800MB
// （见 maxDecodePixels），限并发防止 /thumbs 批量接口 500 路径并发涌入时
// 内存峰值叠加爆掉。缓存命中的快路径不解码，不受此限制。
const maxConcurrentDecode = 4

// Generator 负责生成并缓存缩略图。
type Generator struct {
	cacheDir string
	// 同一 key 的并发请求只生成一次
	mu      sync.Mutex
	flights map[string]*sync.WaitGroup
	// 限制并发解码数（带缓冲 channel 当信号量），控制内存峰值
	decodeSem chan struct{}
}

func New(cacheDir string) (*Generator, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("创建缩略图缓存目录失败: %w", err)
	}
	return &Generator{
		cacheDir:  cacheDir,
		flights:   make(map[string]*sync.WaitGroup),
		decodeSem: make(chan struct{}, maxConcurrentDecode),
	}, nil
}

// Supported 判断扩展名是否支持生成缩略图。
func Supported(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// cacheName 用内容 hash + 尺寸 tag 作为缓存 key。
// 缓存以内容 hash 命名 → 文件移动/改名内容不变，缓存仍命中。
func (g *Generator) cacheName(hash string, sz Size) string {
	return filepath.Join(g.cacheDir, hash+"-"+sizeSpec[sz].tag+".jpg")
}

// resolveHash 取得文件内容的 xxhash64：
// 优先读图片元数据里已嵌入的 hash（只读文件头，快）；
// 没有则全量算一次，并原子写回元数据，下次直接读。
func (g *Generator) resolveHash(srcPath string) (string, error) {
	if h, err := ReadEmbeddedHash(srcPath); err == nil && h != "" {
		return h, nil
	}
	h, err := ComputeHash(srcPath)
	if err != nil {
		return "", err
	}
	if extSupportsEmbed(srcPath) {
		// 写回失败不致命：本次仍用算出的 hash，下次再尝试嵌入
		_ = WriteEmbeddedHash(srcPath, h)
	}
	return h, nil
}

// Get 返回 srcPath 指定尺寸的 JPEG 字节。命中缓存直接读，否则现场生成并写缓存。
func (g *Generator) Get(srcPath string, sz Size) ([]byte, error) {
	if _, err := os.Stat(srcPath); err != nil {
		return nil, err
	}
	hash, err := g.resolveHash(srcPath)
	if err != nil {
		return nil, err
	}
	cachePath := g.cacheName(hash, sz)

	if data, err := os.ReadFile(cachePath); err == nil {
		return data, nil
	}

	// 同 key 去重，避免并发重复生成
	g.mu.Lock()
	if wg, ok := g.flights[cachePath]; ok {
		g.mu.Unlock()
		wg.Wait()
		// 别人生成完了，再读一次缓存
		if data, err := os.ReadFile(cachePath); err == nil {
			return data, nil
		}
		// 别人失败了，自己来
	} else {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		g.flights[cachePath] = wg
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			delete(g.flights, cachePath)
			g.mu.Unlock()
			wg.Done()
		}()
	}

	data, err := g.generate(srcPath, sz)
	if err != nil {
		return nil, err
	}
	// 原子写缓存：temp + rename
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		os.Rename(tmp, cachePath)
	} else {
		os.Remove(tmp)
	}
	return data, nil
}

// maxDecodePixels 解码前的像素总数硬上限，防解压炸弹（小文件声明超大尺寸 →
// image.Decode 按尺寸分配内存 → OOM）。卡的是解码后内存（≈像素×4 字节），与文件
// 大小无关。2 亿像素 ≈ 800MB 解码内存，约 14000×14000：给超长条漫/高清扫图留足
// 空间，又稳稳挡住动辄声明几十亿像素的炸弹。超过的直接拒绝，绝不进 image.Decode。
const maxDecodePixels = 200 * 1000 * 1000

func (g *Generator) generate(srcPath string, sz Size) ([]byte, error) {
	// 限并发解码：控制同时驻留内存的解码图数量，防批量请求内存峰值叠加
	g.decodeSem <- struct{}{}
	defer func() { <-g.decodeSem }()

	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 先只读图头拿到声明尺寸（不解压像素，近乎零成本），超阈值直接拒绝，
	// 避免 image.Decode 按超大尺寸分配内存被打爆。int64 相乘防 int 溢出绕过。
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("读取图片尺寸失败: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, fmt.Errorf("图片尺寸过大，拒绝生成缩略图: %dx%d", cfg.Width, cfg.Height)
	}
	// DecodeConfig 已读走文件头，需把读取位置重置回开头再 Decode
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("解码图片失败: %w", err)
	}

	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, fmt.Errorf("图片尺寸为零")
	}
	spec := sizeSpec[sz]
	w := spec.width
	// 原图本身就比目标小，不放大，按原宽
	if b.Dx() < w {
		w = b.Dx()
	}
	h := w * b.Dy() / b.Dx()
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom 质量好，缩放开销对中图也可接受
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: spec.quality}); err != nil {
		return nil, fmt.Errorf("编码缩略图失败: %w", err)
	}
	return buf.Bytes(), nil
}

package test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"bakaWFS/internal/thumb"
)

// makeImage 生成一张测试用图片，写到 path（按扩展名选 png/jpeg）。
func makeImage(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	switch filepath.Ext(path) {
	case ".png":
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode png: %v", err)
		}
	case ".jpg", ".jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatalf("encode jpeg: %v", err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func decodable(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s (corrupted?): %v", path, err)
	}
	return img
}

func TestThumbSupported(t *testing.T) {
	cases := map[string]bool{
		"a.png": true, "a.PNG": true, "a.jpg": true, "a.jpeg": true,
		"a.webp": true, "a.gif": false, "a.txt": false, "noext": false,
	}
	for name, want := range cases {
		if got := thumb.Supported(name); got != want {
			t.Errorf("Supported(%q)=%v, want %v", name, got, want)
		}
	}
}

func TestThumbParseSize(t *testing.T) {
	if thumb.ParseSize("mid") != thumb.SizeMid {
		t.Error(`ParseSize("mid") should be SizeMid`)
	}
	if thumb.ParseSize("list") != thumb.SizeList {
		t.Error(`ParseSize("list") should be SizeList`)
	}
	if thumb.ParseSize("") != thumb.SizeList {
		t.Error(`ParseSize("") should fall back to SizeList`)
	}
	if thumb.ParseSize("garbage") != thumb.SizeList {
		t.Error(`ParseSize(unknown) should fall back to SizeList`)
	}
}

// 核心：hash 只覆盖图像数据，嵌入元数据后重算结果不变。
func TestComputeHashStableAfterEmbed(t *testing.T) {
	for _, ext := range []string{".png", ".jpg"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "img"+ext)
			makeImage(t, p, 60, 40)

			h1, err := thumb.ComputeHash(p)
			if err != nil {
				t.Fatalf("ComputeHash: %v", err)
			}
			if h1 == "" {
				t.Fatal("hash empty")
			}

			if err := thumb.WriteEmbeddedHash(p, h1); err != nil {
				t.Fatalf("WriteEmbeddedHash: %v", err)
			}
			// 嵌入后图片仍可解码（未损坏）
			decodable(t, p)

			// 重算图像数据 hash，应与嵌入前一致
			h2, err := thumb.ComputeHash(p)
			if err != nil {
				t.Fatalf("ComputeHash after embed: %v", err)
			}
			if h1 != h2 {
				t.Errorf("hash changed after embed: before=%s after=%s", h1, h2)
			}

			// 读回嵌入的 hash
			emb, err := thumb.ReadEmbeddedHash(p)
			if err != nil {
				t.Fatalf("ReadEmbeddedHash: %v", err)
			}
			if emb != h1 {
				t.Errorf("read-back hash=%s, want %s", emb, h1)
			}
		})
	}
}

func TestReadEmbeddedHashNone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "img.png")
	makeImage(t, p, 30, 30)
	// 未写入时应返回空，无错误
	h, err := thumb.ReadEmbeddedHash(p)
	if err != nil {
		t.Fatalf("ReadEmbeddedHash: %v", err)
	}
	if h != "" {
		t.Errorf("expected empty hash for unembedded file, got %s", h)
	}
}

// 写回应幂等：重复写不重复插入 chunk，文件大小稳定。
func TestWriteEmbeddedHashIdempotent(t *testing.T) {
	for _, ext := range []string{".png", ".jpg"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "img"+ext)
			makeImage(t, p, 50, 50)
			h, _ := thumb.ComputeHash(p)

			thumb.WriteEmbeddedHash(p, h)
			fi1, _ := os.Stat(p)
			thumb.WriteEmbeddedHash(p, h)
			fi2, _ := os.Stat(p)
			if fi1.Size() != fi2.Size() {
				t.Errorf("file size changed on second write: %d -> %d (not idempotent)", fi1.Size(), fi2.Size())
			}
		})
	}
}

func TestThumbGetAndCache(t *testing.T) {
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	p := filepath.Join(srcDir, "img.png")
	makeImage(t, p, 200, 300)

	gen, err := thumb.New(cacheDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// list 尺寸
	data, err := gen.Get(p, thumb.SizeList)
	if err != nil {
		t.Fatalf("Get list: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("thumb not valid jpeg: %v", err)
	}
	if img.Bounds().Dx() != 96 {
		t.Errorf("list thumb width=%d, want 96", img.Bounds().Dx())
	}

	// mid 尺寸
	mid, err := gen.Get(p, thumb.SizeMid)
	if err != nil {
		t.Fatalf("Get mid: %v", err)
	}
	mimg, _, _ := image.Decode(bytes.NewReader(mid))
	if mimg.Bounds().Dx() != 200 {
		// 原图宽 200 < 600，不放大，按原宽
		t.Errorf("mid thumb width=%d, want 200 (no upscale)", mimg.Bounds().Dx())
	}

	// 再取一次应命中缓存，结果一致
	data2, _ := gen.Get(p, thumb.SizeList)
	if !bytes.Equal(data, data2) {
		t.Error("cached list thumb differs from first generation")
	}
}

// 文件移动/改名后（内容不变）应命中同一缓存：缓存 key 基于内容 hash。
func TestThumbCacheSurvivesMove(t *testing.T) {
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	p1 := filepath.Join(srcDir, "orig.png")
	makeImage(t, p1, 120, 120)

	gen, _ := thumb.New(cacheDir)
	if _, err := gen.Get(p1, thumb.SizeList); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cacheCountBefore := countFiles(t, cacheDir)

	// 移动到新路径（内容不变，hash 已嵌入会跟着走）
	p2 := filepath.Join(srcDir, "sub", "renamed.png")
	os.MkdirAll(filepath.Dir(p2), 0755)
	if err := os.Rename(p1, p2); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if _, err := gen.Get(p2, thumb.SizeList); err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	cacheCountAfter := countFiles(t, cacheDir)

	if cacheCountAfter != cacheCountBefore {
		t.Errorf("move generated new cache files: before=%d after=%d (key not content-based)",
			cacheCountBefore, cacheCountAfter)
	}
}

func TestThumbGetUnsupported(t *testing.T) {
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	p := filepath.Join(srcDir, "notimage.txt")
	os.WriteFile(p, []byte("not an image"), 0644)
	gen, _ := thumb.New(cacheDir)
	if _, err := gen.Get(p, thumb.SizeList); err == nil {
		t.Error("expected error decoding non-image file")
	}
}

// TestThumbRejectsDecompressionBomb 构造一张文件很小、但 IHDR 声明超大尺寸的 PNG
// （真实解压炸弹的形态），断言 generate 在 DecodeConfig 阶段就拒绝，绝不进 image.Decode
// 去按 36 亿像素分配内存。这样测试本身也是轻量的（不真的造大图）。
func TestThumbRejectsDecompressionBomb(t *testing.T) {
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	p := filepath.Join(srcDir, "bomb.png")

	// 先造一张正常小图，再篡改 IHDR 里声明的宽高 + 修正 CRC。
	makeImage(t, p, 8, 8)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// PNG: 8 字节魔数 + IHDR chunk(4 长度 + 4 "IHDR" + 13 数据 + 4 CRC)。
	// 宽在偏移 16，高在偏移 20；CRC 覆盖 "IHDR"+13 字节数据(偏移 12..29)。
	binary.BigEndian.PutUint32(data[16:], 60000) // 宽
	binary.BigEndian.PutUint32(data[20:], 60000) // 高 → 36 亿像素，远超 1 亿阈值
	crc := crc32.ChecksumIEEE(data[12:29])
	binary.BigEndian.PutUint32(data[29:], crc)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatalf("write bomb: %v", err)
	}

	gen, _ := thumb.New(cacheDir)
	if _, err := gen.Get(p, thumb.SizeList); err == nil {
		t.Fatal("expected error for oversized image, got nil (bomb not rejected)")
	}
	// 拒绝路径不应留下任何缓存
	if n := countFiles(t, cacheDir); n != 0 {
		t.Errorf("rejected bomb still wrote %d cache files", n)
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
			n++
		}
	}
	return n
}

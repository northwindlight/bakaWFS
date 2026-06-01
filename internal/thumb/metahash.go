package thumb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// 把图片内容的 xxhash64 写进图片自身的元数据，避免每次都全量重算。
//
//   - PNG：插入一个 tEXt chunk，keyword = metaKeyword
//   - JPEG：插入一个 APP9 段，以 metaKeyword\0 开头
//
// 元数据不参与像素解码，所以 hash 覆盖“图像内容”、写进“元数据区”，互不影响。
// 文件移动/改名时元数据跟着走，hash 不变，缩略图缓存自然命中。

const metaKeyword = "bakawfs-xxh"

// headProbe 读取文件头部多少字节用于查找已嵌入的 hash。
// PNG 的 tEXt、JPEG 的 APPn 都在文件前部，几 KB 足够；找不到再回退全量。
const headProbe = 64 * 1024

// ComputeHash 计算“图像数据本身”的 xxhash64，跳过所有容器元数据：
//
//   - PNG：只 hash 全部 IDAT chunk 的内容（像素压缩流）
//   - JPEG：只 hash SOS 之后的熵编码扫描数据
//   - 其它格式：回退为整文件 hash
//
// 因此往文件里嵌入我们自己的 hash chunk 不会改变此结果——可随时重算、可校验。
func ComputeHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := xxhash.New()
	switch {
	case bytes.HasPrefix(data, pngMagic):
		if !hashPNGImageData(data, h) {
			return hashAll(data), nil
		}
	case bytes.HasPrefix(data, jpegMagic):
		if !hashJPEGImageData(data, h) {
			return hashAll(data), nil
		}
	default:
		h.Write(data)
	}
	return fmt.Sprintf("%016x", h.Sum64()), nil
}

func hashAll(data []byte) string {
	h := xxhash.New()
	h.Write(data)
	return fmt.Sprintf("%016x", h.Sum64())
}

// hashPNGImageData 把所有 IDAT chunk 的内容喂给 h。成功返回 true。
func hashPNGImageData(data []byte, h *xxhash.Digest) bool {
	off := len(pngMagic)
	found := false
	for off+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[off:]))
		ctype := string(data[off+4 : off+8])
		dataStart := off + 8
		if ctype == "IEND" {
			break
		}
		if dataStart+length > len(data) {
			return false // 结构异常，回退整文件
		}
		if ctype == "IDAT" {
			h.Write(data[dataStart : dataStart+length])
			found = true
		}
		off = dataStart + length + 4 // +4 CRC
	}
	return found
}

// hashJPEGImageData 把 SOS 之后的扫描数据喂给 h。成功返回 true。
func hashJPEGImageData(data []byte, h *xxhash.Digest) bool {
	off := 2 // 跳过 SOI
	for off+4 <= len(data) {
		if data[off] != 0xff {
			return false
		}
		marker := data[off+1]
		if marker == 0xda { // SOS：其后到文件末（去掉结尾 EOI）即扫描数据
			segLen := int(binary.BigEndian.Uint16(data[off+2:]))
			scanStart := off + 2 + segLen
			if scanStart > len(data) {
				return false
			}
			scanEnd := len(data)
			// 去掉结尾 EOI(0xFFD9)，使其不受影响
			if scanEnd >= 2 && data[scanEnd-2] == 0xff && data[scanEnd-1] == 0xd9 {
				scanEnd -= 2
			}
			h.Write(data[scanStart:scanEnd])
			return true
		}
		segLen := int(binary.BigEndian.Uint16(data[off+2:]))
		off = off + 2 + segLen
	}
	return false
}

// ReadEmbeddedHash 只读文件头部，尝试取出之前写入的 hash。
// 没有则返回空字符串（非错误）。
func ReadEmbeddedHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	head := make([]byte, headProbe)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	head = head[:n]

	switch {
	case bytes.HasPrefix(head, pngMagic):
		return readPNGHash(head), nil
	case bytes.HasPrefix(head, jpegMagic):
		return readJPEGHash(head), nil
	}
	return "", nil
}

// WriteEmbeddedHash 把 hash 原子写回图片元数据（temp + rename，绝不写坏原图）。
// 不支持的格式返回 nil（静默跳过，调用方仍可用算出的 hash 当此次 key）。
func WriteEmbeddedHash(path, hash string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var out []byte
	switch {
	case bytes.HasPrefix(data, pngMagic):
		out, err = injectPNGHash(data, hash)
	case bytes.HasPrefix(data, jpegMagic):
		out, err = injectJPEGHash(data, hash)
	default:
		return nil // WebP 等暂不写回
	}
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".xxh-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	// 保留原文件权限
	if fi, statErr := os.Stat(path); statErr == nil {
		os.Chmod(tmpName, fi.Mode())
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ── PNG ──────────────────────────────────────────────────────

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// readPNGHash 扫描 PNG chunk，找 keyword == metaKeyword 的 tEXt。
func readPNGHash(data []byte) string {
	off := len(pngMagic)
	for off+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[off:]))
		ctype := string(data[off+4 : off+8])
		dataStart := off + 8
		if ctype == "IEND" {
			break
		}
		if ctype == "tEXt" && dataStart+length <= len(data) {
			payload := data[dataStart : dataStart+length]
			if i := bytes.IndexByte(payload, 0); i >= 0 {
				if string(payload[:i]) == metaKeyword {
					return string(payload[i+1:])
				}
			}
		}
		off = dataStart + length + 4 // +4 CRC
	}
	return ""
}

// injectPNGHash 在 IEND 前插入 tEXt chunk。若已存在同 keyword 则先不重复（直接返回原数据）。
func injectPNGHash(data []byte, hash string) ([]byte, error) {
	if readPNGHash(data) != "" {
		return data, nil
	}
	// 插在 IHDR 之后（PNG 首个 chunk）。这样 tEXt 落在文件头部，
	// 读取时只需 headProbe 即可命中，无需读到文件末尾的 IEND。
	off := len(pngMagic)
	if off+8 > len(data) {
		return nil, fmt.Errorf("PNG 头部异常")
	}
	if string(data[off+4:off+8]) != "IHDR" {
		return nil, fmt.Errorf("PNG 首个 chunk 不是 IHDR")
	}
	ihdrLen := int(binary.BigEndian.Uint32(data[off:]))
	insertAt := off + 8 + ihdrLen + 4 // 跳过 IHDR 整块（含 CRC）
	if insertAt > len(data) {
		return nil, fmt.Errorf("PNG IHDR 截断")
	}

	payload := append([]byte(metaKeyword), 0)
	payload = append(payload, []byte(hash)...)
	chunk := buildPNGChunk("tEXt", payload)

	out := make([]byte, 0, len(data)+len(chunk))
	out = append(out, data[:insertAt]...)
	out = append(out, chunk...)
	out = append(out, data[insertAt:]...)
	return out, nil
}

func buildPNGChunk(ctype string, payload []byte) []byte {
	chunk := make([]byte, 0, 12+len(payload))
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(payload)))
	chunk = append(chunk, lb[:]...)
	chunk = append(chunk, []byte(ctype)...)
	chunk = append(chunk, payload...)
	crc := crc32.NewIEEE()
	crc.Write([]byte(ctype))
	crc.Write(payload)
	var cb [4]byte
	binary.BigEndian.PutUint32(cb[:], crc.Sum32())
	chunk = append(chunk, cb[:]...)
	return chunk
}

// ── JPEG ─────────────────────────────────────────────────────

var jpegMagic = []byte{0xff, 0xd8}

const jpegAppMarker = 0xE9 // APP9

// readJPEGHash 扫描 JPEG 段，找 APP9 中以 metaKeyword\0 开头的。
func readJPEGHash(data []byte) string {
	off := 2 // 跳过 SOI
	for off+4 <= len(data) {
		if data[off] != 0xff {
			break
		}
		marker := data[off+1]
		// SOS(0xDA) 之后是压缩数据，停止扫描
		if marker == 0xda {
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[off+2:]))
		segStart := off + 4
		segEnd := off + 2 + segLen
		if segEnd > len(data) {
			break
		}
		if marker == jpegAppMarker {
			payload := data[segStart:segEnd]
			prefix := append([]byte(metaKeyword), 0)
			if bytes.HasPrefix(payload, prefix) {
				return string(payload[len(prefix):])
			}
		}
		off = segEnd
	}
	return ""
}

// injectJPEGHash 在 SOI 后插入 APP9 段。
func injectJPEGHash(data []byte, hash string) ([]byte, error) {
	if readJPEGHash(data) != "" {
		return data, nil
	}
	payload := append([]byte(metaKeyword), 0)
	payload = append(payload, []byte(hash)...)
	segLen := len(payload) + 2 // 段长含长度字段自身
	if segLen > 0xffff {
		return nil, fmt.Errorf("hash 段过长")
	}
	seg := make([]byte, 0, 4+len(payload))
	seg = append(seg, 0xff, jpegAppMarker)
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(segLen))
	seg = append(seg, lb[:]...)
	seg = append(seg, payload...)

	out := make([]byte, 0, len(data)+len(seg))
	out = append(out, data[:2]...) // SOI
	out = append(out, seg...)
	out = append(out, data[2:]...)
	return out, nil
}

// extSupportsEmbed 判断该扩展名是否支持写回元数据。
func extSupportsEmbed(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

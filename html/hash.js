// Esm CDN 引入
import xxhashWasm from 'https://cdn.jsdelivr.net/npm/xxhash-wasm@1.0.2/esm/xxhash-wasm.js';

let xxhashCreate = null;

// 【修改点】: 伴随模块加载，立即在后台开始请求和初始化 Wasm
const initPromise = xxhashWasm().then(hasher => {
    xxhashCreate = hasher.create64;
}).catch(err => console.error("xxhash 初始化失败:", err));

// 计算整个 File 的 xxhash64
export async function hashFile(file, onProgress) {
    await initPromise; // 确保 Wasm 已加载完毕
    const h = xxhashCreate();
    const BLOCK = 4 * 1024 * 1024; // 4MB 块读取，避免内存溢出
    let offset = 0;
    while (offset < file.size) {
        const slice = file.slice(offset, offset + BLOCK);
        const buf = await slice.arrayBuffer();
        h.update(new Uint8Array(buf));
        offset += BLOCK;
        if (onProgress) onProgress(Math.min(offset, file.size), file.size);
    }
    // 返回 16 位 hex
    return h.digest().toString(16).padStart(16, '0');
}
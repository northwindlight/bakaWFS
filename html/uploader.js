import { hashFile } from './hash.js';

export const DIRECT_UPLOAD_THRESHOLD = 10 * 1024 * 1024; // 10 MB

export function calcChunkSize(fileSize) {
    const MB = 1024 * 1024;
    if (fileSize <= 1 * 1024 * 1024 * 1024) return 10 * MB;
    if (fileSize <= 10 * 1024 * 1024 * 1024) return 50 * MB;
    return 100 * MB;
}

// ── 直传逻辑（小文件）
export function doDirectUpload({ file, fullPath, taskId, token, API_BASE, progressData, uploadTasks, i18n }) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        uploadTasks.value[taskId] = xhr;
        xhr.open('POST', `${API_BASE}/upload`, true);
        xhr.setRequestHeader('Authorization', `Bearer ${token}`);
        xhr.setRequestHeader('X-Upload-Filename', encodeURIComponent(fullPath));

        let lastLoaded = 0, lastTime = Date.now(), speedSamples = [];
        xhr.upload.onprogress = (e) => {
            if (!e.lengthComputable) return;
            const now = Date.now();
            const dt = (now - lastTime) / 1000;
            const dl = e.loaded - lastLoaded;
            const instant = dt > 0 ? dl / dt : 0;
            speedSamples.push(instant);
            if (speedSamples.length > 5) speedSamples.shift();
            const avg = speedSamples.reduce((a, b) => a + b, 0) / speedSamples.length;
            if (progressData.value[taskId]) {
                progressData.value[taskId].downloadSize = e.loaded;
                progressData.value[taskId].speed = avg;
            }
            lastLoaded = e.loaded;
            lastTime = now;
        };

        xhr.onload = () => {
            if (xhr.status >= 200 && xhr.status < 300) resolve();
            else reject(new Error(xhr.responseText || i18n.unknownError));
        };
        xhr.onerror = () => reject(new Error(i18n.uploadNetworkError));
        xhr.onabort = () => resolve(); // 中断不视为报错
        xhr.send(file);
    });
}

function uploadChunkXHR(url, chunk, headers, onProgress, signal) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('POST', url);
        for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);

        if (signal) {
            signal.onabort = () => xhr.abort();
        }
        xhr.upload.onprogress = (e) => {
            if (e.lengthComputable) onProgress(e.loaded, e.total);
        };
        xhr.onload  = () => xhr.status < 300 ? resolve() : reject(new Error(`HTTP ${xhr.status}`));
        xhr.onerror = () => reject(new Error('Network error'));
        xhr.onabort = () => reject(new DOMException('Aborted', 'AbortError'));

        xhr.send(chunk);
    });
}

export async function doChunkUpload({ file, fullPath, taskId, total, chunkSize, token, API_BASE, progressData, controller }) {
    // 1. Hash 校验
    const fileHash = await hashFile(file, (done, expected) => {
        if (progressData.value[taskId]) {
            progressData.value[taskId].hashDone = done;
            progressData.value[taskId].hashTotal = expected;
        }
    });

    if (controller.signal.aborted) return;

    if (progressData.value[taskId]) {
        progressData.value[taskId].phase = 'uploading';
        progressData.value[taskId].downloadSize = 0;
        progressData.value[taskId].expectedSize = file.size;
    }
    let lastSent = 0, lastTime = Date.now(), speedSamples = [];
    for (let i = 0; i < total; i++) {

        if (controller.signal.aborted) return;
        const start = i * chunkSize;
        const end = Math.min(start + chunkSize, file.size);
        const chunk = file.slice(start, end);

        lastSent = start;
        lastTime = Date.now();

        let attempts = 0;
        while (true) {
            attempts++;
            try {
                await uploadChunkXHR(
                    `${API_BASE}/upload/chunk`,
                    chunk,
                    {
                        'Authorization': `Bearer ${token}`,
                        'X-Upload-Filename': encodeURIComponent(fullPath),
                        'X-Chunk-Index': String(i),
                        'Content-Length': String(end - start),
                    },
                    (loaded) => {
                        const now = Date.now();
                        const base = i * chunkSize;
                        const task = progressData.value[taskId];
                        if (!task) return;
                        task.downloadSize = base + loaded;
                        // 速度用片内增量算，串行下很准
                        const dt = (now - lastTime) / 1000;
                        if (dt > 0.2) {
                            const dl = (base + loaded) - lastSent;
                            const speed = dl / dt;
                            speedSamples.push(speed);
                            if (speedSamples.length > 5) speedSamples.shift();
                            task.speed = speedSamples.reduce((a, b) => a + b, 0) / speedSamples.length;
                            task.remaining = task.speed > 0 ? (file.size - task.downloadSize) / task.speed : null;
                            lastSent = base + loaded;
                            lastTime = now;
                        }
                    },
                    (loaded) => {
                        // 片内流式进度：已传完的片 + 当前片进度
                        const base = i * chunkSize;
                        if (progressData.value[taskId]) {
                            progressData.value[taskId].downloadSize = base + loaded;
                        }
                    },
                    controller.signal
                );
                break;
            } catch (err) {
                if (controller.signal.aborted) return;
                if (attempts >= 3) throw new Error(`分片 ${i} 失败: ${err.message}`);
                await new Promise(r => setTimeout(r, 800 * attempts));
            }

        }
    }
    if (controller.signal.aborted) return;

    // 3. 发送合并请求
    if (progressData.value[taskId]) progressData.value[taskId].phase = 'merging';

    let mergeAttempts = 0;
    while (true) {
        if (controller.signal.aborted) return;
        mergeAttempts++;
        if (mergeAttempts > 5) throw new Error('合并重试次数过多');

        const mergeRes = await fetch(`${API_BASE}/upload/merge`, {
            method: 'POST',
            signal: controller.signal,
            headers: {
                'Authorization': `Bearer ${token}`,
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ filename: fullPath, hash: fileHash, total }),
        });

        if (mergeRes.status === 204) {
            break; // 成功合并
        } else if (mergeRes.status === 202) {
            const { missing } = await mergeRes.json();
            for (const i of missing) {
                if (controller.signal.aborted) return;
                const start = i * chunkSize;
                const end = Math.min(start + chunkSize, file.size);
                await uploadChunkXHR(
                    `${API_BASE}/upload/chunk`,
                    file.slice(start, end),
                    {
                        'Authorization': `Bearer ${token}`,
                        'X-Upload-Filename': encodeURIComponent(fullPath),
                        'X-Chunk-Index': String(i),
                        'Content-Length': String(end - start),
                    },
                    () => {}, // 补传不更新进度
                    controller.signal
                );
            }
        } else {
            throw new Error(`合并失败 HTTP ${mergeRes.status}: ${await mergeRes.text()}`);
        }
    }
}
// Form uploads use ETag V1. Native multipart with non-4-MiB parts uses
// ETag V2: SHA-1 of each part's V1 digest (without its prefix), prefixed 0x9e.
// Read at most 4 MiB at a time, including for very large native parts.
export function cloudPartSize(size) {
    const MB = 1024 * 1024;
    return Math.max(8 * MB, Math.ceil(Math.ceil(size / 10000) / MB) * MB);
}
export async function kodoETag(file, onProgress = () => {}, signal) {
    const encode = bytes => btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const concat = blocks => { const joined = new Uint8Array(blocks.length * 20); blocks.forEach((b, i) => joined.set(b, i * 20)); return joined; };
    const sha1 = async data => new Uint8Array(await crypto.subtle.digest('SHA-1', data));
    const partDigest = async (start, end) => {
        const blocks = [];
        for (let offset = start; offset < end || (start === end && offset === start); offset += 4 * 1024 * 1024) {
            if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');
            blocks.push(await sha1(await file.slice(offset, Math.min(offset + 4 * 1024 * 1024, end)).arrayBuffer()));
            onProgress(Math.min(end, offset + 4 * 1024 * 1024), file.size);
            if (start === end) break;
        }
        const out = new Uint8Array(21); out[0] = blocks.length === 1 ? 0x16 : 0x96;
        out.set(blocks.length === 1 ? blocks[0] : await sha1(concat(blocks)), 1);
        return out;
    };
    if (file.size <= DIRECT_UPLOAD_THRESHOLD) return encode(await partDigest(0, file.size));
    const parts = [], size = cloudPartSize(file.size);
    for (let start = 0; start < file.size; start += size) parts.push((await partDigest(start, Math.min(start + size, file.size))).slice(1));
    const out = new Uint8Array(21); out[0] = 0x9e; out.set(await sha1(concat(parts)), 1); return encode(out);
}

export async function doCloudUpload({ file, fullPath, taskId, token, API_BASE, progressData, controller, onCommitted }) {
    const signal = controller.signal;
    const update = (fields) => { if (progressData.value[taskId]) Object.assign(progressData.value[taskId], fields); };
    const etag = await kodoETag(file, (hashDone, hashTotal) => update({ hashDone, hashTotal }), signal);
    // Stable across reselect/reload: the same user/file/path resumes the same job.
    const storageKey = 'bakawfs:upload:' + API_BASE + ':' + (localStorage.getItem('baka_user') || '') + ':' + fullPath + ':' + file.size + ':' + etag;
    let operationId = localStorage.getItem(storageKey);
    if (!operationId) { operationId = crypto.randomUUID(); localStorage.setItem(storageKey, operationId); }
    const api = async (route, body) => {
        for (let attempt = 0; ; attempt++) {
            try {
                const res = await fetch(API_BASE + route, { method: 'POST', signal, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                if (!res.ok) { const err = new Error(await res.text()); err.retryable = res.status >= 500 && res.status !== 507; err.status = res.status; throw err; }
                return await res.json();
            } catch (e) {
                if (signal.aborted || e.retryable === false || attempt >= 3) throw e;
                await new Promise(r => setTimeout(r, 500 * (attempt + 1)));
            }
        }
    };
    const prepareRequest = { filename: fullPath, size: file.size, etag, operation_id: operationId };
    let prepared;
    try { prepared = await api('/cloud/upload/prepare', prepareRequest); }
    catch (e) { if (e.status === 409) localStorage.removeItem(storageKey); throw e; }
    if (prepared.state === 'published') { localStorage.removeItem(storageKey); return; }
    if (prepared.state !== 'pending') { localStorage.removeItem(storageKey); throw new Error(prepared.state); }
    const cancel = () => {
        localStorage.removeItem(storageKey);
        fetch(API_BASE + '/cancel', { method: 'POST', keepalive: true, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ job_id: prepared.job_id, filename: fullPath }) }).then(res => { if (res.status === 409) { alert('文件已提交，取消未生效'); onCommitted?.(); } }).catch(() => {});
    };
    signal.addEventListener('abort', cancel, { once: true });
    const send = (url, method, body, base) => new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest(); xhr.open(method, url);
        if (method === 'PUT') xhr.setRequestHeader('Authorization', 'UpToken ' + prepared.token);
        const abort = () => xhr.abort(); signal.addEventListener('abort', abort, { once: true });
        let previous = 0, previousTime = Date.now();
        xhr.upload.onprogress = (e) => {
            const now = Date.now(); const speed = (e.loaded - previous) / Math.max(.001, (now - previousTime) / 1000);
            update({ downloadSize: Math.min(file.size, base + e.loaded), speed, remaining: speed > 0 ? (file.size - base - e.loaded) / speed : null }); previous = e.loaded; previousTime = now;
        };
        xhr.onload = () => { signal.removeEventListener('abort', abort); if (xhr.status >= 200 && xhr.status < 300) { try { resolve(JSON.parse(xhr.responseText)); } catch (e) { reject(e); } } else reject(new Error(`Kodo HTTP ${xhr.status}`)); };
        xhr.onerror = () => { signal.removeEventListener('abort', abort); reject(new Error('Upload network error')); };
        xhr.onabort = () => reject(new DOMException('Aborted', 'AbortError'));
        if (signal.aborted) reject(new DOMException('Aborted', 'AbortError')); else xhr.send(body);
    });
    try {
        update({ phase: 'uploading', downloadSize: 0 });
        const parts = [];
        const resumed = new Map((prepared.parts || []).map(p => [p.partNumber, p]));
        if (prepared.upload_id) {
            for (let start = 0, number = 1; start < file.size; start += prepared.part_size, number++) {
                if (resumed.has(number)) { parts.push(resumed.get(number)); update({ downloadSize: Math.min(file.size, start + prepared.part_size) }); continue; }
                let result;
                for (let attempt = 0; ; attempt++) {
                    try { result = await send(prepared.multipart_url + '/' + number, 'PUT', file.slice(start, start + prepared.part_size), start); break; }
                    catch (e) { if (signal.aborted || attempt >= 3) throw e; if (e.message.includes('HTTP 401')) prepared = await api('/cloud/upload/prepare', prepareRequest); await new Promise(r => setTimeout(r, 800 * (attempt + 1))); }
                }
                parts.push({ partNumber: number, etag: result.etag });
            }
        } else {
            const form = new FormData(); form.append('token', prepared.token); form.append('key', prepared.key); form.append('file', file, file.name);
            // A lost response is recovered by stat during Complete; the object is immutable.
            for (let attempt = 0; ; attempt++) {
                try { await send(prepared.upload_url, 'POST', form, 0); break; }
                catch (e) {
                    if (signal.aborted) throw e;
                    if (e.message.includes('HTTP 614') || attempt >= 3) break;
                    if (e.message.includes('HTTP 401')) prepared = await api('/cloud/upload/prepare', prepareRequest);
                    form.set('token', prepared.token);
                    await new Promise(r => setTimeout(r, 600 * (attempt + 1)));
                }
            }
        }
        update({ phase: 'merging' });
        try { await api('/cloud/upload/complete', { job_id: prepared.job_id, parts }); }
        catch (e) { if (e.status === 409) localStorage.removeItem(storageKey); throw e; }
        localStorage.removeItem(storageKey);
    } finally { signal.removeEventListener('abort', cancel); }
}

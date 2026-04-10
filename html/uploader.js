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
        xhr.open('POST', `${API_BASE}/update`, true);
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
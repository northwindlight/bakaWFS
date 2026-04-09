import { hashFile } from './hash.js';

export const DIRECT_UPLOAD_THRESHOLD = 1024 * 1024; // 10 MB

export function calcChunkSize(fileSize) {
    const MB = 1024 * 1024;
    if (fileSize <= 1 * 1024 * 1024 * 1024) return MB / 100;
    if (fileSize <= 10 * 1024 * 1024 * 1024) return MB / 50;
    return 10 * MB;
}

// 简单的并发池封装
async function runParallel(tasks, concurrency) {
    const pool = new Set();
    for (const task of tasks) {
        const p = task().finally(() => pool.delete(p));
        pool.add(p);
        if (pool.size >= concurrency) {
            await Promise.race(pool);
        }
    }
    await Promise.all(pool);
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

// ── 切片上传逻辑（并发版本）
export async function doChunkUpload({ file, fullPath, taskId, total, chunkSize, token, API_BASE, progressData, controller }) {
    // 1. Hash 校验
    const fileHash = await hashFile(file, (done, expected) => {
        if (progressData.value[taskId]) {
            progressData.value[taskId].downloadSize = done;
            progressData.value[taskId].expectedSize = expected;
        }
    });

    if (controller.signal.aborted) return;

    if (progressData.value[taskId]) {
        progressData.value[taskId].phase = 'uploading';
        progressData.value[taskId].downloadSize = 0;
        progressData.value[taskId].expectedSize = file.size;
    }

    let totalSent = 0;
    let lastSent = 0;
    let lastTime = Date.now();
    let speedSamples = [];

    // 并发下平滑计算速度
    const updateProgress = (chunkLen) => {
        totalSent += chunkLen;
        const now = Date.now();
        const dt = (now - lastTime) / 1000;
        const task = progressData.value[taskId];
        if (!task) return;

        task.downloadSize = totalSent;
        task.sent += 1;

        // 限制0.5秒计算一次速度，防止并发返回时UI闪烁
        if (dt >= 0.5) {
            const dl = totalSent - lastSent;
            const instant = dl / dt;
            speedSamples.push(instant);
            if (speedSamples.length > 5) speedSamples.shift();
            const avg = speedSamples.reduce((a, b) => a + b, 0) / speedSamples.length;

            task.speed = avg;
            if (avg > 0) task.remaining = (file.size - totalSent) / avg;
            
            lastSent = totalSent;
            lastTime = now;
        }
    };

    const concurrency = Math.min(20, Math.max(3, Math.ceil(total / 10)));
    const tasks = [];

    // 2. 将所有分片任务丢入数组
    for (let i = 0; i < total; i++) {
        tasks.push(async () => {
            if (controller.signal.aborted) return;
            const start = i * chunkSize;
            const end = Math.min(start + chunkSize, file.size);
            const chunk = file.slice(start, end);
            const chunkLen = end - start;

            let attempts = 0;
            while (true) {
                attempts++;
                try {
                    const res = await fetch(`${API_BASE}/upload/chunk`, {
                        method: 'POST',
                        signal: controller.signal,
                        headers: {
                            'Authorization': `Bearer ${token}`,
                            'X-Upload-Filename': encodeURIComponent(fullPath),
                            'X-Chunk-Index': String(i),
                            'Content-Length': String(chunkLen),
                        },
                        body: chunk,
                    });
                    if (!res.ok) throw new Error(`HTTP ${res.status}: ${await res.text()}`);
                    break;
                } catch (err) {
                    if (controller.signal.aborted) return;
                    if (attempts >= 3) throw new Error(`分片 ${i} 上传失败: ${err.message}`);
                    await new Promise(r => setTimeout(r, 800 * attempts));
                }
            }
            updateProgress(chunkLen);
        });
    }

    // 执行并发发送
    await runParallel(tasks, concurrency);

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
            // 服务器返回缺片列表，复用并发逻辑补传
            const { missing } = await mergeRes.json();
            console.warn('补传缺失分片:', missing);
            const missingTasks = missing.map(i => async () => {
                if (controller.signal.aborted) return;
                const start = i * chunkSize;
                const end = Math.min(start + chunkSize, file.size);
                const chunk = file.slice(start, end);
                const chunkLen = end - start;
                const res = await fetch(`${API_BASE}/upload/chunk`, {
                    method: 'POST',
                    signal: controller.signal,
                    headers: {
                        'Authorization': `Bearer ${token}`,
                        'X-Upload-Filename': encodeURIComponent(fullPath),
                        'X-Chunk-Index': String(i),
                        'Content-Length': String(chunkLen),
                    },
                    body: chunk,
                });
                if (!res.ok) throw new Error(`补传分片 ${i} 失败`);
            });
            await runParallel(missingTasks, concurrency);
        } else {
            throw new Error(`合并失败 HTTP ${mergeRes.status}: ${await mergeRes.text()}`);
        }
    }
}
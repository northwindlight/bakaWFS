const { createApp, ref, computed, watch, onMounted, onUnmounted } = Vue;

// 导入拆分的模块
import { i18n } from './i18n.js';
import { 
    generateTaskId, isValidUrl, formatSpeed, formatSize, 
    naturalCompare, classifyFile, fileTypeIcon, getFileExt 
} from './utils.js';
import { 
    doDirectUpload, doChunkUpload, calcChunkSize, DIRECT_UPLOAD_THRESHOLD 
} from './uploader.js';

createApp({
    setup() {
        const API_BASE = '';
        const rootData = ref(null);
        const currentDir = ref(null);
        const pathStack = ref([]);
        const loading = ref(false);
        const showLogin = ref(false);
        const showRemoteModal = ref(false);
        const showProgressModal = ref(false);
        const showUploadMenu = ref(false);
        const remoteUrl = ref('');
        const progressData = ref({});
        const lastProgress = ref({});
        const uploadTasks = ref({});
        let pollingInterval = null;

        // 文件操作状态
        const opsPanelFor = ref(null);
        const showRenameModal = ref(false);
        const showCopyModal = ref(false);
        const showDeleteConfirm = ref(false);
        const opsTarget = ref(null);
        const pickerStack = ref([]);
        const renameName = ref('');
        const copyName = ref('');
        const showMkdirModal = ref(false);
        const mkdirName = ref('');
        const filenameToTaskId = {};

        const isLoggedIn = ref(!!localStorage.getItem('baka_token'));
        const currentUser = ref(localStorage.getItem('baka_user') || '');
        const loginForm = ref({ username: '', password: '' });
        const authMode = ref(false);

        // 验证 token
        const verifyToken = async () => {
            const token = localStorage.getItem('baka_token');
            if (!token) return false;
            try {
                const res = await fetch(`${API_BASE}/verify`, {
                    method: 'POST',
                    headers: { 'Authorization': `Bearer ${token}` }
                });
                if (res.ok) {
                    const data = await res.json();
                    if (data.token && data.token !== token) {
                        localStorage.setItem('baka_token', data.token);
                    }
                    return true;
                } else {
                    localStorage.removeItem('baka_token');
                    localStorage.removeItem('baka_user');
                    isLoggedIn.value = false;
                    currentUser.value = '';
                    alert(i18n.tokenExpired);
                    return false;
                }
            } catch (error) {
                console.error('Token验证失败:', error);
                return false;
            }
        };

        const handleLogin = async () => {
            if (!loginForm.value.username) return alert(i18n.unknownName);
            try {
                const res = await fetch(`${API_BASE}/login`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(loginForm.value)
                });
                if (!res.ok) throw new Error(i18n.wrongPassword);
                const data = await res.json();
                localStorage.setItem('baka_token', data.token);
                localStorage.setItem('baka_user', data.username);
                isLoggedIn.value = true;
                currentUser.value = data.username;
                showLogin.value = false;
                loginForm.value = { username: '', password: '' };
                if (!rootData.value) fetchData();
            } catch (e) {
                alert(i18n.errorPrefix + e.message);
            }
        };

        const handleLogout = () => {
            localStorage.clear();
            isLoggedIn.value = false;
            currentUser.value = '';
            alert(i18n.loggedOut);
        };

        const fetchWithRetry = async (url, options = {}, retries = 3, backoff = 500) => {
            try {
                const res = await fetch(url, options);
                if (!res.ok) throw new Error(`Server status: ${res.status}`);
                return res;
            } catch (err) {
                if (retries > 0) {
                    console.warn(`Connection retry ${retries}`);
                    await new Promise(r => setTimeout(r, backoff));
                    return fetchWithRetry(url, options, retries - 1, backoff * 1.5);
                }
                throw err;
            }
        };

        const fetchData = async () => {
            loading.value = true;
            try {
                if (isLoggedIn.value) {
                    const tokenValid = await verifyToken();
                    if (!tokenValid) { loading.value = false; return; }
                }
                const token = localStorage.getItem('baka_token');
                const listOpts = token ? { headers: { 'Authorization': `Bearer ${token}` } } : {};
                const res = await fetchWithRetry(`${API_BASE}/list`, listOpts, 3, 500);
                const data = await res.json();
                rootData.value = data;
                // 恢复当前路径
                if (pathStack.value.length === 0) {
                    currentDir.value = data;
                } else {
                    let node = data;
                    const names = pathStack.value.map(p => p.name);
                    for (const name of names) {
                        const child = node.children?.find(c => c.name === name && c.type === 'dir');
                        if (child) { node = child; } else { node = data; break; }
                    }
                    currentDir.value = node;
                }
            } catch (e) {
                console.error(e);
                alert(i18n.loadFailed);
            } finally {
                loading.value = false;
            }
        };

        // ── 统一上传入口 ───────────────────────
        const handleFileUpload = async (event) => {
            if (!isLoggedIn.value) {
                showLogin.value = true;
                alert(i18n.loginFirst);
                event.target.value = '';
                return;
            }
            const file = event.target.files[0];
            if (!file) return;

            openProgressModal();

            const Path = pathStack.value.map(p => p.name);
            const fullPath = [...Path, file.name].join('/');
            const taskId = generateTaskId();
            const token = localStorage.getItem('baka_token');

            if (file.size < DIRECT_UPLOAD_THRESHOLD) {
                progressData.value[taskId] = {
                    filename: fullPath,
                    path: ['home', ...Path].join('/'),
                    displayName: file.name,
                    username: currentUser.value,
                    downloadSize: 0,
                    expectedSize: file.size,
                    speed: 0,
                    remaining: null,
                    type: 'local',
                };
                try {
                    await doDirectUpload({ file, fullPath, taskId, token, API_BASE, progressData, uploadTasks, i18n });
                    fetchData();
                } catch (e) {
                    alert(i18n.uploadFailed + e.message);
                } finally {
                    delete progressData.value[taskId];
                    delete uploadTasks.value[taskId];
                    event.target.value = '';
                }
            } else {
                const chunkSize = calcChunkSize(file.size);
                const total = Math.ceil(file.size / chunkSize);
                progressData.value[taskId] = {
                    filename: fullPath,
                    path: ['home', ...Path].join('/'),
                    displayName: file.name,
                    username: currentUser.value,
                    downloadSize: 0,
                    expectedSize: file.size,
                    speed: 0,
                    remaining: null,
                    type: 'chunk',
                    phase: 'hashing',
                    total,
                    sent: 0,
                };
                
                const controller = new AbortController();
                uploadTasks.value[taskId] = controller;
                
                try {
                    await doChunkUpload({ 
                        file, fullPath, taskId, total, chunkSize, token, 
                        API_BASE, progressData, controller
                    });
                    fetchData();
                } catch (e) {
                    if (!controller.signal.aborted) {
                        console.error('分片上传错误:', e);
                        alert(i18n.chunkUploadFailed + e.message);
                    }
                } finally {
                    delete progressData.value[taskId];
                    delete uploadTasks.value[taskId];
                    event.target.value = '';
                }
            }
        };

        const getFileUrl = (fileName) => {
            if (!rootData.value) return '#';
            const pathParts = [...pathStack.value.map(p => p.name), fileName];
            return `${API_BASE}/files/${pathParts.join('/')}`;
        };

        const getThumbUrl = (fileName) => {
            if (!rootData.value) return '';
            const pathParts = [...pathStack.value.map(p => p.name), fileName];
            return `${API_BASE}/thumb/${pathParts.map(encodeURIComponent).join('/')}`;
        };

        const downloadFile = async (fileName) => {
            try {
                const fileUrl = getFileUrl(fileName);
                const token = localStorage.getItem('baka_token');
                if (token) {
                    const res = await fetch(fileUrl, { headers: { 'Authorization': `Bearer ${token}` } });
                    if (!res.ok) throw new Error(`${res.status}`);
                    const blob = await res.blob();
                    const blobUrl = URL.createObjectURL(blob);
                    const link = document.createElement('a');
                    link.href = blobUrl;
                    link.download = fileName;
                    document.body.appendChild(link);
                    link.click();
                    document.body.removeChild(link);
                    URL.revokeObjectURL(blobUrl);
                } else {
                    const link = document.createElement('a');
                    link.href = fileUrl;
                    link.download = fileName;
                    document.body.appendChild(link);
                    link.click();
                    document.body.removeChild(link);
                }
            } catch (error) {
                console.error('下载失败:', error);
                alert(i18n.downloadFailed + error.message);
            }
        };

        // ── 目录与文件排序 ───────────────────────
        const sortedFiles = computed(() => {
            if (!currentDir.value || !currentDir.value.children) return [];
            return [...currentDir.value.children].sort((a, b) => {
                if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
                return naturalCompare(a.name, b.name);
            });
        });

        // ── 列表缩略图批量加载 ───────────────────────
        // 文件名 -> blob URL（LQIP）。进入目录时批量拉取，列表项据此显示预览。
        const thumbMap = ref({});
        const _thumbBlobs = [];           // 已创建的 blob URL，切目录时释放
        const THUMB_BATCH = 200;
        let _thumbSeq = 0;
        const isImageFile = (name) => classifyFile(name) === 'image';

        // 解析二进制流：[uint32 数量]{[uint16 路径长][路径][uint32 图长][JPEG]}
        const parseThumbStream = (buf) => {
            const dv = new DataView(buf);
            let off = 0;
            const count = dv.getUint32(off); off += 4;
            const out = [];
            const dec = new TextDecoder();
            for (let i = 0; i < count; i++) {
                const plen = dv.getUint16(off); off += 2;
                const path = dec.decode(new Uint8Array(buf, off, plen)); off += plen;
                const dlen = dv.getUint32(off); off += 4;
                const bytes = new Uint8Array(buf, off, dlen); off += dlen;
                out.push({ path, blob: new Blob([bytes], { type: 'image/jpeg' }) });
            }
            return out;
        };

        // 拉一批，整批失败自动重试
        const fetchThumbBatch = async (paths, retries = 2, backoff = 600) => {
            const token = localStorage.getItem('baka_token');
            const headers = { 'Content-Type': 'application/json' };
            if (token) headers['Authorization'] = `Bearer ${token}`;
            try {
                const res = await fetch(`${API_BASE}/thumbs`, {
                    method: 'POST', headers, body: JSON.stringify({ paths })
                });
                if (!res.ok) throw new Error(`status ${res.status}`);
                return parseThumbStream(await res.arrayBuffer());
            } catch (e) {
                if (retries > 0) {
                    await new Promise(r => setTimeout(r, backoff));
                    return fetchThumbBatch(paths, retries - 1, backoff * 1.5);
                }
                return []; // 重试耗尽，这批回退 emoji
            }
        };

        const loadThumbnails = async (dir) => {
            const seq = ++_thumbSeq;
            // 释放上一目录的 blob
            for (const u of _thumbBlobs) URL.revokeObjectURL(u);
            _thumbBlobs.length = 0;
            thumbMap.value = {};
            if (!dir || !dir.children) return;

            // 当前目录相对路径前缀
            const prefix = pathStack.value.map(p => p.name).join('/');
            const imgs = dir.children.filter(c => c.type === 'file' && isImageFile(c.name));
            if (!imgs.length) return;

            for (let i = 0; i < imgs.length; i += THUMB_BATCH) {
                if (seq !== _thumbSeq) return; // 已切到别的目录，停止
                const slice = imgs.slice(i, i + THUMB_BATCH);
                const paths = slice.map(f => prefix ? `${prefix}/${f.name}` : f.name);
                const results = await fetchThumbBatch(paths);
                if (seq !== _thumbSeq) return;
                const next = { ...thumbMap.value };
                for (const { path, blob } of results) {
                    const url = URL.createObjectURL(blob);
                    _thumbBlobs.push(url);
                    const name = path.slice(path.lastIndexOf('/') + 1);
                    next[name] = url;
                }
                thumbMap.value = next; // 这批先显示
            }
        };

        watch(currentDir, (dir) => { loadThumbnails(dir); });

        const goToLevel = (index) => {
            pathStack.value = pathStack.value.slice(0, index + 1);
            currentDir.value = pathStack.value[pathStack.value.length - 1];
        };

        const goHome = () => {
            pathStack.value = [];
            currentDir.value = rootData.value;
        };

        const handleItemClick = (item) => {
            if (item.type === 'dir') {
                pathStack.value.push(item);
                currentDir.value = item;
            } else {
                openFileViewer(item);
            }
        };

        // ── 文件查看器逻辑 ───────────────────────
        const viewer = ref({ show: false, type: null });
        const viewerImageList    = ref([]);
        const viewerImageIndex   = ref(0);
        const viewerVideoUrl     = ref('');
        const viewerTextContent  = ref('');
        const viewerTextTooLarge = ref(false);
        const viewerFileName     = ref('');
        const viewerFileSize     = ref(0);
        const viewerFileExt      = ref('');
        const viewerLoading      = ref(false);

        // 主图：中图先显示，原图加载完替换
        const viewerCurrentImageUrl = ref('');
        const viewerImageReady  = ref(false); // 主图（中图或原图）是否已加载
        // url -> blobUrl 缓存（key 含 ?size= 区分中图/原图），viewer 关闭时统一释放
        const _imageBlobCache = new Map();
        const _imageFetchingMap = new Map();
        const PRELOAD_AHEAD = 3;
        const PRELOAD_BEHIND = 1;
        // 每次切图自增，用于丢弃过期的异步结果（快速翻页时）
        let _viewerSeq = 0;

        const fetchAuthUrl = async (url) => {
            const token = localStorage.getItem('baka_token');
            if (!token) return url;
            const res = await fetch(url, { headers: { 'Authorization': `Bearer ${token}` } });
            if (!res.ok) return url;
            return URL.createObjectURL(await res.blob());
        };

        // 获取某 url 的 blob URL，有缓存直接返回，否则 fetch 并缓存
        const getImageBlobUrl = (url) => {
            if (_imageBlobCache.has(url)) return Promise.resolve(_imageBlobCache.get(url));
            if (_imageFetchingMap.has(url)) return _imageFetchingMap.get(url);
            const p = fetchAuthUrl(url).then(blobUrl => {
                _imageBlobCache.set(url, blobUrl);
                _imageFetchingMap.delete(url);
                return blobUrl;
            });
            _imageFetchingMap.set(url, p);
            return p;
        };

        // 后台静默预加载周边的中图（原图太大不预拉，避免抢带宽）
        const preloadImages = (list, centerIdx) => {
            const start = Math.max(0, centerIdx - PRELOAD_BEHIND);
            const end   = Math.min(list.length - 1, centerIdx + PRELOAD_AHEAD);
            for (let i = start; i <= end; i++) {
                getImageBlobUrl(`${getThumbUrl(list[i].name)}?size=mid`).catch(() => {});
            }
        };

        const closeViewer = () => {
            viewer.value = { show: false, type: null };
            viewerVideoUrl.value = '';
            viewerCurrentImageUrl.value = '';
            viewerImageReady.value = false;
            for (const [, blobUrl] of _imageBlobCache) {
                if (typeof blobUrl === 'string' && blobUrl.startsWith('blob:')) URL.revokeObjectURL(blobUrl);
            }
            _imageBlobCache.clear();
            _imageFetchingMap.clear();
        };
        const viewerPrev = () => { if (viewerImageIndex.value > 0) viewerImageIndex.value--; };
        const viewerNext = () => { if (viewerImageIndex.value < viewerImageList.value.length - 1) viewerImageIndex.value++; };

        watch([viewerImageList, viewerImageIndex], async ([list, idx]) => {
            if (!list.length) { viewerCurrentImageUrl.value = ''; return; }
            const seq = ++_viewerSeq;
            const name = list[idx].name;
            const midUrl  = `${getThumbUrl(name)}?size=mid`;
            const fullUrl = getFileUrl(name);

            // 重置：清空主图，显示加载图标
            viewerImageReady.value = false;
            viewerCurrentImageUrl.value = '';

            // 中图：先显示
            try {
                const midBlob = await getImageBlobUrl(midUrl);
                if (seq === _viewerSeq) viewerCurrentImageUrl.value = midBlob;
            } catch (_) {}

            // 原图：后台拉完替换中图
            getImageBlobUrl(fullUrl).then(fullBlob => {
                if (seq === _viewerSeq) viewerCurrentImageUrl.value = fullBlob;
            }).catch(() => {});

            // 预加载周边中图
            preloadImages(list, idx);
        });

        const pickerDirs = computed(() => {
            const dir = pickerStack.value.length === 0 ? rootData.value : pickerStack.value[pickerStack.value.length - 1];
            if (!dir || !dir.children) return [];
            return dir.children.filter(c => c.type === 'dir').sort((a, b) => naturalCompare(a.name, b.name));
        });

        let touchStartX = 0;
        const onViewerTouchStart = (e) => { touchStartX = e.touches[0].clientX; };
        const onViewerTouchEnd   = (e) => {
            const dx = e.changedTouches[0].clientX - touchStartX;
            if (dx > 50) viewerPrev();
            else if (dx < -50) viewerNext();
        };
        const onViewerKeydown = (e) => {
            if (!viewer.value.show) return;
            if (e.key === 'ArrowLeft')  viewerPrev();
            else if (e.key === 'ArrowRight') viewerNext();
            else if (e.key === 'Escape') closeViewer();
        };

        const openFileViewer = async (item) => {
            const name = item.name;
            const ft = classifyFile(name);
            viewerFileName.value = name;
            viewerFileSize.value = item.size;
            viewerFileExt.value  = getFileExt(name).toUpperCase();

            if (ft === 'image') {
                const imgs = sortedFiles.value.filter(f => f.type === 'file' && classifyFile(f.name) === 'image');
                viewerImageList.value = imgs;
                const idx = imgs.findIndex(f => f.name === name);
                viewerImageIndex.value = idx >= 0 ? idx : 0;
                viewer.value = { show: true, type: 'image' };
            } else if (ft === 'video') {
                viewer.value = { show: true, type: 'video' };
                viewerVideoUrl.value = await fetchAuthUrl(getFileUrl(name));
            } else if (ft === 'text') {
                viewerTextTooLarge.value = false;
                viewerTextContent.value  = '';
                viewerLoading.value = true;
                viewer.value = { show: true, type: 'text' };
                if (item.size > 10 * 1024) {
                    viewerTextTooLarge.value = true;
                    viewerLoading.value = false;
                } else {
                    try {
                        const token = localStorage.getItem('baka_token');
                        const opts = token ? { headers: { 'Authorization': `Bearer ${token}` } } : {};
                        const res = await fetch(getFileUrl(name), opts);
                        viewerTextContent.value = await res.text();
                    } catch(e) {
                        viewerTextContent.value = '读取失败：' + e.message;
                    } finally {
                        viewerLoading.value = false;
                    }
                }
            } else {
                viewer.value = { show: true, type: 'other' };
            }
        };

        // ── 远程上传与进度轮询 ───────────────────────
        const submitRemoteUpload = async () => {
            if (!isLoggedIn.value) return alert(i18n.loginFirst);
            if (!isValidUrl(remoteUrl.value)) return alert(i18n.invalidUrl);
            try {
                const token = localStorage.getItem('baka_token');
                const urlObj = new URL(remoteUrl.value);
                let fileName = urlObj.pathname.split('/').pop() || i18n.unknownFilePrefix + Date.now();
                const storagePath = [...pathStack.value.map(p => p.name), fileName].join('/');
                const res = await fetch(`${API_BASE}/remote-upload`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${token}`,
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({ url: remoteUrl.value, filename: decodeURIComponent(storagePath) })
                });
                if (!res.ok) throw new Error(res.statusText || i18n.transferFailed);
                showRemoteModal.value = false;
                remoteUrl.value = '';
                openProgressModal();
            } catch (e) {
                alert(i18n.transferFailed + e.message);
            }
        };

        const pollProgress = async () => {
            try {
                const res = await fetch(`${API_BASE}/progress`, {
                    method: 'GET',
                    headers: { 'Authorization': `Bearer ${localStorage.getItem('baka_token')}` }
                });
                if (!res.ok) throw new Error('Poll failed');
                const remoteData = await res.json();
                const now = Date.now();
                const remoteFilenames = new Set(Object.keys(remoteData));

                for (const [filename, info] of Object.entries(remoteData)) {
                    let taskId = filenameToTaskId[filename];
                    if (!taskId || !progressData.value[taskId] || progressData.value[taskId].type !== 'remote') {
                        taskId = generateTaskId();
                        const parts = filename.split('/');
                        parts.pop();
                        filenameToTaskId[filename] = taskId;
                        progressData.value[taskId] = {
                            filename,
                            displayName: filename.split('/').pop() || filename,
                            path: ['home', ...parts].join('/'),
                            username: info.username,
                            downloadSize: info.downloadSize,
                            expectedSize: info.expectedSize,
                            speed: 0,
                            remaining: null,
                            type: 'remote'
                        };
                        lastProgress.value[taskId] = { downloadSize: info.downloadSize, timestamp: now };
                    } else {
                        const task = progressData.value[taskId];
                        const last = lastProgress.value[taskId];
                        let speed = 0, remaining = null;
                        if (last) {
                            const timeDiff = (now - last.timestamp) / 1000;
                            if (timeDiff > 0) {
                                const sizeDiff = info.downloadSize - last.downloadSize;
                                speed = sizeDiff / timeDiff;
                                if (speed > 0 && info.expectedSize) {
                                    remaining = (info.expectedSize - info.downloadSize) / speed;
                                }
                            }
                        }
                        task.downloadSize = info.downloadSize;
                        task.expectedSize = info.expectedSize;
                        task.speed = speed;
                        task.remaining = remaining;
                        lastProgress.value[taskId] = { downloadSize: info.downloadSize, timestamp: now };
                    }
                }

                for (const taskId in progressData.value) {
                    const task = progressData.value[taskId];
                    if (task.type === 'remote' && !remoteFilenames.has(task.filename)) {
                        delete progressData.value[taskId];
                        delete lastProgress.value[taskId];
                        for (const f in filenameToTaskId) {
                            if (filenameToTaskId[f] === taskId) { delete filenameToTaskId[f]; break; }
                        }
                    }
                }
            } catch (e) {
                console.error('轮询进度失败', e);
            }
        };

        const startPolling = () => {
            if (pollingInterval) return;
            pollProgress();
            pollingInterval = setInterval(pollProgress, 1000);
        };
        const stopPolling = () => {
            if (pollingInterval) { clearInterval(pollingInterval); pollingInterval = null; }
        };
        const openProgressModal = () => { showProgressModal.value = true; startPolling(); };
        const closeProgressModal = () => { showProgressModal.value = false; stopPolling(); };
        const openRemoteModal = () => {
            if (!isLoggedIn.value) { showLogin.value = true; return alert(i18n.loginFirst); }
            showRemoteModal.value = true;
        };

        // ── 取消任务 ──────────────────────────────────────────
        const cancelTask = async (taskId) => {
            const task = progressData.value[taskId];
            if (!task) return;

            if (task.type === 'local') {
                const xhr = uploadTasks.value[taskId];
                if (xhr) xhr.abort();
                delete progressData.value[taskId];
                delete uploadTasks.value[taskId];
            } else if (task.type === 'chunk') {
                const controller = uploadTasks.value[taskId];
                if (controller) controller.abort();
                delete progressData.value[taskId];
                delete uploadTasks.value[taskId];
            } else if (task.type === 'remote') {
                const filename = task.filename;
                if (!filename) return;
                try {
                    const token = localStorage.getItem('baka_token');
                    const res = await fetch(`${API_BASE}/cancel`, {
                        method: 'POST',
                        headers: { 'Authorization': `Bearer ${token}` },
                        body: JSON.stringify({ filename })
                    });
                    if (res.ok) {
                        delete progressData.value[taskId];
                        delete lastProgress.value[taskId];
                        for (const f in filenameToTaskId) {
                            if (filenameToTaskId[f] === taskId) { delete filenameToTaskId[f]; break; }
                        }
                    } else {
                        alert(i18n.cancelFailed + await res.text());
                    }
                } catch (e) {
                    alert(i18n.cancelError + e.message);
                }
            }
        };

        // ── 文件操作 ──────────────────────────────────────────
        const getItemPath = (item) => {
            return [...pathStack.value.map(p => p.name), item.name].join('/');
        };

        const initPicker = () => {
            pickerStack.value = [...pathStack.value];
        };

        const toggleOpsPanel = (item) => {
            if (opsPanelFor.value === item) {
                opsPanelFor.value = null;
            } else {
                opsPanelFor.value = item;
            }
        };

        let outsideClickCleanup = null;
        watch(opsPanelFor, (val) => {
            if (outsideClickCleanup) {
                outsideClickCleanup();
                outsideClickCleanup = null;
            }
            if (val) {
                const handler = (e) => {
                    if (!e.target.closest('.js-ops-panel') && !e.target.closest('.js-ops-btn')) {
                        opsPanelFor.value = null;
                    }
                };
                setTimeout(() => {
                    document.addEventListener('click', handler, true);
                }, 0);
                outsideClickCleanup = () => document.removeEventListener('click', handler, true);
            }
        });

        const startRename = (item) => {
            opsPanelFor.value = null;
            if (!isLoggedIn.value) { showLogin.value = true; alert(i18n.loginFirst); return; }
            opsTarget.value = item;
            renameName.value = item.name;
            initPicker();
            showRenameModal.value = true;
        };

        const startCopy = (item) => {
            opsPanelFor.value = null;
            if (!isLoggedIn.value) { showLogin.value = true; alert(i18n.loginFirst); return; }
            opsTarget.value = item;
            copyName.value = item.name;
            initPicker();
            showCopyModal.value = true;
        };

        const startDelete = (item) => {
            opsPanelFor.value = null;
            if (!isLoggedIn.value) { showLogin.value = true; alert(i18n.loginFirst); return; }
            opsTarget.value = item;
            showDeleteConfirm.value = true;
        };

        const pickerEnter = (dir) => {
            pickerStack.value.push(dir);
        };

        const pickerGoToLevel = (index) => {
            if (index < 0) { pickerStack.value = []; return; }
            pickerStack.value = pickerStack.value.slice(0, index + 1);
        };

        const getPickerPath = (name) => {
            const parts = pickerStack.value.map(p => p.name);
            parts.push(name);
            return parts.join('/');
        };

        const confirmRename = async () => {
            if (!renameName.value.trim()) return;
            const src = getItemPath(opsTarget.value);
            const dst = getPickerPath(renameName.value.trim());
            try {
                const res = await fetch(`${API_BASE}/rename`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('baka_token')}`,
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({ path: src, dst })
                });
                if (!res.ok) throw new Error(await res.text());
                showRenameModal.value = false;
                fetchData();
            } catch (e) {
                alert(i18n.operationFailed + e.message);
            }
        };

        const confirmCopy = async () => {
            if (!copyName.value.trim()) return;
            const src = getItemPath(opsTarget.value);
            const dst = getPickerPath(copyName.value.trim());
            try {
                const res = await fetch(`${API_BASE}/copy`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('baka_token')}`,
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({ path: src, dst })
                });
                if (!res.ok) throw new Error(await res.text());
                showCopyModal.value = false;
                fetchData();
            } catch (e) {
                alert(i18n.operationFailed + e.message);
            }
        };

        const confirmDelete = async () => {
            const src = getItemPath(opsTarget.value);
            try {
                const res = await fetch(`${API_BASE}/delete`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('baka_token')}`,
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({ path: src })
                });
                if (!res.ok) throw new Error(await res.text());
                showDeleteConfirm.value = false;
                fetchData();
            } catch (e) {
                alert(i18n.operationFailed + e.message);
            }
        };

        // ── 新建文件夹 ──────────────────────────────────────
        const openMkdirModal = () => {
            if (!isLoggedIn.value) { showLogin.value = true; alert(i18n.loginFirst); return; }
            mkdirName.value = '';
            showMkdirModal.value = true;
        };

        const confirmMkdir = async () => {
            if (!mkdirName.value.trim()) return;
            const parts = [...pathStack.value.map(p => p.name), mkdirName.value.trim()];
            const dst = parts.join('/');
            try {
                const res = await fetch(`${API_BASE}/mkdir`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('baka_token')}`,
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify({ path: dst })
                });
                if (!res.ok) throw new Error(await res.text());
                showMkdirModal.value = false;
                fetchData();
            } catch (e) {
                alert(i18n.mkdirFailed + e.message);
            }
        };

        onMounted(async () => {
            try {
                const res = await fetch(`${API_BASE}/api/config`);
                if (res.ok) {
                    const data = await res.json();
                    authMode.value = !!data.auth_mode;
                }
            } catch (_) {}

            if (authMode.value && !isLoggedIn.value) {
                showLogin.value = true;
                return;
            }

            if (isLoggedIn.value) await verifyToken();
            fetchData();
            window.addEventListener('keydown', onViewerKeydown);
        });

        onUnmounted(() => {
            window.removeEventListener('keydown', onViewerKeydown);
            stopPolling();
        });

        return {
            i18n,
            loading, isLoggedIn, currentUser, showLogin, loginForm, authMode, pathStack, sortedFiles,
            handleLogin, handleLogout, handleFileUpload, handleItemClick,
            goHome, goToLevel, formatSize, getFileUrl, downloadFile,
            showRemoteModal, showProgressModal, showUploadMenu, remoteUrl, progressData,
            submitRemoteUpload, openProgressModal, closeProgressModal, openRemoteModal,
            formatSpeed, cancelTask,
            viewer, viewerImageList, viewerImageIndex, viewerVideoUrl,
            viewerTextContent, viewerTextTooLarge, viewerFileName, viewerFileSize,
            viewerFileExt, viewerLoading, viewerCurrentImageUrl,
            viewerImageReady,
            closeViewer, viewerPrev, viewerNext,
            onViewerTouchStart, onViewerTouchEnd,
            classifyFile, fileTypeIcon, getFileExt,
            thumbMap, isImageFile,
            // 文件操作
            opsPanelFor, showRenameModal, showCopyModal, showDeleteConfirm,
            opsTarget, pickerStack, renameName, copyName, pickerDirs,
            toggleOpsPanel, startRename, startCopy, startDelete,
            confirmRename, confirmCopy, confirmDelete,
            pickerEnter, pickerGoToLevel,
            showMkdirModal, mkdirName, openMkdirModal, confirmMkdir,
        };
    }
}).mount('#app');
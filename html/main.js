const { createApp, ref, computed, onMounted, onUnmounted } = Vue;

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
        const filenameToTaskId = {};

        const isLoggedIn = ref(!!localStorage.getItem('baka_token'));
        const currentUser = ref(localStorage.getItem('baka_user') || '');
        const loginForm = ref({ username: '', password: '' });

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
                const res = await fetchWithRetry(`${API_BASE}/list`, {}, 3, 500);
                const data = await res.json();
                rootData.value = data;
                currentDir.value = data;
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

        const downloadFile = async (fileName) => {
            try {
                const fileUrl = getFileUrl(fileName);
                const link = document.createElement('a');
                link.href = fileUrl;
                link.download = fileName;
                document.body.appendChild(link);
                link.click();
                document.body.removeChild(link);
                if (link.href.startsWith('blob:')) URL.revokeObjectURL(link.href);
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

        const closeViewer = () => {
            viewer.value = { show: false, type: null };
            viewerVideoUrl.value = '';
        };
        const viewerPrev = () => { if (viewerImageIndex.value > 0) viewerImageIndex.value--; };
        const viewerNext = () => { if (viewerImageIndex.value < viewerImageList.value.length - 1) viewerImageIndex.value++; };
        const viewerCurrentImageUrl = computed(() => {
            const list = viewerImageList.value;
            if (!list.length) return '';
            return getFileUrl(list[viewerImageIndex.value].name);
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
                viewerVideoUrl.value = getFileUrl(name);
                viewer.value = { show: true, type: 'video' };
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
                        const res = await fetch(getFileUrl(name));
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

        onMounted(async () => {
            if (isLoggedIn.value) await verifyToken();
            fetchData();
            window.addEventListener('keydown', onViewerKeydown);
        });

        onUnmounted(() => {
            window.removeEventListener('keydown', onViewerKeydown);
            stopPolling();
        });

        return {
            loading, isLoggedIn, currentUser, showLogin, loginForm, pathStack, sortedFiles,
            handleLogin, handleLogout, handleFileUpload, handleItemClick,
            goHome, goToLevel, formatSize, getFileUrl, downloadFile,
            showRemoteModal, showProgressModal, showUploadMenu, remoteUrl, progressData,
            submitRemoteUpload, openProgressModal, closeProgressModal, openRemoteModal,
            formatSpeed, cancelTask,
            viewer, viewerImageList, viewerImageIndex, viewerVideoUrl,
            viewerTextContent, viewerTextTooLarge, viewerFileName, viewerFileSize,
            viewerFileExt, viewerLoading, viewerCurrentImageUrl,
            closeViewer, viewerPrev, viewerNext,
            onViewerTouchStart, onViewerTouchEnd,
            classifyFile, fileTypeIcon, getFileExt,
        };
    }
}).mount('#app');
const { createApp, ref, computed, onMounted, onUnmounted } = Vue;

// 国际化字符串映射
const i18n = {
    // 认证相关
    tokenExpired:       '令牌失效了，琪露诺不认识你了，重新登录吧！',
    loginFirst:         '先点右上角的冰块，登录才能进来！',
    unknownName:        '琪露诺不认识无名氏!',
    wrongPassword:      '口令不对！你该不会是伪装者吧？',
    loggedOut:          '你已经走出冰窖了，再见～',

    // 导航 / 加载
    loadFailed:         '琪露诺找不到路了……冰窖的地图好像坏掉了？',

    // 文件操作
    downloadFailed:     '下载失败了：',
    uploadFailed:       '上传失败了：',
    uploadNetworkError: '网络断了，上传只好取消……',

    // 远程搬运
    invalidUrl:         '这根本不是正经的地址！琪露诺才不帮你搬！',
    transferFailed:     '搬运失败了：',
    cancelFailed:       '取消搬运失败了：',
    cancelError:        '取消搬运时出错了：',

    // 杂项
    unknownError:       '发生了不知道是什么的错误',
    unknownFilePrefix:  'unknown_ice_',
    errorPrefix:        '哎呀：',
};

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
        const filenameToTaskId = {};            // 文件名 -> taskid 映射（非响应式）

        const isLoggedIn = ref(!!localStorage.getItem('baka_token'));
        const currentUser = ref(localStorage.getItem('baka_user') || '');
        const loginForm = ref({ username: '', password: '' });

        // 生成唯一任务 ID
        const generateTaskId = () => {
            return 'task_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9);
        };

        const isValidUrl = (url) => {
            try {
                new URL(url);
                return true;
            } catch (e) {
                return false;
            }
        };

        // 验证 token
        const verifyToken = async () => {
            const token = localStorage.getItem('baka_token');
            if (!token) return false;
            
            try {
                const res = await fetch(`${API_BASE}/verify`, {
                    method: 'POST',
                    headers: {
                        'Authorization': `Bearer ${token}`
                    }
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

        const formatSpeed = (bytesPerSec) => {
            if (bytesPerSec === undefined || bytesPerSec === null) return '--';
            if (bytesPerSec === 0) return '0 B/s';
            const k = 1024;
            const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
            const i = Math.floor(Math.log(bytesPerSec) / Math.log(k));
            return parseFloat((bytesPerSec / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
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
                if (link.href.startsWith('blob:')) {
                    URL.revokeObjectURL(link.href);
                }
            } catch (error) {
                console.error('下载失败:', error);
                alert(i18n.downloadFailed + error.message);
            }
        };

        const handleLogin = async () => {
            if (!loginForm.value.username) return alert(i18n.unknownName);
            try {
                const res = await fetch(`${API_BASE}/login`, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
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
                if (!res.ok) {
                    throw new Error(`Server status: ${res.status}`);
                }
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
                    if (!tokenValid) {
                        loading.value = false;
                        return;
                    }
                }
                const res = await fetchWithRetry(`${API_BASE}/node`, {}, 3, 500);
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

        const handleFileUpload = async (event) => {
            if (!isLoggedIn.value) {
                showLogin.value = true;
                alert(i18n.loginFirst);
                event.target.value = '';
                return;
            }

            const file = event.target.files[0];
            if (!file) return;

            // 打开进度弹窗（自动启动轮询）
            openProgressModal();

            // 生成任务 ID
            const taskId = generateTaskId();
            const Path = [...pathStack.value.map(p => p.name)];
            const fullPath = [...Path, file.name].join('/');

            // 初始化本地任务
            progressData.value[taskId] = {
                filename: fullPath,
                path: ['home', ...Path].join('/'),
                displayName: file.name, // 仅显示文件名
                username: currentUser.value,
                downloadSize: 0,
                expectedSize: file.size,
                speed: 0,
                remaining: null,
                type: 'local'
            };

            const token = localStorage.getItem('baka_token');
            const encodedFilename = encodeURIComponent(fullPath);

            const xhr = new XMLHttpRequest();
            uploadTasks.value[taskId] = xhr;
            xhr.open('POST', `${API_BASE}/update`, true);
            xhr.setRequestHeader('Authorization', `Bearer ${token}`);
            xhr.setRequestHeader('X-Upload-Filename', encodedFilename);

            let lastLoaded = 0;
            let lastTime = Date.now();
            let speedSamples = [];

            xhr.upload.onprogress = (e) => {
                if (e.lengthComputable) {
                    const loaded = e.loaded;
                    const total = e.total;
                    const now = Date.now();
                    const deltaTime = (now - lastTime) / 1000;
                    const deltaLoaded = loaded - lastLoaded;

                    let instantSpeed = 0;
                    if (deltaTime > 0) {
                        instantSpeed = deltaLoaded / deltaTime;
                    }

                    speedSamples.push(instantSpeed);
                    if (speedSamples.length > 5) {
                        speedSamples.shift();
                    }
                    const avgSpeed = speedSamples.reduce((a, b) => a + b, 0) / speedSamples.length;

                    if (progressData.value[taskId]) {
                        progressData.value[taskId].downloadSize = loaded;
                        progressData.value[taskId].speed = avgSpeed;
                    }

                    lastLoaded = loaded;
                    lastTime = now;
                }
            };

            xhr.onload = () => {
                if (xhr.status >= 200 && xhr.status < 300) {
                    // 上传成功，删除任务
                    delete progressData.value[taskId];
                    fetchData(); // 刷新文件列表
                } else {
                    alert(i18n.uploadFailed + (xhr.responseText || i18n.unknownError));
                    delete progressData.value[taskId];
                }
                event.target.value = '';
            };

            xhr.onerror = () => {
                alert(i18n.uploadNetworkError);
                delete progressData.value[taskId];
                event.target.value = '';
            };

            xhr.onabort = () => {
                // 取消时也会清理，但 cancelTask 会手动删除，这里做兜底
                delete progressData.value[taskId];
                delete uploadTasks.value[taskId];
                event.target.value = '';
            };
            xhr.send(file);
        };

        const getFileUrl = (fileName) => {
            if (!rootData.value) return '#';
            const pathParts = [...pathStack.value.map(p => p.name), fileName];
            return `${API_BASE}/files/${pathParts.join('/')}`;
        };

                // ---- 文件查看器 ----
        const IMAGE_EXTS = new Set(['jpg','jpeg','png','gif','webp','bmp','svg','avif','ico','tiff']);
        const VIDEO_EXTS = new Set(['mp4','webm','ogg','mov','mkv','avi','flv','m4v','ts']);
        const TEXT_EXTS  = new Set(['txt','md','log','json','xml','yaml','yml','csv','ini','conf','toml','sh','py','js','ts','html','css','htm','env','rs','go','java','c','cpp','h','php','rb']);

        const getFileExt = (name) => (name.split('.').pop() || '').toLowerCase();
        const classifyFile = (name) => {
            const ext = getFileExt(name);
            if (IMAGE_EXTS.has(ext)) return 'image';
            if (VIDEO_EXTS.has(ext)) return 'video';
            if (TEXT_EXTS.has(ext))  return 'text';
            return 'other';
        };
        const fileTypeIcon = (name) => {
            const ext = getFileExt(name);
            if (IMAGE_EXTS.has(ext)) return '🖼️';
            if (VIDEO_EXTS.has(ext)) return '🎬';
            if (TEXT_EXTS.has(ext))  return '📝';
            const m = {'pdf':'📕','zip':'🗜️','rar':'🗜️','7z':'🗜️','tar':'🗜️','gz':'🗜️',
                       'exe':'⚙️','dmg':'💿','iso':'💿','apk':'📱',
                       'doc':'📘','docx':'📘','xls':'📗','xlsx':'📗','ppt':'📙','pptx':'📙',
                       'mp3':'🎵','flac':'🎵','wav':'🎵','aac':'🎵'};
            return m[ext] || '📦';
        };


// 自然排序比较函数（1, 2, 10 而非 1, 10, 2）
        const naturalCompare = (a, b) => {
            const re = /(\d+)|(\D+)/g;
            const tokenA = String(a).match(re) || [];
            const tokenB = String(b).match(re) || [];
            const len = Math.max(tokenA.length, tokenB.length);
            for (let i = 0; i < len; i++) {
                const ta = tokenA[i] || '';
                const tb = tokenB[i] || '';
                const na = parseInt(ta, 10);
                const nb = parseInt(tb, 10);
                if (!isNaN(na) && !isNaN(nb)) {
                    if (na !== nb) return na - nb;
                } else {
                    const cmp = ta.localeCompare(tb, undefined, { sensitivity: 'base' });
                    if (cmp !== 0) return cmp;
                }
            }
            return 0;
        };

        const sortedFiles = computed(() => {
            if (!currentDir.value || !currentDir.value.children) return [];
            return [...currentDir.value.children].sort((a, b) => {
                if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
                return naturalCompare(a.name, b.name);
            });


        });


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

        const handleItemClick = (item) => {
            if (item.type === 'dir') {
                pathStack.value.push(item);
                currentDir.value = item;
            } else {
                openFileViewer(item);
            }
        };

        const goToLevel = (index) => {
            pathStack.value = pathStack.value.slice(0, index + 1);
            currentDir.value = pathStack.value[pathStack.value.length - 1];
        };

        const goHome = () => {
            pathStack.value = [];
            currentDir.value = rootData.value;
        };

        const formatSize = (bytes) => {
            if (!bytes) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
        };

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
                    body: JSON.stringify({
                        url: remoteUrl.value,
                        filename: decodeURIComponent(storagePath)
                    })
                });

                if (!res.ok) throw new Error(res.statusText || i18n.transferFailed);
                
                showRemoteModal.value = false;
                remoteUrl.value = '';
                openProgressModal(); // 打开进度弹窗，轮询会显示新任务
            } catch (e) {
                alert(i18n.transferFailed + e.message);
            }
        };

        // 轮询远程进度
        const pollProgress = async () => {
            try {
                const res = await fetch(`${API_BASE}/progress`, {
                    method: 'GET',
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('baka_token')}`
                    }
                });
                if (!res.ok) throw new Error('Poll failed');
                const remoteData = await res.json(); // { filename: { username, downloadSize, expectedSize } }
                
                const now = Date.now();
                const remoteFilenames = new Set(Object.keys(remoteData));

                // 新增或更新远程任务
                for (const [filename, info] of Object.entries(remoteData)) {
                    let taskId = filenameToTaskId[filename];
                    if (!taskId || !progressData.value[taskId] || progressData.value[taskId].type !== 'remote') {
                        // 新任务
                        taskId = generateTaskId();
                        const parts = filename.split('/');
                        parts.pop();
                        filenameToTaskId[filename] = taskId;
                        progressData.value[taskId] = {
                            filename: filename,
                            displayName: filename.split('/').pop() || filename,
                            path: ['home', ...parts].join('/'),
                            username: info.username,
                            downloadSize: info.downloadSize,
                            expectedSize: info.expectedSize,
                            speed: 0,
                            remaining: null,
                            type: 'remote'
                        };
                        lastProgress.value[taskId] = {
                            downloadSize: info.downloadSize,
                            timestamp: now
                        };
                    } else {
                        // 更新已有任务
                        const task = progressData.value[taskId];
                        const last = lastProgress.value[taskId];
                        let speed = 0;
                        let remaining = null;
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
                        lastProgress.value[taskId] = {
                            downloadSize: info.downloadSize,
                            timestamp: now
                        };
                    }
                }

                // 删除已消失的远程任务
                for (const taskId in progressData.value) {
                    const task = progressData.value[taskId];
                    if (task.type === 'remote' && !remoteFilenames.has(task.filename)) {
                        delete progressData.value[taskId];
                        delete lastProgress.value[taskId];
                        // 从 filenameToTaskId 中移除
                        for (const f in filenameToTaskId) {
                            if (filenameToTaskId[f] === taskId) {
                                delete filenameToTaskId[f];
                                break;
                            }
                        }
                    }
                }
            } catch (e) {
                console.error('轮询进度失败', e);
            }
        };

        const startPolling = () => {
            if (pollingInterval) return;
            pollProgress(); // 立即执行一次
            pollingInterval = setInterval(pollProgress, 1000);
        };

        const stopPolling = () => {
            if (pollingInterval) {
                clearInterval(pollingInterval);
                pollingInterval = null;
            }
        };

        const openProgressModal = () => {
            showProgressModal.value = true;
            startPolling();
        };

        const closeProgressModal = () => {
            showProgressModal.value = false;
            stopPolling();
        };

        const openRemoteModal = () => {
            if (!isLoggedIn.value) {
                showLogin.value = true;
                return alert(i18n.loginFirst);
            }
            showRemoteModal.value = true;
        };

        const cancelTask = async (taskId) => {
            const task = progressData.value[taskId];
            if (!task) return;

            if (task.type === 'local') {
                const xhr = uploadTasks.value[taskId];
                if (xhr) {
                    xhr.abort(); // 会触发 onabort/onerror，但为了避免重复清理，我们先手动删除映射
                }
                // 无论 xhr 是否存在，都从进度数据中移除
                delete progressData.value[taskId];
                delete uploadTasks.value[taskId];
            } else if (task.type === 'remote') {
                const filename = task.filename;
                if (!filename) return;
                try {
                    const token = localStorage.getItem('baka_token');
                    const res = await fetch(`${API_BASE}/cancel?filename=${encodeURIComponent(filename)}`, {
                        method: 'POST',
                        headers: {
                            'Authorization': `Bearer ${token}`
                        }
                    });
                    if (res.ok) {
                        // 从进度数据中删除
                        delete progressData.value[taskId];
                        delete lastProgress.value[taskId];
                        // 从 filenameToTaskId 中移除
                        for (const f in filenameToTaskId) {
                            if (filenameToTaskId[f] === taskId) {
                                delete filenameToTaskId[f];
                                break;
                            }
                        }
                    } else {
                        const errText = await res.text();
                        alert(i18n.cancelFailed + errText);
                    }
                } catch (e) {
                    alert(i18n.cancelError + e.message);
                }
            }
        };

        onMounted(async () => {
            if (isLoggedIn.value) {
                await verifyToken();
            }
            fetchData();
            window.addEventListener('keydown', onViewerKeydown);
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

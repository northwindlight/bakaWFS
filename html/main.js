const { createApp, ref, computed, watch, onMounted, onUnmounted, watchEffect } = Vue;

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
        const currentDir = ref(null);
        const pathStack = ref([]);
        const loading = ref(false);
        const searchQuery = ref('');
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
        const currentRole = ref(localStorage.getItem('baka_role') || '');
        const loginForm = ref({ username: '', password: '' });
        const authMode = ref(false);
        // 仅 admin 可见写操作入口（上传/删除/改名/新建/远程下载）；后端 403 兜底。
        const isAdmin = computed(() => currentRole.value === 'admin');

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
                    if (data.role) {
                        localStorage.setItem('baka_role', data.role);
                        currentRole.value = data.role;
                    }
                    return true;
                } else {
                    localStorage.removeItem('baka_token');
                    localStorage.removeItem('baka_user');
                    localStorage.removeItem('baka_role');
                    isLoggedIn.value = false;
                    currentUser.value = '';
                    currentRole.value = '';
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
                localStorage.setItem('baka_role', data.role || '');
                isLoggedIn.value = true;
                currentUser.value = data.username;
                currentRole.value = data.role || '';
                showLogin.value = false;
                loginForm.value = { username: '', password: '' };
                if (!currentDir.value) loadLevel(parseHash());
            } catch (e) {
                alert(i18n.errorPrefix + e.message);
            }
        };

        const handleLogout = () => {
            localStorage.clear();
            isLoggedIn.value = false;
            currentUser.value = '';
            currentRole.value = '';
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

        // ── hash 路由 + 懒加载 ────────────────────────────────
        // URL 形如 #/汉化组A/本子，每段 encodeURIComponent。当前所在目录由 hash 决定，
        // 浏览器前进/后退触发 hashchange → 按路径向 /list?path= 拉那一层（懒加载，深度2）。
        // 整树只在搜索时才向 /tree 拉一次。

        const authHeaders = () => {
            const token = localStorage.getItem('baka_token');
            return token ? { headers: { 'Authorization': `Bearer ${token}` } } : {};
        };

        // 当前 hash → 路径段数组（已解码）。空 hash = 根。
        const parseHash = () => {
            let h = location.hash || '';
            if (h.startsWith('#')) h = h.slice(1);
            if (h.startsWith('/')) h = h.slice(1);
            if (!h) return [];
            return h.split('/').filter(Boolean).map(s => {
                try { return decodeURIComponent(s); } catch (_) { return s; }
            });
        };

        // 路径段数组 → hash 字符串。
        const buildHash = (names) => '#/' + names.map(encodeURIComponent).join('/');

        // 设置 hash（触发 hashchange 进而加载）。与当前 hash 相同则直接重载本层。
        const navigateTo = (names) => {
            const target = buildHash(names);
            if (location.hash === target) {
                loadLevel(names);
            } else {
                location.hash = target;
            }
        };

        // 向 /list?path= 拉指定层（深度2），刷新 currentDir + pathStack。
        const loadLevel = async (names) => {
            loading.value = true;
            try {
                if (isLoggedIn.value) {
                    const tokenValid = await verifyToken();
                    if (!tokenValid) { loading.value = false; return; }
                }
                const qs = names.length ? `?path=${encodeURIComponent(names.join('/'))}` : '';
                const res = await fetchWithRetry(`${API_BASE}/list${qs}`, authHeaders(), 3, 500);
                const data = await res.json();
                currentDir.value = data;
                // pathStack 用名字段重建（节点本身只需 name 供面包屑/跳转用）
                pathStack.value = names.map(n => ({ name: n }));
                // 搜索跳转到文件：本层加载完后打开该文件查看器
                if (pendingOpenFile.value) {
                    const fname = pendingOpenFile.value;
                    pendingOpenFile.value = null;
                    const f = (data.children || []).find(c => c.name === fname && c.type === 'file');
                    if (f) openFileViewer(f);
                }
            } catch (e) {
                console.error(e);
                // 路径失效（被删/改名）→ 回根
                if (names.length) { navigateTo([]); return; }
                alert(i18n.loadFailed);
            } finally {
                loading.value = false;
            }
        };

        // hashchange 入口：按当前 hash 加载对应层。
        const onHashChange = () => { loadLevel(parseHash()); };

        // 刷新当前层（路径不变）。
        const refreshCurrent = () => loadLevel(parseHash());

        // 写操作后：刷新当前层，并让搜索整树缓存失效（下次搜索重新拉）。
        const refreshAfterWrite = () => {
            searchTree.value = null;
            return loadLevel(parseHash());
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
                    refreshAfterWrite();
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
                    refreshAfterWrite();
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
            if (!currentDir.value) return '#';
            const pathParts = [...pathStack.value.map(p => p.name), fileName];
            return `${API_BASE}/files/${pathParts.join('/')}`;
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

        // 导航全部走 hash（改 location.hash → hashchange → loadLevel），
        // 这样浏览器前进/后退键天然生效。
        const goToLevel = (index) => {
            navigateTo(pathStack.value.slice(0, index + 1).map(p => p.name));
        };

        const goHome = () => { navigateTo([]); };

        const handleItemClick = (item) => {
            if (item.type === 'dir') {
                navigateTo([...pathStack.value.map(p => p.name), item.name]);
            } else {
                openFileViewer(item);
            }
        };

        // ── 搜索 ─────────────────────────────────────
        // 浏览走懒加载（只当前层），整树只在搜索时才向 /tree 拉一次并缓存，
        // 之后的搜索都在这份缓存里递归过滤——不触后端、不破“文件系统即唯一状态”。
        const isSearching = computed(() => searchQuery.value.trim() !== '');
        const searchTree = ref(null);       // /tree 整树缓存
        const searchTreeLoading = ref(false);
        // 跳转到搜索结果（文件）后，待当前层加载完再打开的文件名
        const pendingOpenFile = ref(null);

        // 首次进入搜索时拉整树（缓存）。
        const loadSearchTree = async () => {
            if (searchTree.value || searchTreeLoading.value) return;
            searchTreeLoading.value = true;
            try {
                if (isLoggedIn.value) {
                    const ok = await verifyToken();
                    if (!ok) { searchTreeLoading.value = false; return; }
                }
                const res = await fetchWithRetry(`${API_BASE}/tree`, authHeaders(), 3, 500);
                searchTree.value = await res.json();
            } catch (e) {
                console.error('加载搜索整树失败', e);
            } finally {
                searchTreeLoading.value = false;
            }
        };

        // 递归收集名字命中的节点，每个结果带 trail（祖先目录名链，不含根）。
        const searchResults = computed(() => {
            const q = searchQuery.value.trim().toLowerCase();
            if (!q || !searchTree.value) return [];
            const out = [];
            const walk = (node, trail) => {
                for (const child of node.children || []) {
                    if (child.name.toLowerCase().includes(q)) {
                        out.push({ node: child, trail });
                    }
                    if (child.type === 'dir' && child.children) {
                        walk(child, [...trail, child.name]);
                    }
                }
            };
            walk(searchTree.value, []);
            return out;
        });

        // 命中项所在目录的展示路径，根目录显示为 “/”。trail 现在是名字数组。
        const resultPath = (trail) => {
            if (!trail.length) return '/';
            return '/' + trail.join('/');
        };

        // 点击搜索结果：跳到该项所在目录（hash 导航）；文件则到父目录后打开查看器。
        const goToSearchResult = (result) => {
            const { node, trail } = result;
            searchQuery.value = '';
            if (node.type === 'dir') {
                navigateTo([...trail, node.name]);
            } else {
                pendingOpenFile.value = node.name;
                navigateTo(trail);
            }
        };

        const clearSearch = () => { searchQuery.value = ''; };

        // 进入/退出搜索态：进入时确保整树已加载。
        watch(isSearching, (on) => { if (on) loadSearchTree(); });

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

        const viewerCurrentImageUrl = ref('');
        let _prevImageBlobUrl = '';
        // 每次切图自增，丢弃过期的异步结果（快速翻页时旧请求晚返回不会覆盖当前页）
        let _viewerSeq = 0;

        const fetchAuthUrl = async (url) => {
            const token = localStorage.getItem('baka_token');
            if (!token) return url;
            const res = await fetch(url, { headers: { 'Authorization': `Bearer ${token}` } });
            if (!res.ok) return url;
            return URL.createObjectURL(await res.blob());
        };

        const closeViewer = () => {
            viewer.value = { show: false, type: null };
            viewerVideoUrl.value = '';
            if (_prevImageBlobUrl) { URL.revokeObjectURL(_prevImageBlobUrl); _prevImageBlobUrl = ''; }
            viewerCurrentImageUrl.value = '';
        };
        const viewerPrev = () => { if (viewerImageIndex.value > 0) viewerImageIndex.value--; };
        const viewerNext = () => { if (viewerImageIndex.value < viewerImageList.value.length - 1) viewerImageIndex.value++; };

        watch([viewerImageList, viewerImageIndex], async ([list, idx]) => {
            if (!list.length) { viewerCurrentImageUrl.value = ''; return; }
            const seq = ++_viewerSeq;
            // 先清空：翻页瞬间不残留上一张图，宁可短暂空白
            viewerCurrentImageUrl.value = '';
            if (_prevImageBlobUrl) { URL.revokeObjectURL(_prevImageBlobUrl); _prevImageBlobUrl = ''; }
            const url = await fetchAuthUrl(getFileUrl(list[idx].name));
            if (seq !== _viewerSeq) {
                // 已经翻到别的页，丢弃本次结果
                if (url.startsWith('blob:')) URL.revokeObjectURL(url);
                return;
            }
            if (url.startsWith('blob:')) _prevImageBlobUrl = url;
            viewerCurrentImageUrl.value = url;
        });

        // 目标选择器（复制/移动用）也走懒加载：pickerCurrent 是当前层的已加载节点。
        const pickerCurrent = ref(null);
        const loadPickerLevel = async (names) => {
            try {
                const qs = names.length ? `?path=${encodeURIComponent(names.join('/'))}` : '';
                const res = await fetchWithRetry(`${API_BASE}/list${qs}`, authHeaders(), 3, 500);
                pickerCurrent.value = await res.json();
                pickerStack.value = names.map(n => ({ name: n }));
            } catch (e) {
                console.error('选择器加载失败', e);
            }
        };
        const pickerDirs = computed(() => {
            const dir = pickerCurrent.value;
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
            loadPickerLevel(pathStack.value.map(p => p.name));
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
            loadPickerLevel([...pickerStack.value.map(p => p.name), dir.name]);
        };

        const pickerGoToLevel = (index) => {
            if (index < 0) { loadPickerLevel([]); return; }
            loadPickerLevel(pickerStack.value.slice(0, index + 1).map(p => p.name));
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
                refreshAfterWrite();
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
                refreshAfterWrite();
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
                refreshAfterWrite();
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
                refreshAfterWrite();
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
            window.addEventListener('hashchange', onHashChange);
            loadLevel(parseHash());   // 按初始 hash 加载（支持直链/刷新到某目录）
            window.addEventListener('keydown', onViewerKeydown);
        });

        onUnmounted(() => {
            window.removeEventListener('keydown', onViewerKeydown);
            window.removeEventListener('hashchange', onHashChange);
            stopPolling();
        });

        return {
            i18n,
            loading, isLoggedIn, currentUser, currentRole, isAdmin, showLogin, loginForm, authMode, pathStack, sortedFiles,
            searchQuery, isSearching, searchResults, resultPath, goToSearchResult, clearSearch,
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
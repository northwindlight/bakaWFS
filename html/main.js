const { createApp, ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } = Vue;

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
        const currentDir = ref(null);      // 当前层节点（/list?path= 浅扫 depth2 的结果）
        const pathStack = ref([]);         // 各层 { name }，供面包屑/拼路径（懒加载下只存名字）
        const loading = ref(false);

        // ── 搜索（整树一次性拉 /tree，仅搜索时）───────────────
        const searchQuery = ref('');
        const isSearching = computed(() => searchQuery.value.trim().length > 0);
        const searchTree = ref(null);       // /tree 整树缓存
        const searchTreeLoading = ref(false);
        // 搜索跳转到漫画本/文件后，待本层加载完再打开（见 loadLevel）
        const pendingOpenFile = ref(null);   // 打开指定文件名
        const pendingOpenBook = ref(false);  // 打开本层漫画本第一页
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
                const res = await fetchWithTimeout(`${API_BASE}/verify`, {
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

        // HTTP 非 2xx 的错误类型：带上 status，供调用方区分「鉴权失败」与「网络/服务端错误」。
        class HttpError extends Error {
            constructor(status) { super(`Server status: ${status}`); this.status = status; }
        }
        const isAuthError = (e) => e instanceof HttpError && (e.status === 401 || e.status === 403);

        // 带超时的 fetch：弱网下请求可能永久挂起（fetch 本身无超时），
        // 用 AbortController 到点中断，让上层能重试/报错，而不是把页面卡死在 loading。
        const FETCH_TIMEOUT = 15000;
        const fetchWithTimeout = (url, options = {}, timeout = FETCH_TIMEOUT) => {
            const controller = new AbortController();
            const timer = setTimeout(() => controller.abort(), timeout);
            // 调用方可自带 signal（如导航被后续操作取代要中断）——链到内部 controller 上。
            const ext = options.signal;
            if (ext) {
                if (ext.aborted) controller.abort();
                else ext.addEventListener('abort', () => controller.abort(), { once: true });
            }
            return fetch(url, { ...options, signal: controller.signal })
                .finally(() => clearTimeout(timer));
        };

        const fetchWithRetry = async (url, options = {}, retries = 3, backoff = 500) => {
            try {
                const res = await fetchWithTimeout(url, options);
                if (!res.ok) throw new HttpError(res.status);
                return res;
            } catch (err) {
                // 鉴权失败重试也没用（token 不会自己变好）——直接抛给调用方去弹登录。
                if (isAuthError(err)) throw err;
                // 被上层取代/取消（导航换页）→ 别再重试，静默抛出让调用方按序号丢弃。
                if (options.signal && options.signal.aborted) throw err;
                if (retries > 0) {
                    console.warn(`Connection retry ${retries}`);
                    await new Promise(r => setTimeout(r, backoff));
                    return fetchWithRetry(url, options, retries - 1, backoff * 1.5);
                }
                throw err;
            }
        };

        // ── 非全量树：懒加载浏览（/list?path= 浅扫 depth2）+ hash 路由 ───────
        // 浏览只拉当前层；整树只在搜索时向 /tree 拉一次。漫画语义全在前端从 Node 树推导。

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

        // 需要登录（/list 返回 401/403）：清失效 token、切登录界面。
        // 兼容 authMode 没探测成功的情形——服务器实际要鉴权就以服务器为准，纠正 authMode。
        const handleAuthRequired = () => {
            const wasLoggedIn = isLoggedIn.value;
            if (wasLoggedIn) {
                localStorage.removeItem('baka_token');
                localStorage.removeItem('baka_user');
                localStorage.removeItem('baka_role');
                isLoggedIn.value = false;
                currentUser.value = '';
                currentRole.value = '';
            }
            authMode.value = true;      // 服务器要鉴权 → 纠正可能漏探测到的 authMode，露出登录框
            currentDir.value = null;
            showLogin.value = true;
            if (wasLoggedIn) alert(i18n.tokenExpired);
        };

        // loadLevel 并发序号：弱网/快速翻页会有多个 loadLevel 同时在飞，
        // 只让「最后一次」导航的结果落地，丢弃过期请求，避免旧结果覆盖新页面 / loading 卡住。
        let _loadSeq = 0;
        let _loadAbort = null;   // 上一个还在飞的导航请求，被新导航取代时中断它，别互相抢弱网带宽

        // 向 /list?path= 拉指定层（深度2），刷新 currentDir + pathStack。
        const loadLevel = async (names) => {
            const seq = ++_loadSeq;
            if (_loadAbort) _loadAbort.abort();   // 取消上一次导航，避免请求堆积把新导航挤到超时
            const ac = new AbortController();
            _loadAbort = ac;
            loading.value = true;
            try {
                const qs = names.length ? `?path=${encodeURIComponent(names.join('/'))}` : '';
                const opts = { ...authHeaders(), signal: ac.signal };
                const res = await fetchWithRetry(`${API_BASE}/list${qs}`, opts, 3, 500);
                const data = await res.json();
                if (seq !== _loadSeq) return;   // 已被后续导航取代，丢弃这次结果
                currentDir.value = data;
                // pathStack 用名字段重建（懒加载下节点只需 name 供面包屑/拼路径）
                pathStack.value = names.map(n => ({ name: n }));
                // 进入漫画本：本层即该本，加载完打开第一页（封面图）
                if (pendingOpenBook.value) {
                    pendingOpenBook.value = false;
                    const cover = bookCover(data);
                    if (cover) {
                        nextTick(() => {
                            const first = sortedFiles.value.find(f => f.name === cover);
                            if (first) openFileViewer(first);
                        });
                    }
                }
                // 搜索跳转到具体文件：本层加载完打开该文件查看器
                if (pendingOpenFile.value) {
                    const fname = pendingOpenFile.value;
                    pendingOpenFile.value = null;
                    nextTick(() => {
                        const f = (data.children || []).find(c => c.name === fname && c.type === 'file');
                        if (f) openFileViewer(f);
                    });
                }
            } catch (e) {
                if (seq !== _loadSeq) return;   // 过期请求/被取代 的失败不打扰用户
                // 鉴权失败：token 失效或需要登录 → 弹登录，别当成"找不到路"
                if (isAuthError(e)) { handleAuthRequired(); return; }
                console.error(e);
                // 只有 404 才是「路径真没了」（被删/改名）；弱网超时/断网是 AbortError/TypeError，别混为一谈。
                const status = e instanceof HttpError ? e.status : 0;
                if (currentDir.value) {
                    // 已有内容在显示：弱网瞬时失败别用弹窗打断、也别踹回根（会先加载出来又弹"找不到路"）。
                    // 把地址栏还原到当前真实所在层，静默留在原地，用户可再点一次。
                    const cur = buildHash(pathStack.value.map(p => p.name));
                    if (location.hash !== cur) history.replaceState(null, '', cur);
                } else if (status === 404 && names.length) {
                    navigateTo([]);   // 冷启动深链到已失效路径 → 回根重来
                } else {
                    alert(i18n.loadFailed);   // 冷启动、啥都没有可显示，才提示
                }
            } finally {
                if (seq === _loadSeq) { loading.value = false; _loadAbort = null; }
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

        // ── 搜索：拉整树 → 在树上匹配名字 → 跳转 ───────────────
        const loadSearchTree = async () => {
            if (searchTree.value || searchTreeLoading.value) return;
            searchTreeLoading.value = true;
            try {
                const res = await fetchWithRetry(`${API_BASE}/tree`, authHeaders(), 3, 500);
                searchTree.value = await res.json();
            } catch (e) {
                if (isAuthError(e)) { handleAuthRequired(); return; }
                console.error('加载搜索树失败', e);
            } finally {
                searchTreeLoading.value = false;
            }
        };

        // 在整树上找名字含 query 的节点，返回结果数组。
        // 返回 { node, trail }（trail 为祖先名字数组，不含自身）。连载作品现在是真实目录，
        // 搜作品名直接命中该目录节点，无需对 .series 标记做特殊索引。
        const searchResults = computed(() => {
            const q = searchQuery.value.trim().toLowerCase();
            if (!q || !searchTree.value) return [];
            const out = [];
            const walk = (node, trail) => {
                for (const c of (node.children || [])) {
                    if (c.name.startsWith('.')) continue;   // 隐藏元数据不参与搜索
                    if (c.name.toLowerCase().includes(q)) {
                        out.push({ node: c, trail });
                        if (out.length >= 200) return;
                    }
                    if (c.type === 'dir') walk(c, [...trail, c.name]);
                    if (out.length >= 200) return;
                }
            };
            walk(searchTree.value, []);
            return out;
        });

        // 搜索结果的展示路径
        const resultPath = (trail) => '/' + trail.join('/');

        // 点击搜索结果：目录则进入（漫画本/话进阅读器，连载作品/普通目录进入浏览），
        // 文件则进入其父目录并打开。
        const goToSearchResult = (result) => {
            clearSearch();
            const { node, trail } = result;
            if (node.type === 'dir') {
                if (isMangaBook(node)) pendingOpenBook.value = true;
                navigateTo([...trail, node.name]);
            } else {
                pendingOpenFile.value = node.name;
                navigateTo(trail);
            }
        };

        const clearSearch = () => { searchQuery.value = ''; };

        // 开始搜索时确保整树已加载
        watch(isSearching, (on) => { if (on) loadSearchTree(); });

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

        const getThumbUrl = (fileName) => {
            if (!currentDir.value) return '';
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
            return [...currentDir.value.children]
                .filter(c => !c.name.startsWith('.'))   // 隐藏文件（.crawled.json 等爬虫元数据）不显示
                .sort((a, b) => {
                    if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
                    return naturalCompare(a.name, b.name);
                });
        });

        // ── 漫画本识别（纯前端，从 Node 树推导，后端无感）───────────
        // 一个 dir 若 children 全是图片（忽略 article.md/.json/.log 等），视为「一本漫画」。
        const SKIP_META = /\.(md|json|log|txt)$|^\.series-/i;
        const isMangaBook = (node) => {
            if (!node || node.type !== 'dir' || !node.children || !node.children.length) return false;
            const files = node.children.filter(c => c.type === 'file' && !SKIP_META.test(c.name));
            if (!files.length) return false;
            // 全部非 meta 文件都是图片，且没有子目录
            return node.children.every(c =>
                c.type === 'file' && (SKIP_META.test(c.name) || isImageFile(c.name))
            ) && files.some(c => isImageFile(c.name));
        };

        // ── 连载作品识别（方案 D：连载在目录结构上就是一层）───────────
        // 连载作品目录 = 含子目录（各话）的目录；单本则 children 全是图片。
        // eh_make_series 已把同一连载的多话收进 汉化组/作品名/，作品根放封面 1.jpg。
        const isSeriesDir = (node) => {
            if (!node || node.type !== 'dir' || !node.children) return false;
            return node.children.some(c => c.type === 'dir');
        };

        // 连载作品的话数（直接子目录数）。
        const chapterCount = (node) => {
            if (!node || !node.children) return 0;
            return node.children.filter(c => c.type === 'dir').length;
        };

        // 汉化组层的作品列表：单本（book，点击进阅读器）、连载作品（series，点击进话列表）、
        // 其余普通目录（dir）。连载不再靠 .series 标记聚合——它本身就是一层目录。
        // 返回 [{ name, type:'series'|'book'|'dir', node }]
        const aggregatedBooks = computed(() => {
            const out = [];
            for (const n of sortedFiles.value) {
                if (n.type !== 'dir') continue;
                if (isMangaBook(n)) out.push({ name: n.name, type: 'book', node: n });
                else if (isSeriesDir(n)) out.push({ name: n.name, type: 'series', node: n });
                else out.push({ name: n.name, type: 'dir', node: n });
            }
            return out;
        });

        // 取一本漫画的封面（排序后第一张图，通常 1.jpg）
        const bookCover = (node) => {
            if (!node || !node.children) return null;
            const imgs = node.children
                .filter(c => c.type === 'file' && isImageFile(c.name))
                .sort((a, b) => naturalCompare(a.name, b.name));
            return imgs.length ? imgs[0].name : null;
        };

        // 一本漫画的页数（图片数）
        const bookPageCount = (node) => {
            if (!node || !node.children) return 0;
            return node.children.filter(c => c.type === 'file' && isImageFile(c.name)).length;
        };

        // ── article.md 元数据解析 ───────────────────────
        // 目录名信息量低，article.md 里有真标题/原标题/来源/页数。
        // bookMeta: 漫画本名 -> { title, titleJpn, source, group, pages }，异步填充。
        const bookMeta = reactive({});
        const _metaFetching = new Set();

        const parseArticleMd = (text) => {
            const meta = {};
            const lines = text.split('\n');
            for (const line of lines) {
                const t = line.trim();
                if (t.startsWith('# ') && !meta.title) {
                    meta.title = t.slice(2).trim();
                } else if (t.startsWith('> ')) {
                    const body = t.slice(2).trim();
                    if (body.startsWith('标题：') || body.startsWith('标题:')) {
                        // DeepSeek 清理后的真标题，优先级最高，覆盖 # 行的原始大杂烩
                        meta.cleanTitle = body.replace(/^标题[：:]/, '').trim();
                    } else if (body.startsWith('作者：') || body.startsWith('作者:')) {
                        // 只取第一条，无论空否。DeepSeek 空占位挡住后面的UP主
                        if (!('author' in meta)) {
                            const a = body.replace(/^作者[：:]/, '').trim();
                            meta.author = a;
                        }
                    } else if (body.startsWith('来源：') || body.startsWith('来源:')) {
                        meta.source = body.replace(/^来源[：:]/, '').split('|')[0].trim();
                    } else if (body.startsWith('汉化组：') || body.startsWith('汉化组:')) {
                        meta.group = body.replace(/^汉化组[：:]/, '').trim();
                    } else if (body.startsWith('原标题：') || body.startsWith('原标题:')) {
                        meta.titleJpn = body.replace(/^原标题[：:]/, '').trim();
                    } else if (body.startsWith('页数：') || body.startsWith('页数:')) {
                        meta.pages = parseInt(body.replace(/^页数[：:]/, '').trim(), 10) || 0;
                    }
                }
            }
            // 真标题优先：DeepSeek 清理版 > # 行原始标题
            if (meta.cleanTitle) meta.title = meta.cleanTitle;
            return meta;
        };

        // 给一本漫画拉 article.md（若有）并缓存元数据。relPath = 该书相对根的路径。
        const fetchBookMeta = async (relPath, bookName, node) => {
            if (_metaFetching.has(relPath) || bookMeta[bookName]) return;
            const hasArticle = (node.children || []).some(c => c.type === 'file' && c.name.toLowerCase() === 'article.md');
            if (!hasArticle) return;
            _metaFetching.add(relPath);
            try {
                const token = localStorage.getItem('baka_token');
                const opts = token ? { headers: { 'Authorization': `Bearer ${token}` } } : {};
                const url = `${API_BASE}/files/${relPath.split('/').map(encodeURIComponent).join('/')}/article.md`;
                const res = await fetch(url, opts);
                if (res.ok) {
                    bookMeta[bookName] = parseArticleMd(await res.text());
                }
            } catch (_) { /* 静默 */ }
            finally { _metaFetching.delete(relPath); }
        };

        // 显示标题：优先 article.md 的标题，回退目录名
        const bookTitle = (node) => {
            const m = bookMeta[node.name];
            return (m && m.title) ? m.title : node.name;
        };

        // 当前目录下：哪些子项是漫画本、哪些是普通目录/文件
        const currentIsBookGrid = computed(() =>
            sortedFiles.value.some(n => isMangaBook(n) || isSeriesDir(n))
        );

        // 分组网格（汉化组层）：靠爬虫标记文件判定，而非脆弱地推导子目录内容。
        // 不同来源爬虫写不同名的标记（.crawled.json / .nono_crawl.log / .sanada_recrawl.log …），
        // 统一匹配「.<隐藏> 且含 crawl」的文件 → 当前是漫画库根 = 渲染汉化组大卡片。
        const CRAWL_MARK = /^\..*crawl/i;
        const currentIsGroupGrid = computed(() => {
            const ch = currentDir.value?.children || [];
            return ch.some(c => c.type === 'file' && CRAWL_MARK.test(c.name));
        });

        // 取汉化组封面墙（前 4 部作品）。组列表层是浅扫（depth2），汉化组的直接子目录
        // 就是「作品」（单本或连载作品目录），故直接列前 4 个子目录；封面按约定 组/作品/1.jpg
        // 直取（单本是下载的 1.jpg、连载作品根有迁移时拷的 1.jpg），见 loadCovers。
        const groupBooks = (node) => {
            if (!node || !node.children) return [];
            return node.children
                .filter(c => c.type === 'dir')
                .sort((a, b) => naturalCompare(a.name, b.name))
                .slice(0, 4)
                .map(c => ({ name: c.name, coverNode: c }));
        };
        const groupBookCount = (node) => {
            if (!node || !node.children) return 0;
            return node.children.filter(c => c.type === 'dir').length;
        };

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

        // ── 漫画本封面 URL 生成 ───────────────────────
        // 漫画本目录名(或"组名/本名") -> 封面中图的 URL。
        // 用可缓存 URL 交给 <img>，浏览器原生 HTTP 缓存 + lazy 调度，不手动 fetch。
        const coverMap = ref({});
        const loadCovers = (dir) => {
            coverMap.value = {};
            if (!dir || !dir.children) return;

            const prefix = pathStack.value.map(p => p.name).join('/');
            const pathToKey = {};
            const paths = [];

            // 按界面显示顺序（自然排序）拉，封面才从上到下依次出现
            const sortedChildren = [...dir.children].sort((a, b) => naturalCompare(a.name, b.name));

            // 1) 当前目录下的作品：单本封面取首图 1.jpg；连载作品封面取根部 1.jpg
            //    （迁移时已拷到作品根，是该目录的直接图片 child，bookCover 能取到）。
            for (const c of sortedChildren) {
                if (!isMangaBook(c) && !isSeriesDir(c)) continue;
                const cover = bookCover(c);
                if (!cover) continue;
                const p = (prefix ? `${prefix}/` : '') + `${c.name}/${cover}`;
                pathToKey[p] = c.name;
                paths.push(p);
            }

            // 2) 分组层（组列表层）：封面墙取每组前 4 本，封面按约定文件名 1.jpg
            //    （img @error 回退 1.png，见 onCoverError）。组层是浅扫，本子未展开，
            //    无法用 bookCover（需图片层），故复用 groupBooks(g) 列出本子目录后约定直取。
            for (const g of sortedChildren) {
                if (g.type !== 'dir') continue;
                for (const w of groupBooks(g)) {
                    const p = (prefix ? `${prefix}/` : '') + `${g.name}/${w.coverNode.name}/1.jpg`;
                    pathToKey[p] = `${g.name}/${w.name}`;   // 键用作品名
                    paths.push(p);
                }
            }
            if (!paths.length) return;

            // 直接给 <img> 一个可缓存的 URL（带 token 走 query，鉴权模式也能用），
            // 让浏览器原生 HTTP 缓存接管（后端已发 Cache-Control: max-age=86400）。
            // 切目录/刷新/翻回来都命中磁盘缓存，不重新下载。
            // 不再 fetch+createObjectURL —— blob URL 是临时的、绕过 HTTP 缓存。
            const token = localStorage.getItem('baka_token');
            const tokenQ = token ? `&token=${encodeURIComponent(token)}` : '';
            const next = { ...coverMap.value };
            for (const relPath of paths) {
                const enc = relPath.split('/').map(encodeURIComponent).join('/');
                next[pathToKey[relPath]] = `${API_BASE}/thumb/${enc}?size=mid${tokenQ}`;
            }
            coverMap.value = next;
        };

        // 组级封面墙按约定取 1.jpg，若该本封面其实是 1.png 则首次加载失败，
        // 在此把 src 的 1.jpg 换成 1.png 重试一次（已回退过则不再换，避免死循环）。
        const onCoverError = (e) => {
            const img = e.target;
            if (img.dataset.coverFallback || !img.src.includes('/1.jpg?')) return;
            img.dataset.coverFallback = '1';
            img.src = img.src.replace('/1.jpg?', '/1.png?');
        };

        // 批量拉当前目录下漫画本的 article.md 元数据（真标题）
        const loadBookMetas = (dir) => {
            if (!dir || !dir.children) return;
            const prefix = pathStack.value.map(p => p.name).join('/');
            for (const c of dir.children) {
                if (!isMangaBook(c)) continue;
                const rel = (prefix ? `${prefix}/` : '') + c.name;
                fetchBookMeta(rel, c.name, c);
            }
        };

        watch(currentDir, (dir) => { loadThumbnails(dir); loadCovers(dir); loadBookMetas(dir); });

        // 导航全部走 hash（改 location.hash → hashchange → loadLevel）。
        const goToLevel = (index) => {
            navigateTo(pathStack.value.slice(0, index + 1).map(p => p.name));
        };

        const goHome = () => { navigateTo([]); };

        const handleItemClick = (item) => {
            if (item.type === 'dir') {
                // 漫画本/话：进入目录后直接翻页阅读（看第一页）。
                // 连载作品目录（isSeriesDir）不直接阅读——进入后看话列表（currentIsBookGrid）。
                if (isMangaBook(item)) {
                    pendingOpenBook.value = true;
                }
                navigateTo([...pathStack.value.map(p => p.name), item.name]);
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
        // url -> blobUrl 缓存（key 含 ?size= 区分中图/原图），viewer 关闭时统一释放
        const _imageBlobCache = new Map();
        const _imageFetchingMap = new Map();
        const PRELOAD_AHEAD = 10;
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

        // 后台静默预加载周边中图+原图，并提前让浏览器解码中图（Image() onload）
        const preloadImages = (list, centerIdx) => {
            const start = Math.max(0, centerIdx - PRELOAD_BEHIND);
            const end   = Math.min(list.length - 1, centerIdx + PRELOAD_AHEAD);
            for (let i = start; i <= end; i++) {
                getImageBlobUrl(`${getThumbUrl(list[i].name)}?size=mid`).then(blobUrl => {
                    const img = new Image(); img.src = blobUrl; // 触发浏览器解码
                }).catch(() => {});
                getImageBlobUrl(getFileUrl(list[i].name)).catch(() => {});
            }
        };

        // 等待浏览器解码完这张图，再设置 src 就零延迟渲染
        const decodeThenSet = (blobUrl) => {
            return new Promise(resolve => {
                const img = new Image();
                img.onload = () => resolve();
                img.onerror = () => resolve(); // 解码失败也别卡死
                img.src = blobUrl;
            });
        };

        const closeViewer = () => {
            viewer.value = { show: false, type: null };
            viewerVideoUrl.value = '';
            viewerCurrentImageUrl.value = '';
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

            // 原图已在缓存（顺序翻页时被 preload 预取过，强网常态）→ 直接上原图、跳过中图，
            // 消除"先糊一下再清晰"的无谓闪烁。中图占位只是弱网/首图/随机跳转的保底。
            if (_imageBlobCache.has(fullUrl)) {
                const fullBlob = _imageBlobCache.get(fullUrl);
                await decodeThenSet(fullBlob);
                if (seq === _viewerSeq) viewerCurrentImageUrl.value = fullBlob;
                preloadImages(list, idx);
                return;
            }

            // 立即清空旧帧，避免旧 key 的 img 残留
            viewerCurrentImageUrl.value = '';
            // 中图先显示——等解码完再赋值，一设即出
            try {
                const midBlob = await getImageBlobUrl(midUrl);
                if (seq !== _viewerSeq) return;
                await decodeThenSet(midBlob);
                if (seq === _viewerSeq) viewerCurrentImageUrl.value = midBlob;
            } catch (_) {}

            // 原图：后台拉+预解码完替换中图
            getImageBlobUrl(fullUrl).then(async fullBlob => {
                if (seq !== _viewerSeq) return;
                await decodeThenSet(fullBlob);
                if (seq === _viewerSeq) viewerCurrentImageUrl.value = fullBlob;
            }).catch(() => {});

            // 预加载周边中图
            preloadImages(list, idx);
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
            // 探测服务器鉴权模式。必须带重试：弱网下若静默失败，authMode 会误停在 false，
            // 强制鉴权模式下登录框(v-if=authMode&&!isLoggedIn)就不出，卡在空的主界面。
            try {
                const res = await fetchWithRetry(`${API_BASE}/api/config`, {}, 3, 500);
                authMode.value = !!(await res.json()).auth_mode;
            } catch (_) {
                // 配置都拉不到也不慌：下面 loadLevel 若撞 401 会由 handleAuthRequired 纠正 authMode。
            }

            // 导航监听无条件注册：鉴权模式登录后也要能翻目录，
            // 否则 hash 变了却没人处理 hashchange → URL 改了页面不动。
            window.addEventListener('hashchange', onHashChange);
            window.addEventListener('keydown', onViewerKeydown);

            if (authMode.value && !isLoggedIn.value) {
                showLogin.value = true;
                return;
            }

            if (isLoggedIn.value) await verifyToken();
            loadLevel(parseHash());   // 按初始 hash 加载（支持直链/刷新到某目录）
        });

        onUnmounted(() => {
            window.removeEventListener('hashchange', onHashChange);
            window.removeEventListener('keydown', onViewerKeydown);
            stopPolling();
        });

        return {
            i18n,
            loading, isLoggedIn, currentUser, currentRole, isAdmin, showLogin, loginForm, authMode, pathStack, sortedFiles,
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
            thumbMap, isImageFile,
            // 搜索
            searchQuery, isSearching, searchTreeLoading, searchResults, resultPath, goToSearchResult, clearSearch,
            // 漫画版渲染
            isMangaBook, bookCover, bookPageCount, coverMap, currentIsBookGrid,
            currentIsGroupGrid, groupBooks, groupBookCount, onCoverError, bookMeta, bookTitle, aggregatedBooks, isSeriesDir, chapterCount,
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
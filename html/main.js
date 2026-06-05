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
        const rootData = ref(null);
        const currentDir = ref(null);
        const pathStack = ref([]);
        const loading = ref(false);
        const currentSeries = ref(null);   // 连载话列表视图（非 null 时显示话列表而非海报网格）
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
                if (!rootData.value) fetchData();
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

        // ── 连载聚合（靠 .series-作品名 标记文件，不靠正则猜）───────────
        // 话目录下放 .series-作品名 空文件，AI 扫码写。前端只读不猜。
        const seriesMarkFile = (node) => {
            if (!node || node.type !== 'dir' || !node.children) return null;
            const m = node.children.find(c => c.type === 'file' && c.name.startsWith('.series-'));
            return m ? m.name.slice(8) : null;  // '.series-'.length = 8
        };

        // 聚合：同标记名的漫画本合成一部连载；无标记的单本原样；非漫画目录保留。
        // 返回 [{ name, type:'series'|'book'|'dir', node?, chapters?, coverNode? }]
        const aggregatedBooks = computed(() => {
            const items = sortedFiles.value.filter(n => isMangaBook(n));
            const seriesMap = new Map();
            const out = [];
            for (const n of items) {
                const mark = seriesMarkFile(n);
                if (mark) {
                    if (!seriesMap.has(mark)) {
                        const entry = { name: mark, type: 'series', chapters: [] };
                        seriesMap.set(mark, entry);
                        out.push(entry);
                    }
                    seriesMap.get(mark).chapters.push(n);
                } else {
                    out.push({ name: n.name, type: 'book', node: n });
                }
            }
            // 连载话按自然排序，封面取第一话
            for (const e of out) {
                if (e.type === 'series') {
                    e.chapters.sort((a, b) => naturalCompare(a.name, b.name));
                    e.coverNode = e.chapters[0];
                }
            }
            // 补入非漫画子目录
            for (const n of sortedFiles.value) {
                if (n.type === 'dir' && !isMangaBook(n)) {
                    out.push({ name: n.name, type: 'dir', node: n });
                }
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
            sortedFiles.value.some(n => isMangaBook(n))
        );

        // 分组网格（汉化组层）：靠爬虫标记文件判定，而非脆弱地推导子目录内容。
        // 不同来源爬虫写不同名的标记（.crawled.json / .nono_crawl.log / .sanada_recrawl.log …），
        // 统一匹配「.<隐藏> 且含 crawl」的文件 → 当前是漫画库根 = 渲染汉化组大卡片。
        const CRAWL_MARK = /^\..*crawl/i;
        const currentIsGroupGrid = computed(() => {
            const ch = currentDir.value?.children || [];
            return ch.some(c => c.type === 'file' && CRAWL_MARK.test(c.name));
        });

        // 取汉化组封面墙（聚合后前 4 部，连载靠 .series-* 标记）
        // 先自然排序，封面墙取的「前 4 部」才和封面网格显示顺序一致（后端 children 是默认排序）。
        const groupBooks = (node) => {
            if (!node || !node.children) return [];
            const items = node.children.filter(c => isMangaBook(c))
                .sort((a, b) => naturalCompare(a.name, b.name));
            const seriesMap = new Map();
            const works = [];
            for (const n of items) {
                const mark = seriesMarkFile(n);
                if (mark) {
                    if (!seriesMap.has(mark)) {
                        const entry = { name: mark, coverNode: n };
                        seriesMap.set(mark, entry);
                        works.push(entry);
                    }
                    // 取自然排序第一话当封面
                    const cur = seriesMap.get(mark);
                    if (naturalCompare(n.name, cur.coverNode.name) < 0) {
                        cur.coverNode = n;
                        cur.name = mark;
                    }
                } else {
                    works.push({ name: n.name, coverNode: n });
                }
            }
            return works.slice(0, 4);
        };
        const groupBookCount = (node) => {
            if (!node || !node.children) return 0;
            const items = node.children.filter(c => isMangaBook(c));
            const keys = new Set();
            for (const n of items) {
                keys.add(seriesMarkFile(n) || n.name);
            }
            return keys.size;
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

            // 1) 当前目录下的漫画本：封面 key = 本名
            for (const c of sortedChildren) {
                if (!isMangaBook(c)) continue;
                const cover = bookCover(c);
                if (!cover) continue;
                const p = (prefix ? `${prefix}/` : '') + `${c.name}/${cover}`;
                pathToKey[p] = c.name;
                paths.push(p);
            }

            // 2) 分组层：聚合连载后取前 4 部作品的封面（键= "组名/作品名"）
            for (const g of sortedChildren) {
                if (g.type !== 'dir') continue;
                // 自然排序后再取前 4 部，和 groupBooks 渲染的封面墙选用同一批本子
                const items = (g.children || []).filter(c => isMangaBook(c))
                    .sort((a, b) => naturalCompare(a.name, b.name));
                // 聚合连载（靠 .series-* 标记）
                const sMap = new Map();
                const works = [];
                for (const n of items) {
                    const mark = seriesMarkFile(n);
                    if (mark) {
                        if (!sMap.has(mark)) { const e = { name: mark, coverNode: n }; sMap.set(mark, e); works.push(e); }
                        const cur = sMap.get(mark);
                        if (naturalCompare(n.name, cur.coverNode.name) < 0) cur.coverNode = n;
                    } else {
                        works.push({ name: n.name, coverNode: n });
                    }
                }
                // 取前 4 部作品的封面
                for (const w of works.slice(0, 4)) {
                    const cover = bookCover(w.coverNode);
                    if (!cover) continue;
                    const p = (prefix ? `${prefix}/` : '') + `${g.name}/${w.coverNode.name}/${cover}`;
                    pathToKey[p] = `${g.name}/${w.name}`;   // 键用聚合名
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

        const goToLevel = (index) => {
            pathStack.value = pathStack.value.slice(0, index + 1);
            currentDir.value = pathStack.value[pathStack.value.length - 1];
        };

        const goHome = () => {
            pathStack.value = [];
            currentDir.value = rootData.value;
        };

        // ── 连载系列视图 ───────────────────────
        const openSeries = (series) => { currentSeries.value = series; };
        const closeSeries = () => { currentSeries.value = null; };
        const openChapter = (chapterNode) => {
            pathStack.value.push(chapterNode);
            currentDir.value = chapterNode;
            closeSeries();   // 回到目录树，进阅读器
            const cover = bookCover(chapterNode);
            if (cover) {
                nextTick(() => {
                    const first = sortedFiles.value.find(f => f.name === cover);
                    if (first) openFileViewer(first);
                });
            }
        };

        const handleItemClick = (item) => {
            if (item.type === 'dir') {
                // 漫画本：进入目录后直接翻页阅读（看第一页）
                if (isMangaBook(item)) {
                    pathStack.value.push(item);
                    currentDir.value = item;
                    const cover = bookCover(item);
                    if (cover) {
                        // 等 sortedFiles 跟着 currentDir 更新后再开阅读器
                        nextTick(() => {
                            const first = sortedFiles.value.find(f => f.name === cover);
                            if (first) openFileViewer(first);
                        });
                    }
                    return;
                }
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
            // 漫画版渲染
            isMangaBook, bookCover, bookPageCount, coverMap, currentIsBookGrid,
            currentIsGroupGrid, groupBooks, groupBookCount, bookMeta, bookTitle, aggregatedBooks, currentSeries, openSeries, closeSeries, openChapter,
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
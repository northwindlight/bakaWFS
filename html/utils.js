export const generateTaskId = () => 'task_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9);

export const isValidUrl = (url) => {
    try { new URL(url); return true; } catch (e) { return false; }
};

export const formatSpeed = (bytesPerSec) => {
    if (bytesPerSec === undefined || bytesPerSec === null) return '--';
    if (bytesPerSec === 0) return '0 B/s';
    const k = 1024;
    const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
    const i = Math.floor(Math.log(bytesPerSec) / Math.log(k));
    return parseFloat((bytesPerSec / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

export const formatSize = (bytes) => {
    if (!bytes) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

// 首字符类别：0=数字、1=标点、2=中文、3=英文/其它。
// 用于分组排序：数字 → 标点（半角+全角）→ 中文 → 英文。
const nameCategory = (s) => {
    const c = String(s).trimStart().charCodeAt(0);
    if (c >= 48 && c <= 57) return 0;            // 0-9
    if (c >= 0x4e00 && c <= 0x9fff) return 2;    // CJK 统一表意文字
    // 标点：ASCII 标点区 + 全角/CJK 标点（含括号、书名号等）
    const isPunct =
        (c >= 0x21 && c <= 0x2f) || (c >= 0x3a && c <= 0x40) ||
        (c >= 0x5b && c <= 0x60) || (c >= 0x7b && c <= 0x7e) ||  // 半角标点
        (c >= 0x3000 && c <= 0x303f) ||   // CJK 符号与标点（、。「」【】《》等）
        (c >= 0xff00 && c <= 0xff0f) || (c >= 0xff1a && c <= 0xff20) ||
        (c >= 0xff3b && c <= 0xff40) || (c >= 0xff5b && c <= 0xff65);  // 全角标点
    if (isPunct) return 1;
    return 3;                                     // 拉丁字母及其它
};

// 标点组内子级：0=全角标点（在前）、1=半角标点（在后）。
const punctWidth = (s) => {
    const c = String(s).trimStart().charCodeAt(0);
    return c < 0x80 ? 1 : 0;   // ASCII (<0x80) = 半角，排后面
};

export const naturalCompare = (a, b) => {
    // 先按类别分组：数字 < 标点 < 中文 < 英文
    const ca = nameCategory(a), cb = nameCategory(b);
    if (ca !== cb) return ca - cb;

    // 标点组内：全角在前，半角在后
    if (ca === 1) {
        const wa = punctWidth(a), wb = punctWidth(b);
        if (wa !== wb) return wa - wb;
    }

    // 同组内走自然排序（数字段按数值，其余按本地化字符比较）
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

// 文件查看器相关工具
const IMAGE_EXTS = new Set(['jpg','jpeg','png','gif','webp','bmp','svg','avif','ico','tiff']);
const VIDEO_EXTS = new Set(['mp4','webm','ogg','mov','mkv','avi','flv','m4v','ts']);
const TEXT_EXTS  = new Set(['txt','md','log','json','xml','yaml','yml','csv','ini','conf','toml','sh','py','js','ts','html','css','htm','env','rs','go','java','c','cpp','h','php','rb']);

export const getFileExt = (name) => (name.split('.').pop() || '').toLowerCase();

export const classifyFile = (name) => {
    const ext = getFileExt(name);
    if (IMAGE_EXTS.has(ext)) return 'image';
    if (VIDEO_EXTS.has(ext)) return 'video';
    if (TEXT_EXTS.has(ext))  return 'text';
    return 'other';
};

export const fileTypeIcon = (name) => {
    const ext = getFileExt(name);
    if (IMAGE_EXTS.has(ext)) return '🖼️';
    if (VIDEO_EXTS.has(ext)) return '🎬';
    if (TEXT_EXTS.has(ext))  return '📝';
    const m = {'pdf':'📕','zip':'📦','rar':'📦','7z':'📦','tar':'📦','gz':'📦','xz':'📦',
               'exe':'⚙️','dmg':'💿','iso':'💿','apk':'📱',
               'doc':'📘','docx':'📘','xls':'📗','xlsx':'📗','ppt':'📙','pptx':'📙',
               'mp3':'🎵','flac':'🎵','wav':'🎵','aac':'🎵'};
    return m[ext] || '🗂️';
};
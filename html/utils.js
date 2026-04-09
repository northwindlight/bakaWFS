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

export const naturalCompare = (a, b) => {
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
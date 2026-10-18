// 只复制可公开分发的运行库和许可证；不包含本机字体或部署配置。
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '..');
const out = path.join(root, 'html/vendor');
fs.mkdirSync(out, { recursive: true });
for (const [source, target] of [
  ['vue/dist/vue.global.prod.js', 'vue.global.prod.js'],
  ['vue/LICENSE', 'vue-LICENSE.txt'],
  ['xxhash-wasm/esm/xxhash-wasm.js', 'xxhash-wasm.js'],
  ['xxhash-wasm/LICENSE.md', 'xxhash-wasm-LICENSE.txt'],
  ['tailwindcss/LICENSE', 'tailwindcss-LICENSE.txt'],
]) fs.copyFileSync(path.join(root, 'node_modules', source), path.join(out, target));

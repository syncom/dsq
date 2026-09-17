// A minimal contenteditable rich-text editor limited to bold, italic,
// ordered/unordered lists and pasted images, serializing to Markdown.

import { blocksToMarkdown, markdownToBlocks } from './markdown.js';

const FILES_PATH = 'api/files/';
const IMAGE_TYPES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);
const ALLOWED_FORMATS = new Set(['formatBold', 'formatItalic', 'formatIndent', 'formatOutdent']);
const BLOCKED_INPUTS = new Set(['insertLink', 'insertHorizontalRule', 'insertFromPasteAsQuotation']);
const EDITOR_TAGS = new Set(['P', 'DIV', 'BR', 'B', 'STRONG', 'I', 'EM', 'UL', 'OL', 'LI', 'IMG']);
const DROP_TAGS = new Set([
  'SCRIPT', 'STYLE', 'TEMPLATE', 'HEAD', 'TITLE', 'META', 'LINK', 'IFRAME', 'OBJECT', 'EMBED', 'SVG',
  'MATH', 'CANVAS', 'VIDEO', 'AUDIO', 'NOSCRIPT', 'BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'HR',
]);
const BLOCK_TAGS = new Set([
  'P', 'DIV', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'BLOCKQUOTE', 'PRE', 'TABLE', 'THEAD', 'TBODY', 'TFOOT',
  'TR', 'TD', 'TH', 'CAPTION', 'SECTION', 'ARTICLE', 'HEADER', 'FOOTER', 'ASIDE', 'NAV', 'MAIN', 'FIGURE',
  'FIGCAPTION', 'ADDRESS', 'DL', 'DT', 'DD', 'LI',
]);

const isList = (node) => node?.nodeType === Node.ELEMENT_NODE && (node.tagName === 'UL' || node.tagName === 'OL');

/** Pasted images not yet uploaded, shared by all editors on the page. */
export class ImageStore {
  #byToken = new Map();
  #tokenByUrl = new Map();

  add(blob) {
    const bytes = crypto.getRandomValues(new Uint8Array(12));
    const token = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
    const url = URL.createObjectURL(blob);
    this.#byToken.set(token, { blob, url });
    this.#tokenByUrl.set(url, token);
    return { token, url };
  }

  blob(token) { return this.#byToken.get(token)?.blob; }
  url(token) { return this.#byToken.get(token)?.url; }
  tokenForUrl(url) { return this.#tokenByUrl.get(url); }

  clear() {
    for (const { url } of this.#byToken.values()) URL.revokeObjectURL(url);
    this.#byToken.clear();
    this.#tokenByUrl.clear();
  }
}

export class Editor {
  /**
   * @param {{label: string, labelId: string, images: ImageStore,
   *          onChange: () => void, onNotice: (msg: string) => void}} opts
   */
  constructor({ labelId, images, onChange, onNotice }) {
    this.images = images;
    this.onChange = onChange;
    this.onNotice = onNotice;

    this.element = el('div', 'editor');
    this.toolbar = el('div', 'toolbar');
    this.toolbar.setAttribute('role', 'toolbar');
    this.buttons = [
      this.#button('bold', 'B', 'Bold (Ctrl+B)'),
      this.#button('italic', 'I', 'Italic (Ctrl+I)'),
      this.#button('insertUnorderedList', '•', 'Bulleted list'),
      this.#button('insertOrderedList', '1.', 'Numbered list'),
    ];
    this.toolbar.append(...this.buttons);

    this.area = el('div', 'area');
    this.area.contentEditable = 'true';
    this.area.spellcheck = true;
    this.area.setAttribute('role', 'textbox');
    this.area.setAttribute('aria-multiline', 'true');
    this.area.setAttribute('aria-labelledby', labelId);
    this.element.append(this.toolbar, this.area);

    this.area.addEventListener('beforeinput', (e) => this.#onBeforeInput(e));
    this.area.addEventListener('input', () => this.#onInput());
    this.area.addEventListener('keydown', (e) => this.#onKeyDown(e));
    this.area.addEventListener('paste', (e) => this.#onPaste(e));
    this.area.addEventListener('dragover', (e) => e.preventDefault());
    this.area.addEventListener('drop', (e) => this.#onDrop(e));
    this.setMarkdown('');
  }

  setMarkdown(md) {
    this.area.replaceChildren();
    renderBlocks(markdownToBlocks(md), this.area, this.images);
    if (!this.area.firstChild) this.area.append(el('p', null, el('br')));
  }

  getMarkdown() {
    return blocksToMarkdown(domToBlocks(this.area, this.images));
  }

  setEnabled(enabled) {
    this.area.contentEditable = enabled ? 'true' : 'false';
    for (const b of this.buttons) b.disabled = !enabled;
  }

  /** Refreshes toolbar button states for the current selection. */
  updateToolbar() {
    const inside = this.area.contains(document.getSelection()?.anchorNode ?? null);
    for (const b of this.buttons) {
      let on = false;
      try { on = inside && document.queryCommandState(b.dataset.command); } catch { /* unsupported */ }
      b.setAttribute('aria-pressed', String(on));
    }
  }

  #button(command, text, title) {
    const b = el('button', 'tool', text);
    b.type = 'button';
    b.title = title;
    b.setAttribute('aria-label', title);
    b.setAttribute('aria-pressed', 'false');
    b.dataset.command = command;
    b.addEventListener('mousedown', (e) => e.preventDefault()); // keep the selection
    b.addEventListener('click', () => {
      if (!this.area.contains(document.getSelection()?.anchorNode ?? null)) this.area.focus();
      document.execCommand(command);
      this.#onInput();
    });
    return b;
  }

  #onBeforeInput(e) {
    const t = e.inputType;
    if ((t.startsWith('format') && !ALLOWED_FORMATS.has(t)) || BLOCKED_INPUTS.has(t)) e.preventDefault();
  }

  #onInput() {
    // Browsers sometimes add inline styles when merging blocks; they carry no meaning here.
    for (const node of this.area.querySelectorAll('[style]')) node.removeAttribute('style');
    this.updateToolbar();
    this.onChange();
  }

  #onKeyDown(e) {
    if (e.key !== 'Tab' || e.ctrlKey || e.altKey || e.metaKey) return;
    const node = document.getSelection()?.anchorNode;
    const li = (node?.nodeType === Node.ELEMENT_NODE ? node : node?.parentElement)?.closest('li');
    if (li && this.area.contains(li)) {
      e.preventDefault();
      document.execCommand(e.shiftKey ? 'outdent' : 'indent');
      this.#onInput();
    }
  }

  #onPaste(e) {
    e.preventDefault();
    const dt = e.clipboardData;
    if (!dt) return;
    const html = dt.getData('text/html');
    if (html) {
      const clean = sanitizeHtml(html, this.images);
      if (clean.textContent.trim() || clean.querySelector('img')) {
        document.execCommand('insertHTML', false, clean.innerHTML);
        this.#afterInsert();
        return;
      }
    }
    if (this.#insertImageFiles(dt)) return;
    const text = dt.getData('text/plain');
    if (text) document.execCommand('insertText', false, text);
  }

  #onDrop(e) {
    e.preventDefault();
    if (!e.dataTransfer) return;
    const pos = document.caretPositionFromPoint?.(e.clientX, e.clientY);
    const range = pos ? rangeAt(pos.offsetNode, pos.offset) : document.caretRangeFromPoint?.(e.clientX, e.clientY);
    if (range && this.area.contains(range.startContainer)) {
      const sel = document.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
    } else {
      this.area.focus();
    }
    this.#insertImageFiles(e.dataTransfer);
  }

  #insertImageFiles(dt) {
    const files = [...dt.files];
    if (!files.length) return false;
    const images = files.filter((f) => IMAGE_TYPES.has(f.type));
    if (images.length < files.length) this.onNotice('Only PNG, JPEG, GIF and WebP images can be added.');
    if (!images.length) return true;
    const html = images
      .map((f) => {
        const { token, url } = this.images.add(f);
        return `<img src="${url}" data-token="${token}" alt="">`;
      })
      .join('');
    document.execCommand('insertHTML', false, html);
    this.#afterInsert();
    return true;
  }

  #afterInsert() {
    scrub(this.area, this.images);
    this.#onInput();
  }
}

// ---------- DOM -> model ----------

function domToBlocks(root, images) {
  const blocks = [];
  let para = [];
  const endPara = () => {
    if (para.some((n) => n.type === 'img' || (n.type === 'text' && n.text.trim()))) {
      blocks.push({ type: 'p', inlines: para });
    }
    para = [];
  };
  const walk = (node, b, i) => {
    for (const child of node.childNodes) {
      if (child.nodeType === Node.TEXT_NODE) {
        para.push({ type: 'text', text: child.data, b, i });
        continue;
      }
      if (child.nodeType !== Node.ELEMENT_NODE) continue;
      const tag = child.tagName;
      if (isList(child)) {
        endPara();
        const list = domToList(child, images);
        if (list.items.length) blocks.push(list);
      } else if (tag === 'BR') {
        para.push({ type: 'br' });
      } else if (tag === 'IMG') {
        const src = imageSource(child, images);
        if (src) para.push({ type: 'img', src });
      } else if (!DROP_TAGS.has(tag)) {
        const nb = b || tag === 'B' || tag === 'STRONG';
        const ni = i || tag === 'I' || tag === 'EM';
        if (BLOCK_TAGS.has(tag)) {
          endPara();
          walk(child, nb, ni);
          endPara();
        } else {
          walk(child, nb, ni);
        }
      }
    }
  };
  walk(root, false, false);
  endPara();
  return blocks;
}

function domToList(listEl, images) {
  const list = { type: 'list', ordered: listEl.tagName === 'OL', items: [] };
  for (const child of listEl.childNodes) {
    if (child.nodeType !== Node.ELEMENT_NODE) continue;
    if (child.tagName === 'LI') {
      list.items.push(domToItem(child, images));
    } else if (isList(child)) {
      // Some browsers nest lists as siblings of <li> rather than inside one.
      const nested = domToList(child, images);
      if (!nested.items.length) continue;
      const last = list.items[list.items.length - 1];
      if (last) last.children.push(nested);
      else list.items.push({ inlines: [], children: [nested] });
    }
  }
  list.items = list.items.filter((item) => item.inlines.length || item.children.length);
  return list;
}

function domToItem(li, images) {
  const item = { inlines: [], children: [] };
  for (const block of domToBlocks(li, images)) {
    if (block.type === 'list') {
      item.children.push(block);
    } else {
      if (item.inlines.length) item.inlines.push({ type: 'br' });
      item.inlines.push(...block.inlines);
    }
  }
  return item;
}

function imageSource(img, images) {
  if (img.dataset.token && images.blob(img.dataset.token)) return `pending:${img.dataset.token}`;
  if (img.dataset.file) return img.dataset.file;
  return null;
}

// ---------- model -> DOM ----------

function renderBlocks(blocks, parent, images) {
  for (const block of blocks) {
    if (block.type === 'list') {
      parent.append(renderList(block, images));
    } else {
      const p = el('p');
      renderInlines(block.inlines, p, images);
      parent.append(p);
    }
  }
}

function renderList(list, images) {
  const listEl = el(list.ordered ? 'ol' : 'ul');
  for (const item of list.items) {
    const li = el('li');
    renderInlines(item.inlines, li, images);
    if (!li.firstChild) li.append(el('br'));
    for (const child of item.children) li.append(renderList(child, images));
    listEl.append(li);
  }
  return listEl;
}

function renderInlines(inlines, parent, images) {
  for (const node of inlines) {
    if (node.type === 'br') {
      parent.append(el('br'));
    } else if (node.type === 'img') {
      const img = imageElement(node.src, images);
      if (img) parent.append(img);
    } else {
      let out = document.createTextNode(node.text);
      if (node.i) out = el('em', null, out);
      if (node.b) out = el('strong', null, out);
      parent.append(out);
    }
  }
}

function imageElement(src, images) {
  const img = el('img');
  img.alt = '';
  if (src.startsWith('pending:')) {
    const token = src.slice('pending:'.length);
    if (!images.url(token)) return null;
    img.src = images.url(token);
    img.dataset.token = token;
  } else {
    img.src = FILES_PATH + encodeURIComponent(src);
    img.dataset.file = src;
  }
  return img;
}

// ---------- paste sanitizing ----------

/** Converts arbitrary HTML into the editor's small element vocabulary. */
function sanitizeHtml(html, images) {
  // The page's CSP also applies to parsed documents and would block (and log)
  // inline styles, so move them to an inert attribute and read them as text.
  const inert = html.replace(/(<[a-z][^>]*?\s)style(\s*=)/gi, '$1data-paste-style$2');
  const doc = new DOMParser().parseFromString(inert, 'text/html');
  const out = el('div');
  for (const child of doc.body.childNodes) convertNode(child, out, false, false, images);
  return out;
}

function convertNode(node, dst, b, i, images) {
  const tag = node.nodeType === Node.ELEMENT_NODE ? node.tagName : null;
  if (isList(dst) && tag !== 'LI' && tag !== 'UL' && tag !== 'OL') {
    if (node.nodeType === Node.TEXT_NODE && !node.data.trim()) return;
    const li = el('li');
    convertNode(node, li, b, i, images);
    if (li.firstChild) dst.append(li);
    return;
  }
  if (node.nodeType === Node.TEXT_NODE) {
    let out = document.createTextNode(node.data);
    if (i) out = el('em', null, out);
    if (b) out = el('strong', null, out);
    dst.append(out);
    return;
  }
  if (!tag || DROP_TAGS.has(tag)) return;

  const style = node.getAttribute('data-paste-style') || '';
  const weight = fontWeight(cssValue(style, 'font-weight'));
  b = tag === 'B' || tag === 'STRONG' ? (weight ?? true) : (weight ?? b);
  const italic = fontItalic(cssValue(style, 'font-style'));
  i = tag === 'I' || tag === 'EM' ? (italic ?? true) : (italic ?? i);

  let target;
  if (tag === 'BR') {
    dst.append(el('br'));
    return;
  } else if (tag === 'IMG') {
    const img = importImage(node.getAttribute('src') || '', images);
    if (img) dst.append(img);
    return;
  } else if (tag === 'UL' || tag === 'OL' || (tag === 'LI' && isList(dst))) {
    target = el(tag.toLowerCase());
  } else if (BLOCK_TAGS.has(tag)) {
    target = el('div'); // headers, tables, quotes etc. become plain paragraphs
  } else {
    for (const child of node.childNodes) convertNode(child, dst, b, i, images);
    return;
  }
  for (const child of node.childNodes) convertNode(child, target, b, i, images);
  if (target.firstChild) dst.append(target);
}

function cssValue(style, property) {
  const m = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;!]+)`, 'i').exec(style);
  return m ? m[1].trim().toLowerCase() : '';
}

function fontWeight(value) {
  if (!value) return null;
  if (value === 'bold' || value === 'bolder') return true;
  if (value === 'normal' || value === 'lighter') return false;
  const n = Number(value);
  return Number.isFinite(n) ? n >= 600 : null;
}

function fontItalic(value) {
  if (value === 'italic' || value === 'oblique') return true;
  if (value === 'normal') return false;
  return null;
}

/** Returns an editor <img> for a pasted image source, or null if it cannot be kept. */
function importImage(src, images) {
  const data = /^data:(image\/(?:png|jpeg|gif|webp));base64,([A-Za-z0-9+/=\s]+)$/.exec(src);
  if (data) {
    try {
      const bin = atob(data[2].replace(/\s+/g, ''));
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      const { token } = images.add(new Blob([bytes], { type: data[1] }));
      return imageElement(`pending:${token}`, images);
    } catch {
      return null;
    }
  }
  const token = images.tokenForUrl(src);
  if (token) return imageElement(`pending:${token}`, images);
  const file = savedFileName(src);
  return file ? imageElement(file, images) : null;
}

function savedFileName(src) {
  try {
    const url = new URL(src, document.baseURI);
    const base = new URL(FILES_PATH, document.baseURI);
    if (url.origin !== base.origin || !url.pathname.startsWith(base.pathname)) return null;
    const name = decodeURIComponent(url.pathname.slice(base.pathname.length));
    return name && !name.includes('/') ? name : null;
  } catch {
    return null;
  }
}

/** Removes anything outside the editor's vocabulary that the browser introduced on insert. */
function scrub(root, images) {
  for (const node of [...root.querySelectorAll('*')]) {
    if (!node.isConnected) continue;
    if (node.tagName === 'IMG') {
      const src = node.getAttribute('src') || '';
      let token = node.dataset.token || images.tokenForUrl(src);
      let file = node.dataset.file || (!token && savedFileName(src));
      if (token && !images.blob(token)) token = null;
      for (const attr of [...node.attributes]) node.removeAttribute(attr.name);
      if (token) {
        node.src = images.url(token);
        node.dataset.token = token;
      } else if (file) {
        node.src = FILES_PATH + encodeURIComponent(file);
        node.dataset.file = file;
      } else {
        node.remove();
        continue;
      }
      node.alt = '';
      continue;
    }
    for (const attr of [...node.attributes]) node.removeAttribute(attr.name);
    if (!EDITOR_TAGS.has(node.tagName)) node.replaceWith(...node.childNodes);
  }
}

// ---------- helpers ----------

function el(tag, className, ...children) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.append(...children);
  return node;
}

function rangeAt(node, offset) {
  const range = document.createRange();
  range.setStart(node, offset);
  range.collapse(true);
  return range;
}

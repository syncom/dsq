// Conversion between Markdown and a small document model.
//
// The model only knows what answers may contain:
//   Block  = { type: 'p', inlines: Inline[] }
//          | { type: 'list', ordered: boolean, items: Item[] }
//   Item   = { inlines: Inline[], children: Block[] }   (children are lists)
//   Inline = { type: 'text', text, b, i } | { type: 'br' } | { type: 'img', src }
//
// Output uses "**" for bold, "*" for italic, "- " / "1. " lists indented to
// the item content, "\" line-end hard breaks, and backslash-escapes every
// character that could otherwise introduce headers, links, tables or HTML.

const ESCAPE_RE = /[\\`*_[\]<>#|&~]/g;
const PUNCT_RE = /[!-/:-@[-`{-~]/;
const LIST_RE = /^( *)([-+*]|\d{1,9}[.)])(?: +(.*)|$)/;

// ---------- model -> Markdown ----------

export function blocksToMarkdown(blocks) {
  const parts = [];
  for (const block of blocks) {
    const md = block.type === 'list' ? listToLines(block, '').join('\n') : inlinesToMarkdown(block.inlines);
    if (md.trim()) parts.push(md);
  }
  return parts.join('\n\n');
}

function listToLines(list, indent) {
  const lines = [];
  list.items.forEach((item, idx) => {
    const marker = list.ordered ? `${idx + 1}.` : '-';
    const pad = ' '.repeat(marker.length + 1);
    const content = inlinesToMarkdown(item.inlines).split('\n');
    lines.push(indent + marker + (content[0] ? ' ' + content[0] : ''));
    for (const line of content.slice(1)) lines.push(indent + pad + line);
    for (const child of item.children) lines.push(...listToLines(child, indent + pad));
  });
  return lines;
}

function escapeText(text, atLineStart) {
  let out = text.replace(ESCAPE_RE, '\\$&');
  if (atLineStart) {
    out = out.replace(/^([-+=])/, '\\$1').replace(/^(\d+)([.)])/, '$1\\$2');
  }
  return out;
}

export function inlinesToMarkdown(inlines) {
  let out = '';
  let bold = false;
  let italic = false;
  let order = []; // open delimiters, innermost last
  let pendingSpace = false;
  let lineStart = true;

  const setFormat = (b, i) => {
    let closes = '';
    let opens = '';
    for (const d of [...order].reverse()) {
      if ((d === '**' && bold && !b) || (d === '*' && italic && !i)) closes += d;
    }
    order = order.filter((d) => (d === '**' ? b : i));
    if (b && !bold) { opens += '**'; order.push('**'); }
    if (i && !italic) { opens += '*'; order.push('*'); }
    bold = b;
    italic = i;
    return [closes, opens];
  };

  // Drop leading/trailing breaks; browsers often leave a trailing <br>.
  let start = 0;
  let end = inlines.length;
  while (start < end && inlines[start].type === 'br') start++;
  while (end > start && inlines[end - 1].type === 'br') end--;

  for (const node of mergeText(inlines.slice(start, end))) {
    if (node.type === 'br') {
      const [closes] = setFormat(false, false);
      out += closes + '\\\n';
      pendingSpace = false;
      lineStart = true;
      continue;
    }
    if (node.type === 'img') {
      if (pendingSpace && !lineStart) out += ' ';
      pendingSpace = false;
      out += `![](${node.src})`;
      lineStart = false;
      continue;
    }
    const text = node.text.replace(/\s+/g, ' ');
    const core = text.trim();
    if (!core) {
      if (text) pendingSpace = true;
      continue;
    }
    if (text[0] === ' ') pendingSpace = true;
    const [closes, opens] = setFormat(!!node.b, !!node.i);
    out += closes;
    if (pendingSpace && !lineStart) out += ' ';
    out += opens;
    out += escapeText(core, lineStart && !closes && !opens);
    pendingSpace = text[text.length - 1] === ' ';
    lineStart = false;
  }
  out += setFormat(false, false)[0];
  return out;
}

// Merges adjacent text nodes with the same formatting, so escaping sees
// the text as it will appear (e.g. "9" + ". x" must be escaped as a whole).
function mergeText(inlines) {
  const out = [];
  for (const node of inlines) {
    const last = out[out.length - 1];
    if (node.type === 'text' && last?.type === 'text' && !!last.b === !!node.b && !!last.i === !!node.i) {
      out[out.length - 1] = { ...last, text: last.text + node.text };
    } else {
      out.push(node);
    }
  }
  return out;
}

// ---------- Markdown -> model ----------

export function markdownToBlocks(md) {
  const lines = (md || '').replace(/\r\n?/g, '\n').replace(/\t/g, '    ').split('\n');
  return parseBlocks(lines);
}

function indentOf(line) {
  return line.length - line.trimStart().length;
}

function parseBlocks(lines) {
  const blocks = [];
  let i = 0;
  while (i < lines.length) {
    if (!lines[i].trim()) { i++; continue; }
    if (LIST_RE.test(lines[i])) {
      const [list, next] = parseList(lines, i);
      blocks.push(list);
      i = next;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !LIST_RE.test(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    blocks.push({ type: 'p', inlines: parseInlines(joinParagraph(para)) });
  }
  return blocks;
}

// Joins paragraph lines, turning two-space line endings into "\" hard breaks.
function joinParagraph(lines) {
  return lines
    .map((l, idx) => {
      let line = l.trimStart();
      if (idx < lines.length - 1 && / {2,}$/.test(line)) line = line.trimEnd() + '\\';
      return idx < lines.length - 1 && /\\$/.test(line) ? line : line.trimEnd();
    })
    .join('\n');
}

function parseList(lines, start) {
  const first = LIST_RE.exec(lines[start]);
  const baseIndent = first[1].length;
  const ordered = /\d/.test(first[2]);
  const list = { type: 'list', ordered, items: [] };
  let i = start;

  while (i < lines.length) {
    // Blank lines between items of the same list do not end it.
    let j = i;
    while (j < lines.length && !lines[j].trim()) j++;
    const m = j < lines.length ? LIST_RE.exec(lines[j]) : null;
    if (!m || m[1].length !== baseIndent || /\d/.test(m[2]) !== ordered) break;
    i = j;

    const contentIndent = baseIndent + m[2].length + 1;
    const itemLines = [m[3] ?? ''];
    i++;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        let k = i;
        while (k < lines.length && !lines[k].trim()) k++;
        if (k < lines.length && indentOf(lines[k]) > baseIndent) {
          for (; i < k; i++) itemLines.push('');
          continue;
        }
        break;
      }
      const indent = indentOf(line);
      if (indent > baseIndent) {
        itemLines.push(line.slice(Math.min(indent, contentIndent)));
      } else if (!LIST_RE.test(line) && itemLines[itemLines.length - 1].trim()) {
        itemLines.push(line.trimStart()); // lazy continuation
      } else {
        break;
      }
      i++;
    }
    list.items.push(parseItem(itemLines));
  }
  return [list, i];
}

function parseItem(lines) {
  const item = { inlines: [], children: [] };
  for (const block of parseBlocks(lines)) {
    if (block.type === 'list') {
      item.children.push(block);
    } else {
      if (item.inlines.length) item.inlines.push({ type: 'br' });
      item.inlines.push(...block.inlines);
    }
  }
  return item;
}

export function parseInlines(s) {
  const out = [];
  let bold = false;
  let italic = false;
  let buf = '';
  const flush = () => {
    if (buf) out.push({ type: 'text', text: buf, b: bold, i: italic });
    buf = '';
  };

  for (let k = 0; k < s.length; ) {
    const c = s[k];
    if (c === '\\') {
      const next = s[k + 1];
      if (next === '\n') { flush(); out.push({ type: 'br' }); k += 2; continue; }
      if (next !== undefined && PUNCT_RE.test(next)) { buf += next; k += 2; continue; }
      buf += c; k++; continue;
    }
    if (c === '\n') { buf += ' '; k++; continue; }
    if (c === '*') {
      let n = 0;
      while (s[k + n] === '*') n++;
      const before = k === 0 ? ' ' : s[k - 1];
      const after = s[k + n] ?? ' ';
      if (/\s/.test(before) && /\s/.test(after)) { buf += '*'.repeat(n); k += n; continue; }
      flush();
      if ((n >> 1) & 1) bold = !bold;
      if (n & 1) italic = !italic;
      k += n;
      continue;
    }
    if (c === '!' && s[k + 1] === '[') {
      const m = /^!\[[^\]\n]*\]\(([^()\s]*)\)/.exec(s.slice(k));
      if (m) { flush(); out.push({ type: 'img', src: m[1] }); k += m[0].length; continue; }
    }
    buf += c;
    k++;
  }
  flush();
  return out;
}

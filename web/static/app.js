import { Editor, ImageStore } from './editor.js';

const $ = (id) => document.getElementById(id);

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

const state = {
  puid: '',
  headers: {},
  submitterHeader: 'X-Submitter-Id',
  editors: new Map(),
  images: new ImageStore(),
  dirty: false,
  saving: false,
};

async function api(path, options = {}) {
  const res = await fetch(path, { ...options, headers: { ...state.headers, ...options.headers } });
  let body = null;
  try {
    body = await res.json();
  } catch {
    /* not JSON */
  }
  if (!res.ok) throw new ApiError(res.status, body?.error || `${res.status} ${res.statusText}`);
  return body;
}

function questionnairePath(suffix = '') {
  return `api/questionnaires/${encodeURIComponent(state.puid)}${suffix}`;
}

function parseTimestamp(ts) {
  const m = /^(\d{4})(\d\d)(\d\d)T(\d\d)(\d\d)(\d\d)\.(\d{3})\d*Z$/.exec(ts || '');
  return m ? new Date(Date.UTC(+m[1], m[2] - 1, +m[3], +m[4], +m[5], +m[6], +m[7])) : null;
}

function formatTimestamp(ts) {
  const d = parseTimestamp(ts);
  return d ? d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ts;
}

function showNotice(message, kind = 'info') {
  const notice = $('notice');
  notice.textContent = message;
  notice.dataset.kind = kind;
  notice.hidden = !message;
}

function setSaveState(text, kind = '') {
  const el = $('save-state');
  el.textContent = text;
  el.dataset.kind = kind;
}

function markDirty() {
  if (state.saving) return;
  state.dirty = true;
  setSaveState('Unsaved changes', 'dirty');
}

async function main() {
  const params = new URLSearchParams(location.search);
  state.puid = params.get('puid') || '';
  if (!state.puid) {
    showStart(params);
    return;
  }

  document.title = `Questionnaire · ${state.puid}`;
  $('title').textContent = state.puid;

  try {
    const config = await api('api/config');
    state.submitterHeader = config.submitterHeader;
    // For local testing without a proxy; in production the proxy sets the header.
    const submitter = params.get('submitter');
    if (submitter) state.headers[config.submitterHeader] = submitter;

    const [questions, latest] = await Promise.all([api('api/questions'), api(questionnairePath('/latest'))]);
    renderQuestions(questions);
    applyLatest(latest);
    $('questionnaire').hidden = false;
    loadHistory(latest.submitterId);
  } catch (err) {
    $('subtitle').textContent = '';
    if (err.status === 400 && /submitter/i.test(err.message)) {
      showNotice(
        `No submitter identity was received. The service expects it in the ${state.submitterHeader} header, ` +
          'normally set by the reverse proxy. For local testing, add &submitter=NAME to the URL.',
        'error',
      );
    } else {
      showNotice(`Could not load the questionnaire: ${err.message}`, 'error');
    }
  }
}

function showStart(params) {
  $('title').textContent = 'Open a questionnaire';
  $('start').hidden = false;
  $('start').addEventListener('submit', (e) => {
    e.preventDefault();
    const next = new URLSearchParams({ puid: $('start-puid').value.trim() });
    if (params.get('submitter')) next.set('submitter', params.get('submitter'));
    location.search = next.toString();
  });
  $('start-puid').focus();
}

function renderQuestions(questions) {
  const list = $('questions');
  list.replaceChildren();
  state.editors.clear();
  questions.forEach((q, index) => {
    const item = document.createElement('li');
    item.className = 'card question';
    const labelId = `question-${index}`;
    const label = document.createElement('h2');
    label.id = labelId;
    label.className = 'prompt';
    const number = document.createElement('span');
    number.className = 'number';
    number.textContent = String(index + 1);
    label.append(number, document.createTextNode(q.prompt));

    const editor = new Editor({
      labelId,
      images: state.images,
      onChange: markDirty,
      onNotice: (msg) => showNotice(msg, 'error'),
    });
    item.append(label, editor.element);
    list.append(item);
    state.editors.set(q.id, editor);
  });
}

function applyLatest(latest) {
  $('submitter').textContent = `Submitter: ${latest.submitterId}`;
  $('submitter').hidden = !latest.submitterId;
  for (const [id, editor] of state.editors) editor.setMarkdown(latest.answers[id] || '');
  state.dirty = false;
  if (latest.timestamp) {
    $('subtitle').textContent = `Editing your response last saved ${formatTimestamp(latest.timestamp)}`;
    setSaveState('All changes saved', 'saved');
  } else {
    $('subtitle').textContent = 'New response';
    setSaveState('');
  }
}

async function loadHistory(submitterId) {
  try {
    const versions = (await api(questionnairePath('/versions'))).filter((v) => v.submitterId === submitterId);
    const list = $('history-list');
    list.replaceChildren(
      ...versions.map((v, i) => {
        const li = document.createElement('li');
        li.textContent = formatTimestamp(v.timestamp) + (i === 0 ? ' (current)' : '');
        li.title = v.filename;
        return li;
      }),
    );
    $('history').hidden = versions.length === 0;
    $('history').querySelector('summary').textContent = `Version history (${versions.length})`;
  } catch {
    $('history').hidden = true;
  }
}

async function save(e) {
  e.preventDefault();
  if (state.saving) return;

  const answers = {};
  const tokens = new Set();
  for (const [id, editor] of state.editors) {
    answers[id] = editor.getMarkdown();
    for (const m of answers[id].matchAll(/!\[\]\(pending:([A-Za-z0-9_-]+)\)/g)) tokens.add(m[1]);
  }
  const body = new FormData();
  body.append('answers', JSON.stringify(answers));
  for (const token of tokens) body.append(`image:${token}`, state.images.blob(token), token);

  state.saving = true;
  $('save').disabled = true;
  for (const editor of state.editors.values()) editor.setEnabled(false);
  setSaveState('Saving…');
  showNotice('');
  try {
    const saved = await api(questionnairePath(), { method: 'POST', body });
    const latest = await api(questionnairePath('/latest'));
    applyLatest(latest);
    state.images.clear();
    setSaveState(`Saved ${formatTimestamp(saved.timestamp)}`, 'saved');
    loadHistory(latest.submitterId);
  } catch (err) {
    setSaveState('Not saved', 'error');
    if (err.status === 409) {
      showNotice('Another save of this questionnaire is still in progress. Wait a moment and try again.', 'error');
    } else if (err.status === 413) {
      showNotice('The response is too large to save. Try using smaller images.', 'error');
    } else {
      showNotice(`Saving failed: ${err.message}`, 'error');
    }
  } finally {
    state.saving = false;
    $('save').disabled = false;
    for (const editor of state.editors.values()) editor.setEnabled(true);
  }
}

$('questionnaire').addEventListener('submit', save);
document.addEventListener('selectionchange', () => {
  for (const editor of state.editors.values()) editor.updateToolbar();
});
window.addEventListener('beforeunload', (e) => {
  if (state.dirty) e.preventDefault();
});

try {
  document.execCommand('defaultParagraphSeparator', false, 'p');
} catch {
  /* unsupported */
}
main();

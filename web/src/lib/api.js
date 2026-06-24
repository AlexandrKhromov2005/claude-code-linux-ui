// REST API client. Token is read from location.hash once and reused.

function getToken() {
  const hash = window.location.hash.slice(1);
  const params = new URLSearchParams(hash);
  return params.get('token') || '';
}

const token = getToken();

function authHeaders() {
  return { Authorization: `Bearer ${token}` };
}

async function get(path) {
  const res = await fetch(path, { headers: authHeaders() });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}

async function post(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { ...authHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${path}: ${res.status}`);
  }
  return res.json();
}

export const api = {
  token,

  getState: () => get('/api/state'),
  getProjects: () => get('/api/projects'),
  openProject: (slug) => post('/api/projects/open', { slug }),
  useProject: (cwd, mode) => post('/api/projects/use', mode ? { cwd, mode } : { cwd }),

  getThreads: () => get('/api/threads'),
  openThread: (id) => post('/api/threads/open', { id }),
  newThread: () => post('/api/threads/new', {}),
  deleteThread: (id) => post('/api/threads/delete', { id }),

  search: (q) => get(`/api/search?q=${encodeURIComponent(q)}`),

  setMode: (mode) => post('/api/mode', { mode }),
  setSkipPerms: (skip) => post('/api/permissions/skip', { skip }),
  setEffort: (effort) => post('/api/effort', { effort }),
  getMemory: () => get('/api/memory'),
  setMemory: (content) => post('/api/memory', { content }),
  setTheme: (name) => post('/api/theme', { name }),
  setBudget: (usd) => post('/api/budget', { usd }),
  exportThread: (path) => post('/api/export', path ? { path } : {}),

  async upload(file) {
    const form = new FormData();
    form.append('file', file);
    const res = await fetch('/api/upload', {
      method: 'POST',
      headers: authHeaders(),
      body: form,
    });
    if (!res.ok) throw new Error(`upload: ${res.status}`);
    return res.json();
  },
};

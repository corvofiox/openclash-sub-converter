/* 订阅转换管理台 —— 原生 JS 单页，零依赖 */
'use strict';

const $ = (sel) => document.querySelector(sel);
const TOKEN_KEY = 'osc_token';
const state = { editingSourceId: null, editingTemplateId: null };

/* ---------------- 通用工具 ---------------- */

function toast(msg, isErr) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (isErr ? ' err' : '');
  clearTimeout(t._timer);
  t._timer = setTimeout(() => t.classList.add('hidden'), 2600);
}

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// 复制文本：优先 Clipboard API，失败回退 textarea 全选 + execCommand
async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch (e) { /* fallthrough */ }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) { /* ignore */ }
  ta.remove();
  return ok;
}

async function readErr(res) {
  try {
    const j = await res.json();
    return j && j.error ? j.error : ('HTTP ' + res.status);
  } catch (e) {
    return 'HTTP ' + res.status;
  }
}

/* ---------------- 认证：apiFetch 统一封装 ---------------- */

// auth401Streak 连续 401 计数：≥2 时不再自动弹令牌框（token 持续错误时
// 每次请求弹窗会无限递归），只提示；任何非 401 响应重置。
let auth401Streak = 0;

// 401 时弹出令牌输入框，保存后自动重试原请求；弹窗前先移除已存在的
// 模态框（id 复用），避免并发请求叠加多个弹窗。
function askToken() {
  return new Promise((resolve) => {
    const existing = document.getElementById('tokenModal');
    if (existing) existing.remove();
    const modal = document.createElement('div');
    modal.id = 'tokenModal';
    modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:100;display:flex;align-items:center;justify-content:center;';
    modal.innerHTML = `
      <div class="card" style="width:380px;margin:0">
        <h3>需要管理台令牌</h3>
        <p class="hint">服务端设置了 OSC_ADMIN_TOKEN，请填写后重试。</p>
        <input type="password" id="modalToken" placeholder="管理台令牌（X-Token）" style="width:100%">
        <div class="form-actions">
          <button id="modalOk" class="btn primary">保存并重试</button>
          <button id="modalCancel" class="btn">取消</button>
        </div>
      </div>`;
    modal.querySelector('#modalOk').onclick = () => {
      const v = modal.querySelector('#modalToken').value.trim();
      if (!v) return;
      localStorage.setItem(TOKEN_KEY, v);
      document.body.removeChild(modal);
      updateAuthBadge();
      resolve(v);
    };
    modal.querySelector('#modalCancel').onclick = () => {
      document.body.removeChild(modal);
      resolve(null);
    };
    modal.querySelector('#modalToken').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') modal.querySelector('#modalOk').click();
    });
    document.body.appendChild(modal);
    modal.querySelector('#modalToken').focus();
  });
}

async function apiFetch(path, opts) {
  opts = opts || {};
  const headers = Object.assign({}, opts.headers || {});
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) headers['X-Token'] = token;
  let res = await fetch(path, Object.assign({}, opts, { headers }));
  if (res.status === 401) {
    auth401Streak++;
    if (auth401Streak >= 2) {
      // 连续 401：停止自动弹窗（防递归），提示用户检查令牌
      console.warn('apiFetch: repeated 401 for ' + path + ', skip token modal');
      $('#banner401').classList.remove('hidden');
      const err = new Error('未授权（401）：请检查管理台令牌');
      err.status = 401;
      throw err;
    }
    const newToken = await askToken();
    if (!newToken) {
      const err = new Error('未授权（401）');
      err.status = 401;
      throw err;
    }
    res = await apiFetch(path, opts); // 用新令牌重试
  } else {
    auth401Streak = 0; // 正常响应重置连续计数
  }
  return res;
}

async function apiJSON(path, opts) {
  const res = await apiFetch(path, opts);
  if (!res.ok) throw new Error(await readErr(res));
  if (res.status === 204) return null;
  return res.json();
}

function updateAuthBadge() {
  const badge = $('#authBadge');
  const token = localStorage.getItem(TOKEN_KEY);
  badge.className = 'badge ' + (token ? 'badge-auth' : 'badge-unknown');
  badge.textContent = token ? '已保存令牌' : '未设置令牌';
  // 用 /api/v1 探活确认实际鉴权状态
  apiFetch('/api/v1/sources').then((res) => {
    if (res.ok) {
      badge.className = 'badge badge-ok';
      badge.textContent = token ? '已认证' : 'API 正常（无鉴权）';
      $('#banner401').classList.add('hidden');
    } else {
      badge.className = 'badge badge-auth';
      badge.textContent = '需要令牌';
      $('#banner401').classList.remove('hidden');
    }
  }).catch(() => {
    badge.className = 'badge badge-unknown';
    badge.textContent = 'API 不可用';
  });
}

/* ---------------- Tab 切换 ---------------- */

function switchTab(name) {
  document.querySelectorAll('.tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === name));
  document.querySelectorAll('.sec').forEach((s) => s.classList.toggle('active', s.id === 'sec-' + name));
  if (name === 'sources') loadSources();
  if (name === 'convert') loadConvertOptions();
  if (name === 'logs') loadLogs();
  if (name === 'templates') loadTemplates();
  if (name === 'auth') syncAuthForm();
}
$('#tabs').addEventListener('click', (e) => {
  const btn = e.target.closest('.tab');
  if (btn) switchTab(btn.dataset.tab);
});

/* ---------------- 顶部：版本 ---------------- */

async function loadVersion() {
  try {
    const v = await (await fetch('/version')).json();
    $('#version').textContent = 'v' + v.version + ' · mihomo ' + v.mihomo;
  } catch (e) {
    $('#version').textContent = '版本获取失败';
  }
}

/* ---------------- 订阅源管理 ---------------- */

async function loadSources() {
  const tbody = $('#sourceRows');
  try {
    const data = await apiJSON('/api/v1/sources');
    const list = data.sources || [];
    if (!list.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty">暂无订阅源，点击「新增订阅源」创建</td></tr>';
      return;
    }
    tbody.innerHTML = list.map((s) => `
      <tr>
        <td>${esc(s.name)}</td>
        <td class="url" title="${esc(s.url)}">${esc(s.url)}</td>
        <td><input type="checkbox" data-id="${esc(s.id)}" class="src-toggle" ${s.enabled ? 'checked' : ''}></td>
        <td><div class="row-actions">
          <button class="btn small" data-act="edit" data-id="${esc(s.id)}">编辑</button>
          <button class="btn small danger" data-act="del" data-id="${esc(s.id)}">删除</button>
        </div></td>
      </tr>`).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="4" class="empty">加载失败：${esc(e.message)}</td></tr>`;
  }
}

function resetSourceForm() {
  state.editingSourceId = null;
  $('#sourceFormTitle').textContent = '新增订阅源';
  $('#srcName').value = '';
  $('#srcUrl').value = '';
  $('#srcUrl').placeholder = 'https://example.com/sub?token=...';
  $('#srcEnabled').checked = true;
  $('#sourceForm').classList.add('hidden');
}

$('#btnNewSource').onclick = () => {
  resetSourceForm();
  $('#sourceForm').classList.remove('hidden');
  $('#srcName').focus();
};
$('#btnCancelSource').onclick = resetSourceForm;

$('#sourceForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const body = { name: $('#srcName').value.trim(), enabled: $('#srcEnabled').checked };
  if (state.editingSourceId) {
    // 编辑：URL 留空表示保持不变（列表里是脱敏 URL，不能回填）
    if ($('#srcUrl').value.trim()) body.url = $('#srcUrl').value.trim();
    try {
      await apiJSON('/api/v1/sources/' + state.editingSourceId, {
        method: 'PUT', body: JSON.stringify(body),
      });
      toast('订阅源已更新');
    } catch (err) { toast('更新失败：' + err.message, true); }
  } else {
    body.url = $('#srcUrl').value.trim();
    if (!body.url) { toast('请填写订阅 URL', true); return; }
    try {
      await apiJSON('/api/v1/sources', { method: 'POST', body: JSON.stringify(body) });
      toast('订阅源已创建');
    } catch (err) { toast('创建失败：' + err.message, true); }
  }
  resetSourceForm();
  loadSources();
});

$('#sourceRows').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'edit') {
    try {
      const data = await apiJSON('/api/v1/sources');
      const src = (data.sources || []).find((s) => s.id === id);
      if (!src) return;
      state.editingSourceId = id;
      $('#sourceFormTitle').textContent = '编辑订阅源：' + src.name;
      $('#srcName').value = src.name;
      $('#srcUrl').value = '';
      $('#srcUrl').placeholder = '留空保持不变（原 URL 已脱敏，如需更换请填写新 URL）';
      $('#srcEnabled').checked = src.enabled;
      $('#sourceForm').classList.remove('hidden');
      $('#srcName').focus();
    } catch (err) { toast('获取失败：' + err.message, true); }
  } else if (btn.dataset.act === 'del') {
    if (!confirm('确认删除该订阅源？')) return;
    try {
      await apiJSON('/api/v1/sources/' + id, { method: 'DELETE' });
      toast('已删除');
      loadSources();
    } catch (err) { toast('删除失败：' + err.message, true); }
  }
});

// 启用开关：即时 PUT
$('#sourceRows').addEventListener('change', async (e) => {
  if (!e.target.classList.contains('src-toggle')) return;
  const id = e.target.dataset.id;
  try {
    await apiJSON('/api/v1/sources/' + id, {
      method: 'PUT', body: JSON.stringify({ enabled: e.target.checked }),
    });
    toast(e.target.checked ? '已启用' : '已禁用');
  } catch (err) {
    toast('切换失败：' + err.message, true);
    e.target.checked = !e.target.checked; // 回滚
  }
});

/* ---------------- 订阅转换 ---------------- */

async function loadConvertOptions() {
  try {
    const [sd, td] = await Promise.all([
      apiJSON('/api/v1/sources'), apiJSON('/api/v1/templates'),
    ]);
    const srcs = (sd.sources || []).filter((s) => s.enabled);
    $('#convSource').innerHTML = srcs.length
      ? '<option value="">请选择订阅源</option>' + srcs.map((s) => `<option value="${esc(s.id)}">${esc(s.name)}</option>`).join('')
      : '<option value="">（暂无启用的订阅源）</option>';
    const tpls = (td.templates || []).filter((t) => t.enabled);
    $('#convTemplate').innerHTML = '<option value="">（不使用）</option>' +
      tpls.map((t) => `<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('');
  } catch (e) { /* 顶部 badge 已提示鉴权问题 */ }
}

// 临时 URL 折叠展开时互斥下拉
$('#tempUrlWrap').addEventListener('toggle', () => {
  $('#convSource').disabled = $('#tempUrlWrap').open;
});

function convertBody() {
  const body = {
    include: $('#convInclude').value.trim(),
    exclude: $('#convExclude').value.trim(),
    rename: $('#convRename').value.trim(),
    udp: $('#convUdp').checked,
    tls13: $('#convTls13').checked,
    scv: $('#convScv').checked,
  };
  const tpl = $('#convTemplate').value;
  if (tpl) body.template_id = tpl;
  if ($('#tempUrlWrap').open && $('#convUrl').value.trim()) {
    body.url = $('#convUrl').value.trim();
  } else {
    const srcId = $('#convSource').value;
    if (!srcId) throw new Error('请选择订阅源或填写临时 URL');
    body.source_id = srcId;
  }
  return body;
}

function renderPreview(data) {
  $('#convResult').classList.remove('hidden');
  $('#convMeta').textContent = `节点 ${data.node_count} 个 · 耗时 ${data.duration_ms}ms`;
  const nodes = data.nodes || [];
  $('#nodeList').innerHTML = nodes.length
    ? nodes.map((n) => `<div class="node">${esc(n.name)}<span class="ntype">${esc(n.type)}</span></div>`).join('')
    : '<div class="empty">无节点</div>';
  const groups = data.groups || [];
  $('#groupList').innerHTML = groups.length
    ? '<div class="hint" style="margin:6px 0">策略组：</div>' +
      groups.map((g) => `<div class="grp">▸ ${esc(g.name || g.type)} <span class="ntype">(${esc(g.type || '')}${Array.isArray(g.proxies) ? ' · ' + g.proxies.length + ' 个成员' : ''})</span></div>`).join('')
    : '';
}

// 生成订阅链接：已存源用 src=ID；临时 URL 用 url= 参数。
// 注意：URLSearchParams.set 已做一次编码，绝不能再 encodeURIComponent
// （双重编码会让服务端解码后仍带 %XX，validateSources 直接 400）。
function genSubLink() {
  const q = new URLSearchParams();
  q.set('target', 'clash');
  if ($('#tempUrlWrap').open && $('#convUrl').value.trim()) {
    q.set('url', $('#convUrl').value.trim());
  } else {
    const srcId = $('#convSource').value;
    if (!srcId) throw new Error('请选择订阅源或填写临时 URL');
    q.set('src', srcId);
  }
  const inc = $('#convInclude').value.trim();
  const exc = $('#convExclude').value.trim();
  const ren = $('#convRename').value.trim();
  if (inc) q.set('include', inc);
  if (exc) q.set('exclude', exc);
  if (ren) q.set('rename', ren);
  if ($('#convUdp').checked) q.set('udp', 'true');
  if ($('#convTls13').checked) q.set('tls13', 'true');
  if ($('#convScv').checked) q.set('scv', 'true');
  // window.location.host 已含端口（如 192.168.1.5:25500）；协议跟随当前页面
  // （http/https），避免硬编码 http:// 在反代 TLS 场景下生成错误链接
  return window.location.protocol + '//' + window.location.host + '/sub?' + q.toString();
}

$('#btnPreview').onclick = async () => {
  let body;
  try { body = convertBody(); } catch (e) { toast(e.message, true); return; }
  $('#btnPreview').disabled = true;
  try {
    const data = await apiJSON('/api/v1/convert/preview', { method: 'POST', body: JSON.stringify(body) });
    renderPreview(data);
    $('#yamlBox').classList.add('hidden');
    $('#btnCopyYaml').classList.add('hidden');
    toast('预览完成');
  } catch (err) { toast('预览失败：' + err.message, true); }
  $('#btnPreview').disabled = false;
};

$('#btnGenLink').onclick = () => {
  try {
    const link = genSubLink();
    $('#convLink').textContent = link;
    $('#convLinkBox').classList.remove('hidden');
    $('#convResult').classList.remove('hidden');
  } catch (e) { toast(e.message, true); }
};

$('#btnRunYaml').onclick = async () => {
  let body;
  try { body = convertBody(); } catch (e) { toast(e.message, true); return; }
  $('#btnRunYaml').disabled = true;
  try {
    const data = await apiJSON('/api/v1/convert/run', { method: 'POST', body: JSON.stringify(body) });
    $('#convResult').classList.remove('hidden');
    $('#convMeta').textContent = `节点 ${data.node_count} 个 · 耗时 ${data.duration_ms}ms`;
    $('#yamlBox').value = data.yaml || '';
    $('#yamlBox').classList.remove('hidden');
    $('#btnCopyYaml').classList.remove('hidden');
    toast('YAML 生成完成');
  } catch (err) { toast('生成失败：' + err.message, true); }
  $('#btnRunYaml').disabled = false;
};

$('#btnCopyLink').onclick = async () => {
  const ok = await copyText($('#convLink').textContent);
  toast(ok ? '链接已复制' : '复制失败，请手动选择', !ok);
};
$('#btnCopyYaml').onclick = async () => {
  toast((await copyText($('#yamlBox').value)) ? 'YAML 已复制' : '复制失败，请手动选择');
};

/* ---------------- 转换日志 ---------------- */

function paramSummary(p) {
  if (!p) return '<span class="dim">—</span>';
  const parts = [];
  ['include', 'exclude', 'rename'].forEach((k) => {
    if (p[k]) parts.push(`<span class="param-badge">${esc(k)}=${esc(p[k])}</span>`);
  });
  ['udp', 'tls13', 'scv'].forEach((k) => {
    if (p[k]) parts.push(`<span class="param-badge">${esc(k)}</span>`);
  });
  if (p.template_id) parts.push(`<span class="param-badge">template</span>`);
  return parts.length ? parts.join('') : '<span class="dim">—</span>';
}

async function loadLogs() {
  const tbody = $('#logRows');
  try {
    const data = await apiJSON('/api/v1/logs?limit=50');
    const list = data.logs || [];
    if (!list.length) {
      tbody.innerHTML = '<tr><td colspan="8" class="empty">暂无转换记录</td></tr>';
      return;
    }
    tbody.innerHTML = list.map((l) => {
      const fail = l.status === 'fail';
      const ts = (l.ts || '').replace('T', ' ').replace(/\.\d+Z?$/, '');
      return `<tr class="${fail ? 'fail' : ''}">
        <td>${esc(ts)}</td>
        <td>${esc(l.kind === 'sub' ? '转换接口 /sub' : (l.kind === 'preview' ? '节点预览' : '完整转换'))}${l.source_name ? ' · ' + esc(l.source_name) : ''}</td>
        <td>${paramSummary(l.params)}</td>
        <td>${fail ? '✗ 失败' : '✓ 成功'}</td>
        <td>${fail && l.error ? `<span class="err">${esc(l.error)}</span>` : '<span class="dim">—</span>'}</td>
        <td>${l.node_count}</td>
        <td>${l.duration_ms}ms</td>
        <td>${fail ? `<button class="btn small retry" data-id="${esc(l.id)}">重试</button>` : '<span class="dim">—</span>'}</td>
      </tr>`;
    }).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="8" class="empty">加载失败：${esc(e.message)}</td></tr>`;
  }
}

$('#btnRefreshLogs').onclick = loadLogs;
$('#logRows').addEventListener('click', async (e) => {
  const btn = e.target.closest('button.retry');
  if (!btn) return;
  btn.disabled = true;
  try {
    await apiJSON('/api/v1/logs/' + btn.dataset.id + '/retry', { method: 'POST' });
    toast('重试成功，已刷新日志');
    loadLogs();
  } catch (err) {
    toast('重试失败：' + err.message, true);
    btn.disabled = false;
  }
});

/* ---------------- 规则模板 ---------------- */

async function loadTemplates() {
  const tbody = $('#templateRows');
  try {
    const data = await apiJSON('/api/v1/templates');
    const list = data.templates || [];
    if (!list.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">暂无规则模板，点击「新增模板」创建</td></tr>';
      return;
    }
    tbody.innerHTML = list.map((t) => `
      <tr>
        <td>${esc(t.name)}</td>
        <td class="url" title="${esc(t.url)}">${esc(t.url)}</td>
        <td>${esc(t.behavior)}</td>
        <td>${esc(t.format)}</td>
        <td><input type="checkbox" data-id="${esc(t.id)}" class="tpl-toggle" ${t.enabled ? 'checked' : ''}></td>
        <td><div class="row-actions">
          <button class="btn small" data-act="edit" data-id="${esc(t.id)}">编辑</button>
          <button class="btn small danger" data-act="del" data-id="${esc(t.id)}">删除</button>
        </div></td>
      </tr>`).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">加载失败：${esc(e.message)}</td></tr>`;
  }
}

function resetTemplateForm() {
  state.editingTemplateId = null;
  $('#templateFormTitle').textContent = '新增模板';
  $('#tplName').value = '';
  $('#tplUrl').value = '';
  $('#tplUrl').placeholder = 'https://example.com/rules.yaml';
  $('#tplBehavior').value = 'domain';
  $('#tplFormat').value = 'yaml';
  $('#tplEnabled').checked = true;
  $('#templateForm').classList.add('hidden');
}

$('#btnNewTemplate').onclick = () => {
  resetTemplateForm();
  $('#templateForm').classList.remove('hidden');
  $('#tplName').focus();
};
$('#btnCancelTemplate').onclick = resetTemplateForm;

$('#templateForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const body = {
    name: $('#tplName').value.trim(),
    behavior: $('#tplBehavior').value,
    format: $('#tplFormat').value,
    enabled: $('#tplEnabled').checked,
  };
  if (state.editingTemplateId) {
    if ($('#tplUrl').value.trim()) body.url = $('#tplUrl').value.trim();
    try {
      await apiJSON('/api/v1/templates/' + state.editingTemplateId, {
        method: 'PUT', body: JSON.stringify(body),
      });
      toast('模板已更新');
    } catch (err) { toast('更新失败：' + err.message, true); }
  } else {
    body.url = $('#tplUrl').value.trim();
    if (!body.url) { toast('请填写规则集 URL', true); return; }
    try {
      await apiJSON('/api/v1/templates', { method: 'POST', body: JSON.stringify(body) });
      toast('模板已创建');
    } catch (err) { toast('创建失败：' + err.message, true); }
  }
  resetTemplateForm();
  loadTemplates();
});

$('#templateRows').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'edit') {
    try {
      const data = await apiJSON('/api/v1/templates');
      const tpl = (data.templates || []).find((t) => t.id === id);
      if (!tpl) return;
      state.editingTemplateId = id;
      $('#templateFormTitle').textContent = '编辑模板：' + tpl.name;
      $('#tplName').value = tpl.name;
      $('#tplUrl').value = '';
      $('#tplUrl').placeholder = '留空保持不变（原 URL 已脱敏）';
      $('#tplBehavior').value = tpl.behavior;
      $('#tplFormat').value = tpl.format;
      $('#tplEnabled').checked = tpl.enabled;
      $('#templateForm').classList.remove('hidden');
      $('#tplName').focus();
    } catch (err) { toast('获取失败：' + err.message, true); }
  } else if (btn.dataset.act === 'del') {
    if (!confirm('确认删除该规则模板？')) return;
    try {
      await apiJSON('/api/v1/templates/' + id, { method: 'DELETE' });
      toast('已删除');
      loadTemplates();
    } catch (err) { toast('删除失败：' + err.message, true); }
  }
});

$('#templateRows').addEventListener('change', async (e) => {
  if (!e.target.classList.contains('tpl-toggle')) return;
  const id = e.target.dataset.id;
  try {
    await apiJSON('/api/v1/templates/' + id, {
      method: 'PUT', body: JSON.stringify({ enabled: e.target.checked }),
    });
    toast(e.target.checked ? '已启用' : '已禁用');
  } catch (err) {
    toast('切换失败：' + err.message, true);
    e.target.checked = !e.target.checked;
  }
});

/* ---------------- 认证页 ---------------- */

function syncAuthForm() {
  $('#authToken').value = localStorage.getItem(TOKEN_KEY) || '';
  const token = localStorage.getItem(TOKEN_KEY);
  $('#authState').textContent = token
    ? '当前已保存令牌（' + token.slice(0, 4) + '****），请求自动携带 X-Token。'
    : '未保存令牌；服务端未设 OSC_ADMIN_TOKEN 时无需令牌即可访问。';
}

$('#btnSaveToken').onclick = () => {
  const v = $('#authToken').value.trim();
  if (!v) { toast('令牌不能为空', true); return; }
  localStorage.setItem(TOKEN_KEY, v);
  toast('令牌已保存');
  syncAuthForm();
  updateAuthBadge();
  loadSources();
};
$('#btnClearToken').onclick = () => {
  localStorage.removeItem(TOKEN_KEY);
  toast('令牌已清除');
  syncAuthForm();
  updateAuthBadge();
};

/* ---------------- 启动 ---------------- */

loadVersion();
updateAuthBadge();
loadSources();

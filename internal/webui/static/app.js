/* 订阅转换管理台 —— 原生 JS 单页，零依赖 */
'use strict';

const $ = (sel) => document.querySelector(sel);
const TOKEN_KEY = 'osc_token';
const state = {
  editingSourceId: null, editingRuleSetId: null, rsBehavior: null, rsFormat: null,
  srcOptions: [], ruleSetOptions: [],      // 已加载的启用选项（卡片渲染数据源）
  selectedSources: new Set(), selectedRuleSets: new Set(), // 选中 ID 集合
};

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
  if (name === 'rule-sets') loadRuleSets();
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

// hostOfURL 提取 URL host 作为选项行 meta（Source 结构无节点数字段，不造假）
function hostOfURL(u) {
  try { return new URL(u).host; } catch (e) { return ''; }
}

// pruneSelection 移除已不存在（被删除/禁用）的选项 id，保持选中集合有效
function pruneSelection(sel, validIds) {
  const valid = new Set(validIds);
  for (const id of [...sel]) if (!valid.has(id)) sel.delete(id);
}

// R5 长条多选下拉（数据源 + 规则集同组件）：触发器展开面板 → 选项行 toggle
// 选中（.on + ✓）→ 摘要区 chips 实时刷新；面板多选不自动关闭
async function loadConvertOptions() {
  try {
    const [sd, td] = await Promise.all([
      apiJSON('/api/v1/sources'), apiJSON('/api/v1/rule-sets'),
    ]);
    state.srcOptions = (sd.sources || []).filter((s) => s.enabled);
    state.ruleSetOptions = (td.rule_sets || []).filter((t) => t.enabled);
    pruneSelection(state.selectedSources, state.srcOptions.map((s) => s.id));
    pruneSelection(state.selectedRuleSets, state.ruleSetOptions.map((t) => t.id));
    renderSrcGrid();
    renderRuleSetGrid();
  } catch (e) {
    // P2：失败时下拉不能永久卡占位——给出可感知的失败提示（鉴权失败仍由顶部 badge 提示）
    $('#convSrcGrid').innerHTML = '<span class="hint">加载失败，请刷新重试</span>';
    $('#convRsGrid').innerHTML = '<span class="hint">加载失败，请刷新重试</span>';
    toast('数据源/规则集加载失败：' + (e && e.message ? e.message : String(e)), true);
  }
}

function renderSrcGrid() {
  const grid = $('#convSrcGrid');
  const list = state.srcOptions;
  grid.innerHTML = list.length
    ? list.map((s) => `
      <div class="ms-option${state.selectedSources.has(s.id) ? ' on' : ''}" data-id="${esc(s.id)}" role="option" aria-selected="${state.selectedSources.has(s.id)}" tabindex="0" title="${esc(s.name)}">
        <span class="ms-opt-check">✓</span>
        <span class="ms-opt-name">${esc(s.name)}</span>
        <span class="ms-opt-meta">${esc(hostOfURL(s.url) || '—')}</span>
      </div>`).join('')
    : '<span class="hint">（暂无启用的订阅源）</span>';
  updateSrcSummary();
}

function renderRuleSetGrid() {
  const grid = $('#convRsGrid');
  const list = state.ruleSetOptions;
  grid.innerHTML = list.length
    ? list.map((t) => `
      <div class="ms-option${state.selectedRuleSets.has(t.id) ? ' on' : ''}" data-id="${esc(t.id)}" role="option" aria-selected="${state.selectedRuleSets.has(t.id)}" tabindex="0" title="${esc(t.name)}">
        <span class="ms-opt-check">✓</span>
        <span class="ms-opt-name">${esc(t.name)}</span>
        <span class="ms-opt-meta">${esc(t.format || '')}${t.url ? ' · ' + esc(hostOfURL(t.url)) : ''}</span>
      </div>`).join('')
    : '<span class="hint">（无启用的规则集）</span>';
  updateRuleSetSummary();
}

// 摘要条：已选 N 项 + 可单独移除的 chips
function updateSrcSummary() {
  const sel = state.srcOptions.filter((s) => state.selectedSources.has(s.id));
  $('#convSrcSummary').innerHTML = sel.length
    ? `已选 ${sel.length} 项 ` + sel.map((s) => `<span class="chip">${esc(s.name)}<span class="chip-x" data-kind="src" data-id="${esc(s.id)}">×</span></span>`).join('')
    : '未选择';
}

function updateRuleSetSummary() {
  const sel = state.ruleSetOptions.filter((t) => state.selectedRuleSets.has(t.id));
  $('#convRsSummary').innerHTML = sel.length
    ? `已选 ${sel.length} 项 ` + sel.map((t) => `<span class="chip">${esc(t.name)}<span class="chip-x" data-kind="rs" data-id="${esc(t.id)}">×</span></span>`).join('')
    : '未选择';
}

// 长条多选下拉组件：触发器展开/收起（chips 的 × 只移除不切换）、Esc 关闭
function setupMultiSelect(rootId) {
  const root = document.getElementById(rootId);
  const trigger = root.querySelector('.ms-trigger');
  const panel = root.querySelector('.ms-panel');
  const setOpen = (open) => {
    root.classList.toggle('open', open);
    trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
  };
  trigger.addEventListener('click', (e) => {
    // chips 的 × 被点击时内层 handler 已重建摘要（e.target 脱离文档树），closest 会失效；
    // 改用 composedPath 判定，避免误切换面板（面板开着会误关、关着会误开）
    const path = e.composedPath ? e.composedPath() : [e.target];
    if (path.some((n) => n.classList && n.classList.contains('chip-x'))) return;
    setOpen(!root.classList.contains('open'));
  });
  trigger.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(!root.classList.contains('open')); }
    else if (e.key === 'Escape') setOpen(false);
  });
  panel.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { setOpen(false); trigger.focus(); }
  });
  return { root, trigger, setOpen };
}
const srcMs = setupMultiSelect('srcMulti');
const rsMs = setupMultiSelect('rsMulti');

// 点击外部关闭所有面板；点击组件内部时面板互斥（打开 src 面板关闭 rs，反之亦然）。
// 注意：选项/chips 点击会重建内层 DOM，e.target 可能已脱离文档树使 closest 失效，改用 composedPath
document.addEventListener('click', (e) => {
  const path = e.composedPath ? e.composedPath() : [];
  const inSrc = path.includes(srcMs.root);
  const inRs = path.includes(rsMs.root);
  if (!inSrc && !inRs) { srcMs.setOpen(false); rsMs.setOpen(false); return; }
  if (inSrc) rsMs.setOpen(false);
  if (inRs) srcMs.setOpen(false);
});

function toggleSrc(id) {
  if (state.selectedSources.has(id)) state.selectedSources.delete(id); else state.selectedSources.add(id);
  renderSrcGrid();
}
function toggleRuleSet(id) {
  if (state.selectedRuleSets.has(id)) state.selectedRuleSets.delete(id); else state.selectedRuleSets.add(id);
  renderRuleSetGrid();
}

// focusOpt 重建 innerHTML 后恢复焦点：优先同 id 选项行，行被移除时回退触发器
function focusOpt(ms, gridId, id) {
  const grid = document.getElementById(gridId);
  const el = [...grid.querySelectorAll('.ms-option')].find((n) => n.dataset.id === id);
  (el || ms.trigger).focus();
}

// 选项行点击 toggle（事件委托）；chips 的 × 单独移除；面板保持打开支持连续多选
$('#convSrcGrid').addEventListener('click', (e) => {
  const opt = e.target.closest('.ms-option');
  if (!opt) return;
  toggleSrc(opt.dataset.id);
});
// 键盘可达性：选项行 Enter/Space 触发与 click 相同的 toggle，Esc 关闭面板
$('#convSrcGrid').addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { srcMs.setOpen(false); srcMs.trigger.focus(); return; }
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const opt = e.target.closest('.ms-option');
  if (!opt) return;
  e.preventDefault(); // 阻止 Space 滚动页面
  const id = opt.dataset.id;
  toggleSrc(id);
  focusOpt(srcMs, 'convSrcGrid', id);
});
$('#convRsGrid').addEventListener('click', (e) => {
  const opt = e.target.closest('.ms-option');
  if (!opt) return;
  toggleRuleSet(opt.dataset.id);
});
$('#convRsGrid').addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { rsMs.setOpen(false); rsMs.trigger.focus(); return; }
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const opt = e.target.closest('.ms-option');
  if (!opt) return;
  e.preventDefault();
  const id = opt.dataset.id;
  toggleRuleSet(id);
  focusOpt(rsMs, 'convRsGrid', id);
});
$('#convSrcSummary').addEventListener('click', (e) => {
  const x = e.target.closest('.chip-x');
  if (!x) return;
  state.selectedSources.delete(x.dataset.id);
  renderSrcGrid();
});
$('#convRsSummary').addEventListener('click', (e) => {
  const x = e.target.closest('.chip-x');
  if (!x) return;
  state.selectedRuleSets.delete(x.dataset.id);
  renderRuleSetGrid();
});
$('#btnSrcAll').onclick = () => { state.srcOptions.forEach((s) => state.selectedSources.add(s.id)); renderSrcGrid(); };
$('#btnSrcClear').onclick = () => { state.selectedSources.clear(); renderSrcGrid(); };
$('#btnRsAll').onclick = () => { state.ruleSetOptions.forEach((t) => state.selectedRuleSets.add(t.id)); renderRuleSetGrid(); };
$('#btnRsClear').onclick = () => { state.selectedRuleSets.clear(); renderRuleSetGrid(); };

function convertBody() {
  const body = {
    include: $('#convInclude').value.trim(),
    exclude: $('#convExclude').value.trim(),
    rename: $('#convRename').value.trim(),
    udp: $('#convUdp').checked,
    tls13: $('#convTls13').checked,
    scv: $('#convScv').checked,
    strip_emoji: $('#convStripEmoji').checked,
  };
  const ruleSets = [...state.selectedRuleSets];
  if (ruleSets.length) body.ruleset_id = ruleSets.join(',');
  const srcIds = [...state.selectedSources];
  const urlVal = $('#tempUrlWrap').open ? $('#convUrl').value.trim() : '';
  if (!srcIds.length && !urlVal) throw new Error('请选择数据源或填写临时 URL');
  if (srcIds.length) body.source_ids = srcIds; // 多选数组；可与 url 并存（混合聚合）
  if (urlVal) body.url = urlVal;
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
  const srcIds = [...state.selectedSources];
  const urlVal = $('#tempUrlWrap').open ? $('#convUrl').value.trim() : '';
  if (!srcIds.length && !urlVal) throw new Error('请选择数据源或填写临时 URL');
  if (srcIds.length) q.set('src', srcIds.join(',')); // 逗号多值；可与 url 并存（混合聚合）
  if (urlVal) q.set('url', urlVal);
  const inc = $('#convInclude').value.trim();
  const exc = $('#convExclude').value.trim();
  const ren = $('#convRename').value.trim();
  if (inc) q.set('include', inc);
  if (exc) q.set('exclude', exc);
  if (ren) q.set('rename', ren);
  if ($('#convUdp').checked) q.set('udp', 'true');
  if ($('#convTls13').checked) q.set('tls13', 'true');
  if ($('#convScv').checked) q.set('scv', 'true');
  if ($('#convStripEmoji').checked) q.set('strip_emoji', 'true');
  const ruleSets = [...state.selectedRuleSets];
  if (ruleSets.length) q.set('ruleset_id', ruleSets.join(','));
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
  ['udp', 'tls13', 'scv', 'strip_emoji'].forEach((k) => {
    if (p[k]) parts.push(`<span class="param-badge">${esc(k)}</span>`);
  });
  if (p.ruleset_id) parts.push(`<span class="param-badge">规则集</span>`);
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

/* ---------------- 规则集管理 ---------------- */

async function loadRuleSets() {
  const tbody = $('#ruleSetRows');
  try {
    const data = await apiJSON('/api/v1/rule-sets');
    const list = data.rule_sets || [];
    if (!list.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">暂无规则集，点击「新增规则集」创建</td></tr>';
      return;
    }
    tbody.innerHTML = list.map((t) => `
      <tr>
        <td>${esc(t.name)}</td>
        <td class="url" title="${esc(t.url)}">${esc(t.url)}</td>
        <td>${esc(t.behavior)}</td>
        <td>${esc(t.format)}</td>
        <td><input type="checkbox" data-id="${esc(t.id)}" class="rs-toggle" ${t.enabled ? 'checked' : ''}></td>
        <td><div class="row-actions">
          <button class="btn small" data-act="edit" data-id="${esc(t.id)}">编辑</button>
          <button class="btn small danger" data-act="del" data-id="${esc(t.id)}">删除</button>
        </div></td>
      </tr>`).join('');
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">加载失败：${esc(e.message)}</td></tr>`;
  }
}

// hideProbeResult 隐藏探测预览区并清空内容：探测失败或用户修改 URL 后调用，
// 防止把过期探测结果当成新结果保存。
function hideProbeResult() {
  $('#rsProbeBox').classList.add('hidden');
  $('#rsProbeReason').textContent = '';
  $('#rsProbePreview').textContent = '';
}

function resetRuleSetForm() {
  state.editingRuleSetId = null;
  state.rsBehavior = null;
  state.rsFormat = null;
  $('#btnProbeRuleSet').dataset.probedUrl = '';
  $('#btnProbeRuleSet').dataset.probedBehavior = '';
  $('#btnProbeRuleSet').dataset.probedFormat = '';
  $('#ruleSetFormTitle').textContent = '新增规则集';
  $('#rsName').value = '';
  $('#rsUrl').value = '';
  $('#rsUrl').placeholder = 'https://example.com/rules.yaml';
  $('#rsBehavior').value = 'domain';
  $('#rsFormat').value = 'yaml';
  $('#rsEnabled').checked = true;
  hideProbeResult();
  $('#ruleSetForm').classList.add('hidden');
}

$('#btnNewRuleSet').onclick = () => {
  resetRuleSetForm();
  $('#ruleSetForm').classList.remove('hidden');
  $('#rsName').focus();
};
$('#btnCancelRuleSet').onclick = resetRuleSetForm;

$('#ruleSetForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const url = $('#rsUrl').value.trim();
  // 统一判定：表单 URL 与探测时 URL 不一致（含 URL 留空=编辑保持原值、
  // URL 改成别的两种情况，URL 为空时探测 URL 非空所以必不一致）时，探测
  // 回填的 behavior/format 不再适用。仅当表单值仍是探测回填值（用户未手
  // 改，stillProbed）才还原规则集原值/清空；用户手改过则尊重其选择，不动。
  let probeIgnored = false;
  const probedUrl = $('#btnProbeRuleSet').dataset.probedUrl;
  if (probedUrl && url !== probedUrl) {
    const b = $('#rsBehavior').value, f = $('#rsFormat').value;
    const stillProbed = (b === $('#btnProbeRuleSet').dataset.probedBehavior &&
                         f === $('#btnProbeRuleSet').dataset.probedFormat);
    if (stillProbed) {
      probeIgnored = true;
      if (state.editingRuleSetId) {
        // 编辑模式：恢复规则集原值（state.rsBehavior/rsFormat 编辑加载时已存）
        $('#rsBehavior').value = state.rsBehavior ?? '';
        $('#rsFormat').value = state.rsFormat ?? '';
      } else {
        // 新建模式：清空让用户重选
        $('#rsBehavior').value = '';
        $('#rsFormat').value = '';
      }
    }
  }
  const body = {
    name: $('#rsName').value.trim(),
    behavior: $('#rsBehavior').value,
    format: $('#rsFormat').value,
    enabled: $('#rsEnabled').checked,
  };
  if (state.editingRuleSetId) {
    // 编辑模式 URL 留空 = PUT 不带 url（服务端保持原 URL）
    if (url) body.url = url;
    try {
      await apiJSON('/api/v1/rule-sets/' + state.editingRuleSetId, {
        method: 'PUT', body: JSON.stringify(body),
      });
      toast(probeIgnored ? '规则集已更新（URL 与探测源不一致，已忽略探测结果）' : '规则集已更新');
    } catch (err) { toast('更新失败：' + err.message, true); }
  } else {
    body.url = url;
    if (!body.url) { toast('请填写规则集 URL', true); return; }
    try {
      await apiJSON('/api/v1/rule-sets', { method: 'POST', body: JSON.stringify(body) });
      toast(probeIgnored ? '规则集已创建（URL 已变更，请重新探测或手动选择）' : '规则集已创建');
    } catch (err) { toast('创建失败：' + err.message, true); }
  }
  resetRuleSetForm();
  loadRuleSets();
});

// 自动探测：新建/编辑表单共用。探测结果只回填表单控件（不写 state），
// 保存逻辑与 state.editingRuleSetId 完全不变；format/behavior 非空才
// 回填下拉框（空串不动，避免清掉用户已选值），reason/preview 用
// textContent 赋值（零 XSS）。探测成功把探测 URL 与回填的
// behavior/format 记到按钮 dataset（probedUrl/probedBehavior/
// probedFormat），供保存逻辑统一判定「表单 URL 与探测 URL 不一致时
// 探测结果是否仍适用」（URL 留空或改 URL 均覆盖）。
$('#btnProbeRuleSet').onclick = async () => {
  const url = $('#rsUrl').value.trim();
  if (!url) { toast('请先填写规则集 URL', true); return; }
  const btn = $('#btnProbeRuleSet');
  btn.disabled = true;
  btn.textContent = '探测中…';
  try {
    const data = await apiJSON('/api/v1/rule-sets/probe', {
      method: 'POST', body: JSON.stringify({ url }),
    });
    btn.dataset.probedUrl = url;
    // 记录实际回填的下拉值（未识别则为空串）：保存时用「表单值仍等于
    // 回填值」判断用户是否手改过，决定是否还原/清空
    btn.dataset.probedBehavior = data.behavior || '';
    btn.dataset.probedFormat = data.format || '';
    if (data.format) $('#rsFormat').value = data.format;
    if (data.behavior) $('#rsBehavior').value = data.behavior;
    $('#rsProbeReason').textContent = data.reason || '';
    $('#rsProbePreview').textContent = (data.preview || []).join('\n');
    $('#rsProbeBox').classList.remove('hidden');
    toast('探测完成：' + (data.behavior || '未识别') + ' / ' + (data.format || '未识别'));
  } catch (err) {
    // 失败时隐藏预览区：残留的旧结果会误导用户当成新结果保存
    hideProbeResult();
    toast('探测失败：' + err.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = '自动探测';
  }
};

// 用户修改 URL 后隐藏预览区：旧探测结果与新 URL 不匹配，防止误保存
$('#rsUrl').addEventListener('input', hideProbeResult);

$('#ruleSetRows').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'edit') {
    // 清理上一次新建/编辑残留的探测预览与探测记录：JS 赋 value 不触发
    // input 事件，预览不会自动隐藏；probedUrl 残留会让保存误判
    // 「探测结果仍适用」。
    hideProbeResult();
    $('#btnProbeRuleSet').dataset.probedUrl = '';
    $('#btnProbeRuleSet').dataset.probedBehavior = '';
    $('#btnProbeRuleSet').dataset.probedFormat = '';
    try {
      const data = await apiJSON('/api/v1/rule-sets');
      const rs = (data.rule_sets || []).find((t) => t.id === id);
      if (!rs) return;
      state.editingRuleSetId = id;
      // 记录规则集原 behavior/format：探测回填会覆盖表单值，URL 留空保存时
      // 需恢复原值（见 submit 处理），故在探测前存入 state
      state.rsBehavior = rs.behavior;
      state.rsFormat = rs.format;
      $('#ruleSetFormTitle').textContent = '编辑规则集：' + rs.name;
      $('#rsName').value = rs.name;
      $('#rsUrl').value = '';
      $('#rsUrl').placeholder = '留空保持不变（原 URL 已脱敏）';
      $('#rsBehavior').value = rs.behavior;
      $('#rsFormat').value = rs.format;
      $('#rsEnabled').checked = rs.enabled;
      $('#ruleSetForm').classList.remove('hidden');
      $('#rsName').focus();
    } catch (err) { toast('获取失败：' + err.message, true); }
  } else if (btn.dataset.act === 'del') {
    if (!confirm('确认删除该规则集？')) return;
    try {
      await apiJSON('/api/v1/rule-sets/' + id, { method: 'DELETE' });
      toast('已删除');
      loadRuleSets();
    } catch (err) { toast('删除失败：' + err.message, true); }
  }
});

$('#ruleSetRows').addEventListener('change', async (e) => {
  if (!e.target.classList.contains('rs-toggle')) return;
  const id = e.target.dataset.id;
  try {
    await apiJSON('/api/v1/rule-sets/' + id, {
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

// MidnightConduit
const app = window.go?.main?.App;

const logOut = document.getElementById('log-output');
const statusBar = document.getElementById('status-bar');
const statusPill = document.getElementById('status-pill');
const configPath = document.getElementById('config-path');

const tabsNav = document.getElementById('tabs-nav');
const views = document.getElementById('views');
const tunnelList = document.getElementById('tunnel-list');
const tplT = document.getElementById('tpl-tunnel');
const orchList = document.getElementById('orch-list');
const tplO = document.getElementById('tpl-orch');

const projectSelect = document.getElementById('project-select');
const projectRefresh = document.getElementById('project-refresh');

const orchLogViewer = document.getElementById('orch-log-viewer');
const orchLogTitle = document.getElementById('orch-log-title');
const orchLogOutput = document.getElementById('orch-log-output');

const projectListEl = document.getElementById('projects-list');
const mainTabContent = document.getElementById('main-tab-content');
const portsListEl = document.getElementById('ports-list');
const tplPort = document.getElementById('tpl-port');
const serversListEl = document.getElementById('servers-list');
const tplServer = document.getElementById('tpl-server');

const tunnelAddForm = document.getElementById('tunnel-add-form');
const tfName = document.getElementById('tf-name');
const tfHost = document.getElementById('tf-host');
const tfLPort = document.getElementById('tf-lport');
const tfRHost = document.getElementById('tf-rhost');
const tfRPort = document.getElementById('tf-rport');

const protectedTabKeys = new Set(['main', 'ports', 'tunnels', 'servers', 'orchestrator', 'database']);
function isProtectedTabKey(key) {
  return protectedTabKeys.has(String(key).trim());
}

const defaultProjectID = 'main';

let appState = null;
let currentTab = 'projects';
let currentProject = '';
let orchPoll;
let orchLogPoll;
const orchCollapsed = {}; // namespace -> true when its group is collapsed
let editingTunnel = null;  // the TunnelInfo being edited, or null when creating

// Wire controls
document.getElementById('log-toggle').onclick = () => {
  document.getElementById('log-panel').classList.toggle('collapsed');
};
document.getElementById('log-clear').onclick = () => {
  logOut.innerHTML = '';
};

projectRefresh.onclick = async () => {
  await loadState();
};
projectSelect.onchange = async () => {
  const selected = projectSelect.value;
  if (!selected || selected === currentProject) {
    return;
  }
  await setProjectSelected(selected);
};

document.getElementById('btn-add-project').onclick = async () => {
  const name = window.prompt('Project name');
  if (!name) {
    return;
  }
  const suggested = String(name).trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || `project-${Date.now()}`;
  const id = window.prompt('Project ID', suggested);
  if (!id) {
    return;
  }
  const project = {
    id: id.trim(),
    name,
    slug: id.trim(),
    kind: 'project',
    enabled: true,
    sort_order: 0,
    description: '',
    config: '',
  };
  try {
    await window.go.main.App.UpsertProject(project);
    await loadState();
    await setProjectSelected(id.trim());
  } catch (e) {
    showErr('Create project failed: ' + e);
  }
};

document.getElementById('btn-refresh-projects').onclick = async () => {
  await loadState();
};

document.getElementById('btn-tunnels-start-all').onclick = () => runStartBatch('startall');
document.getElementById('btn-tunnels-start-auto').onclick = () => runStartBatch('startauto');
document.getElementById('btn-tunnels-stop-all').onclick = async () => {
  try {
    await app.StopAllTunnels();
    showOk('Tunnel stop-all requested');
    await refreshTunnels();
  } catch (e) {
    showErr('Stop-all failed: ' + e);
  }
};

// Open the tunnel form. Pass a TunnelInfo to edit it (pre-filled), or null to
// create. The same form/handler serves both — edit just carries the id through.
function openTunnelForm(tunnel) {
  editingTunnel = tunnel || null;
  const title = document.getElementById('tf-form-title');
  const pass = document.getElementById('tf-pass');
  const ident = document.getElementById('tf-ident');
  if (tunnel) {
    tfName.value = tunnel.name || '';
    tfHost.value = tunnel.ssh_host || '';
    tfLPort.value = tunnel.local_port || '';
    tfRHost.value = tunnel.remote_host || '';
    tfRPort.value = tunnel.remote_port || '';
    document.getElementById('tf-open').value = tunnel.open_url || '';
    document.getElementById('tf-health').value = tunnel.health_url || '';
    document.getElementById('tf-autostart').checked = !!tunnel.auto_start;
    // Secrets are never sent to the UI; leaving these blank keeps the stored value.
    pass.value = ''; ident.value = '';
    pass.placeholder = '(unchanged)'; ident.placeholder = '(unchanged)';
    if (title) title.textContent = `Edit Tunnel: ${tunnel.name || tunnel.id}`;
  } else {
    ['tf-name','tf-host','tf-lport','tf-rhost','tf-rport','tf-pass','tf-ident','tf-open','tf-health'].forEach(id => { document.getElementById(id).value = ''; });
    document.getElementById('tf-autostart').checked = false;
    pass.placeholder = '(optional)'; ident.placeholder = '(optional)';
    if (title) title.textContent = 'Create Tunnel';
  }
  tunnelAddForm.classList.remove('collapsed');
  tfName.focus();
}

function resetTunnelForm() {
  editingTunnel = null;
  tunnelAddForm.classList.add('collapsed');
  ['tf-name','tf-host','tf-lport','tf-rhost','tf-rport','tf-pass','tf-ident','tf-open','tf-health'].forEach(id => { document.getElementById(id).value = ''; });
  document.getElementById('tf-autostart').checked = false;
  document.getElementById('tf-pass').placeholder = '(optional)';
  document.getElementById('tf-ident').placeholder = '(optional)';
  const title = document.getElementById('tf-form-title');
  if (title) title.textContent = 'Create Tunnel';
}

document.getElementById('btn-tunnels-add').onclick = () => {
  if (tunnelAddForm.classList.contains('collapsed')) openTunnelForm(null);
  else resetTunnelForm();
};

document.getElementById('btn-tf-save').onclick = async () => {
  const name = tfName.value.trim();
  const host = tfHost.value.trim();
  const lport = parseInt(tfLPort.value, 10);
  const rhost = tfRHost.value.trim();
  const rport = parseInt(tfRPort.value, 10);
  if (!name) { showErr('Name is required'); return; }
  if (!host) { showErr('SSH Host is required'); return; }
  if (!rhost) { showErr('Remote Host is required'); return; }
  if (!lport || lport <= 0) { showErr('Valid Local Port is required'); return; }
  if (!rport || rport <= 0) { showErr('Valid Remote Port is required'); return; }
  const isEdit = !!editingTunnel;
  try {
    await app.UpsertProjectTunnel({
      id: isEdit ? editingTunnel.id : '',
      name, ssh_host: host, local_port: lport,
      remote_host: rhost, remote_port: rport,
      password: document.getElementById('tf-pass').value.trim(),
      identity_file: document.getElementById('tf-ident').value.trim(),
      open_url: document.getElementById('tf-open').value.trim(),
      health_url: document.getElementById('tf-health').value.trim(),
      auto_start: document.getElementById('tf-autostart').checked,
      enabled: true, sort: isEdit ? (editingTunnel.sort || 100) : 100,
    });
    showOk(`${isEdit ? 'Updated' : 'Created'} tunnel: ${name}`);
    resetTunnelForm();
    await loadState();
  } catch (e) { showErr(`${isEdit ? 'Update' : 'Create'} tunnel failed: ` + e); }
};

document.getElementById('btn-tf-cancel').onclick = () => {
  resetTunnelForm();
};

document.getElementById('btn-servers-add').onclick = async () => {
  const name = window.prompt('Server name');
  if (!name) return;
  const command = window.prompt('Command (optional, press OK to skip)', '');
  const dockerImage = window.prompt('Docker image (optional, press OK to skip)', '');
  if (!(command || '').trim() && !(dockerImage || '').trim()) {
    showErr('Either command or docker image is required');
    return;
  }
  try {
    await app.UpsertServer({
      name: name.trim(), display_name: name.trim(),
      command: (command || '').trim(), docker_image: (dockerImage || '').trim(),
      auto_start: false, enabled: true, sort: 100,
    });
    showOk(`Created server: ${name.trim()}`);
    await loadState();
    if (currentTab === 'servers') await refreshServers();
  } catch (e) { showErr('Create server failed: ' + e); }
};

document.getElementById('btn-servers-refresh').onclick = async () => {
  await refreshServers();
};

document.getElementById('orch-start').onclick = async () => {
  try {
    await app.OrchStart();
    showOk('Orchestrator start called');
    await refreshOrchestrator();
  } catch (e) {
    showErr('Orchestrator start failed: ' + e);
  }
};
document.getElementById('orch-shutdown').onclick = async () => {
  try {
    await app.OrchShutdown();
    showOk('Orchestrator shutdown requested');
    await refreshOrchestrator();
  } catch (e) {
    showErr('Orchestrator shutdown failed: ' + e);
  }
};
document.getElementById('orch-reload').onclick = async () => {
  try {
    await app.ReloadOrchestratorConfig();
    showOk('Orchestrator config reloaded');
    await refreshOrchestrator();
  } catch (e) {
    showErr('Orchestrator reload failed: ' + e);
  }
};
document.getElementById('orch-start-all').onclick = async () => {
  try {
    await app.OrchStartAll();
    showOk('Orchestrator start all requested');
    await refreshOrchestrator();
  } catch (e) {
    showErr('Start all processes failed: ' + e);
  }
};
document.getElementById('orch-stop-all').onclick = async () => {
  try {
    await app.OrchStopAll();
    showOk('Orchestrator stop all requested');
    await refreshOrchestrator();
  } catch (e) {
    showErr('Stop all processes failed: ' + e);
  }
};

// Logs popover
document.getElementById('orch-log-close').onclick = () => {
  orchLogViewer.classList.add('collapsed');
};
document.getElementById('orch-log-refresh').onclick = async () => {
  if (orchLogTitle.dataset.name) {
    await showOrchLogs(orchLogTitle.dataset.name, 220);
  }
};
document.getElementById('btn-ports-refresh').onclick = async () => {
  await refreshPorts();
};

document.getElementById('btn-add-tab').onclick = async () => {
  const label = window.prompt('New tab label (for example: Monitoring)');
  if (!label) {
    return;
  }
  const defaultKey = label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  const key = window.prompt('New tab key (must be unique)', defaultKey);
  if (!key) {
    return;
  }
  try {
    const created = await app.UpsertTab({
      id: key,
      label,
      key,
      kind: 'custom',
      enabled: true,
      sort: 100,
      config: '',
    });
    showOk(`Created tab: ${created.label || label}`);
    await loadState();
    currentTab = created.key;
  } catch (e) {
    showErr('Add tab failed: ' + e);
  }
};

function pushLog(text, cls = '') {
  const row = document.createElement('div');
  row.className = cls || 'log-info';
  row.textContent = `[${new Date().toLocaleTimeString()}] ${text}`;
  logOut.appendChild(row);
  logOut.scrollTop = logOut.scrollHeight;
}

function setStatus(msg, kind = '') {
  statusBar.textContent = msg;
  statusPill.textContent = msg;
  if (kind === 'err') {
    statusBar.style.color = '#e0556a';
  } else if (kind === 'warn') {
    statusBar.style.color = '#e5b567';
  } else {
    statusBar.style.color = '';
  }
}

function showErr(msg) {
  pushLog(msg, 'log-err');
  setStatus(msg, 'err');
}

function showWarn(msg) {
  pushLog(msg, 'log-warn');
  setStatus(msg, 'warn');
}

function showOk(msg) {
  pushLog(msg, 'log-info');
  setStatus(msg);
}

function normalizeStatus(status) {
  const s = String(status || 'stopped').toLowerCase();
  if (s === 'running') return 'running';
  if (s === 'ready') return 'running';
  if (s === 'healthy') return 'running';
  if (s === 'readying' || s === 'starting') return 'starting';
  if (s === 'stopped') return 'stopped';
  if (s === 'completed') return 'stopped';
  if (s === 'failed') return 'failed';
  if (s === 'exited') return 'failed';
  return 'other';
}

function setCardStatus(card, statusText) {
  const s = normalizeStatus(statusText);
  const dot = card.querySelector('.dot');
  const label = card.querySelector('.status-label');
  const health = card.querySelector('.health-badge');

  dot.className = `dot ${s}`;
  label.textContent = statusText || 'stopped';
  label.className = `status-label ${s}`;
  if (health) {
    health.textContent = health.textContent || '';
  }
}

function normalizeTabs(tabs) {
  return (tabs || [])
    .filter(Boolean)
    .filter(t => t.enabled !== false)
    .sort((a, b) => (a.sort || 0) - (b.sort || 0) || String(a.label).localeCompare(String(b.label)));
}

function normalizeProcesses(processes) {
  return (processes || [])
    .filter(Boolean)
    .sort((a, b) => {
      const namespaceCmp = String(a.namespace || '').localeCompare(String(b.namespace || ''));
      if (namespaceCmp !== 0) {
        return namespaceCmp;
      }
      return String(a.name || '').localeCompare(String(b.name || ''));
    });
}

function normalizeTunnels(tunnels) {
  return (tunnels || [])
    .filter(Boolean)
    .sort((a, b) => {
      const sortCmp = (a.sort || 0) - (b.sort || 0);
      if (sortCmp !== 0) {
        return sortCmp;
      }
      const nameCmp = String(a.name || '').localeCompare(String(b.name || ''));
      if (nameCmp !== 0) {
        return nameCmp;
      }
      return String(a.id || '').localeCompare(String(b.id || ''));
    });
}

function normalizeOpenPorts(ports) {
  return (ports || [])
    .filter(Boolean)
    .sort((a, b) => {
      const p1 = Number(a.port || 0);
      const p2 = Number(b.port || 0);
      if (p1 !== p2) {
        return p1 - p2;
      }
      const protoCmp = String(a.protocol || '').localeCompare(String(b.protocol || ''));
      if (protoCmp !== 0) {
        return protoCmp;
      }
      return String(a.address || '').localeCompare(String(b.address || ''));
    });
}

function normalizeProjectList(projects) {
  return (projects || [])
    .filter(Boolean)
    .sort((a, b) => {
      const sortCmp = (a.sort_order || 0) - (b.sort_order || 0);
      if (sortCmp !== 0) {
        return sortCmp;
      }
      return String(a.name || a.id || '').localeCompare(String(b.name || b.id || ''));
    });
}

function normalizeServers(servers) {
  return (servers || [])
    .filter(Boolean)
    .sort((a, b) => {
      const sortCmp = (a.sort || 0) - (b.sort || 0);
      if (sortCmp !== 0) return sortCmp;
      return String(a.name || a.id || '').localeCompare(String(b.name || b.id || ''));
    });
}

async function setProjectSelected(projectID) {
  if (!projectID) {
    return;
  }
  try {
    await app.SetActiveProject(projectID);
    currentProject = projectID;
    currentTab = '';
    await loadState();
  } catch (e) {
    showErr('Set active project failed: ' + e);
  }
}

function mapTabToPanel(tab) {
  if (tab.key === 'projects') return 'view-projects';
  if (tab.key === 'main') return 'view-main';
  if (tab.key === 'ports') return 'view-ports';
  if (tab.key === 'tunnels') return 'view-tunnels';
  if (tab.key === 'servers') return 'view-servers';
  if (tab.key === 'orchestrator') return 'view-orchestrator';
  if (tab.key === 'database') return 'view-database';
  return null;
}

function ensureCustomPanel(tab) {
  let existing = document.getElementById(`view-${tab.key}`);
  if (existing) return existing;

  const node = document.createElement('section');
  node.id = `view-${tab.key}`;
  node.className = 'tab-panel';
  node.innerHTML = `
    <div class="toolbar">
      <button class="custom-action">No action yet</button>
    </div>
    <pre class="custom-config"></pre>
  `;
  views.appendChild(node);
  node.querySelector('.custom-config').textContent = tab.config || 'No config payload.';
  return node;
}

function syncProjects() {
  const list = Array.isArray(appState?.projects) ? appState.projects : [];
  if (!projectSelect) {
    return;
  }

  projectSelect.innerHTML = '';

  if (!list.length) {
    const fallback = document.createElement('option');
    fallback.value = appState?.active_project_id || 'main';
    fallback.textContent = 'main';
    projectSelect.appendChild(fallback);
    projectSelect.value = fallback.value;
    currentProject = fallback.value;
    return;
  }

  for (const project of list) {
    const option = document.createElement('option');
    option.value = project.id;
    option.textContent = project.name || project.id;
    option.title = project.description || '';
    if (!project.enabled) {
      option.disabled = true;
      option.textContent += ' (disabled)';
    }
    projectSelect.appendChild(option);
  }

  const active = appState?.active_project_id || '';
  if (active) {
    currentProject = active;
    if (Array.from(projectSelect.options).some(o => o.value === active)) {
      projectSelect.value = active;
      return;
    }
  }

  if (projectSelect.options.length > 0) {
    currentProject = projectSelect.options[0].value;
    projectSelect.value = currentProject;
  }
}

function renderProjectsPanel() {
  if (!projectListEl) {
    return;
  }

  const projects = normalizeProjectList(appState?.projects || []);
  projectListEl.innerHTML = '';

  if (!projects.length) {
    projectListEl.innerHTML = '<div class="empty">No projects found.</div>';
    return;
  }

  for (const project of projects) {
    const row = document.createElement('div');
    row.className = 'project-list-item';

    const info = document.createElement('div');
    const label = document.createElement('div');
    const details = document.createElement('div');
    label.textContent = `${project.name || project.id} (${project.kind || 'project'})`;
    details.textContent = project.id;
    details.style.fontSize = '11px';
    details.style.opacity = '0.8';
    info.appendChild(label);
    info.appendChild(details);

    const actions = document.createElement('div');
    actions.style.display = 'flex';
    actions.style.gap = '6px';

    const activate = document.createElement('button');
    const isActive = project.id === currentProject;
    activate.textContent = isActive ? 'Active' : 'Activate';
    activate.disabled = isActive;
    if (isActive) activate.classList.add('is-active');
    activate.onclick = async () => {
      await setProjectSelected(project.id);
      currentTab = 'projects';
      renderProjectsPanel();
    };

    const remove = document.createElement('button');
    remove.textContent = 'Delete';
    remove.className = 'danger';
    remove.disabled = project.id === defaultProjectID;
    remove.onclick = async () => {
      if (project.id === defaultProjectID) {
        showWarn('Cannot delete main project.');
        return;
      }
      const confirmDelete = window.confirm(`Delete project "${project.name || project.id}"?`);
      if (!confirmDelete) {
        return;
      }
      try {
        await window.go.main.App.DeleteProject(project.id);
        if (currentProject === project.id) {
          currentProject = defaultProjectID;
        }
        await loadState();
      } catch (e) {
        showErr('Delete project failed: ' + e);
      }
    };

    actions.appendChild(activate);
    actions.appendChild(remove);
    row.appendChild(info);
    row.appendChild(actions);
    projectListEl.appendChild(row);
  }
}

function renderTabs() {
  const dbTabs = normalizeTabs(appState.tabs || []);
  const existing = new Set((dbTabs || []).map((tab) => tab.key));
  const fixed = [
    { id: 'projects_tab', key: 'projects', label: 'Projects', kind: 'projects', enabled: true, sort: -200, config: '' },
  ];

  for (const fallback of [
    { key: 'main', label: 'Discussion', kind: 'main', sort: -100 },
    { key: 'ports', label: 'Open Ports', kind: 'ports', sort: -90 },
    { key: 'tunnels', label: 'Tunnels', kind: 'tunnels', sort: -80 },
    { key: 'servers', label: 'Servers', kind: 'servers', sort: -75 },
    { key: 'orchestrator', label: 'Orchestrator', kind: 'orchestrator', sort: -70 },
    { key: 'database', label: 'Database', kind: 'database', sort: -60 },
  ]) {
    if (!existing.has(fallback.key)) {
      fixed.push({
        id: `projects_${fallback.key}`, key: fallback.key, label: fallback.label,
        kind: fallback.kind, enabled: true, sort: fallback.sort, config: '',
      });
    }
  }

  const merged = [...fixed, ...dbTabs]
    .filter((tab, index, arr) => {
      const first = arr.findIndex((t) => t.key === tab.key);
      return first === index;
    })
    .sort((a, b) => {
      if ((a.sort || 0) !== (b.sort || 0)) return (a.sort || 0) - (b.sort || 0);
      return String(a.label || '').localeCompare(String(b.label || ''));
    });

  tabsNav.innerHTML = '';
  document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));

  if (!currentTab && merged.length) currentTab = merged[0].key;
  if (currentTab && !merged.some(tab => tab.key === currentTab)) {
    currentTab = merged.length ? merged[0].key : '';
  }

  if (!merged.length) {
    const btn = document.createElement('button');
    btn.className = 'tab active';
    btn.textContent = 'No tabs';
    tabsNav.appendChild(btn);
    return;
  }

  const knownPanels = new Set(['view-tunnels', 'view-orchestrator', 'view-main', 'view-ports', 'view-servers', 'view-projects', 'view-database']);
  for (const tab of merged) {
    const btn = document.createElement('button');
    btn.className = 'tab';

    const labelSpan = document.createElement('span');
    labelSpan.textContent = tab.label;
    btn.appendChild(labelSpan);

    const panelId = mapTabToPanel(tab) || `view-${tab.key}`;
    const panel = knownPanels.has(panelId)
      ? document.getElementById(panelId)
      : ensureCustomPanel(tab);
    if (!panel) continue;

    if (tab.key === currentTab) {
      btn.classList.add('active');
      panel.classList.add('active');
      btn.setAttribute('aria-current', 'page');
    } else {
      btn.removeAttribute('aria-current');
    }

    btn.onclick = async () => {
      currentTab = tab.key;
      renderTabs();
      // Single source of truth for per-tab loading. Previously this handler
      // listed each tab inline and omitted 'tunnels', so clicking Tunnels
      // never refreshed it (it only updated on the 3.5s poll).
      await refreshCurrentTab();
    };

    // Add delete button for non-protected, non-fixed (db-backed) tabs
    if (!isProtectedTabKey(tab.key) && !fixed.some(f => f.key === tab.key)) {
      const del = document.createElement('button');
      del.className = 'tab-delete';
      del.textContent = '×';
      del.title = `Delete tab "${tab.label}"`;
      del.onclick = async (e) => {
        e.stopPropagation();
        if (!window.confirm(`Delete tab "${tab.label}"? This cannot be undone.`)) return;
        try {
          await app.DeleteTab(tab.id);
          showOk(`Deleted tab: ${tab.label}`);
          if (currentTab === tab.key) currentTab = '';
          await loadState();
        } catch (err) { showErr('Delete tab failed: ' + err); }
      };
      btn.appendChild(del);
    }

    tabsNav.appendChild(btn);
  }
}

// Returns true on success. May fail transiently at startup while the store is
// still connecting (notably Postgres, whose connect+migrate takes a moment) —
// init() retries until it succeeds. `quiet` suppresses the error toast during
// those startup retries.
async function loadState(quiet) {
  try {
    appState = await app.GetAppState();
    try { appState.servers = await app.GetServers() || []; } catch (_) { appState.servers = []; }
    syncProjects();
    configPath.textContent = appState.db_file || '';
    renderTabs();
    await refreshCurrentTab();
    return true;
  } catch (e) {
    if (!quiet) showErr('Load state failed: ' + e);
    return false;
  }
}

// Show a loading placeholder in the active tab's container before its async
// fetch resolves, so switching tabs doesn't briefly read as an empty/"nothing
// configured" state. Only called on tab-switch / initial load, not the poll.
function showTabLoading(tab) {
  const el = { tunnels: tunnelList, orchestrator: orchList, ports: portsListEl, servers: serversListEl }[tab];
  if (el) el.innerHTML = '<div class="empty">Loading…</div>';
}

async function refreshCurrentTab() {
  if (!currentTab) {
    return;
  }
  showTabLoading(currentTab);
  if (currentTab === 'tunnels') {
    await refreshTunnels();
    return;
  }
  if (currentTab === 'orchestrator') {
    await refreshOrchestrator();
    return;
  }
  if (currentTab === 'ports') {
    await refreshPorts();
    return;
  }
  if (currentTab === 'servers') {
    await refreshServers();
    return;
  }
  if (currentTab === 'main') {
    renderMainTab();
    return;
  }
  if (currentTab === 'projects') {
    renderProjectsPanel();
    return;
  }
  if (currentTab === 'database') {
    renderDatabasePanel();
  }
}

async function renderDatabasePanel() {
  let cfg;
  try {
    cfg = await app.GetDatabaseConfig();
  } catch (e) {
    showErr('Load DB config failed: ' + e);
    return;
  }
  const cur = document.getElementById('db-current');
  if (cur) {
    cur.textContent = cfg.current === 'postgres'
      ? `Postgres — ${cfg.url_masked || 'remote'}`
      : 'Local SQLite';
  }
  const note = document.getElementById('db-restart-note');
  if (note) note.classList.toggle('hidden', !cfg.needs_restart);
  const sp = document.getElementById('db-sqlite-path');
  if (sp) sp.textContent = cfg.sqlite_path ? `Local SQLite file: ${cfg.sqlite_path}` : '';
  // Migration only makes sense when connected to Postgres.
  const mig = document.getElementById('btn-db-migrate');
  if (mig) {
    mig.disabled = cfg.current !== 'postgres';
    mig.title = cfg.current === 'postgres' ? '' : 'Connect to Postgres first';
  }
  // The URL field is left blank on purpose — we never echo the saved password
  // back into an editable field. Type a fresh URL to change the target.
}

document.getElementById('btn-db-test').onclick = async () => {
  const url = document.getElementById('db-url').value.trim();
  if (!url) { showWarn('Enter a database URL to test.'); return; }
  try {
    await app.TestDatabaseConnection(url);
    showOk('Connection OK ✓');
  } catch (e) {
    showErr('Connection failed: ' + e);
  }
};

document.getElementById('btn-db-save').onclick = async () => {
  const url = document.getElementById('db-url').value.trim();
  if (!url) { showWarn('Enter a URL, or use Revert to go back to local SQLite.'); return; }
  try {
    await app.SetDatabaseConfig(url);
    showOk('Saved. Restart MidnightConduit to connect.');
    document.getElementById('db-url').value = '';
    await renderDatabasePanel();
  } catch (e) {
    showErr('Save failed: ' + e);
  }
};

document.getElementById('btn-db-revert').onclick = async () => {
  if (!window.confirm('Revert to local SQLite on next start?')) return;
  try {
    await app.ClearDatabaseConfig();
    showOk('Reverted. Restart to use local SQLite.');
    document.getElementById('db-url').value = '';
    await renderDatabasePanel();
  } catch (e) {
    showErr('Revert failed: ' + e);
  }
};

document.getElementById('btn-db-migrate').onclick = async () => {
  if (!window.confirm('Copy local SQLite config into the connected database, REPLACING its current contents? This cannot be undone.')) return;
  try {
    const counts = await app.MigrateLocalDatabase();
    const total = Object.values(counts || {}).reduce((a, b) => a + (b || 0), 0);
    showOk(`Migrated ${total} rows from local SQLite.`);
    await renderDatabasePanel();
  } catch (e) {
    showErr('Migration failed: ' + e);
  }
};

function renderMainTab() {
  if (!mainTabContent) {
    return;
  }
  const active = appState?.active_project_id || 'main';
  const activeProject = (appState?.projects || []).find(p => p.id === active);
  const projects = Array.isArray(appState?.projects) ? appState.projects.length : 0;
  mainTabContent.textContent = [
    `Active project: ${active}`,
    `Project name: ${activeProject?.name || 'Unknown'}`,
    `Defined projects: ${projects}`,
    `DB file: ${appState?.db_file || '(unknown)'}`,
    `API: ${appState?.api_address || '(not started)'}`,
    `MCP: ${appState?.mcp_path || '(not started)'}`,
  ].join('\n');
}

function renderTunnels() {
  tunnelList.innerHTML = '';
  const tunnels = normalizeTunnels(appState && appState.tunnels ? appState.tunnels : []);
  if (!tunnels.length) {
    tunnelList.innerHTML = '<div class="empty">No tunnels configured in database.</div>';
    return;
  }

  for (const t of tunnels) {
    const card = tplT.content.firstElementChild.cloneNode(true);
    card.dataset.id = t.id || t.name;
    card.querySelector('.card-name').textContent = t.name || t.id;
    card.querySelector('.card-port').textContent = t.local_port;
    card.querySelector('.card-host').textContent = t.ssh_host;
    card.querySelector('.card-remote').textContent = `${t.remote_host}:${t.remote_port}`;

    const status = normalizeStatus(t.status);
    const health = t.last_health_status ? ` / ${t.last_health_status}` : '';

    setCardStatus(card, status);
    card.querySelector('.health-badge').textContent = health;

    const startBtn = card.querySelector('.btn-start');
    const stopBtn = card.querySelector('.btn-stop');
    const restartBtn = card.querySelector('.btn-restart');
    const openBtn = card.querySelector('.btn-open');

    const isRunning = status === 'running';
    startBtn.disabled = isRunning;
    stopBtn.disabled = !isRunning;
    restartBtn.disabled = !isRunning;

    startBtn.onclick = async () => {
      try {
        const updated = await app.StartTunnel(t.id);
        if (!updated || !updated.status) {
          await refreshTunnels();
          return;
        }
        Object.assign(t, updated);
        renderTunnels();
        showOk(`Started tunnel: ${t.name || t.id}`);
      } catch (e) {
        showErr(`Start ${t.name || t.id} failed: ${e}`);
      }
    };

    stopBtn.onclick = async () => {
      try {
        const updated = await app.StopTunnel(t.id);
        if (updated && updated.status) {
          Object.assign(t, updated);
        }
        await refreshTunnels();
        showOk(`Stopped tunnel: ${t.name || t.id}`);
      } catch (e) {
        showErr(`Stop ${t.name || t.id} failed: ${e}`);
      }
    };

    restartBtn.onclick = async () => {
      try {
        await app.RestartTunnel(t.id);
        await refreshTunnels();
        showOk(`Restarted tunnel: ${t.name || t.id}`);
      } catch (e) {
        showErr(`Restart ${t.name || t.id} failed: ${e}`);
      }
    };

    openBtn.onclick = () => {
      if (t.open_url) {
        app.OpenURL(t.open_url);
      } else {
        showWarn(`No URL configured for ${t.name || t.id}`);
      }
    };

    const editBtn = card.querySelector('.btn-edit');
    if (editBtn) editBtn.onclick = () => openTunnelForm(t);

    tunnelList.appendChild(card);
  }
}

function renderServers() {
  if (!serversListEl || !tplServer) return;
  serversListEl.innerHTML = '';
  const servers = normalizeServers(appState?.servers || []);
  if (!servers.length) {
    serversListEl.innerHTML = '<div class="empty">No servers configured.</div>';
    return;
  }
  for (const s of servers) {
    const card = tplServer.content.firstElementChild.cloneNode(true);
    card.dataset.id = s.id;
    card.querySelector('.card-name').textContent = s.display_name || s.name || s.id;
    card.querySelector('.server-cmd').textContent = s.command || '';
    card.querySelector('.server-img').textContent = s.docker_image ? `[${s.docker_image}]` : '';
    card.querySelector('.status-label').textContent = s.enabled ? 'configured' : 'disabled';
    setCardStatus(card, s.enabled ? 'running' : 'stopped');
    card.querySelector('.btn-delete').onclick = async () => {
      if (!window.confirm(`Delete server "${s.display_name || s.name}"?`)) return;
      try {
        await app.DeleteServerForProject(currentProject, s.id);
        showOk(`Deleted server: ${s.display_name || s.name}`);
        await loadState();
        if (currentTab === 'servers') await renderServers();
      } catch (e) { showErr('Delete server failed: ' + e); }
    };
    serversListEl.appendChild(card);
  }
}

async function refreshServers() {
  if (!serversListEl || !tplServer) return;
  try {
    const servers = await app.GetServers();
    appState = appState || {};
    appState.servers = servers || [];
    if (currentTab === 'servers' || !currentTab) renderServers();
  } catch (e) {
    serversListEl.innerHTML = '<div class="empty">Failed to load servers.</div>';
    showErr('Load servers failed: ' + e);
  }
}

function buildOrchCard(p) {
  const card = tplO.content.firstElementChild.cloneNode(true);
  card.dataset.name = p.name || p.id || '';
  card.querySelector('.card-name').textContent = p.name || 'process';
  card.querySelector('.orch-ns').textContent = p.namespace || 'default';
  card.querySelector('.orch-port').textContent = p.port ? `:${p.port}` : '';

  const running = !!p.is_running;
  const ready = !!p.is_ready;
  const statusText = ready ? 'running' : running ? 'readying' : 'stopped';
  card.querySelector('.status-label').textContent = statusText;
  card.querySelector('.health-badge').textContent = p.is_ready_text ? `(${p.is_ready_text})` : '';
  setCardStatus(card, statusText);

  const startBtn = card.querySelector('.btn-start');
  const stopBtn = card.querySelector('.btn-stop');
  const restartBtn = card.querySelector('.btn-restart');
  const logsBtn = card.querySelector('.btn-logs');

  startBtn.disabled = running;
  stopBtn.disabled = !running;
  restartBtn.disabled = !running;

  startBtn.onclick = async () => {
    try {
      await app.OrchStartProcess(p.name);
      await refreshOrchestrator();
      showOk(`Process start requested: ${p.name}`);
    } catch (e) {
      showErr(`Process start failed: ${e}`);
    }
  };
  stopBtn.onclick = async () => {
    try {
      await app.OrchStopProcess(p.name);
      await refreshOrchestrator();
      showOk(`Process stop requested: ${p.name}`);
    } catch (e) {
      showErr(`Process stop failed: ${e}`);
    }
  };
  restartBtn.onclick = async () => {
    try {
      await app.OrchRestartProcess(p.name);
      await refreshOrchestrator();
      showOk(`Process restart requested: ${p.name}`);
    } catch (e) {
      showErr(`Process restart failed: ${e}`);
    }
  };
  logsBtn.onclick = () => showOrchLogs(p.name, 400);

  return card;
}

// Start/stop every process in a group, in array order so a backend comes up
// before the frontend that depends on it. Errors are collected, not thrown.
async function orchGroupAction(procs, action, label) {
  const errs = [];
  for (const p of procs) {
    if (action === 'start' && p.is_running) continue;
    if (action === 'stop' && !p.is_running) continue;
    try {
      if (action === 'start') await app.OrchStartProcess(p.name);
      else await app.OrchStopProcess(p.name);
    } catch (e) {
      errs.push(`${p.name}: ${e}`);
    }
  }
  await refreshOrchestrator();
  if (errs.length) showErr(`${label} partial failure: ${errs.join('; ')}`);
  else showOk(`${label} ${action === 'start' ? 'started' : 'stopped'}`);
}

function renderProcessList(processes) {
  const normalizedProcesses = normalizeProcesses(processes);
  orchList.innerHTML = '';
  if (!normalizedProcesses || !normalizedProcesses.length) {
    orchList.innerHTML = '<div class="empty">No orchestrated processes available.</div>';
    return;
  }

  // Group by namespace, preserving the namespace-sorted order from normalize.
  const groups = [];
  const byNs = {};
  for (const p of normalizedProcesses) {
    const ns = p.namespace || 'default';
    if (!(ns in byNs)) { byNs[ns] = groups.length; groups.push({ ns, procs: [] }); }
    groups[byNs[ns]].procs.push(p);
  }

  for (const g of groups) {
    const runningCount = g.procs.filter(p => p.is_running).length;
    const collapsed = !!orchCollapsed[g.ns];

    const groupEl = document.createElement('div');
    groupEl.className = 'orch-group' + (collapsed ? ' collapsed' : '');

    const header = document.createElement('div');
    header.className = 'orch-group-header';

    const left = document.createElement('div');
    left.className = 'orch-group-left';
    const toggle = document.createElement('span');
    toggle.className = 'orch-group-toggle';
    toggle.textContent = collapsed ? '▶' : '▼';
    const title = document.createElement('span');
    title.className = 'orch-group-title';
    title.textContent = g.ns;
    const summary = document.createElement('span');
    summary.className = 'orch-group-summary';
    summary.textContent = `${runningCount}/${g.procs.length} up`;
    left.append(toggle, title, summary);
    left.onclick = () => {
      orchCollapsed[g.ns] = !orchCollapsed[g.ns];
      groupEl.classList.toggle('collapsed');
      toggle.textContent = orchCollapsed[g.ns] ? '▶' : '▼';
    };

    const actions = document.createElement('div');
    actions.className = 'orch-group-actions';
    const startAll = document.createElement('button');
    startAll.textContent = '▶ Start all';
    startAll.disabled = runningCount === g.procs.length;
    startAll.onclick = (e) => { e.stopPropagation(); orchGroupAction(g.procs, 'start', g.ns); };
    const stopAll = document.createElement('button');
    stopAll.textContent = '⏹ Stop all';
    stopAll.disabled = runningCount === 0;
    stopAll.onclick = (e) => { e.stopPropagation(); orchGroupAction(g.procs, 'stop', g.ns); };
    actions.append(startAll, stopAll);

    header.append(left, actions);

    const body = document.createElement('div');
    body.className = 'orch-group-body';
    for (const p of g.procs) body.appendChild(buildOrchCard(p));

    groupEl.append(header, body);
    orchList.appendChild(groupEl);
  }
}

async function renderPorts(ports) {
  portsListEl.innerHTML = '';
  if (!ports || !ports.length) {
    portsListEl.innerHTML = '<div class="empty">No open ports found.</div>';
    return;
  }

  for (const p of ports) {
    const card = tplPort.content.firstElementChild.cloneNode(true);
    card.dataset.pid = `${p.pid || ''}`;
    card.querySelector('.card-name').textContent = p.process ? p.process : (p.address || `:${p.port}`);
    card.querySelector('.card-port').textContent = `${p.port}`;
    card.querySelector('.slash').textContent = '@';
    card.querySelector('.card-protocol').textContent = p.protocol || '';
    card.querySelector('.card-state').textContent = p.state || '';
    card.querySelector('.card-pid').textContent = p.pid ? `pid ${p.pid}` : '';

    setCardStatus(card, 'running');

    const openBtn = card.querySelector('.btn-open');
    if (openBtn) {
      const isTcp = !p.protocol || String(p.protocol).toLowerCase().includes('tcp');
      openBtn.disabled = !(Number(p.port) > 0) || !isTcp;
      openBtn.onclick = () => app.OpenURL(`http://localhost:${p.port}`);
    }

    const stopBtn = card.querySelector('.btn-stop');
    stopBtn.disabled = !(Number(p.pid) > 0);
    stopBtn.onclick = async () => {
      if (!p.pid || Number(p.pid) <= 0) {
        return;
      }
      if (!window.confirm(`Kill process ${p.pid} and close port ${p.port}?`)) {
        return;
      }
      try {
        await app.KillOpenPort(p.pid);
        showOk(`Killed pid ${p.pid}`);
        await refreshPorts();
      } catch (e) {
        showErr(`Kill pid ${p.pid} failed: ${e}`);
      }
    };
    portsListEl.appendChild(card);
  }
}

async function refreshPorts() {
  if (!portsListEl || !tplPort) {
    return;
  }
  if (!app.KillOpenPort || !app.ListOpenPorts) {
    portsListEl.innerHTML = '<div class="empty">Port scanning not available in this environment.</div>';
    return;
  }
  try {
    const ports = await app.ListOpenPorts();
    renderPorts(normalizeOpenPorts(ports || []));
  } catch (e) {
    portsListEl.innerHTML = '<div class="empty">Failed to load ports.</div>';
    showErr('Load ports failed: ' + e);
  }
}

async function refreshTunnels() {
  try {
    appState = await app.GetAppState();
    if (currentTab === 'tunnels' || !currentTab) {
      renderTunnels();
    }
  } catch (e) {
    showErr('refresh tunnels: ' + e);
  }
}

async function runStartBatch(mode) {
  try {
    const result = mode === 'startauto'
      ? await app.StartAutoTunnels()
      : await app.StartAllTunnels();

    let msg = '';
    if (result.started.length) msg += `Started: ${result.started.join(', ')}. `;
    if (result.already_running.length) msg += `Already running: ${result.already_running.join(', ')}. `;
    if (result.skipped.length) msg += `Skipped: ${result.skipped.join(', ')}. `;
    if (result.failed.length) {
      for (const fail of result.failed) {
        showErr(`[${fail.name || fail.id}] ${fail.error}`);
      }
    }
    await refreshTunnels();
    showOk(msg || 'No action taken');
  } catch (e) {
    showErr('Batch start failed: ' + e);
  }
}

function setOrchLogOutput(logs) {
  orchLogOutput.textContent = logs || '(no logs)';
  orchLogOutput.scrollTop = orchLogOutput.scrollHeight;
}

async function refreshOrchLogView(name, limit = 220, opts = { silent: false }) {
  if (!name) {
    return;
  }
  try {
    const logs = await app.OrchGetProcessLogs(name, limit);
    if (orchLogTitle.dataset.name === name) {
      setOrchLogOutput(logs);
    }
  } catch (e) {
    if (!opts.silent) {
      showErr(`Log fetch failed for ${name}: ${e}`);
    }
  }
}

async function showOrchLogs(name, limit = 200) {
  if (!name) return;
  orchLogTitle.textContent = `Logs: ${name}`;
  orchLogTitle.dataset.name = name;
  orchLogViewer.classList.remove('collapsed');
  await refreshOrchLogView(name, limit, { silent: false });
}

async function refreshOrchestrator() {
  const running = await app.OrchIsRunning();
  const startBtn = document.getElementById('orch-start');
  const stopBtn = document.getElementById('orch-shutdown');
  const startAllBtn = document.getElementById('orch-start-all');
  const stopAllBtn = document.getElementById('orch-stop-all');

  startBtn.disabled = running;
  stopBtn.disabled = !running;
  startAllBtn.disabled = !running;
  stopAllBtn.disabled = !running;

  if (!running) {
    renderProcessList([]);
    return;
  }

  try {
    const list = await app.OrchListProcesses();
    renderProcessList(list);
  } catch (e) {
    showWarn('Could not list orchestrator processes: ' + e);
  }
}

function setPoll() {
  if (orchPoll) {
    clearInterval(orchPoll);
  }
  let refreshing = false;
  orchPoll = setInterval(async () => {
    if (refreshing) {
      return;
    }
    refreshing = true;
    try {
      await refreshTunnels();
      if (currentTab === 'orchestrator') {
        await refreshOrchestrator();
      }
      if (!orchLogViewer.classList.contains('collapsed') && orchLogTitle.dataset.name) {
        await refreshOrchLogView(orchLogTitle.dataset.name, 220, { silent: true });
      }
    } finally {
      refreshing = false;
    }
  }, 3500);
}

async function init() {
  if (!app) {
    showErr('Wails binding not ready.');
    return;
  }
  // The store may still be connecting (Postgres connect+migrate takes a moment).
  // Retry quietly until the first load succeeds, then start polling.
  let ok = false;
  for (let i = 0; i < 40 && !ok; i++) {
    ok = await loadState(i < 39);
    if (!ok) await new Promise(r => setTimeout(r, 500));
  }
  setPoll();
}

if (document.readyState === 'loading') {
  window.addEventListener('DOMContentLoaded', init);
} else {
  init();
}

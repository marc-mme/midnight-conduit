// Tunnel Deck — Wails frontend

const app = window.go?.main?.App;
if (!app) {
  document.querySelector('.empty').textContent = 'Wails runtime not loaded.';
  throw new Error('Wails not ready');
}

let tunnelData;
const list = document.getElementById('tunnel-list');
const statusBar = document.getElementById('status-bar');
const configPath = document.getElementById('config-path');
const tpl = document.getElementById('tpl');

// ── Init ──
(async function init() {
  await refresh();
  setInterval(refresh, 5000);

  document.getElementById('btn-start-auto').onclick = startAuto;
  document.getElementById('btn-stop-all').onclick = stopAll;
  document.getElementById('btn-reload').onclick = reload;
  document.getElementById('btn-open-config').onclick = openConfig;
})();

async function refresh() {
  try {
    const info = await app.GetAppInfo();
    tunnelData = info;
    configPath.textContent = info.config_path;
    render();
  } catch (e) {
    statusBar.textContent = 'Error: ' + e;
  }
}

// ── Render ──
function render() {
  const tunnels = tunnelData?.tunnels;
  if (!tunnels || tunnels.length === 0) {
    list.innerHTML = '<div class="empty">No tunnels configured.<br>Edit tunnels.toml to add some.</div>';
    statusBar.textContent = '0 tunnels';
    return;
  }

  list.innerHTML = '';
  statusBar.textContent = `${tunnels.length} tunnel${tunnels.length > 1 ? 's' : ''}`;

  for (const t of tunnels) {
    const el = tpl.content.cloneNode(true);
    const card = el.querySelector('.card');
    card.dataset.id = t.id;

    card.querySelector('.card-name').textContent = t.name;
    card.querySelector('.card-port').textContent = `:${t.local_port}`;
    card.querySelector('.card-remote').textContent = `${t.remote_host}:${t.remote_port}`;
    card.querySelector('.card-host').textContent = t.ssh_host;

    const dot = card.querySelector('.dot');
    const label = card.querySelector('.status-label');
    const badge = card.querySelector('.health-badge');

    dot.className = `dot ${t.status}`;
    label.className = `status-label ${t.status}`;
    label.textContent = t.status;
    badge.textContent = t.last_health_status ? `(${t.last_health_status})` : '';

    const isRunning = t.status === 'running' || t.status === 'starting';
    const startBtn = card.querySelector('.btn-start');
    const stopBtn = card.querySelector('.btn-stop');
    const restartBtn = card.querySelector('.btn-restart');
    const openBtn = card.querySelector('.btn-open');

    startBtn.style.display = isRunning ? 'none' : '';
    startBtn.disabled = isRunning;
    stopBtn.style.display = isRunning ? '' : 'none';
    stopBtn.disabled = !isRunning;
    restartBtn.disabled = !isRunning;
    openBtn.disabled = !t.open_url;

    startBtn.onclick = () => start(t.id);
    stopBtn.onclick = () => stop(t.id);
    restartBtn.onclick = () => restart(t.id);
    openBtn.onclick = () => app.OpenURL(t.open_url).catch(()=>{});

    list.appendChild(card);
  }
}

// ── Actions ──
async function start(id) {
  setStatus(id, 'starting');
  try { await app.StartTunnel(id); } catch(e) { statusBar.textContent = String(e); }
  await refresh();
}

async function stop(id) {
  setStatus(id, 'stopping');
  try { await app.StopTunnel(id); } catch(e) { statusBar.textContent = String(e); }
  await refresh();
}

async function restart(id) {
  setStatus(id, 'restarting');
  try { await app.RestartTunnel(id); } catch(e) { statusBar.textContent = String(e); }
  await refresh();
}

async function startAuto() {
  try {
    const r = await app.StartAutoTunnels();
    statusBar.textContent = `Auto-started: ${r.join(', ') || 'none'}`;
    await refresh();
  } catch(e) { statusBar.textContent = String(e); }
}

async function stopAll() {
  try {
    await app.StopAllTunnels();
    statusBar.textContent = 'All tunnels stopped';
    await refresh();
  } catch(e) { statusBar.textContent = String(e); }
}

async function reload() {
  try {
    tunnelData = await app.ReloadConfig();
    configPath.textContent = tunnelData.config_path;
    render();
    statusBar.textContent = 'Config reloaded';
  } catch(e) { statusBar.textContent = String(e); }
}

async function openConfig() {
  try { await app.OpenConfigFile(); } catch(e) { statusBar.textContent = String(e); }
}

function setStatus(id, status) {
  const card = document.querySelector(`.card[data-id="${id}"]`);
  if (!card) return;
  card.querySelector('.dot').className = `dot ${status}`;
  card.querySelector('.status-label').className = `status-label ${status}`;
  card.querySelector('.status-label').textContent = status;
}

// MidnightConduit — Tunnels + Orchestrator
const app = window.go?.main?.App;

// ── Logging ──
const logOut = document.getElementById('log-output');
function log(msg, cls) {
  const time = new Date().toLocaleTimeString();
  const line = document.createElement('div');
  line.className = cls || '';
  line.textContent = `[${time}] ${msg}`;
  logOut.appendChild(line);
  logOut.scrollTop = logOut.scrollHeight;
}
document.getElementById('log-toggle').onclick = () => {
  document.getElementById('log-panel').classList.toggle('collapsed');
};
document.getElementById('log-clear').onclick = () => { logOut.innerHTML = ''; };
log('MidnightConduit started', 'log-info');

// ── Tab switching ──
document.querySelectorAll('.tab').forEach(t => {
  t.onclick = () => {
    document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.tab-panel').forEach(x => { x.style.display = 'none'; x.classList.remove('active'); });
    t.classList.add('active');
    const panel = document.getElementById('tab-' + t.dataset.tab);
    panel.style.display = '';
    panel.classList.add('active');
    if (t.dataset.tab === 'orchestrator') refreshOrch();
    if (t.dataset.tab === 'tunnels') refreshTunnels();
  };
});

// ── Tunnels ──
let tunnelData;
const tunnelList = document.getElementById('tunnel-list');
const statusBar = document.getElementById('status-bar');
const configPath = document.getElementById('config-path');
const tplT = document.getElementById('tpl-tunnel');

let tunnelRefresh;

async function refreshTunnels() {
  try {
    tunnelData = await app.GetAppInfo();
    configPath.textContent = tunnelData.config_path;
    renderTunnels();
  } catch(e) { statusBar.textContent = 'Error: ' + e; }
}

function renderTunnels() {
  const ts = tunnelData?.tunnels;
  if (!ts?.length) { tunnelList.innerHTML = '<div class="empty">No tunnels configured.</div>'; return; }
  tunnelList.innerHTML = '';
  statusBar.textContent = ts.length + ' tunnel' + (ts.length>1?'s':'');
  for (const t of ts) {
    const el = tplT.content.cloneNode(true);
    const c = el.querySelector('.card'); c.dataset.id = t.id;
    c.querySelector('.card-name').textContent = t.name;
    c.querySelector('.card-port').textContent = ':' + t.local_port;
    c.querySelector('.card-remote').textContent = t.remote_host + ':' + t.remote_port;
    c.querySelector('.card-host').textContent = t.ssh_host;
    const dot = c.querySelector('.dot'), lbl = c.querySelector('.status-label');
    dot.className = 'dot ' + t.status; lbl.className = 'status-label ' + t.status; lbl.textContent = t.status;
    c.querySelector('.health-badge').textContent = t.last_health_status ? '('+t.last_health_status+')' : '';
    const running = t.status === 'running' || t.status === 'starting';
    c.querySelector('.btn-start').style.display = running ? 'none' : ''; c.querySelector('.btn-start').disabled = running;
    c.querySelector('.btn-stop').style.display = running ? '' : 'none'; c.querySelector('.btn-stop').disabled = !running;
    c.querySelector('.btn-restart').disabled = !running;
    c.querySelector('.btn-open').disabled = !t.open_url;
    c.querySelector('.btn-start').onclick = () => tunnelStart(t.id);
    c.querySelector('.btn-stop').onclick = () => tunnelStop(t.id);
    c.querySelector('.btn-restart').onclick = () => tunnelRestart(t.id);
    c.querySelector('.btn-open').onclick = () => app.OpenURL(t.open_url).catch(()=>{});
    tunnelList.appendChild(c);
  }
}

async function tunnelStart(id) { log('Start: '+id); setTunnelStatus(id,'starting'); try{await app.StartTunnel(id); log('Started: '+id)}catch(e){log('FAIL: '+e,'log-err')} refreshTunnels(); }
async function tunnelStop(id)  { log('Stop: '+id); setTunnelStatus(id,'stopping');  try{await app.StopTunnel(id); log('Stopped: '+id)}catch(e){log('FAIL: '+e,'log-err')}  refreshTunnels(); }
async function tunnelRestart(id){ log('Restart: '+id); setTunnelStatus(id,'restarting');try{await app.RestartTunnel(id); log('Restarted: '+id)}catch(e){log('FAIL: '+e,'log-err')}refreshTunnels(); }

function setTunnelStatus(id,s){ const c=document.querySelector('.card[data-id="'+id+'"]'); if(!c)return; c.querySelector('.dot').className='dot '+s; c.querySelector('.status-label').className='status-label '+s; c.querySelector('.status-label').textContent=s; }

function logTunnelBatchResult(r, emptyMessage) {
  const started = Array.isArray(r?.started) ? r.started : [];
  const already = Array.isArray(r?.already_running) ? r.already_running : [];
  const failed = Array.isArray(r?.failed) ? r.failed : [];
  if (started.length) log('Started tunnels: ' + started.join(', '));
  if (already.length) log('Already running: ' + already.join(', '));
  for (const f of failed) log('FAIL tunnel '+(f.id || f.name)+': '+f.error, 'log-err');
  if (!started.length && !already.length && !failed.length) log(emptyMessage, 'log-warn');
}

async function startAllTunnels(){
  log('Start all tunnels...');
  try{ logTunnelBatchResult(await app.StartAllTunnels(), 'No tunnels configured') }
  catch(e){ log('FAIL start all tunnels: '+e,'log-err') }
  refreshTunnels();
}
async function startAuto(){
  log('Start tunnels with auto_start=true...');
  try{ logTunnelBatchResult(await app.StartAutoTunnels(), 'No tunnels have auto_start=true') }
  catch(e){ log('FAIL auto-start: '+e,'log-err') }
  refreshTunnels();
}
async function stopAllTunnels(){ log('Stop all tunnels'); try{await app.StopAllTunnels(); log('All stopped')}catch(e){log('FAIL stop all: '+e,'log-err')} refreshTunnels(); }
async function reload(){ log('Reload config'); try{tunnelData=await app.ReloadConfig(); log('Config reloaded')}catch(e){log('FAIL reload: '+e,'log-err')} refreshTunnels(); }
async function openConfig(){ app.OpenConfigFile().catch(()=>{}); }

document.getElementById('btn-start-all-tunnels').onclick = startAllTunnels;
document.getElementById('btn-start-auto-tunnels').onclick = startAuto;
document.getElementById('btn-stop-all-tunnels').onclick = stopAllTunnels;
document.getElementById('btn-reload-tunnels').onclick = reload;
document.getElementById('btn-open-config').onclick = openConfig;

// ── Orchestrator ──
let orchData = [];
const orchList = document.getElementById('orch-list');
const tplO = document.getElementById('tpl-orch');
const orchLogViewer = document.getElementById('orch-log-viewer');
const orchLogTitle = document.getElementById('orch-log-title');
const orchLogOutput = document.getElementById('orch-log-output');
let orchRefresh;
let selectedOrchLogProcess = '';

async function refreshOrch() {
  try {
    orchData = await app.OrchListProcesses();
  } catch(e) {
    orchData = [];
  }
  renderOrch();
  const running = await app.OrchIsRunning();
  document.getElementById('orch-shutdown').disabled = !running;
  document.getElementById('orch-stop-all').disabled = !running;
  document.getElementById('orch-start').disabled = running;
}

function renderOrch() {
  if (!orchData.length) {
    orchList.innerHTML = '<div class="empty">Orchestrator not running. Click Start.</div>';
    return;
  }
  orchList.innerHTML = '';
  for (const p of orchData) {
    const el = tplO.content.cloneNode(true);
    const c = el.querySelector('.card'); c.dataset.name = p.name;
    c.querySelector('.card-name').textContent = p.name;
    c.querySelector('.orch-ns').textContent = p.namespace || '';
    c.querySelector('.orch-port').textContent = [p.port ? ':' + p.port : '', p.pid ? 'pid ' + p.pid : '', !p.is_running ? 'exit ' + p.exit_code : '', p.is_ready_text ? 'ready ' + p.is_ready_text : ''].filter(Boolean).join(' · ');
    const dot = c.querySelector('.dot'), lbl = c.querySelector('.status-label');
    const cls = p.is_running ? (p.is_ready ? 'running' : 'starting') : 'stopped';
    dot.className = 'dot ' + cls; lbl.className = 'status-label ' + cls; lbl.textContent = p.status;
    c.querySelector('.health-badge').textContent = p.is_ready ? '(ready)' : '';
    c.querySelector('.btn-start').disabled = p.is_running;
    c.querySelector('.btn-stop').disabled = !p.is_running;
    c.querySelector('.btn-restart').disabled = !p.is_running;
    c.querySelector('.btn-start').onclick = () => orchStart(p.name);
    c.querySelector('.btn-stop').onclick = () => orchStop(p.name);
    c.querySelector('.btn-restart').onclick = () => orchRestart(p.name);
    c.querySelector('.btn-logs').onclick = () => showOrchLogs(p.name);
    orchList.appendChild(c);
  }
}

async function showOrchLogs(name) {
  selectedOrchLogProcess = name;
  orchLogViewer.classList.remove('collapsed');
  orchLogTitle.textContent = 'Logs: ' + name;
  orchLogOutput.textContent = 'Loading logs...';
  try {
    const txt = await app.OrchGetProcessLogs(name, 300);
    orchLogOutput.textContent = txt || '(no logs)';
    orchLogOutput.scrollTop = orchLogOutput.scrollHeight;
    log('[orch] Loaded logs for '+name);
  } catch(e) {
    orchLogOutput.textContent = String(e);
    log('[orch] FAIL logs '+name+': '+e, 'log-err');
  }
}

document.getElementById('orch-log-refresh').onclick = () => { if (selectedOrchLogProcess) showOrchLogs(selectedOrchLogProcess); };
document.getElementById('orch-log-close').onclick = () => { orchLogViewer.classList.add('collapsed'); selectedOrchLogProcess = ''; };

async function orchStart(name) { log('[orch] Start: '+name); try{await app.OrchStartProcess(name); log('[orch] Started: '+name)}catch(e){log('[orch] FAIL: '+e,'log-err')} refreshOrch(); }
async function orchStop(name)  { log('[orch] Stop: '+name); try{await app.OrchStopProcess(name); log('[orch] Stopped: '+name)}catch(e){log('[orch] FAIL: '+e,'log-err')}  refreshOrch(); }
async function orchRestart(name){ log('[orch] Restart: '+name); try{await app.OrchRestartProcess(name); log('[orch] Restarted: '+name)}catch(e){log('[orch] FAIL: '+e,'log-err')}refreshOrch(); }

document.getElementById('orch-start').onclick = async () => { const cfg = await app.OrchGetConfigPath(); log('[orch] Starting orchestrator with '+cfg+'...'); try{await app.OrchStart(); log('[orch] Orchestrator started/attached')}catch(e){log('[orch] FAIL start: '+e,'log-err')} refreshOrch(); };
document.getElementById('orch-shutdown').onclick = async () => { log('[orch] Shutting down...'); try{await app.OrchShutdown(); log('[orch] Shut down')}catch(e){log('[orch] FAIL shutdown: '+e,'log-err')} refreshOrch(); };
document.getElementById('orch-reload').onclick = async () => {
  const cfg = await app.OrchGetConfigPath();
  log('[orch] Reloading config: '+cfg);
  try{await app.OrchReloadConfig(cfg); log('[orch] Config reloaded')}catch(e){log('[orch] FAIL reload: '+e,'log-err')} refreshOrch();
};
document.getElementById('orch-start-all').onclick = async () => { log('[orch] Start all...'); try{await app.OrchStartAll(); log('[orch] All started')}catch(e){log('[orch] FAIL start all: '+e,'log-err')} refreshOrch(); };
document.getElementById('orch-stop-all').onclick = async () => { log('[orch] Stop all...'); try{await app.OrchStopAll(); log('[orch] All stopped')}catch(e){log('[orch] FAIL stop all: '+e,'log-err')} refreshOrch(); };

// ── Init ──
(async function init() {
  await refreshTunnels();
  tunnelRefresh = setInterval(refreshTunnels, 5000);
  orchRefresh = setInterval(refreshOrch, 5000);
})();

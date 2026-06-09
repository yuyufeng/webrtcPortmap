// controller.js - Web Controller (HTTP Web Console 版本)

const state = {
    signalURL: '',
    webConsoleBase: '',
    proxyServiceBase: '',
    userSessionToken: '',
    currentUser: null,
    agentID: '',
    agentPassword: '',
    pc: null,
    dataChannel: null,
    authenticated: false,
    agentConfig: null,
    derivedKey: null,
    selectedHTTPPort: null,
    pendingHTTPRequests: new Map(), // id -> {resolve, reject}
    cookieJar: new Map(), // portID -> Map(name -> cookie)
    wsConnections: new Map(), // socketId -> DcWebSocket
    currentPreview: null,
    previewHistory: [],
    previewHistoryIndex: -1,
    selectedAgentName: '',
    selectedAgent: null,
    agentsById: {},
    // 内嵌终端
    term: null,        // xterm.js 实例
    termFit: null,     // fit addon
    termOpen: false,   // 是否已打开/附着终端会话
    termDataDisposable: null,
    termResizeHandler: null
};

const DEFAULT_TENANT_CODE = 'convnet';
const DEFAULT_TENANT_NAME = 'convnet';
const SESSION_STORAGE_KEY = 'webrtc_portmap_session';
const PREVIEW_PATH_REWRITE_RULES = [
    {
        name: 'tenant-static-to-root-static',
        pattern: /^\/t\/[^/]+\/static\//,
        replace: '/static/'
    }
];

// ==================== 日志工具 ====================
function log(message, type = 'info') {
    const logs = document.getElementById('logs');
    if (!logs) return;
    const time = new Date().toLocaleTimeString();
    const colorClass = type === 'error' ? 'log-error' : type === 'warn' ? 'log-warn' : 'log-info';
    logs.innerHTML += `<div><span class="log-time">[${time}]</span> <span class="${colorClass}">${escapeHtml(message)}</span></div>`;
    logs.scrollTop = logs.scrollHeight;
    console.log(`[${type}] ${message}`);
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function decodeBase64Utf8(value) {
    if (!value) return '';
    try {
        const binary = atob(value);
        const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
        return new TextDecoder().decode(bytes);
    } catch (e) {
        try {
            return atob(value);
        } catch (err) {
            return '';
        }
    }
}

function showStatus(elementId, message, type) {
    const el = document.getElementById(elementId);
    if (!el) return;
    el.className = `status ${type}`;
    el.textContent = message;
    el.classList.remove('hidden');
}

function showAuthPanel(mode) {
    const loginTab = document.getElementById('auth-tab-login');
    const registerTab = document.getElementById('auth-tab-register');
    const loginPanel = document.getElementById('auth-panel-login');
    const registerPanel = document.getElementById('auth-panel-register');
    const isLogin = mode !== 'register';
    if (loginTab) loginTab.classList.toggle('active', isLogin);
    if (registerTab) registerTab.classList.toggle('active', !isLogin);
    if (loginPanel) loginPanel.classList.toggle('hidden', !isLogin);
    if (registerPanel) registerPanel.classList.toggle('hidden', isLogin);
}

function getSignalURL() {
    return (state.signalURL || window.location.origin || '').replace(/\/+$/, '');
}

function getWebConsoleBase() {
    return state.webConsoleBase || `${window.location.origin}/webconsole`;
}

function getProxyServiceBase() {
    return state.proxyServiceBase || `${window.location.origin}/proxyservice`;
}

function buildJSONHeaders(includeAuth = true) {
    const headers = { 'Content-Type': 'application/json' };
    if (includeAuth && state.userSessionToken) {
        headers.Authorization = `Bearer ${state.userSessionToken}`;
    }
    return headers;
}

function saveSessionState() {
    if (!state.userSessionToken || !state.currentUser) {
        localStorage.removeItem(SESSION_STORAGE_KEY);
        return;
    }
    localStorage.setItem(SESSION_STORAGE_KEY, JSON.stringify({
        signalURL: state.signalURL,
        webConsoleBase: state.webConsoleBase,
        proxyServiceBase: state.proxyServiceBase,
        token: state.userSessionToken,
        currentUser: state.currentUser
    }));
}

function loadSessionState() {
    try {
        const raw = localStorage.getItem(SESSION_STORAGE_KEY);
        if (!raw) return null;
        return JSON.parse(raw);
    } catch (err) {
        localStorage.removeItem(SESSION_STORAGE_KEY);
        return null;
    }
}

function clearSessionState() {
    localStorage.removeItem(SESSION_STORAGE_KEY);
}

function updateLoginVisibility() {
    const loggedIn = !!(state.userSessionToken && state.currentUser);
    document.getElementById('topbar')?.classList.toggle('hidden', !loggedIn);
}

function updateCurrentUserUI() {
    const text = state.currentUser
        ? `当前用户：${state.currentUser.username}${state.currentUser.email ? ` (${state.currentUser.email})` : ''}`
        : '';
    updateAdminVisibility();
    updateMyQuotaVisibility();
    const currentUser = document.getElementById('current-user');
    const agentListUser = document.getElementById('agent-list-user');
    const userHash = document.getElementById('user-hash');
    const agentStartCommand = document.getElementById('agent-start-command');
    const agentPortsJSON = document.getElementById('agent-ports-json');
    const agentStartCommandWithPorts = document.getElementById('agent-start-command-with-ports');
    const clientStartCommand = document.getElementById('client-start-command');
    const agentDownloadURLWindows = document.getElementById('agent-download-url-windows');
    const agentDownloadURLLinux = document.getElementById('agent-download-url-linux');
    const agentDownloadURLMac = document.getElementById('agent-download-url-mac');
    const clientDownloadURLWindows = document.getElementById('client-download-url-windows');
    const clientDownloadURLLinux = document.getElementById('client-download-url-linux');
    const clientDownloadURLMac = document.getElementById('client-download-url-mac');
    if (currentUser) currentUser.textContent = text;
    if (agentListUser) agentListUser.textContent = text;
    if (userHash) userHash.value = state.currentUser?.user_hash || '';
    const signalURL = getSignalURL() || 'http://127.0.0.1:8443';
    const hash = state.currentUser?.user_hash || '<user_hash>';
    const setDownloadLink = (element, url) => {
        if (!element) return;
        element.href = url;
        element.dataset.url = url;
    };
    if (agentDownloadURLWindows) {
        setDownloadLink(agentDownloadURLWindows, `${signalURL}/download/agent/windows`);
    }
    if (agentDownloadURLLinux) {
        setDownloadLink(agentDownloadURLLinux, `${signalURL}/download/agent/linux`);
    }
    if (agentDownloadURLMac) {
        setDownloadLink(agentDownloadURLMac, `${signalURL}/download/agent/mac`);
    }
    if (clientDownloadURLWindows) {
        setDownloadLink(clientDownloadURLWindows, `${signalURL}/download/client/windows`);
    }
    if (clientDownloadURLLinux) {
        setDownloadLink(clientDownloadURLLinux, `${signalURL}/download/client/linux`);
    }
    if (clientDownloadURLMac) {
        setDownloadLink(clientDownloadURLMac, `${signalURL}/download/client/mac`);
    }
    if (agentStartCommand) {
        agentStartCommand.value = `# Windows\n`
            + `agent.exe -name \"我的客户端\" -owner-hash ${hash} -signal ${signalURL} -terminal -password <local_password>\n`
            + `# Linux / macOS\n`
            + `./agent -name \"我的客户端\" -owner-hash ${hash} -signal ${signalURL} -terminal -password <local_password>\n`
            + `# -name 为该 Agent 的标识（同名即同一 Agent）；agent_id 由服务器自动生成，无需手动指定\n`
            + `# -terminal 启用内嵌远程终端（断线重连自动回放，不重置）；可选 -terminal-shell powershell|cmd|bash|sh\n`
            + `# 直连失败会自动经服务器 TURN 中转（-use-server-turn 默认开）`;
    }
    if (agentPortsJSON) {
        agentPortsJSON.value = JSON.stringify({
            ports: [
                {
                    id: 'http',
                    name: '测试环境',
                    local_addr: '127.0.0.1:8080',
                    protocol: 'tcp',
                    allow_access: true
                },
                {
                    id: 'http2',
                    name: 'CLAUDE-AI',
                    local_addr: '172.18.247.29:8082',
                    protocol: 'tcp',
                    allow_access: true
                },
                {
                    id: 'mysql',
                    name: 'Mysql',
                    local_addr: '192.168.0.152:3306',
                    protocol: 'tcp',
                    allow_access: true
                }
            ]
        }, null, 2);
    }
    if (agentStartCommandWithPorts) {
        agentStartCommandWithPorts.value = `# Windows\n`
            + `agent.exe -name \"我的客户端\" -owner-hash ${hash} -signal ${signalURL} -ports ./ports.json -terminal -password <local_password>\n`
            + `# Linux / macOS\n`
            + `./agent -name \"我的客户端\" -owner-hash ${hash} -signal ${signalURL} -ports ./ports.json -terminal -password <local_password>\n`
            + `# -terminal 启用内嵌远程终端；-ports 指定端口映射配置；直连失败自动经服务器 TURN 中转`;
    }
    if (clientStartCommand) {
        clientStartCommand.value = `# Windows\n`
            + `client.exe -signal ${signalURL} -user-hash ${hash}\n`
            + `# Linux / macOS\n`
            + `./client -signal ${signalURL} -user-hash ${hash}\n`
            + `# -user-hash 即第一层身份（免账户名/密码）；默认进入交互模式：选择 Agent、输入本地密码、选择服务并指定本地端口`;
    }
    updateLoginVisibility();
}

// ==================== Crypto ====================
function fnv1a32(input) {
    let hash = 0x811c9dc5;
    const bytes = new TextEncoder().encode(String(input || ''));
    for (let i = 0; i < bytes.length; i++) {
        hash ^= bytes[i];
        hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    return hash >>> 0;
}

function portableHash256(input) {
    let result = '';
    for (let i = 0; i < 8; i++) {
        result += fnv1a32(`${i}|${input}`).toString(16).padStart(8, '0');
    }
    return result;
}

async function deriveKey(password, id) {
    return portableHash256(`key|${id}|${password}`);
}

// ==================== 账号 ====================
async function sendEmailCode() {
    const signalURL = getSignalURL();
    const email = document.getElementById('register-email')?.value.trim() || '';
    if (!email) {
        alert('请先输入邮箱');
        return;
    }
    try {
        const response = await fetch(`${signalURL}/auth/send-code`, {
            method: 'POST',
            headers: buildJSONHeaders(false),
            body: JSON.stringify({ email })
        });
        const text = await response.text();
        if (!response.ok) {
            throw new Error(text || `发送失败: ${response.status}`);
        }
        showStatus('register-status', '验证码已发送，如未配置 SMTP 请查看 signaling 日志输出的验证码', 'success');
    } catch (err) {
        log(`发送验证码失败: ${err.message}`, 'error');
        showStatus('register-status', `发送验证码失败: ${err.message}`, 'error');
    }
}

async function registerUser() {
    const signalURL = getSignalURL();
    const payload = {
        tenant_code: DEFAULT_TENANT_CODE,
        tenant_name: DEFAULT_TENANT_NAME,
        username: document.getElementById('register-username')?.value.trim() || '',
        email: document.getElementById('register-email')?.value.trim() || '',
        password: document.getElementById('register-password')?.value || '',
        verification_code: document.getElementById('register-code')?.value.trim() || ''
    };
    try {
        const response = await fetch(`${signalURL}/auth/register`, {
            method: 'POST',
            headers: buildJSONHeaders(false),
            body: JSON.stringify(payload)
        });
        const text = await response.text();
        if (!response.ok) {
            throw new Error(text || `注册失败: ${response.status}`);
        }
        showStatus('register-status', '注册成功，请使用用户名和密码登录', 'success');
    } catch (err) {
        log(`注册失败: ${err.message}`, 'error');
        showStatus('register-status', `注册失败: ${err.message}`, 'error');
    }
}

async function loginUser() {
    state.signalURL = window.location.origin;
    state.webConsoleBase = `${window.location.origin}/webconsole`; 
    state.proxyServiceBase = `${window.location.origin}/proxyservice`; 
    const payload = {
        tenant_code: DEFAULT_TENANT_CODE,
        username: document.getElementById('login-username')?.value.trim() || '',
        password: document.getElementById('login-password')?.value || ''
    };
    try {
        const response = await fetch(`${getSignalURL()}/auth/login`, {
            method: 'POST',
            headers: buildJSONHeaders(false),
            body: JSON.stringify(payload)
        });
        const text = await response.text();
        if (!response.ok) {
            throw new Error(text || `登录失败: ${response.status}`);
        }
        const data = JSON.parse(text);
        state.userSessionToken = data.token;
        state.currentUser = data;
        updateCurrentUserUI();
        saveSessionState();
        document.getElementById('intro-card')?.classList.add('hidden');
        document.getElementById('account-card')?.classList.add('hidden');
        document.getElementById('agent-register-card')?.classList.remove('hidden');
        showStatus('login-status', '登录成功', 'success');
        log(`用户已登录: ${data.username}`);
        await listAgents();
    } catch (err) {
        log(`登录失败: ${err.message}`, 'error');
        showStatus('login-status', `登录失败: ${err.message}`, 'error');
    }
}

// ==================== 普通用户：我的中转额度 ====================

// updateMyQuotaVisibility 对所有登录用户显示「我的中转额度」卡片并加载。
function updateMyQuotaVisibility() {
    const loggedIn = !!(state.userSessionToken && state.currentUser);
    const card = document.getElementById('my-quota-card');
    if (!card) return;
    card.classList.toggle('hidden', !loggedIn);
    if (loggedIn) loadMyQuota();
}

async function loadMyQuota() {
    if (!state.userSessionToken) return;
    try {
        const resp = await fetch(`${getSignalURL()}/me/quota`, { headers: buildJSONHeaders(true) });
        if (!resp.ok) return;
        renderMyQuota(await resp.json());
    } catch (err) { /* 忽略 */ }
}

function renderMyQuota(d) {
    const body = document.getElementById('my-quota-body');
    if (!body) return;
    if (!d || !d.turn_enabled) {
        body.textContent = '内嵌 TURN 中转未启用，当前不产生中转流量（直连/P2P 不计额度）。';
        return;
    }
    const used = formatBytesHuman(d.used_bytes);
    const hasCap = d.monthly_quota_bytes > 0;
    const cap = hasCap ? formatBytesHuman(d.monthly_quota_bytes) : '不限';
    const pct = hasCap ? Math.min(100, Math.round((d.used_bytes / d.monthly_quota_bytes) * 100)) : 0;
    const speed = d.max_bps ? (bytesPerSecToMbps(d.max_bps).toFixed(2) + ' Mbps') : '不限';
    const exhausted = d.exhausted ? ' <span style="color:#c00;">（本月额度已用满，中转暂停）</span>' : '';
    const bar = hasCap
        ? `<div style="background:#eee;border-radius:6px;height:10px;margin:6px 0;overflow:hidden;"><div style="height:100%;width:${pct}%;background:${pct >= 100 ? '#c00' : '#4caf50'};"></div></div>`
        : '';
    body.innerHTML = `本月已用：<b>${used}</b> / ${cap}（${d.quota_month}）${exhausted}${bar}每会话限速：<b>${speed}</b>`;
}

// ==================== 管理员：用户管理 ====================

const ADMIN_BYTES_PER_GB = 1024 * 1024 * 1024;
function gbToBytes(gb) { return Math.round((parseFloat(gb) || 0) * ADMIN_BYTES_PER_GB); }
function bytesToGB(b) { return (b || 0) / ADMIN_BYTES_PER_GB; }
function mbpsToBytesPerSec(mbps) { return Math.round((parseFloat(mbps) || 0) * 1000000 / 8); }
function bytesPerSecToMbps(bps) { return (bps || 0) * 8 / 1000000; }
function formatBytesHuman(b) {
    b = b || 0;
    if (b < 1024) return b + ' B';
    const u = ['KB', 'MB', 'GB', 'TB'];
    let n = b / 1024, i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(2) + ' ' + u[i];
}

// updateAdminVisibility 仅对管理员显示「用户管理」面板，并按需加载数据。
function updateAdminVisibility() {
    const isAdmin = !!state.currentUser?.is_admin;
    const card = document.getElementById('admin-user-mgmt-card');
    if (!card) return;
    card.classList.toggle('hidden', !isAdmin);
    if (isAdmin) loadAdminUsers();
}

async function loadAdminUsers() {
    if (!state.currentUser?.is_admin) return;
    try {
        const resp = await fetch(`${getSignalURL()}/admin/users`, { headers: buildJSONHeaders(true) });
        if (!resp.ok) { log(`加载用户列表失败: ${resp.status}`, 'error'); return; }
        const data = await resp.json();
        const note = document.getElementById('admin-turn-note');
        if (note) {
            note.textContent = data.turn_enabled
                ? '内嵌 TURN 中转：已启用，额度将实际生效。'
                : '⚠️ 内嵌 TURN 未启用（启动加 -turn-enabled -turn-public-ip）。额度可设置但暂不会产生中转流量。';
        }
        renderAdminUsers(data.users || []);
    } catch (err) {
        log(`加载用户列表失败: ${err.message}`, 'error');
    }
}

function renderAdminUsers(users) {
    const tb = document.getElementById('admin-users-tbody');
    if (!tb) return;
    const esc = (s) => (s == null ? '' : String(s)).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
    if (!users.length) { tb.innerHTML = '<tr><td colspan="6" style="padding:8px;color:#888;">暂无用户</td></tr>'; return; }
    tb.innerHTML = users.map(u => {
        const gb = u.monthly_quota_bytes ? bytesToGB(u.monthly_quota_bytes).toFixed(2) : '0';
        const mbps = u.max_bps ? bytesPerSecToMbps(u.max_bps).toFixed(2) : '0';
        const used = formatBytesHuman(u.used_bytes);
        const exhausted = u.exhausted ? ' <span style="color:#c00;">(已用满)</span>' : '';
        const adminTag = u.is_admin ? ' 👑' : '';
        const id = esc(u.user_id);
        return `<tr style="border-bottom:1px solid #eee;">
            <td style="padding:6px;">${esc(u.username)}${adminTag}</td>
            <td style="padding:6px;">${esc(u.email)}</td>
            <td style="padding:6px;">${used}${exhausted}</td>
            <td style="padding:6px;"><input type="number" min="0" step="0.1" value="${gb}" id="q-gb-${id}" style="width:90px;"></td>
            <td style="padding:6px;"><input type="number" min="0" step="0.1" value="${mbps}" id="q-mbps-${id}" style="width:90px;"></td>
            <td style="padding:6px;white-space:nowrap;">
                <button class="btn btn-secondary" onclick="saveUserQuota('${id}')">保存</button>
                <button class="btn btn-secondary" onclick="resetUserUsage('${id}')">清零用量</button>
            </td>
        </tr>`;
    }).join('');
}

async function saveUserQuota(userId) {
    const gb = document.getElementById(`q-gb-${userId}`)?.value;
    const mbps = document.getElementById(`q-mbps-${userId}`)?.value;
    try {
        const resp = await fetch(`${getSignalURL()}/admin/users/quota`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({ user_id: userId, monthly_quota_bytes: gbToBytes(gb), max_bps: mbpsToBytesPerSec(mbps) })
        });
        if (!resp.ok) { log(`保存额度失败: ${await resp.text()}`, 'error'); return; }
        log('额度已保存');
        loadAdminUsers();
    } catch (err) {
        log(`保存额度失败: ${err.message}`, 'error');
    }
}

async function resetUserUsage(userId) {
    try {
        const resp = await fetch(`${getSignalURL()}/admin/users/reset-usage`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({ user_id: userId })
        });
        if (!resp.ok) { log(`清零失败: ${await resp.text()}`, 'error'); return; }
        log('用量已清零');
        loadAdminUsers();
    } catch (err) {
        log(`清零失败: ${err.message}`, 'error');
    }
}

async function restoreLoginSession() {
    const saved = loadSessionState();
    if (!saved?.token) {
        updateLoginVisibility();
        resumeBouncedPreviewTarget();
        return false;
    }
    state.signalURL = (saved.signalURL || getSignalURL() || window.location.origin || '').replace(/\/+$/, '');
    state.userSessionToken = saved.token;
    try {
        const response = await fetch(`${getSignalURL()}/auth/me`, {
            headers: buildJSONHeaders(true)
        });
        if (!response.ok) {
            throw new Error(`restore failed: ${response.status}`);
        }
        const data = await response.json();
        state.currentUser = {
            ...saved.currentUser,
            ...data,
            token: state.userSessionToken
        };
        updateCurrentUserUI();
        document.getElementById('intro-card')?.classList.add('hidden');
        document.getElementById('account-card')?.classList.add('hidden');
        document.getElementById('agent-register-card')?.classList.remove('hidden');
        await listAgents();
        resumeBouncedPreviewTarget();
        log(`已恢复登录会话: ${state.currentUser.username}`);
        return true;
    } catch (err) {
        clearSessionState();
        state.userSessionToken = '';
        state.currentUser = null;
        updateLoginVisibility();
        return false;
    }
}

// ==================== Agent列表 ====================
async function listAgents() {
    state.signalURL = window.location.origin;
    state.webConsoleBase = `${window.location.origin}/webconsole`; 
    state.proxyServiceBase = `${window.location.origin}/proxyservice`; 
    if (!state.signalURL) {
        alert('请输入信令服务器URL');
        return;
    }
    if (!state.userSessionToken) {
        alert('请先登录');
        return;
    }

    try {
        log('获取Agent列表...');
        const response = await fetch(`${getSignalURL()}/controller/list`, { headers: buildJSONHeaders(true) });
        
        if (!response.ok) throw new Error(`获取失败: ${response.status}`);
        
        const result = await response.json();
        const agents = Array.isArray(result) ? result : [];
        renderAgentList(agents);
        document.getElementById('agents-card')?.classList.remove('hidden');
        log(`找到 ${agents.length} 个Agent`);
    } catch (err) {
        const message = '获取 Agent 列表失败，请刷新页面后重试；如果问题持续存在，请重新登录。';
        log(`获取Agent列表失败: ${err.message}`, 'error');
        alert(message);
    }
}

function renderAgentList(agents) {
    const list = document.getElementById('agent-list');
    const registerDetails = document.getElementById('agent-register-details');
    if (!list) return;
    list.innerHTML = '';
    state.agentsById = {};
    if (registerDetails) {
        registerDetails.open = agents.length === 0;
    }
    
    if (agents.length === 0) {
        list.innerHTML = '<li class="text-center">当前账户下暂无已连接过的 Agent</li>';
        return;
    }
    
    agents.forEach(agent => {
        state.agentsById[agent.id] = agent;
        const li = document.createElement('li');
        li.className = 'agent-item';
        const onlineClass = agent.online ? 'online' : 'offline';
        const statusText = agent.online ? '在线' : '离线';
        const agentName = escapeHtml(agent.display_name || agent.id);
        const description = agent.description
            ? `<div class="agent-description">${escapeHtml(agent.description)}</div>`
            : '<div class="agent-description">已登记的共享 Agent，可用于 Web 访问或 Client 端口映射。</div>';
        
        li.innerHTML = `
            <div class="agent-info">
                <div class="agent-name">${agentName}</div>
                ${description}
                <div class="agent-status ${onlineClass}">${statusText}</div>
            </div>
            <div class="agent-actions">
                <button class="btn btn-primary" onclick="selectAgent('${escapeHtml(agent.id)}', '${agentName}')">连接</button>
                <button class="btn btn-secondary" onclick="showClientCommands('${escapeHtml(agent.id)}')">📋 命令</button>
                <button class="btn btn-danger" onclick="deleteAgent('${escapeHtml(agent.id)}', '${agentName}')">删除</button>
            </div>
        `;
        list.appendChild(li);
    });
}

// ==================== client 连接命令复制 ====================

function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (e) {}
    document.body.removeChild(ta);
    if (done) done();
}

function copyText(text, btn) {
    const done = () => {
        if (!btn) return;
        const old = btn.textContent;
        btn.textContent = '已复制';
        setTimeout(() => { btn.textContent = old; }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
    } else {
        fallbackCopy(text, done);
    }
}

function cmdRowHTML(label, cmd, idx) {
    return `
        <div style="display:flex;gap:8px;align-items:stretch;margin-bottom:8px;">
            <div style="flex:0 0 92px;font-size:12px;color:#94a3b8;display:flex;align-items:center;">${label}</div>
            <code style="flex:1;background:#0d1117;color:#c9d1d9;padding:9px 11px;border-radius:6px;font-size:12px;white-space:pre-wrap;word-break:break-all;font-family:Consolas,monospace;">${escapeHtml(cmd)}</code>
            <button class="btn btn-secondary" data-cmd-idx="${idx}" style="flex:0 0 auto;align-self:center;">复制</button>
        </div>`;
}

// showClientCommands 弹出某 agent 的 client 连接命令（agent_id 自动填好，密码留占位）。
function showClientCommands(agentId) {
    const agent = state.agentsById[agentId];
    if (!agent) return;
    const url = getSignalURL();
    const hash = (state.currentUser && state.currentUser.user_hash) || '<USER_HASH>';
    const id = agent.id;
    const name = agent.display_name || agent.id;
    // -user-hash 做第一层身份（免账户名/密码）；密码参数放最后，方便填。
    const cmd = (exe, suffix) => `${exe} -signal ${url} -user-hash ${hash} -agent ${id}${suffix} -agent-password <AGENT_PASSWORD>`;
    const variants = [
        { title: '交互连接（连接后交互选择服务并指定本地端口）', suffix: '' },
        { title: '远程终端', suffix: ' -term' },
        { title: '端口映射示例（本地 18080 → 远端 http 服务）', suffix: ' -map 127.0.0.1:18080=http' },
    ];
    const commands = [];
    let sectionsHTML = '';
    variants.forEach(v => {
        const win = cmd('client.exe', v.suffix);
        const nix = cmd('./client', v.suffix);
        const iWin = commands.push({ cmd: win }) - 1;
        const iNix = commands.push({ cmd: nix }) - 1;
        sectionsHTML += `
            <div style="margin-bottom:16px;">
                <div style="font-weight:600;font-size:14px;color:#334155;margin-bottom:8px;">${escapeHtml(v.title)}</div>
                ${cmdRowHTML('Windows', win, iWin)}
                ${cmdRowHTML('Linux/macOS', nix, iNix)}
            </div>`;
    });
    const overlay = document.createElement('div');
    overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,.5);display:flex;align-items:center;justify-content:center;z-index:10000;padding:20px;';
    overlay.innerHTML = `
        <div style="background:#fff;border-radius:10px;max-width:780px;width:100%;max-height:85vh;overflow:auto;padding:22px;box-shadow:0 20px 50px rgba(0,0,0,.3);">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px;">
                <h2 style="font-size:18px;color:#333;">复制 client 连接命令</h2>
                <button class="btn btn-secondary" data-close="1" style="padding:4px 12px;">✕</button>
            </div>
            <div style="font-size:13px;color:#475569;margin-bottom:4px;">Agent：<b>${escapeHtml(name)}</b></div>
            <p style="font-size:12.5px;color:#64748b;margin-bottom:16px;line-height:1.6;">
                agent_id 与 user-hash 已自动填好（user-hash 即第一层身份，免账户名/密码）；只需把<b>末尾</b>的 <code>&lt;AGENT_PASSWORD&gt;</code>（Agent 本地密码，服务器不存、必须手填）替换为真实值即可。也可不带 <code>-agent</code> 直接运行 client 交互选择。
            </p>
            ${sectionsHTML}
        </div>`;
    document.body.appendChild(overlay);
    const close = () => overlay.remove();
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });
    const closeBtn = overlay.querySelector('[data-close="1"]');
    if (closeBtn) closeBtn.onclick = close;
    overlay.querySelectorAll('[data-cmd-idx]').forEach(btn => {
        btn.onclick = () => copyText(commands[parseInt(btn.getAttribute('data-cmd-idx'), 10)].cmd, btn);
    });
}

function selectAgent(id, displayName) {
    state.agentID = id;
    state.selectedAgentName = displayName || id;
    state.selectedAgent = state.agentsById[id] || null;
    const modalText = document.getElementById('connect-modal-agent');
    const passwordInput = document.getElementById('agent-password');
    const status = document.getElementById('password-status');
    if (modalText) {
        modalText.textContent = state.selectedAgentName;
    }
    if (passwordInput) {
        passwordInput.value = '';
        setTimeout(() => passwordInput.focus(), 0);
    }
    if (status) {
        status.className = 'hidden';
        status.textContent = '';
    }
    log(`已选择Agent: ${id}`);
    document.getElementById('connect-modal')?.classList.remove('hidden');
}

function closeConnectModal() {
    document.getElementById('connect-modal')?.classList.add('hidden');
}

function openChangePasswordModal() {
    document.getElementById('change-password-old').value = '';
    document.getElementById('change-password-new').value = '';
    document.getElementById('change-password-confirm').value = '';
    const status = document.getElementById('change-password-status');
    if (status) {
        status.className = 'hidden';
        status.textContent = '';
    }
    document.getElementById('change-password-modal')?.classList.remove('hidden');
}

function closeChangePasswordModal() {
    document.getElementById('change-password-modal')?.classList.add('hidden');
}

async function changePassword() {
    const oldPassword = document.getElementById('change-password-old')?.value || '';
    const newPassword = document.getElementById('change-password-new')?.value || '';
    const confirmPassword = document.getElementById('change-password-confirm')?.value || '';
    if (!oldPassword || !newPassword) {
        showStatus('change-password-status', '请填写完整密码信息', 'error');
        return;
    }
    if (newPassword !== confirmPassword) {
        showStatus('change-password-status', '两次输入的新密码不一致', 'error');
        return;
    }
    try {
        const response = await fetch(`${getSignalURL()}/auth/change-password`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({
                old_password: oldPassword,
                new_password: newPassword
            })
        });
        const text = await response.text();
        if (!response.ok) {
            throw new Error(text || `修改失败: ${response.status}`);
        }
        showStatus('change-password-status', '密码修改成功', 'success');
        setTimeout(() => closeChangePasswordModal(), 500);
    } catch (err) {
        log(`修改密码失败: ${err.message}`, 'error');
        showStatus('change-password-status', `修改密码失败: ${err.message}`, 'error');
    }
}

async function deleteAgent(id, displayName) {
    const name = displayName || id;
    if (!window.confirm(`确认删除 Agent "${name}" 吗？在线 Agent 会被立即断开。`)) {
        return;
    }
    try {
        const response = await fetch(`${getSignalURL()}/controller/agent/delete`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({ agent_id: id })
        });
        const text = await response.text();
        if (!response.ok) {
            throw new Error(text || `删除失败: ${response.status}`);
        }
        log(`已删除 Agent: ${id}`);
        if (state.agentID === id) {
            disconnect();
        }
        await listAgents();
    } catch (err) {
        log(`删除 Agent 失败: ${err.message}`, 'error');
        alert(`删除 Agent 失败: ${err.message}`);
    }
}

// ==================== WebRTC连接 ====================
async function connect() {
    // 去重守卫：握手未完成前忽略重复点击，避免发出第二个 offer 把已建立的会话拆掉。
    if (state.connecting) {
        log('正在连接中，请勿重复点击');
        return;
    }
    resetConnectionState();
    state.connecting = true;

    state.signalURL = window.location.origin;
    state.webConsoleBase = `${window.location.origin}/webconsole`; 
    state.proxyServiceBase = `${window.location.origin}/proxyservice`; 
    const passwordInput = document.getElementById('agent-password');
    state.agentPassword = passwordInput ? passwordInput.value.replace(/^\s+|\s+$/g, '') : '';
    
    if (!state.signalURL || !state.userSessionToken || !state.agentID || !state.agentPassword) {
        alert('请填写所有必填项');
        return;
    }
    
    try {
        log('连接到信令服务器...');

        const connectRes = await claimWebAgentControl(false);
        
        if (!connectRes.ok) throw new Error(`连接失败: ${connectRes.status}`);
        
        log('已连接到Agent，建立WebRTC...');
        
        await createPeerConnection();
        createDataChannel();
        
        const offer = await state.pc.createOffer();
        await state.pc.setLocalDescription(offer);
        await waitForIceGathering();
        
        await fetch(`${getSignalURL()}/controller/send?agent_id=${state.agentID}`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({ type: 'offer', sdp: state.pc.localDescription })
        });
        
        startSignalingPoll();
        showStatus('password-status', 'WebRTC连接建立中...', 'info');
        
    } catch (err) {
        state.connecting = false;
        log(`连接失败: ${err.message}`, 'error');
        showStatus('password-status', `连接失败: ${err.message}`, 'error');
    }
}

async function claimWebAgentControl(force) {
    const response = await fetch(`${getSignalURL()}/controller/connect`, {
        method: 'POST',
        headers: buildJSONHeaders(true),
        body: JSON.stringify({ agent_id: state.agentID, force: !!force })
    });
    if (response.status === 409) {
        let busy = null;
        try {
            busy = await response.json();
        } catch (e) {}
        const controllerUser = busy?.controller_user || '其他用户';
        const controllerKind = busy?.controller_kind || '未知会话';
        const ok = window.confirm(`当前 Agent 正由 ${controllerUser} (${controllerKind}) 使用，是否强行断开之前的会话？`);
        if (!ok) {
            throw new Error('Agent 正在被使用');
        }
        const forced = await claimWebAgentControl(true);
        await new Promise((resolve) => setTimeout(resolve, 500));
        return forced;
    }
    return response;
}

async function createPeerConnection() {
    let iceServers = [
        { urls: 'stun:stun.l.google.com:19302' },
        { urls: 'stun:stun1.l.google.com:19302' }
    ];
    if (state.selectedAgent && Array.isArray(state.selectedAgent.ice_servers) && state.selectedAgent.ice_servers.length > 0) {
        iceServers = state.selectedAgent.ice_servers.map((server) => {
            const urls = Array.isArray(server.urls)
                ? server.urls.map((item) => decodeBase64Utf8(item)).filter(Boolean)
                : [];
            return {
                urls,
                username: server.username ? decodeBase64Utf8(server.username) || undefined : undefined,
                credential: server.credential ? decodeBase64Utf8(server.credential) || undefined : undefined
            };
        }).filter((server) => Array.isArray(server.urls) && server.urls.length > 0);
        log(`使用 Agent 提供的 ICE 配置 (${iceServers.length})`);
    }
    const config = { iceServers };

    state.pc = new RTCPeerConnection(config);
    // 远端描述就绪前到达的候选先缓冲，待 setRemoteDescription 后再灌入，
    // 否则较晚到达的 relay 候选会被 addIceCandidate 直接拒绝，导致无法回退到 TURN 中转。
    state.remoteDescSet = false;
    state.pendingRemoteCandidates = [];

    state.pc.onicecandidate = (event) => {
        if (event.candidate) {
            fetch(`${getSignalURL()}/controller/send?agent_id=${state.agentID}`, {
                method: 'POST',
                headers: buildJSONHeaders(true),
                body: JSON.stringify({ type: 'candidate', candidate: event.candidate })
            }).catch(err => log(`发送ICE候选失败: ${err.message}`, 'error'));
        }
    };
    
    state.pc.onconnectionstatechange = () => {
        log(`连接状态: ${state.pc.connectionState}`);
        if (state.pc.connectionState === 'connected') {
            showStatus('password-status', 'WebRTC已连接，等待鉴权...', 'success');
        } else if (state.pc.connectionState === 'disconnected') {
            // disconnected 是可恢复的瞬态（relay 切换/短暂丢包都会经过它），
            // 不立刻拆连接，给 ICE 自愈或回退到 TURN 中转的机会；真失败会进入 failed。
            showStatus('password-status', 'WebRTC连接中断，尝试恢复中...', 'error');
        } else if (state.pc.connectionState === 'failed' || state.pc.connectionState === 'closed') {
            showStatus('password-status', 'WebRTC连接断开', 'error');
            disconnect();
        }
    };
}

function createDataChannel() {
    state.dataChannel = state.pc.createDataChannel('portmap', { ordered: true });
    
    state.dataChannel.onopen = () => {
        log('DataChannel已打开，开始鉴权...');
        startAuthentication();
    };
    
    state.dataChannel.onclose = () => {
        log('DataChannel已关闭');
    };
    
    state.dataChannel.onmessage = (event) => {
        let data = event.data;
        if (data instanceof ArrayBuffer) {
            data = new TextDecoder().decode(data);
        }
        handleDataChannelMessage(data);
    };
}

async function waitForIceGathering() {
    return new Promise((resolve) => {
        if (state.pc.iceGatheringState === 'complete') {
            resolve();
            return;
        }
        const checkState = () => {
            if (state.pc.iceGatheringState === 'complete') {
                state.pc.removeEventListener('icegatheringstatechange', checkState);
                resolve();
            }
        };
        state.pc.addEventListener('icegatheringstatechange', checkState);
        setTimeout(() => {
            state.pc.removeEventListener('icegatheringstatechange', checkState);
            resolve();
        }, 5000);
    });
}

async function startSignalingPoll() {
    const poll = async () => {
        if (!state.pc || state.pc.connectionState === 'closed') return;
        
        try {
            const response = await fetch(`${getSignalURL()}/controller/poll?agent_id=${state.agentID}`, { headers: buildJSONHeaders(true) });
            
            if (response.status === 200) {
                const msg = await response.json();
                if (msg.type === 'answer' && msg.sdp) {
                    await state.pc.setRemoteDescription(new RTCSessionDescription(msg.sdp));
                    state.remoteDescSet = true;
                    // 灌入此前缓冲的候选（含较晚到达的 relay 候选）。
                    const pending = state.pendingRemoteCandidates || [];
                    state.pendingRemoteCandidates = [];
                    for (const cand of pending) {
                        try {
                            await state.pc.addIceCandidate(new RTCIceCandidate(cand));
                        } catch (e) {
                            log(`灌入缓冲ICE候选失败: ${e.message}`, 'error');
                        }
                    }
                    if (pending.length > 0) log(`已灌入 ${pending.length} 个缓冲ICE候选`);
                } else if (msg.type === 'candidate' && msg.candidate) {
                    if (!state.remoteDescSet) {
                        // 远端描述尚未就绪，先缓冲。
                        (state.pendingRemoteCandidates = state.pendingRemoteCandidates || []).push(msg.candidate);
                    } else {
                        await state.pc.addIceCandidate(new RTCIceCandidate(msg.candidate));
                    }
                }
            }
        } catch (err) {}
        
        if (state.pc && state.pc.connectionState !== 'closed') {
            setTimeout(poll, 100);
        }
    };
    poll();
}

// ==================== 鉴权 ====================
let authStarted = false;

async function startAuthentication() {
    if (authStarted) {
        log('鉴权已启动，忽略重复请求');
        return;
    }
    authStarted = true;
    
    log('派生密钥...');
    try {
        state.derivedKey = await deriveKey(state.agentPassword, state.agentID);
        log('密钥派生完成');
    } catch (err) {
        log(`密钥派生失败: ${err.name || 'Error'} ${err.message || err}`, 'error');
        authStarted = false;
        return;
    }
    
    const challenge = generateChallenge();
    const timestamp = Date.now();
    state.authChallenge = { challenge, timestamp };
    
    log('发送鉴权挑战...');
    sendProtocolMessage({
        type: 1,
        payload: { challenge, timestamp }
    });
}

function generateChallenge() {
    const array = new Uint8Array(32);
    crypto.getRandomValues(array);
    return btoa(String.fromCharCode(...array));
}

async function handleDataChannelMessage(data) {
    try {
        const msg = JSON.parse(data);
        
        if (msg.type === 2) {
            const payload = msg.payload;
            
            let expectedResponse;
            try {
                expectedResponse = await computeAuthResponse();
            } catch (err) {
                log(`计算响应失败: ${err.message}`, 'error');
                return;
            }
            
            if (payload.response === expectedResponse) {
                sendProtocolMessage({
                    type: 3,
                    payload: { success: true, message: 'Authentication successful' }
                });
                
                state.authenticated = true;
                state.connecting = false;
                log('鉴权成功！');
                showStatus('password-status', '已连接并鉴权成功', 'success');
                document.body.classList.add('connected-mode');
                
                document.getElementById('config-card')?.classList.add('hidden');
                document.getElementById('agents-card')?.classList.add('hidden');
                closeConnectModal();
                document.getElementById('http-console-card')?.classList.remove('hidden');
            } else {
                log('鉴权失败: 响应不匹配', 'error');
                showStatus('password-status', '鉴权失败: 密码错误', 'error');
            }
        } else if (msg.type === 14) {
            log(`收到Agent配置消息`);
            state.agentConfig = msg.payload;
            if (msg.payload && msg.payload.ports) {
                log(`Agent有 ${msg.payload.ports.length} 个端口配置`);
                renderPortButtons(msg.payload.ports);
            }
            applyTerminalCapability(msg.payload && msg.payload.terminal);
        } else if (msg.type === 18) { // MsgTypeHTTPResponse
            handleHTTPResponse(msg.payload);
        } else if (msg.type === 20) { // MsgTypeWSOpenAck
            handleWSOpenAck(msg.payload);
        } else if (msg.type === 21) { // MsgTypeWSData
            handleWSData(msg.payload);
        } else if (msg.type === 22) { // MsgTypeWSClose
            handleWSClose(msg.payload);
        } else if (msg.type === 23) { // MsgTypeWSError
            handleWSError(msg.payload);
        } else if (msg.type === 25) { // MsgTypeTermData
            handleTermData(msg.payload);
        } else if (msg.type === 28) { // MsgTypeTermExit
            handleTermExit(msg.payload);
        }
    } catch (err) {
        log(`处理消息失败: ${err.message}`, 'error');
    }
}

async function computeAuthResponse() {
    return portableHash256(`resp|${state.derivedKey}|${state.authChallenge.challenge}|${state.authChallenge.timestamp}`);
}

function sendProtocolMessage(msg) {
    if (state.dataChannel && state.dataChannel.readyState === 'open') {
        state.dataChannel.send(JSON.stringify(msg));
    }
}

// ==================== HTTP Web 控制台 ====================
function renderPortButtons(ports) {
    const container = document.getElementById('port-buttons');
    if (!container) return;
    container.innerHTML = '';

    const httpPorts = ports.filter(port => {
        if (!port.allow_access) return false;
        return port.local_addr.includes(':80') ||
            port.local_addr.includes(':8080') ||
            port.local_addr.includes(':443') ||
            String(port.id || '').toLowerCase().includes('http');
    });

    if (httpPorts.length === 0) {
        container.innerHTML = '<span style="color: #999;">当前Agent没有开放HTTP/HTTPS端口</span>';
        return;
    }

    const selector = document.getElementById('http-port');
    if (selector) {
        selector.innerHTML = '';
        httpPorts.forEach(port => {
            const option = document.createElement('option');
            option.value = port.id;
            option.textContent = `${port.name} (${port.local_addr})`;
            selector.appendChild(option);
        });
        state.selectedHTTPPort = selector.value || httpPorts[0].id;
        selector.value = state.selectedHTTPPort;
    }

    httpPorts.forEach(port => {
        
        const btn = document.createElement('button');
        btn.className = 'btn btn-secondary';
        btn.style.margin = '5px';
        btn.textContent = `${port.name} (${port.local_addr})`;
        btn.onclick = () => {
            const pathInput = document.getElementById('http-path');
            const selector = document.getElementById('http-port');
            const previewSelector = document.getElementById('preview-modal-service');
            state.selectedHTTPPort = port.id;
            if (selector) {
                selector.value = port.id;
            }
            if (previewSelector) {
                previewSelector.value = port.id;
            }
            if (pathInput && !pathInput.value.trim()) {
                pathInput.value = '/';
            }
            log(`已选择服务 ${port.name} (${port.id} -> ${port.local_addr})`);
        };
        container.appendChild(btn);
    });
}

async function fetchDc(portID, path, options = {}) {
    const method = (options.method || 'GET').toUpperCase();
    const headers = { ...(options.headers || {}) };
    const bodyText = options.body || '';
    const requestId = generateRequestId();
    const cookieHeader = buildCookieHeader(portID, path);
    if (cookieHeader && !headers.Cookie && !headers.cookie) {
        headers.Cookie = cookieHeader;
    }

    const responsePromise = new Promise((resolve, reject) => {
        state.pendingHTTPRequests.set(requestId, { resolve, reject });
        setTimeout(() => {
            if (state.pendingHTTPRequests.has(requestId)) {
                state.pendingHTTPRequests.delete(requestId);
                reject(new Error('请求超时'));
            }
        }, options.timeout || 30000);
    });

    sendProtocolMessage({
        type: 17,
        payload: {
            id: requestId,
            port_id: portID,
            method,
            path,
            headers,
            body: utf8ToBase64(bodyText)
        }
    });

    return await responsePromise;
}

async function fetchDcText(portID, path, options = {}) {
    const response = await fetchDc(portID, path, options);
    return {
        ...response,
        bodyText: response.body ? base64ToUtf8(response.body) : ''
    };
}

async function sendHTTPRequest() {
    const portInput = document.getElementById('http-port');
    const pathInput = document.getElementById('http-path');
    const methodInput = document.getElementById('http-method');
    const headersInput = document.getElementById('http-headers');
    const bodyInput = document.getElementById('http-body');

    const portID = (portInput && portInput.value) || state.selectedHTTPPort;
    if (!portID) {
        alert('请选择服务端口');
        return;
    }

    const path = normalizeHTTPPath(pathInput ? pathInput.value : '/');
    const method = methodInput ? methodInput.value : 'GET';
    const headers = {};
    if (headersInput && headersInput.value) {
        headersInput.value.split('\n').forEach(line => {
            const [key, ...value] = line.split(':');
            if (key && value.length > 0) {
                headers[key.trim()] = value.join(':').trim();
            }
        });
    }

    log(`[Web→Agent] 通过 DataChannel 发送 HTTP ${method} [${portID}] ${path}`);

    try {
        const response = await fetchDc(portID, path, {
            method,
            headers,
            body: bodyInput ? bodyInput.value : ''
        });
        displayHTTPResponse(response, { portID, path });
    } catch (err) {
        log(`请求失败: ${err.message}`, 'error');
        displayHTTPResponse({ error: err.message, status_code: 0, body: utf8ToBase64(err.message) }, { portID, path });
    }
}

function resetConnectionState() {
    state.connecting = false;
    if (state.dataChannel) {
        try { state.dataChannel.close(); } catch (e) {}
    }
    if (state.pc) {
        try { state.pc.close(); } catch (e) {}
    }
    state.pc = null;
    state.dataChannel = null;
    state.authenticated = false;
    state.agentConfig = null;
    state.derivedKey = null;
    state.selectedHTTPPort = null;
    state.currentPreview = null;
    state.previewHistory = [];
    state.previewHistoryIndex = -1;
    state.pendingHTTPRequests.clear();
    authStarted = false;
    closePreviewModal();
}

function updatePreviewNavUI() {
    const backBtn = document.getElementById('preview-back-btn');
    const forwardBtn = document.getElementById('preview-forward-btn');
    if (backBtn) backBtn.disabled = state.previewHistoryIndex <= 0;
    if (forwardBtn) forwardBtn.disabled = state.previewHistoryIndex < 0 || state.previewHistoryIndex >= state.previewHistory.length - 1;
}

function populatePreviewServiceSelector(selectedPortID) {
    const selector = document.getElementById('preview-modal-service');
    if (!selector) return;
    const mainSelector = document.getElementById('http-port');
    selector.innerHTML = '';
    if (mainSelector && mainSelector.options.length > 0) {
        Array.from(mainSelector.options).forEach((option) => {
            if (!option.value) return;
            const cloned = document.createElement('option');
            cloned.value = option.value;
            cloned.textContent = option.textContent;
            selector.appendChild(cloned);
        });
    }
    selector.value = selectedPortID || state.currentPreview?.portID || state.selectedHTTPPort || '';
    selector.onchange = () => {
        const pathInput = document.getElementById('preview-modal-path');
        const nextPortID = selector.value;
        const path = normalizeHTTPPath(pathInput?.value || state.currentPreview?.path || '/');
        if (!nextPortID) return;
        state.selectedHTTPPort = nextPortID;
        navigatePreviewTo(nextPortID, path);
    };
}

function recordPreviewHistory(meta) {
    if (!meta || !meta.portID || !meta.path) return;
    const normalized = { portID: meta.portID, path: normalizeHTTPPath(meta.path) };
    const current = state.previewHistory[state.previewHistoryIndex];
    if (current && current.portID === normalized.portID && current.path === normalized.path) {
        updatePreviewNavUI();
        return;
    }
    state.previewHistory = state.previewHistory.slice(0, state.previewHistoryIndex + 1);
    state.previewHistory.push(normalized);
    state.previewHistoryIndex = state.previewHistory.length - 1;
    updatePreviewNavUI();
}

function openPreviewModal(content, requestMeta = null) {
    const modal = document.getElementById('preview-modal');
    const title = document.getElementById('preview-modal-title');
    const frame = document.getElementById('preview-modal-frame');
    const pathInput = document.getElementById('preview-modal-path');
    if (!modal || !frame) return;
    if (title) {
        const port = requestMeta?.portID || state.currentPreview?.portID || '';
        title.textContent = port ? `服务: ${port}` : 'HTML 预览';
    }
    if (pathInput) {
        pathInput.value = requestMeta?.path || state.currentPreview?.path || '/';
        pathInput.onkeydown = (event) => {
            if (event.key === 'Enter') {
                event.preventDefault();
                navigatePreviewModal();
            }
        };
    }
    populatePreviewServiceSelector(requestMeta?.portID || state.currentPreview?.portID || state.selectedHTTPPort);
    frame.srcdoc = content || '';
    modal.classList.remove('hidden');
    updatePreviewNavUI();
}

function closePreviewModal() {
    const modal = document.getElementById('preview-modal');
    const frame = document.getElementById('preview-modal-frame');
    if (frame) frame.srcdoc = '';
    if (modal) modal.classList.add('hidden');
}

async function navigatePreviewTo(portID, path) {
    if (!portID) {
        log('当前没有可用的预览上下文', 'warn');
        return;
    }
    try {
        const response = await fetchDc(portID, path, { method: 'GET' });
        displayHTTPResponse(response, { portID, path });
    } catch (err) {
        log(`预览跳转失败: ${err.message}`, 'error');
    }
}

function resumeBouncedPreviewTarget() {
    try {
        const currentUrl = new URL(window.location.href);
        const queryBounce = currentUrl.searchParams.get('bounce');
        const bouncedTarget = queryBounce || sessionStorage.getItem('webrtc_portmap_bounce_target');
        if (!bouncedTarget) {
            return;
        }
        sessionStorage.removeItem('webrtc_portmap_bounce_target');
        if (queryBounce) {
            currentUrl.searchParams.delete('bounce');
            window.history.replaceState({}, '', currentUrl.pathname + (currentUrl.search ? currentUrl.search : '') + (currentUrl.hash ? currentUrl.hash : ''));
        }
        if (!state.selectedHTTPPort) {
            log(`检测到页面跳转回流，但当前没有选中的服务，已忽略: ${bouncedTarget}`, 'warn');
            return;
        }
        log(`恢复跳转页面: ${bouncedTarget}`);
        navigatePreviewTo(state.selectedHTTPPort, bouncedTarget);
    } catch (err) {
        console.warn('resume bounced preview target failed', err);
    }
}

async function navigatePreviewModal() {
    const pathInput = document.getElementById('preview-modal-path');
    const serviceSelector = document.getElementById('preview-modal-service');
    const portID = serviceSelector?.value || state.currentPreview?.portID;
    const path = normalizeHTTPPath(pathInput?.value || '/');
    navigatePreviewTo(portID, path);
}

async function navigatePreviewHistory(delta) {
    const nextIndex = state.previewHistoryIndex + delta;
    if (nextIndex < 0 || nextIndex >= state.previewHistory.length) {
        return;
    }
    const target = state.previewHistory[nextIndex];
    if (!target) return;
    state.previewHistoryIndex = nextIndex;
    updatePreviewNavUI();
    try {
        const response = await fetchDc(target.portID, target.path, { method: 'GET' });
        displayHTTPResponse(response, { portID: target.portID, path: target.path }, false);
    } catch (err) {
        log(`历史跳转失败: ${err.message}`, 'error');
    }
}

function generateRequestId() {
    return Math.random().toString(36).substring(2) + Date.now().toString(36);
}

function handleHTTPResponse(payload) {
    const requestId = payload.id;
    const pending = state.pendingHTTPRequests.get(requestId);
    if (!pending) return;

    if (!payload.total_chunks || payload.total_chunks <= 1) {
        storeResponseCookies(payload);
        state.pendingHTTPRequests.delete(requestId);
        pending.resolve(payload);
        return;
    }

    if (!pending.chunks) {
        pending.chunks = new Array(payload.total_chunks);
        pending.responseMeta = {
            id: payload.id,
            status_code: payload.status_code,
            headers: payload.headers || {},
            error: payload.error || ''
        };
    }

    pending.chunks[payload.chunk_index || 0] = payload.body || '';
    if (payload.headers && Object.keys(payload.headers).length > 0) {
        pending.responseMeta.headers = payload.headers;
    }
    if (payload.error) {
        pending.responseMeta.error = payload.error;
    }

    if (payload.done) {
        const finalPayload = {
            ...pending.responseMeta,
            body: pending.chunks.join('')
        };
        storeResponseCookies(finalPayload);
        state.pendingHTTPRequests.delete(requestId);
        pending.resolve(finalPayload);
    }
}

function handleWSOpenAck(payload) {
    const ws = state.wsConnections.get(payload.socket_id);
    if (!ws) return;
    if (payload.success) {
        log(`[DC-WS] 打开成功 socket=${payload.socket_id}`);
        ws.__setOpen();
        return;
    }
    log(`[DC-WS] 打开失败 socket=${payload.socket_id}: ${payload.error || 'unknown error'}`, 'error');
    ws.__emitError(payload.error || 'WebSocket open failed');
    ws.__setClosed(1006, payload.error || 'WebSocket open failed');
}

function handleWSData(payload) {
    const ws = state.wsConnections.get(payload.socket_id);
    if (!ws) return;
    log(`[DC-WS] 收到消息 socket=${payload.socket_id}, text=${!!payload.text}, size=${payload.data ? payload.data.length : 0}`);
    if (payload.text) {
        ws.__emitMessage(base64ToUtf8(payload.data || ''));
        return;
    }
    const bytes = base64ToBytes(payload.data || '');
    if (ws.binaryType === 'arraybuffer') {
        ws.__emitMessage(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
        return;
    }
    ws.__emitMessage(new Blob([bytes]));
}

function handleWSClose(payload) {
    const ws = state.wsConnections.get(payload.socket_id);
    if (!ws) return;
    log(`[DC-WS] 连接关闭 socket=${payload.socket_id}, code=${payload.code || 1000}, reason=${payload.reason || ''}`);
    ws.__setClosed(payload.code || 1000, payload.reason || '');
}

function handleWSError(payload) {
    const ws = state.wsConnections.get(payload.socket_id);
    if (!ws) return;
    log(`[DC-WS] 连接错误 socket=${payload.socket_id}: ${payload.error || 'unknown error'}`, 'error');
    ws.__emitError(payload.error || 'WebSocket error');
}

async function displayHTTPResponse(response, requestMeta = null, pushHistory = true) {
    const statusEl = document.getElementById('http-response-status');
    const headersEl = document.getElementById('http-response-headers');
    const bodyEl = document.getElementById('http-response-body');
    if (requestMeta && requestMeta.portID && requestMeta.path) {
        state.currentPreview = requestMeta;
        if (pushHistory) {
            recordPreviewHistory(requestMeta);
        } else {
            updatePreviewNavUI();
        }
    }
    
    if (statusEl) {
        if (response.error) {
            statusEl.textContent = `Error: ${response.error}`;
            statusEl.style.color = 'red';
        } else {
            statusEl.textContent = `Status: ${response.status_code}`;
            statusEl.style.color = response.status_code >= 200 && response.status_code < 300 ? 'green' : 'orange';
        }
    }
    
    if (headersEl) {
        if (response.headers) {
            headersEl.textContent = Object.entries(response.headers)
                .map(([k, v]) => `${k}: ${v}`)
                .join('\n');
        } else {
            headersEl.textContent = '';
        }
    }
    
    if (bodyEl) {
        const bodyStr = response.body ? base64ToUtf8(response.body) : '';
        bodyEl.textContent = bodyStr.substring(0, 10000); // 限制显示长度
    }
    
    // 尝试在 iframe 中预览 HTML
    if (response.body && response.status_code === 200) {
        const bodyStr = base64ToUtf8(response.body);
        if (bodyStr.includes('<!DOCTYPE') || bodyStr.includes('<html')) {
            const prepared = await preparePreviewHTML(bodyStr, state.currentPreview);
            openPreviewModal(prepared, state.currentPreview);
        } else {
            closePreviewModal();
        }
    } else {
        closePreviewModal();
    }
    
    log(`收到响应: ${response.status_code || 'Error'}`);
}

function normalizeHTTPPath(input) {
    const value = (input || '').trim();
    if (!value) return '/';
    if (value.startsWith('/')) return value;
    return `/${value}`;
}

function applyPreviewPathRewriteRules(path) {
    let result = normalizeHTTPPath(path || '/');
    for (const rule of PREVIEW_PATH_REWRITE_RULES) {
        try {
            if (rule && rule.pattern && rule.pattern.test(result)) {
                result = result.replace(rule.pattern, rule.replace || '');
            }
        } catch (err) {
            console.warn('preview path rewrite failed', rule && rule.name, err);
        }
    }
    return result;
}

function utf8ToBase64(text) {
    return btoa(unescape(encodeURIComponent(text || '')));
}

function base64ToUtf8(base64) {
    if (!base64) return '';
    return decodeURIComponent(escape(atob(base64)));
}

function base64ToBytes(base64) {
    if (!base64) return new Uint8Array(0);
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i);
    }
    return bytes;
}

function bytesToBase64(input) {
    const bytes = input instanceof Uint8Array ? input : new Uint8Array(input);
    let binary = '';
    for (let i = 0; i < bytes.length; i++) {
        binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary);
}

function escapeJsString(text) {
    return String(text || '')
        .replace(/\\/g, '\\\\')
        .replace(/'/g, "\\'")
        .replace(/\r/g, '\\r')
        .replace(/\n/g, '\\n');
}

function getHeaderIgnoreCase(headers, name) {
    if (!headers) return '';
    const target = String(name || '').toLowerCase();
    for (const [key, value] of Object.entries(headers)) {
        if (String(key).toLowerCase() === target) {
            return value;
        }
    }
    return '';
}

function getPortCookieStore(portID) {
    if (!state.cookieJar.has(portID)) {
        state.cookieJar.set(portID, new Map());
    }
    return state.cookieJar.get(portID);
}

function splitSetCookieHeader(value) {
    if (!value) return [];
    const normalized = String(value).replace(/\r\n/g, '\n');
    const lines = normalized.split('\n').filter(Boolean);
    if (lines.length > 1) return lines;

    const parts = [];
    let current = '';
    let inExpires = false;
    for (let i = 0; i < normalized.length; i++) {
        const ch = normalized[i];
        if (normalized.slice(i, i + 8).toLowerCase() === 'expires=') {
            inExpires = true;
        }
        if (ch === ',') {
            if (inExpires) {
                current += ch;
                continue;
            }
            parts.push(current.trim());
            current = '';
            continue;
        }
        if (ch === ';') {
            inExpires = false;
        }
        current += ch;
    }
    if (current.trim()) {
        parts.push(current.trim());
    }
    return parts.filter(Boolean);
}

function parseSetCookie(line) {
    const segments = String(line || '').split(';').map(s => s.trim()).filter(Boolean);
    if (segments.length === 0) return null;
    const [nameValue, ...attrs] = segments;
    const idx = nameValue.indexOf('=');
    if (idx <= 0) return null;
    const cookie = {
        name: nameValue.slice(0, idx).trim(),
        value: nameValue.slice(idx + 1),
        path: '/',
        secure: false,
        expiresAt: null
    };
    attrs.forEach(attr => {
        const [keyRaw, ...valueRaw] = attr.split('=');
        const key = keyRaw.trim().toLowerCase();
        const value = valueRaw.join('=').trim();
        if (key === 'path' && value) cookie.path = value;
        if (key === 'secure') cookie.secure = true;
        if (key === 'max-age') {
            const seconds = parseInt(value, 10);
            if (!Number.isNaN(seconds)) cookie.expiresAt = Date.now() + seconds * 1000;
        }
        if (key === 'expires' && value) {
            const ts = Date.parse(value);
            if (!Number.isNaN(ts)) cookie.expiresAt = ts;
        }
    });
    return cookie;
}

function storeResponseCookies(response) {
    if (!response || !response.headers || !state.currentPreview || !state.currentPreview.portID) {
        return;
    }
    const setCookieHeader = getHeaderIgnoreCase(response.headers, 'set-cookie');
    if (!setCookieHeader) return;

    const store = getPortCookieStore(state.currentPreview.portID);
    splitSetCookieHeader(setCookieHeader).forEach(line => {
        const cookie = parseSetCookie(line);
        if (!cookie) return;
        if (cookie.expiresAt && cookie.expiresAt <= Date.now()) {
            store.delete(cookie.name);
            return;
        }
        store.set(cookie.name, cookie);
    });
}

function buildCookieHeader(portID, path) {
    if (!portID || !state.cookieJar.has(portID)) return '';
    const store = state.cookieJar.get(portID);
    const requestPath = normalizeHTTPPath(path || '/');
    const now = Date.now();
    const cookies = [];
    for (const [name, cookie] of store.entries()) {
        if (cookie.expiresAt && cookie.expiresAt <= now) {
            store.delete(name);
            continue;
        }
        if (cookie.path && !requestPath.startsWith(cookie.path)) {
            continue;
        }
        cookies.push(`${cookie.name}=${cookie.value}`);
    }
    return cookies.join('; ');
}

function isSkippableResourceURL(value) {
    if (!value) return true;
    const lower = value.toLowerCase();
    return lower.startsWith('data:') ||
        lower.startsWith('blob:') ||
        lower.startsWith('javascript:') ||
        lower.startsWith('mailto:') ||
        lower.startsWith('#') ||
        /^https?:\/\//i.test(value) ||
        value.startsWith('//');
}

function getPreviewBaseHref(path) {
    const normalized = normalizeHTTPPath(path || '/');
    const withoutQuery = normalized.split('#')[0].split('?')[0];
    if (withoutQuery.endsWith('/')) {
        return withoutQuery;
    }
    const idx = withoutQuery.lastIndexOf('/');
    if (idx >= 0) {
        return withoutQuery.substring(0, idx + 1) || '/';
    }
    return '/';
}

function resolvePreviewPath(target, currentPath) {
    const base = 'https://dc.local' + normalizeHTTPPath(currentPath || '/');
    const resolved = new URL(target, base);
    return applyPreviewPathRewriteRules(resolved.pathname) + (resolved.search || '') + (resolved.hash || '');
}

async function fetchDcResource(portID, resourcePath, headers = {}) {
    return await fetchDc(portID, resourcePath, {
        method: 'GET',
        headers
    });
}

async function inlineLinkedStyles(doc, meta) {
    const links = Array.from(doc.querySelectorAll('link[rel="stylesheet"][href]'));
    await Promise.all(links.map(async (link) => {
        const href = link.getAttribute('href');
        if (isSkippableResourceURL(href)) return;
        const resourcePath = resolvePreviewPath(href, meta.path);
        try {
            const response = await fetchDcResource(meta.portID, resourcePath, { Accept: 'text/css,*/*' });
            let css = base64ToUtf8(response.body || '');
            css = await rewriteCSSUrls(css, meta, resourcePath);
            const style = doc.createElement('style');
            style.textContent = css;
            link.replaceWith(style);
        } catch (err) {
            console.error('inline css failed', resourcePath, err);
        }
    }));
}

async function inlineScriptSources(doc, meta) {
    const scripts = Array.from(doc.querySelectorAll('script[src]'));
    await Promise.all(scripts.map(async (script) => {
        const src = script.getAttribute('src');
        if (isSkippableResourceURL(src)) return;
        const resourcePath = resolvePreviewPath(src, meta.path);
        try {
            const response = await fetchDcResource(meta.portID, resourcePath, { Accept: 'application/javascript,text/javascript,*/*' });
            const contentType = getHeaderIgnoreCase(response.headers, 'content-type') || '';
            const scriptText = base64ToUtf8(response.body || '');
            const loweredContentType = String(contentType).toLowerCase();
            const looksLikeScript = loweredContentType.indexOf('javascript') >= 0
                || loweredContentType.indexOf('ecmascript') >= 0
                || loweredContentType.indexOf('text/plain') >= 0;
            if ((contentType && !looksLikeScript) || /^\s*</.test(scriptText)) {
                console.error('inline script got non-js response', resourcePath, contentType, scriptText.substring(0, 200));
                return;
            }
            const inlineScript = doc.createElement('script');
            inlineScript.textContent = scriptText;
            script.replaceWith(inlineScript);
        } catch (err) {
            console.error('inline script failed', resourcePath, err);
        }
    }));
}

async function inlineImageSources(doc, meta) {
    const nodes = Array.from(doc.querySelectorAll('img[src], source[src], video[poster]'));
    await Promise.all(nodes.map(async (node) => {
        const attr = node.tagName.toLowerCase() === 'video' ? 'poster' : 'src';
        const raw = node.getAttribute(attr);
        if (isSkippableResourceURL(raw)) return;
        const resourcePath = resolvePreviewPath(raw, meta.path);
        try {
            const response = await fetchDcResource(meta.portID, resourcePath, { Accept: '*/*' });
            const contentType = getHeaderIgnoreCase(response.headers, 'content-type') || 'application/octet-stream';
            node.setAttribute(attr, `data:${contentType};base64,${response.body || ''}`);
        } catch (err) {
            console.error('inline media failed', resourcePath, err);
        }
    }));
}

async function rewriteCSSUrls(css, meta, cssPath) {
    const matches = [];
    const regex = /url\(([^)]+)\)/g;
    let match;
    while ((match = regex.exec(css)) !== null) {
        matches.push({ raw: match[0], value: match[1] });
    }
    if (matches.length === 0) {
        return css;
    }

    let result = css;
    await Promise.all(matches.map(async (item) => {
        const original = item.value.trim().replace(/^['"]|['"]$/g, '');
        if (isSkippableResourceURL(original)) return;
        const resourcePath = resolvePreviewPath(original, cssPath || meta.path);
        try {
            const response = await fetchDcResource(meta.portID, resourcePath, { Accept: '*/*' });
            const contentType = getHeaderIgnoreCase(response.headers, 'content-type') || 'application/octet-stream';
            const dataURL = `url("data:${contentType};base64,${response.body || ''}")`;
            result = result.replace(item.raw, dataURL);
        } catch (err) {
            console.error('rewrite css url failed', resourcePath, err);
        }
    }));
    return result;
}

async function preparePreviewHTML(html, meta) {
    if (!meta || !meta.portID || !meta.path) {
        return html;
    }
    try {
        const parser = new DOMParser();
        const doc = parser.parseFromString(html, 'text/html');
        doc.querySelectorAll('base').forEach((node) => node.remove());
        const base = doc.createElement('base');
        base.setAttribute('href', getPreviewBaseHref(meta.path));
        if (doc.head) {
            doc.head.prepend(base);
        } else if (doc.documentElement) {
            const head = doc.createElement('head');
            head.appendChild(base);
            doc.documentElement.prepend(head);
        }
        await inlineLinkedStyles(doc, meta);
        await inlineScriptSources(doc, meta);
        await inlineImageSources(doc, meta);
        return injectProxySupport('<!DOCTYPE html>\n' + doc.documentElement.outerHTML, meta);
    } catch (err) {
        console.error('prepare preview html failed', err);
        return injectProxySupport(html, meta);
    }
}

function generateSocketId() {
    return 'ws_' + generateRequestId();
}

function resolvePreviewWSPath(target, currentPath) {
    const raw = String(target || '').trim();
    if (!raw) {
        return normalizeHTTPPath(currentPath || '/');
    }
    if (/^wss?:\/\//i.test(raw)) {
        const parsed = new URL(raw);
        return parsed.pathname + (parsed.search || '') + (parsed.hash || '');
    }
    return resolvePreviewPath(raw, currentPath || '/');
}

class DcWebSocket {
    constructor(portID, url, protocols, currentPath) {
        this.portID = portID;
        this.url = url;
        this.protocols = protocols;
        this.currentPath = currentPath || '/';
        this.socketId = generateSocketId();
        this.readyState = DcWebSocket.CONNECTING;
        this.binaryType = 'blob';
        this.extensions = '';
        this.protocol = Array.isArray(protocols) ? (protocols[0] || '') : (protocols || '');
        this.onopen = null;
        this.onmessage = null;
        this.onclose = null;
        this.onerror = null;
        this._listeners = new Map();
        state.wsConnections.set(this.socketId, this);
        log(`[DC-WS] 创建代理连接 socket=${this.socketId}, port=${portID}, url=${url}`);

        const headers = {};
        const cookieHeader = buildCookieHeader(portID, this.currentPath);
        if (cookieHeader) {
            headers.Cookie = cookieHeader;
        }
        if (Array.isArray(protocols) && protocols.length > 0) {
            headers['Sec-WebSocket-Protocol'] = protocols.join(', ');
        } else if (typeof protocols === 'string' && protocols.trim()) {
            headers['Sec-WebSocket-Protocol'] = protocols.trim();
        }
        sendProtocolMessage({
            type: 19,
            payload: {
                socket_id: this.socketId,
                port_id: portID,
                path: resolvePreviewWSPath(url, this.currentPath),
                headers
            }
        });
    }

    send(data) {
        if (this.readyState !== DcWebSocket.OPEN) {
            throw new Error('WebSocket is not open');
        }
        if (typeof data === 'string') {
            log(`[DC-WS] 发送文本消息 socket=${this.socketId}, size=${data.length}`);
            sendProtocolMessage({
                type: 21,
                payload: {
                    socket_id: this.socketId,
                    data: utf8ToBase64(data),
                    text: true
                }
            });
            return;
        }
        if (data instanceof Blob) {
            data.arrayBuffer().then((buffer) => this.send(buffer)).catch((err) => this.__emitError(err.message || 'blob send failed'));
            return;
        }
        let bytes = null;
        if (data instanceof ArrayBuffer) {
            bytes = new Uint8Array(data);
        } else if (ArrayBuffer.isView(data)) {
            bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
        }
        if (bytes) {
            log(`[DC-WS] 发送二进制消息 socket=${this.socketId}, size=${bytes.byteLength}`);
            sendProtocolMessage({
                type: 21,
                payload: {
                    socket_id: this.socketId,
                    data: bytesToBase64(bytes),
                    text: false
                }
            });
            return;
        }
        const text = String(data);
        log(`[DC-WS] 发送文本消息 socket=${this.socketId}, size=${text.length}`);
        sendProtocolMessage({
            type: 21,
            payload: {
                socket_id: this.socketId,
                data: utf8ToBase64(text),
                text: true
            }
        });
    }

    close(code = 1000, reason = '') {
        if (this.readyState === DcWebSocket.CLOSED) return;
        this.readyState = DcWebSocket.CLOSING;
        log(`[DC-WS] 主动关闭 socket=${this.socketId}, code=${code}, reason=${reason}`);
        sendProtocolMessage({
            type: 22,
            payload: {
                socket_id: this.socketId,
                code,
                reason
            }
        });
    }

    addEventListener(type, listener) {
        if (!type || typeof listener !== 'function') return;
        if (!this._listeners.has(type)) {
            this._listeners.set(type, new Set());
        }
        this._listeners.get(type).add(listener);
    }

    removeEventListener(type, listener) {
        if (!type || !this._listeners.has(type)) return;
        this._listeners.get(type).delete(listener);
    }

    dispatchEvent(event) {
        if (!event || !event.type) return true;
        const listeners = this._listeners.get(event.type);
        if (listeners) {
            for (const listener of listeners) {
                try {
                    listener.call(this, event);
                } catch (err) {
                    console.error('DcWebSocket listener error', err);
                }
            }
        }
        const handler = this['on' + event.type];
        if (typeof handler === 'function') {
            try {
                handler.call(this, event);
            } catch (err) {
                console.error('DcWebSocket handler error', err);
            }
        }
        return true;
    }

    __setOpen() {
        if (this.readyState === DcWebSocket.OPEN) return;
        this.readyState = DcWebSocket.OPEN;
        this.dispatchEvent({ type: 'open', target: this });
    }

    __emitMessage(data) {
        this.dispatchEvent({ type: 'message', data, target: this });
    }

    __emitError(message) {
        this.dispatchEvent({ type: 'error', message, target: this });
    }

    __setClosed(code, reason) {
        if (this.readyState === DcWebSocket.CLOSED) return;
        this.readyState = DcWebSocket.CLOSED;
        state.wsConnections.delete(this.socketId);
        this.dispatchEvent({ type: 'close', code, reason, target: this });
    }
}

DcWebSocket.CONNECTING = 0;
DcWebSocket.OPEN = 1;
DcWebSocket.CLOSING = 2;
DcWebSocket.CLOSED = 3;

function injectProxySupport(html, meta) {
    if (!meta || !meta.portID || !meta.path) {
        return html;
    }
    const injected = `
<script>
(function() {
  var portID = '${escapeJsString(meta.portID)}';
  var currentPath = '${escapeJsString(meta.path)}';
  var rawFetch = window.fetch;
  var RawXMLHttpRequest = window.XMLHttpRequest;
  var RawFormSubmit = window.HTMLFormElement && window.HTMLFormElement.prototype ? window.HTMLFormElement.prototype.submit : null;
  var rawOpen = window.open ? window.open.bind(window) : null;
  var signalOrigin = window.location.origin;
  function shouldTraceResource(value) {
    var raw = String(value || '');
    return /logo\\.png(?:[?#].*)?$/i.test(raw) || /controller\\.js(?:[?#].*)?$/i.test(raw) || /layui(?:\\.all)?\\.js(?:[?#].*)?$/i.test(raw);
  }
  function traceResource(stage, value, extra) {
    if (!shouldTraceResource(value)) return;
    try {
      console.warn('[dc-trace]', stage, value, extra || '', new Error().stack);
    } catch (e) {
      console.warn('[dc-trace]', stage, value, extra || '');
    }
  }
  function applyPathRewriteRules(path) {
    var result = path || '/';
    var rules = Array.isArray(window.parent.PREVIEW_PATH_REWRITE_RULES) ? window.parent.PREVIEW_PATH_REWRITE_RULES : [];
    rules.forEach(function(rule) {
      try {
        if (rule && rule.pattern && rule.pattern.test(result)) {
          result = result.replace(rule.pattern, rule.replace || '');
        }
      } catch (e) {}
    });
    return result;
  }
  function hasHttpScheme(value) {
    var lower = String(value || '').toLowerCase();
    return lower.indexOf('http://') === 0 || lower.indexOf('https://') === 0;
  }
  function isSameOriginAbsoluteUrl(value) {
    try {
      var parsed = new URL(value, signalOrigin + '/');
      return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && parsed.origin === signalOrigin;
    } catch (e) {
      return false;
    }
  }
  function normalizePath(value) {
    try {
      var base = isSameOriginAbsoluteUrl(value)
        ? signalOrigin + (currentPath.startsWith('/') ? currentPath : '/' + currentPath)
        : 'https://dc.local' + (currentPath.startsWith('/') ? currentPath : '/' + currentPath);
      var url = new URL(value, base);
      return applyPathRewriteRules(url.pathname) + (url.search || '') + (url.hash || '');
    } catch (e) {
      return value;
    }
  }
  function isProxyTarget(value) {
    return !!value && (!hasHttpScheme(value) || isSameOriginAbsoluteUrl(value)) && !String(value).startsWith('//') &&
      !String(value).startsWith('data:') && !String(value).startsWith('blob:') &&
      !String(value).startsWith('javascript:') && !String(value).startsWith('mailto:') &&
      !String(value).startsWith('#');
  }
  function navigateViaProxy(value, options) {
    if (!isProxyTarget(value)) return false;
    traceResource('navigate', value, options && options.method ? options.method : 'GET');
    window.parent.__dcProxyNavigate(portID, normalizePath(value), options || {});
    return true;
  }
  async function inlineMediaNode(node, attr) {
    if (!node || !node.getAttribute) return;
    var raw = node.getAttribute(attr);
    if (!isProxyTarget(raw) || node.getAttribute('data-dc-inline') === '1') return;
    traceResource('inline-media', raw, attr);
    node.setAttribute('data-dc-inline', '1');
    try {
      var response = await window.parent.__dcProxyFetchRaw(portID, normalizePath(raw), { method: 'GET' });
      var headers = response.headers || {};
      var contentType = headers['content-type'] || headers['Content-Type'] || 'application/octet-stream';
      node.setAttribute(attr, 'data:' + contentType + ';base64,' + (response.body || ''));
    } catch (e) {
      console.error('dc inline media failed', raw, e);
      node.removeAttribute('data-dc-inline');
    }
  }
  async function inlineStylesheetNode(node) {
    if (!node || !node.getAttribute || node.getAttribute('data-dc-inline') === '1') return;
    var href = node.getAttribute('href');
    if (!isProxyTarget(href)) return;
    traceResource('inline-stylesheet', href);
    node.setAttribute('data-dc-inline', '1');
    try {
      var response = await window.parent.__dcProxyFetch(portID, normalizePath(href), { method: 'GET', headers: { Accept: 'text/css,*/*' } });
      var style = document.createElement('style');
      style.textContent = response.bodyText || '';
      node.parentNode && node.parentNode.replaceChild(style, node);
    } catch (e) {
      console.error('dc inline stylesheet failed', href, e);
      node.removeAttribute('data-dc-inline');
    }
  }
  async function inlineScriptNode(node) {
    if (!node || !node.getAttribute || node.getAttribute('data-dc-inline') === '1') return;
    var src = node.getAttribute('src');
    if (!isProxyTarget(src)) return;
    traceResource('inline-script', src);
    node.setAttribute('data-dc-inline', '1');
    try {
      var response = await window.parent.__dcProxyFetch(portID, normalizePath(src), { method: 'GET', headers: { Accept: 'application/javascript,text/javascript,*/*' } });
      var headers = response.headers || {};
      var contentType = headers['content-type'] || headers['Content-Type'] || '';
      var loweredContentType = String(contentType || '').toLowerCase();
      var looksLikeScript = loweredContentType.indexOf('javascript') >= 0 ||
        loweredContentType.indexOf('ecmascript') >= 0 ||
        loweredContentType.indexOf('text/plain') >= 0;
      if ((contentType && !looksLikeScript) || /^\\s*</.test(response.bodyText || '')) {
        console.error('dc inline script got non-js response', src, contentType, (response.bodyText || '').substring(0, 200));
        node.removeAttribute('data-dc-inline');
        return;
      }
      var script = document.createElement('script');
      script.textContent = response.bodyText || '';
      node.parentNode && node.parentNode.replaceChild(script, node);
    } catch (e) {
      console.error('dc inline script failed', src, e);
      node.removeAttribute('data-dc-inline');
    }
  }
  function processProxyNode(node) {
    if (!node || node.nodeType !== 1) return;
    var tag = (node.tagName || '').toLowerCase();
    if (tag === 'img' || tag === 'source') {
      inlineMediaNode(node, 'src');
    } else if (tag === 'video') {
      inlineMediaNode(node, 'poster');
    } else if (tag === 'link' && (node.getAttribute('rel') || '').toLowerCase() === 'stylesheet') {
      inlineStylesheetNode(node);
    } else if (tag === 'script' && node.getAttribute('src')) {
      inlineScriptNode(node);
    }
    if (node.querySelectorAll) {
      Array.prototype.forEach.call(node.querySelectorAll('img[src],source[src],video[poster]'), function(el) {
        processProxyNode(el);
      });
      Array.prototype.forEach.call(node.querySelectorAll('link[rel="stylesheet"][href],script[src]'), function(el) {
        processProxyNode(el);
      });
    }
  }
  function patchLocationMethod(name) {
    try {
      var original = window.location[name];
      if (typeof original !== 'function') return;
      window.location[name] = function(value) {
        if (!navigateViaProxy(value)) {
          return original.call(window.location, value);
        }
      };
    } catch (e) {}
  }
  document.addEventListener('click', function(event) {
    var link = event.target && event.target.closest ? event.target.closest('a[href]') : null;
    if (!link) return;
    var href = link.getAttribute('href');
    if (!href || href.startsWith('javascript:') || href.startsWith('#') || link.target === '_blank') return;
    event.preventDefault();
    navigateViaProxy(href);
  }, true);
  function submitFormViaProxy(form) {
    if (!form) return false;
    var method = (form.method || 'GET').toUpperCase();
    var actionPath = normalizePath(form.getAttribute('action') || currentPath);
    var formData = new FormData(form);
    if (method === 'GET') {
      var params = new URLSearchParams(formData).toString();
      window.parent.__dcProxyNavigate(portID, params ? actionPath.split('?')[0] + '?' + params : actionPath.split('?')[0]);
      return true;
    }
    var body = new URLSearchParams(formData).toString();
    window.parent.__dcProxyNavigate(portID, actionPath, {
      method: method,
      headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
      body: body
    });
    return true;
  }
  document.addEventListener('submit', function(event) {
    var form = event.target;
    if (!form || !form.action) return;
    event.preventDefault();
    submitFormViaProxy(form);
  }, true);
  if (RawFormSubmit) {
    window.HTMLFormElement.prototype.submit = function() {
      if (submitFormViaProxy(this)) {
        return;
      }
      return RawFormSubmit.call(this);
    };
  }
  function DcXMLHttpRequest() {
    this._method = 'GET';
    this._url = '';
    this._headers = {};
    this._async = true;
    this.readyState = 0;
    this.status = 0;
    this.statusText = '';
    this.responseText = '';
    this.response = '';
    this.onreadystatechange = null;
    this.onload = null;
    this.onerror = null;
  }
  DcXMLHttpRequest.prototype.open = function(method, url, async) {
    this._method = (method || 'GET').toUpperCase();
    this._url = url || '';
    this._async = async !== false;
    this.readyState = 1;
    if (typeof this.onreadystatechange === 'function') this.onreadystatechange();
  };
  DcXMLHttpRequest.prototype.setRequestHeader = function(key, value) {
    this._headers[key] = value;
  };
  DcXMLHttpRequest.prototype.getResponseHeader = function(name) {
    if (!this._responseHeaders) return null;
    var target = String(name || '').toLowerCase();
    for (var key in this._responseHeaders) {
      if (Object.prototype.hasOwnProperty.call(this._responseHeaders, key) &&
          String(key).toLowerCase() === target) {
        return this._responseHeaders[key];
      }
    }
    return null;
  };
  DcXMLHttpRequest.prototype.getAllResponseHeaders = function() {
    if (!this._responseHeaders) return '';
    return Object.keys(this._responseHeaders).map(function(key) {
      return key + ': ' + this._responseHeaders[key];
    }, this).join('\\r\\n');
  };
  DcXMLHttpRequest.prototype.send = function(body) {
    var self = this;
    var targetUrl = self._url || '';
    if (!targetUrl || ((/^https?:\\/\\//i.test(targetUrl) || targetUrl.startsWith('//')) && !isSameOriginAbsoluteUrl(targetUrl))) {
      var raw = new RawXMLHttpRequest();
      raw.onreadystatechange = function() {
        self.readyState = raw.readyState;
        self.status = raw.status;
        self.statusText = raw.statusText;
        self.responseText = raw.responseText;
        self.response = raw.response;
        if (typeof self.onreadystatechange === 'function') self.onreadystatechange();
      };
      raw.onload = function(evt) {
        if (typeof self.onload === 'function') self.onload(evt);
      };
      raw.onerror = function(evt) {
        if (typeof self.onerror === 'function') self.onerror(evt);
      };
      raw.open(self._method, targetUrl, self._async);
      Object.keys(self._headers).forEach(function(key) {
        raw.setRequestHeader(key, self._headers[key]);
      });
      raw.send(body);
      return;
    }
      window.parent.__dcProxyFetch(portID, normalizePath(targetUrl), {
        method: self._method,
        headers: self._headers,
        body: body || ''
      }).then(function(proxyResp) {
      self.status = proxyResp.status_code || 200;
      self.statusText = String(self.status);
      self.responseText = proxyResp.bodyText || '';
      self.response = self.responseText;
      self._responseHeaders = proxyResp.headers || {};
      self.readyState = 4;
      if (typeof self.onreadystatechange === 'function') self.onreadystatechange();
      if (typeof self.onload === 'function') self.onload({ type: 'load', target: self });
    }).catch(function(err) {
      self.readyState = 4;
      if (typeof self.onreadystatechange === 'function') self.onreadystatechange();
      if (typeof self.onerror === 'function') self.onerror({ type: 'error', error: err, target: self });
    });
  };
  window.XMLHttpRequest = DcXMLHttpRequest;
  window.WebSocket = function(url, protocols) {
    return window.parent.__dcCreateWebSocket(portID, url, protocols, currentPath);
  };
  window.WebSocket.CONNECTING = 0;
  window.WebSocket.OPEN = 1;
  window.WebSocket.CLOSING = 2;
  window.WebSocket.CLOSED = 3;
  if (rawOpen) {
    window.open = function(url, target, features) {
      if (navigateViaProxy(url)) {
        return window;
      }
      return rawOpen(url, target, features);
    };
  }
  function patchLayerOpen(layerObj) {
    if (!layerObj || typeof layerObj.open !== 'function' || layerObj.__dcLayerPatched) {
      return;
    }
    var rawLayerOpen = layerObj.open.bind(layerObj);
    layerObj.open = function(options) {
      try {
        if (options && Number(options.type) === 2) {
          var content = options.content;
          var targetUrl = '';
          if (Array.isArray(content) && content.length > 0) {
            targetUrl = content[0] || '';
          } else if (typeof content === 'string') {
            targetUrl = content;
          }
          if (targetUrl && navigateViaProxy(targetUrl)) {
            return -1;
          }
        }
      } catch (e) {
        console.warn('dc patch layer.open failed', e);
      }
      return rawLayerOpen.apply(layerObj, arguments);
    };
    layerObj.__dcLayerPatched = true;
  }
  patchLayerOpen(window.layer);
  try {
    Object.defineProperty(window, 'layer', {
      configurable: true,
      enumerable: true,
      get: function() {
        return this.__dcLayer;
      },
      set: function(value) {
        this.__dcLayer = value;
        patchLayerOpen(value);
      }
    });
    if (window.layer) {
      window.layer = window.layer;
    }
  } catch (e) {
    patchLayerOpen(window.layer);
  }
  patchLocationMethod('assign');
  patchLocationMethod('replace');
  Object.defineProperty(document, 'cookie', {
    configurable: true,
    get: function() {
      return window.parent.__dcProxyGetCookie(portID, currentPath);
    },
    set: function(value) {
      window.parent.__dcProxySetCookie(portID, value);
      return value;
    }
  });
  window.fetch = function(input, init) {
    var url = typeof input === 'string' ? input : (input && input.url ? input.url : '');
    var method = init && init.method ? init.method : (input && input.method ? input.method : 'GET');
    traceResource('fetch', url, method);
    var headers = {};
    if (init && init.headers) {
      if (init.headers.forEach) {
        init.headers.forEach(function(v, k) { headers[k] = v; });
      } else {
        headers = init.headers;
      }
    }
    if (!url || (/^https?:\\/\\//i.test(url) && !isSameOriginAbsoluteUrl(url))) {
      return rawFetch.apply(window, arguments);
    }
    return window.parent.__dcProxyFetch(portID, normalizePath(url), {
      method: method,
      headers: headers,
      body: init && init.body ? init.body : ''
    }).then(function(proxyResp) {
      return new Response(proxyResp.bodyText || '', {
        status: proxyResp.status_code || 200,
        headers: proxyResp.headers || {}
      });
    });
  };
  Array.prototype.forEach.call(document.querySelectorAll('img[src],source[src],video[poster],link[rel="stylesheet"][href],script[src]'), function(node) {
    processProxyNode(node);
  });
  new MutationObserver(function(mutations) {
    mutations.forEach(function(mutation) {
      if (mutation.type === 'attributes') {
        processProxyNode(mutation.target);
        return;
      }
      Array.prototype.forEach.call(mutation.addedNodes || [], function(node) {
        processProxyNode(node);
      });
    });
  }).observe(document.documentElement || document, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ['src', 'href', 'poster']
  });
})();
</script>`;
    if (/<head[^>]*>/i.test(html)) {
        return html.replace(/<head([^>]*)>/i, `<head$1>${injected}`);
    }
    if (/<html[^>]*>/i.test(html)) {
        return html.replace(/<html([^>]*)>/i, `<html$1><head>${injected}</head>`);
    }
    if (/<\/body>/i.test(html)) {
        return html.replace(/<\/body>/i, injected + '\n</body>');
    }
    return html + injected;
}

window.__dcProxyFetch = async function(portID, path, options = {}) {
    return await fetchDcText(portID, normalizeHTTPPath(path), options);
};

window.__dcProxyFetchRaw = async function(portID, path, options = {}) {
    return await fetchDc(portID, normalizeHTTPPath(path), options);
};

window.__dcProxyGetCookie = function(portID, path) {
    return buildCookieHeader(portID, path);
};

window.__dcProxySetCookie = function(portID, cookieString) {
    const store = getPortCookieStore(portID);
    const cookie = parseSetCookie(cookieString);
    if (!cookie) return;
    if (cookie.expiresAt && cookie.expiresAt <= Date.now()) {
        store.delete(cookie.name);
        return;
    }
    store.set(cookie.name, cookie);
};

window.__dcProxyNavigate = async function(portID, path, options = {}) {
    const method = (options.method || 'GET').toUpperCase();
    const headers = options.headers || {};
    const body = options.body || '';
    try {
        const response = await fetchDc(portID, normalizeHTTPPath(path), {
            method,
            headers,
            body
        });
        displayHTTPResponse(response, { portID, path: normalizeHTTPPath(path) });
    } catch (err) {
        log(`页面内导航失败: ${err.message}`, 'error');
        displayHTTPResponse({ error: err.message, status_code: 0, body: utf8ToBase64(err.message) }, { portID, path: normalizeHTTPPath(path) });
    }
};

window.__dcCreateWebSocket = function(portID, url, protocols, currentPath) {
    return new DcWebSocket(portID, url, protocols, currentPath);
};

// ==================== 内嵌终端（浮动窗口） ====================
function setTermStatus(text) {
    const st = document.getElementById('term-status');
    if (st) st.textContent = text;
    const cardSt = document.getElementById('term-card-status');
    if (cardSt) cardSt.textContent = text;
}

// showTerminalWindow 显示终端浮动窗口（必要时初始化拖动与尺寸自适应）。
function showTerminalWindow() {
    const win = document.getElementById('terminal-float');
    if (!win) return;
    win.classList.remove('hidden');
    setupTerminalFloat();
    // 显示后下一帧再 fit，确保容器已有真实尺寸
    requestAnimationFrame(() => fitTerminal());
}

// closeTerminalWindow 关闭浮动窗口（仅隐藏，不结束远端会话，可重新打开）。
function closeTerminalWindow() {
    document.getElementById('terminal-float')?.classList.add('hidden');
}

// toggleTerminalMaximize 在大窗口与全屏之间切换。
function toggleTerminalMaximize() {
    const win = document.getElementById('terminal-float');
    if (!win) return;
    const maxed = win.classList.toggle('maximized');
    const btn = document.getElementById('term-maximize-btn');
    if (btn) btn.textContent = maxed ? '⤡ 还原' : '⤢ 最大化';
    requestAnimationFrame(() => fitTerminal());
}

// fitTerminal 重新计算 xterm 行列以填满当前窗口并上报 resize。
function fitTerminal() {
    if (state.termFit) { try { state.termFit.fit(); } catch (e) {} }
    sendTermResize();
}

// setupTerminalFloat 初始化标题栏拖动与窗口尺寸变化自适应（仅一次）。
function setupTerminalFloat() {
    const win = document.getElementById('terminal-float');
    if (!win || win.dataset.floatReady) return;
    win.dataset.floatReady = '1';

    // 拖动：按住标题栏移动窗口（最大化时禁用）。
    const bar = document.getElementById('terminal-float-titlebar');
    if (bar) {
        let dragging = false, sx = 0, sy = 0, ox = 0, oy = 0;
        bar.addEventListener('mousedown', (e) => {
            if (e.target.closest('button') || win.classList.contains('maximized')) return;
            dragging = true;
            const rect = win.getBoundingClientRect();
            ox = rect.left; oy = rect.top; sx = e.clientX; sy = e.clientY;
            win.style.right = 'auto'; win.style.bottom = 'auto';
            e.preventDefault();
        });
        window.addEventListener('mousemove', (e) => {
            if (!dragging) return;
            let nx = ox + (e.clientX - sx), ny = oy + (e.clientY - sy);
            nx = Math.max(0, Math.min(nx, window.innerWidth - 80));
            ny = Math.max(0, Math.min(ny, window.innerHeight - 40));
            win.style.left = nx + 'px'; win.style.top = ny + 'px';
        });
        window.addEventListener('mouseup', () => { dragging = false; });
    }

    // 窗口（CSS resize 手柄 / 最大化）尺寸变化时自动 fit。
    if (typeof ResizeObserver !== 'undefined') {
        const ro = new ResizeObserver(() => fitTerminal());
        ro.observe(win);
    }
}

// 根据 Agent 上报的能力显示/隐藏终端卡片；断线重连后自动重新附着（触发回放）。
function applyTerminalCapability(termInfo) {
    const card = document.getElementById('terminal-card');
    if (!card) return;
    if (termInfo && termInfo.enabled) {
        card.classList.remove('hidden');
        setTermStatus('shell: ' + (termInfo.shell || 'default'));
        if (state.termOpen) {
            // 重连：自动重新附着并回放历史
            openTerminal(true);
        }
    } else {
        card.classList.add('hidden');
    }
}

function ensureXterm() {
    if (state.term) return state.term;
    if (typeof Terminal === 'undefined') {
        log('xterm.js 未加载（请确认能访问 CDN，或改为本地引入）', 'error');
        setTermStatus('xterm.js 未加载');
        return null;
    }
    const term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: 'Consolas, "Courier New", monospace',
        theme: { background: '#1e1e1e' },
        scrollback: 5000
    });
    let fit = null;
    try {
        if (typeof FitAddon !== 'undefined' && FitAddon.FitAddon) {
            fit = new FitAddon.FitAddon();
            term.loadAddon(fit);
        }
    } catch (e) {}

    const host = document.getElementById('terminal-host');
    term.open(host);
    if (fit) { try { fit.fit(); } catch (e) {} }

    state.term = term;
    state.termFit = fit;

    // 键盘输入 -> Agent
    state.termDataDisposable = term.onData((data) => sendTermInput(data));

    // 浏览器窗口尺寸变化 -> 重新 fit 并上报 resize
    state.termResizeHandler = () => fitTerminal();
    window.addEventListener('resize', state.termResizeHandler);
    return term;
}

function openTerminal(isReattach) {
    if (!state.authenticated) { log('请先连接并鉴权', 'error'); return; }
    showTerminalWindow();
    const term = ensureXterm();
    if (!term) return;
    requestAnimationFrame(() => fitTerminal());
    state.termOpen = true;
    sendProtocolMessage({ type: 24, payload: { cols: term.cols || 80, rows: term.rows || 24 } }); // TermOpen
    term.focus();
    setTermStatus(isReattach ? '已重连，正在回放历史…' : '终端已连接');
}

function killTerminal() {
    sendProtocolMessage({ type: 29, payload: {} }); // TermClose
    state.termOpen = false;
    if (state.term) {
        state.term.reset();
        state.term.write('\r\n[会话已结束]\r\n');
    }
    setTermStatus('会话已结束');
}

function sendTermInput(data) {
    if (!state.termOpen) return;
    const bytes = new TextEncoder().encode(data);
    sendProtocolMessage({ type: 26, payload: { data: bytesToBase64(bytes) } }); // TermInput
}

function sendTermResize() {
    if (!state.termOpen || !state.term) return;
    sendProtocolMessage({ type: 27, payload: { cols: state.term.cols, rows: state.term.rows } }); // TermResize
}

function handleTermData(payload) {
    if (!payload) return;
    const term = ensureXterm();
    if (!term) return;
    const bytes = base64ToBytes(payload.data || '');
    if (payload.replay) {
        // 重连回放：先清屏，再写入历史快照，恢复到断线前的画面
        term.reset();
    }
    term.write(bytes);
}

function handleTermExit(payload) {
    const code = payload ? payload.code : 0;
    const msg = (payload && payload.message) ? payload.message : 'shell exited';
    if (state.term) state.term.write(`\r\n[终端退出 code=${code}] ${msg}\r\n`);
    state.termOpen = false;
    setTermStatus(`已退出 (code=${code})`);
}

window.openTerminal = openTerminal;
window.killTerminal = killTerminal;
window.closeTerminalWindow = closeTerminalWindow;
window.toggleTerminalMaximize = toggleTerminalMaximize;

// ==================== 断开连接 ====================
function disconnect() {
    if (state.userSessionToken && state.agentID) {
        fetch(`${getSignalURL()}/controller/disconnect`, {
            method: 'POST',
            headers: buildJSONHeaders(true),
            body: JSON.stringify({ agent_id: state.agentID })
        }).catch(() => {});
    }
    for (const ws of state.wsConnections.values()) {
        try { ws.close(1000, 'disconnect'); } catch (e) {}
    }
    state.wsConnections.clear();
    resetConnectionState();
    state.cookieJar.clear();
    
    document.body.classList.remove('connected-mode');
    document.getElementById('http-console-card')?.classList.add('hidden');
    // 隐藏终端卡片，但保留 xterm 实例与 termOpen 意图，
    // 以便重新连接鉴权后自动重新附着并回放历史（断线不重置反馈）。
    document.getElementById('terminal-card')?.classList.add('hidden');
    closeTerminalWindow(); // 断开时收起浮动窗口（保留 termOpen 意图，重连自动恢复）
    if (state.termOpen) setTermStatus('连接已断开，重连后自动恢复');
    closeConnectModal();
    if (state.userSessionToken) {
        document.getElementById('agent-register-card')?.classList.remove('hidden');
        document.getElementById('agents-card')?.classList.remove('hidden');
    } else {
        document.getElementById('intro-card')?.classList.remove('hidden');
        document.getElementById('agent-register-card')?.classList.add('hidden');
        document.getElementById('agents-card')?.classList.add('hidden');
        document.getElementById('account-card')?.classList.remove('hidden');
        showAuthPanel('login');
    }
    
    log('已断开连接');
}

function logoutUser() {
    state.userSessionToken = '';
    state.currentUser = null;
    clearSessionState();
    disconnect();
    updateCurrentUserUI();
    showAuthPanel('login');
}

// ==================== 初始化 ====================
window.onload = async () => {
    const signalInput = document.getElementById('signal-url');
    if (signalInput && (!signalInput.value || signalInput.value === 'http://localhost:8443')) {
        signalInput.value = window.location.origin;
    }
    state.signalURL = window.location.origin;
    state.webConsoleBase = `${window.location.origin}/webconsole`; 
    log('Web Controller已加载');
    log('步骤: 1) 登录/注册 2) 查看用户 Hash 与客户端下载 3) 选择 Agent 并输入本地密码连接 4) 发送 HTTP 请求');
    showAuthPanel('login');
    const selector = document.getElementById('http-port');
    if (selector) {
        selector.addEventListener('change', (event) => {
            state.selectedHTTPPort = event.target.value || null;
        });
    }
    const passwordInput = document.getElementById('agent-password');
    if (passwordInput) {
        passwordInput.addEventListener('keydown', (event) => {
            if (event.key === 'Enter') {
                event.preventDefault();
                connect();
            }
        });
    }
    updatePreviewNavUI();
    await restoreLoginSession();
};

function clearLogs() {
    const logs = document.getElementById('logs');
    if (logs) logs.innerHTML = '';
}

function backToList() {
    closeConnectModal();
    document.getElementById('agents-card')?.classList.remove('hidden');
}

window.showAuthPanel = showAuthPanel;
window.sendEmailCode = sendEmailCode;
window.registerUser = registerUser;
window.loginUser = loginUser;
window.listAgents = listAgents;
window.selectAgent = selectAgent;
window.deleteAgent = deleteAgent;
window.closeConnectModal = closeConnectModal;
window.connect = connect;
window.disconnect = disconnect;
window.clearLogs = clearLogs;
window.backToList = backToList;
window.logoutUser = logoutUser;
window.openChangePasswordModal = openChangePasswordModal;
window.closeChangePasswordModal = closeChangePasswordModal;
window.changePassword = changePassword;
window.PREVIEW_PATH_REWRITE_RULES = PREVIEW_PATH_REWRITE_RULES;
window.WEB_CONSOLE_BASE = getWebConsoleBase();
window.PROXY_SERVICE_BASE = getProxyServiceBase();

// ==================== 构建版本时间戳 ====================
// 取 controller.js?v=<yyyyMMddHHmmss>（构建脚本每次构建自动更新），格式化显示在右下角，
// 方便一眼确认当前页面是否最新版（旧缓存/旧二进制时间戳会偏小）。
(function showBuildStamp() {
    const el = document.getElementById('build-stamp');
    if (!el) return;
    let v = '';
    const s = [...document.scripts].find(x => /controller\.js/.test(x.src || ''));
    if (s) { const m = (s.src || '').match(/[?&]v=([0-9A-Za-z]+)/); if (m) v = m[1]; }
    let label = v;
    const m = /^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(v);
    if (m) label = `${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}:${m[6]}`;
    el.textContent = label ? `build ${label}` : 'build dev';
})();

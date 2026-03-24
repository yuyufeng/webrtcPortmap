// controller.js - Web Controller (HTTP Web Console 版本)

const state = {
    signalURL: '',
    signalToken: '',
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
    currentPreview: null
};

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

function showStatus(elementId, message, type) {
    const el = document.getElementById(elementId);
    if (!el) return;
    el.className = `status ${type}`;
    el.textContent = message;
    el.classList.remove('hidden');
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

// ==================== Agent列表 ====================
async function listAgents() {
    const signalURL = document.getElementById('signal-url')?.value.trim();
    const signalToken = document.getElementById('signal-token')?.value.trim();
    
    if (!signalURL) {
        alert('请输入信令服务器URL');
        return;
    }

    try {
        log('获取Agent列表...');
        const headers = signalToken ? { 'Authorization': `Bearer ${signalToken}` } : {};
        const response = await fetch(`${signalURL}/controller/list`, { headers });
        
        if (!response.ok) throw new Error(`获取失败: ${response.status}`);
        
        const agents = await response.json();
        renderAgentList(agents);
        document.getElementById('agents-card')?.classList.remove('hidden');
        log(`找到 ${agents.length} 个Agent`);
    } catch (err) {
        log(`获取Agent列表失败: ${err.message}`, 'error');
        alert(`获取Agent列表失败: ${err.message}`);
    }
}

function renderAgentList(agents) {
    const list = document.getElementById('agent-list');
    if (!list) return;
    list.innerHTML = '';
    
    if (agents.length === 0) {
        list.innerHTML = '<li class="text-center">暂无在线Agent</li>';
        return;
    }
    
    agents.forEach(agent => {
        const li = document.createElement('li');
        li.className = 'agent-item';
        const onlineClass = agent.online ? 'online' : 'offline';
        const statusText = agent.online ? '🟢 在线' : '⚫ 离线';
        
        li.innerHTML = `
            <div class="agent-info">
                <div class="agent-id">${escapeHtml(agent.id)}</div>
                <div class="agent-status ${onlineClass}">${statusText}</div>
            </div>
            <button class="btn btn-primary" onclick="selectAgent('${escapeHtml(agent.id)}')">选择</button>
        `;
        list.appendChild(li);
    });
}

function selectAgent(id) {
    const agentIdInput = document.getElementById('agent-id');
    if (agentIdInput) agentIdInput.value = id;
    log(`已选择Agent: ${id}`);
    document.getElementById('password-card')?.classList.remove('hidden');
    document.getElementById('config-card')?.classList.add('hidden');
}

// ==================== WebRTC连接 ====================
async function connect() {
    resetConnectionState();

    state.signalURL = document.getElementById('signal-url')?.value.trim() || '';
    state.signalToken = document.getElementById('signal-token')?.value.trim() || '';
    state.agentID = document.getElementById('agent-id')?.value.trim() || '';
    const passwordInput = document.getElementById('agent-password');
    state.agentPassword = passwordInput ? passwordInput.value.replace(/^\s+|\s+$/g, '') : '';
    
    if (!state.signalURL || !state.agentID || !state.agentPassword) {
        alert('请填写所有必填项');
        return;
    }
    
    try {
        log('连接到信令服务器...');
        
        const headers = state.signalToken ? { 
            'Authorization': `Bearer ${state.signalToken}`, 
            'Content-Type': 'application/json' 
        } : { 'Content-Type': 'application/json' };
        
        const connectRes = await fetch(`${state.signalURL}/controller/connect`, {
            method: 'POST',
            headers,
            body: JSON.stringify({ agent_id: state.agentID })
        });
        
        if (!connectRes.ok) throw new Error(`连接失败: ${connectRes.status}`);
        
        log('已连接到Agent，建立WebRTC...');
        
        await createPeerConnection();
        createDataChannel();
        
        const offer = await state.pc.createOffer();
        await state.pc.setLocalDescription(offer);
        await waitForIceGathering();
        
        await fetch(`${state.signalURL}/controller/send?agent_id=${state.agentID}`, {
            method: 'POST',
            headers: state.signalToken ? { 
                'Authorization': `Bearer ${state.signalToken}`, 
                'Content-Type': 'application/json' 
            } : { 'Content-Type': 'application/json' },
            body: JSON.stringify({ type: 'offer', sdp: state.pc.localDescription })
        });
        
        startSignalingPoll();
        showStatus('password-status', 'WebRTC连接建立中...', 'info');
        
    } catch (err) {
        log(`连接失败: ${err.message}`, 'error');
        showStatus('password-status', `连接失败: ${err.message}`, 'error');
    }
}

async function createPeerConnection() {
    const config = {
        iceServers: [
            { urls: 'stun:stun.l.google.com:19302' },
            { urls: 'stun:stun1.l.google.com:19302' }
        ]
    };
    
    state.pc = new RTCPeerConnection(config);
    
    state.pc.onicecandidate = (event) => {
        if (event.candidate) {
            fetch(`${state.signalURL}/controller/send?agent_id=${state.agentID}`, {
                method: 'POST',
                headers: state.signalToken ? { 
                    'Authorization': `Bearer ${state.signalToken}`, 
                    'Content-Type': 'application/json' 
                } : { 'Content-Type': 'application/json' },
                body: JSON.stringify({ type: 'candidate', candidate: event.candidate })
            }).catch(err => log(`发送ICE候选失败: ${err.message}`, 'error'));
        }
    };
    
    state.pc.onconnectionstatechange = () => {
        log(`连接状态: ${state.pc.connectionState}`);
        if (state.pc.connectionState === 'connected') {
            showStatus('password-status', 'WebRTC已连接，等待鉴权...', 'success');
        } else if (state.pc.connectionState === 'failed' || state.pc.connectionState === 'disconnected') {
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
            const headers = state.signalToken ? { 'Authorization': `Bearer ${state.signalToken}` } : {};
            const response = await fetch(`${state.signalURL}/controller/poll?agent_id=${state.agentID}`, { headers });
            
            if (response.status === 200) {
                const msg = await response.json();
                if (msg.type === 'answer' && msg.sdp) {
                    await state.pc.setRemoteDescription(new RTCSessionDescription(msg.sdp));
                } else if (msg.type === 'candidate' && msg.candidate) {
                    await state.pc.addIceCandidate(new RTCIceCandidate(msg.candidate));
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
                log('鉴权成功！');
                showStatus('password-status', '已连接并鉴权成功', 'success');
                document.body.classList.add('connected-mode');
                
                document.getElementById('config-card')?.classList.add('hidden');
                document.getElementById('agents-card')?.classList.add('hidden');
                document.getElementById('password-card')?.classList.add('hidden');
                document.getElementById('http-console-card')?.classList.remove('hidden');
            } else {
                log('鉴权失败: 响应不匹配', 'error');
                showStatus('password-status', '鉴权失败: 密码错误', 'error');
            }
        } else if (msg.type === 13) {
            log(`收到Agent配置消息`);
            state.agentConfig = msg.payload;
            if (msg.payload && msg.payload.ports) {
                log(`Agent有 ${msg.payload.ports.length} 个端口配置`);
                renderPortButtons(msg.payload.ports);
            }
        } else if (msg.type === 17) { // MsgTypeHTTPResponse
            handleHTTPResponse(msg.payload);
        } else if (msg.type === 19) { // MsgTypeWSOpenAck
            handleWSOpenAck(msg.payload);
        } else if (msg.type === 20) { // MsgTypeWSData
            handleWSData(msg.payload);
        } else if (msg.type === 21) { // MsgTypeWSClose
            handleWSClose(msg.payload);
        } else if (msg.type === 22) { // MsgTypeWSError
            handleWSError(msg.payload);
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
            state.selectedHTTPPort = port.id;
            if (selector) {
                selector.value = port.id;
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
        type: 16,
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
    state.pendingHTTPRequests.clear();
    authStarted = false;
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

async function displayHTTPResponse(response, requestMeta = null) {
    const statusEl = document.getElementById('http-response-status');
    const headersEl = document.getElementById('http-response-headers');
    const bodyEl = document.getElementById('http-response-body');
    const previewEl = document.getElementById('http-response-preview');
    if (requestMeta && requestMeta.portID && requestMeta.path) {
        state.currentPreview = requestMeta;
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
    if (previewEl && response.body && response.status_code === 200) {
        const bodyStr = base64ToUtf8(response.body);
        if (bodyStr.includes('<!DOCTYPE') || bodyStr.includes('<html')) {
            previewEl.srcdoc = await preparePreviewHTML(bodyStr, state.currentPreview);
            previewEl.classList.remove('hidden');
        } else {
            previewEl.srcdoc = '';
            previewEl.classList.add('hidden');
        }
    }
    
    log(`收到响应: ${response.status_code || 'Error'}`);
}

function normalizeHTTPPath(input) {
    const value = (input || '').trim();
    if (!value) return '/';
    if (value.startsWith('/')) return value;
    return `/${value}`;
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

function resolvePreviewPath(target, currentPath) {
    const base = 'https://dc.local' + normalizeHTTPPath(currentPath || '/');
    const resolved = new URL(target, base);
    return resolved.pathname + (resolved.search || '') + (resolved.hash || '');
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
            const inlineScript = doc.createElement('script');
            inlineScript.textContent = base64ToUtf8(response.body || '');
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
            type: 18,
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
                type: 20,
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
                type: 20,
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
            type: 20,
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
            type: 21,
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
  function normalizePath(value) {
    try {
      var url = new URL(value, 'https://dc.local' + (currentPath.startsWith('/') ? currentPath : '/' + currentPath));
      return url.pathname + (url.search || '') + (url.hash || '');
    } catch (e) {
      return value;
    }
  }
  document.addEventListener('click', function(event) {
    var link = event.target && event.target.closest ? event.target.closest('a[href]') : null;
    if (!link) return;
    var href = link.getAttribute('href');
    if (!href || href.startsWith('javascript:') || href.startsWith('#') || link.target === '_blank') return;
    event.preventDefault();
    window.parent.__dcProxyNavigate(portID, normalizePath(href));
  }, true);
  document.addEventListener('submit', function(event) {
    var form = event.target;
    if (!form || !form.action) return;
    event.preventDefault();
    var method = (form.method || 'GET').toUpperCase();
    var actionPath = normalizePath(form.getAttribute('action') || currentPath);
    var formData = new FormData(form);
    if (method === 'GET') {
      var params = new URLSearchParams(formData).toString();
      window.parent.__dcProxyNavigate(portID, params ? actionPath.split('?')[0] + '?' + params : actionPath.split('?')[0]);
      return;
    }
    var body = new URLSearchParams(formData).toString();
    window.parent.__dcProxyNavigate(portID, actionPath, {
      method: method,
      headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' },
      body: body
    });
  }, true);
  var rawFetch = window.fetch;
  var RawXMLHttpRequest = window.XMLHttpRequest;
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
    if (!targetUrl || /^https?:\\/\\//i.test(targetUrl) || targetUrl.startsWith('//')) {
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
    var headers = {};
    if (init && init.headers) {
      if (init.headers.forEach) {
        init.headers.forEach(function(v, k) { headers[k] = v; });
      } else {
        headers = init.headers;
      }
    }
    if (!url || /^https?:\\/\\//i.test(url)) {
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

// ==================== 断开连接 ====================
function disconnect() {
    for (const ws of state.wsConnections.values()) {
        try { ws.close(1000, 'disconnect'); } catch (e) {}
    }
    state.wsConnections.clear();
    resetConnectionState();
    state.cookieJar.clear();
    
    document.body.classList.remove('connected-mode');
    document.getElementById('http-console-card')?.classList.add('hidden');
    document.getElementById('agents-card')?.classList.add('hidden');
    document.getElementById('config-card')?.classList.remove('hidden');
    document.getElementById('password-card')?.classList.add('hidden');
    
    log('已断开连接');
}

// ==================== 初始化 ====================
window.onload = () => {
    log('Web Controller已加载');
    log('步骤: 1) 查看Agent列表 2) 选择Agent 3) 输入密码连接 4) 发送HTTP请求');
    const selector = document.getElementById('http-port');
    if (selector) {
        selector.addEventListener('change', (event) => {
            state.selectedHTTPPort = event.target.value || null;
        });
    }
};

function clearLogs() {
    const logs = document.getElementById('logs');
    if (logs) logs.innerHTML = '';
}

function backToList() {
    document.getElementById('password-card')?.classList.add('hidden');
    document.getElementById('agents-card')?.classList.remove('hidden');
}

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
    pendingHTTPRequests: new Map() // id -> {resolve, reject}
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
async function sha256(message) {
    const encoder = new TextEncoder();
    const data = encoder.encode(message);
    const hashBuffer = await crypto.subtle.digest('SHA-256', data);
    return new Uint8Array(hashBuffer);
}

async function deriveKey(password, id) {
    const salt = await sha256(id);
    const encoder = new TextEncoder();
    const passwordKey = await crypto.subtle.importKey(
        'raw', encoder.encode(password), 'PBKDF2', false, ['deriveBits']
    );
    const derivedBits = await crypto.subtle.deriveBits(
        { name: 'PBKDF2', salt, iterations: 100000, hash: 'SHA-256' },
        passwordKey, 256
    );
    return new Uint8Array(derivedBits);
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
        log(`密钥派生失败: ${err.message}`, 'error');
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
        }
    } catch (err) {
        log(`处理消息失败: ${err.message}`, 'error');
    }
}

async function computeAuthResponse() {
    const data = `${state.authChallenge.challenge}:${state.authChallenge.timestamp}`;
    
    const encoder = new TextEncoder();
    const messageData = encoder.encode(data);
    
    const cryptoKey = await crypto.subtle.importKey(
        'raw', state.derivedKey, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']
    );
    
    const signature = await crypto.subtle.sign('HMAC', cryptoKey, messageData);
    return btoa(String.fromCharCode(...new Uint8Array(signature)));
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
    
    ports.forEach(port => {
        if (!port.allow_access) return;
        if (!port.local_addr.includes(':80') && !port.local_addr.includes(':8080')) return;
        
        const btn = document.createElement('button');
        btn.className = 'btn btn-secondary';
        btn.style.margin = '5px';
        btn.textContent = `${port.name} (${port.local_addr})`;
        btn.onclick = () => {
            const urlInput = document.getElementById('http-url');
            if (urlInput) {
                const isHttps = port.local_addr.includes(':443');
                urlInput.value = isHttps ? `https://${port.local_addr}` : `http://${port.local_addr}`;
            }
        };
        container.appendChild(btn);
    });
}

async function sendHTTPRequest() {
    const urlInput = document.getElementById('http-url');
    const methodInput = document.getElementById('http-method');
    const headersInput = document.getElementById('http-headers');
    const bodyInput = document.getElementById('http-body');
    
    if (!urlInput || !urlInput.value.trim()) {
        alert('请输入URL');
        return;
    }
    
    const requestId = generateRequestId();
    const url = urlInput.value.trim();
    const method = methodInput ? methodInput.value : 'GET';
    
    // 解析 headers
    const headers = {};
    if (headersInput && headersInput.value) {
        headersInput.value.split('\n').forEach(line => {
            const [key, ...value] = line.split(':');
            if (key && value.length > 0) {
                headers[key.trim()] = value.join(':').trim();
            }
        });
    }
    
    const body = bodyInput ? new TextEncoder().encode(bodyInput.value) : null;
    
    log(`[Web→Agent] 通过 DataChannel 发送 HTTP ${method} ${url}`);
    
    // 创建 Promise 等待响应
    const responsePromise = new Promise((resolve, reject) => {
        state.pendingHTTPRequests.set(requestId, { resolve, reject });
        setTimeout(() => {
            if (state.pendingHTTPRequests.has(requestId)) {
                state.pendingHTTPRequests.delete(requestId);
                reject(new Error('请求超时'));
            }
        }, 30000);
    });
    
    // 发送请求
    sendProtocolMessage({
        type: 16, // MsgTypeHTTPRequest
        payload: {
            id: requestId,
            method: method,
            url: url,
            headers: headers,
            body: body ? Array.from(body) : []
        }
    });
    
    try {
        const response = await responsePromise;
        displayHTTPResponse(response);
    } catch (err) {
        log(`请求失败: ${err.message}`, 'error');
        displayHTTPResponse({ error: err.message, status_code: 0, body: new TextEncoder().encode(err.message) });
    }
}

function generateRequestId() {
    return Math.random().toString(36).substring(2) + Date.now().toString(36);
}

function handleHTTPResponse(payload) {
    const requestId = payload.id;
    const pending = state.pendingHTTPRequests.get(requestId);
    if (pending) {
        state.pendingHTTPRequests.delete(requestId);
        pending.resolve(payload);
    }
}

function displayHTTPResponse(response) {
    const statusEl = document.getElementById('http-response-status');
    const headersEl = document.getElementById('http-response-headers');
    const bodyEl = document.getElementById('http-response-body');
    const previewEl = document.getElementById('http-response-preview');
    
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
        const bodyStr = response.body ? new TextDecoder().decode(new Uint8Array(response.body)) : '';
        bodyEl.textContent = bodyStr.substring(0, 10000); // 限制显示长度
    }
    
    // 尝试在 iframe 中预览 HTML
    if (previewEl && response.body && response.status_code === 200) {
        const bodyStr = new TextDecoder().decode(new Uint8Array(response.body));
        if (bodyStr.includes('<!DOCTYPE') || bodyStr.includes('<html')) {
            previewEl.srcdoc = bodyStr;
            previewEl.classList.remove('hidden');
        } else {
            previewEl.srcdoc = '';
            previewEl.classList.add('hidden');
        }
    }
    
    log(`收到响应: ${response.status_code || 'Error'}`);
}

// ==================== 断开连接 ====================
function disconnect() {
    if (state.dataChannel) state.dataChannel.close();
    if (state.pc) state.pc.close();
    
    state.authenticated = false;
    state.agentConfig = null;
    state.derivedKey = null;
    authStarted = false;
    state.pendingHTTPRequests.clear();
    
    document.getElementById('http-console-card')?.classList.add('hidden');
    document.getElementById('config-card')?.classList.remove('hidden');
    document.getElementById('password-card')?.classList.add('hidden');
    
    log('已断开连接');
}

// ==================== 初始化 ====================
window.onload = () => {
    log('Web Controller已加载');
    log('步骤: 1) 查看Agent列表 2) 选择Agent 3) 输入密码连接 4) 发送HTTP请求');
};

function clearLogs() {
    const logs = document.getElementById('logs');
    if (logs) logs.innerHTML = '';
}

function backToList() {
    document.getElementById('password-card')?.classList.add('hidden');
    document.getElementById('agents-card')?.classList.remove('hidden');
}

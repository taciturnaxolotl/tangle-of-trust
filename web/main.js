import { Cosmograph } from '@cosmograph/cosmograph';
import { getInternalApi } from '@cosmograph/cosmograph/cosmograph/internal.js';
import { prepareCosmographData } from '@cosmograph/cosmograph/data-kit';
import './style.css';

const container = document.getElementById('graph-container');
const loading = document.getElementById('loading');
const tooltip = document.getElementById('tooltip');
const headerStats = document.getElementById('header-stats');
const searchInput = document.getElementById('search-input');
const searchDropdown = document.getElementById('search-dropdown');
const sidebar = document.getElementById('sidebar');

let graphData = null;
let profileMap = {};
let cosmograph = null;
let pointIdToIndex = {};
let resolvingDIDs = new Set();
let currentRawPoints = [];
let currentLinks = [];
let currentNodes = [];

let selectedDID = null;
let highlightState = null;

let nodeDegrees = {};
let nodeInVouch = {};
let nodeInDenounce = {};
let nodeOutVouch = {};
let nodeOutDenounce = {};
let nodeInFollow = {};
let nodeOutFollow = {};
let nodeColors = {};

const edgeFilters = {
    'vouch/vouch': true,
    'vouch/denounce': true,
    'vouch/mixed': true,
    'follow': false,
};

document.querySelectorAll('.filter-btn').forEach(btn => {
    btn.addEventListener('click', () => {
        const kind = btn.dataset.kind;
        edgeFilters[kind] = !edgeFilters[kind];
        btn.classList.toggle('active', edgeFilters[kind]);
        rebuildGraph();
    });
});

function isDarkMode() {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

function shortenDID(did) {
    if (did.length > 24) return did.slice(0, 12) + '…' + did.slice(-8);
    return did;
}

async function resolveDID(did) {
    if (profileMap[did]?.handle) return profileMap[did];
    if (resolvingDIDs.has(did)) return profileMap[did] || null;
    resolvingDIDs.add(did);
    try {
        const resp = await fetch(`/api/resolve?did=${encodeURIComponent(did)}`);
        if (resp.ok) {
            const p = await resp.json();
            if (p.handle) {
                profileMap[did] = { handle: p.handle, avatar: p.avatar_url || p.avatar || '' };
                return profileMap[did];
            }
        }
    } catch {}
    return profileMap[did] || null;
}

function buildGraph() {
    if (!graphData) return { points: [], links: [] };

    const nodeSet = new Set();
    const links = [];

    const pairIndex = new Map();
    for (const edge of graphData.edges) {
        if (!edgeFilters[edge.kind]) continue;
        const min = edge.source < edge.target ? edge.source : edge.target;
        const max = edge.source < edge.target ? edge.target : edge.source;
        const pairKey = min + '|' + max;
        if (!pairIndex.has(pairKey)) pairIndex.set(pairKey, {});
        const entry = pairIndex.get(pairKey);
        const kind = edge.kind === 'vouch/vouch' ? 'vouch' : edge.kind === 'vouch/denounce' ? 'denounce' : 'follow';
        if (kind === 'vouch' || kind === 'denounce') {
            if (!entry[kind]) {
                entry[kind] = { source: edge.source, target: edge.target, kind: edge.kind, reasons: [{ source: edge.source, reason: edge.reason }], time: edge.time, mutual: false };
            } else {
                entry[kind].mutual = true;
                entry[kind].reasons.push({ source: edge.source, reason: edge.reason });
            }
        } else {
            if (!entry.follows) entry.follows = [];
            entry.follows.push({ source: edge.source, target: edge.target, kind: edge.kind, reason: edge.reason, time: edge.time, mutual: false });
        }
    }

    for (const entry of pairIndex.values()) {
        if (entry.vouch && entry.denounce) {
            nodeSet.add(entry.vouch.source); nodeSet.add(entry.vouch.target);
            links.push({ source: entry.vouch.source, target: entry.vouch.target, kind: 'vouch/mixed', reasons: [...entry.vouch.reasons, ...entry.denounce.reasons], time: entry.vouch.time, mutual: true });
        } else if (entry.vouch) {
            nodeSet.add(entry.vouch.source); nodeSet.add(entry.vouch.target);
            links.push(entry.vouch);
        } else if (entry.denounce) {
            nodeSet.add(entry.denounce.source); nodeSet.add(entry.denounce.target);
            links.push(entry.denounce);
        }
        if (entry.follows) {
            for (const fl of entry.follows) {
                nodeSet.add(fl.source); nodeSet.add(fl.target);
                links.push(fl);
            }
        }
    }

    const nodes = [...nodeSet].map(id => {
        const p = profileMap[id] || {};
        return { id, label: p.handle || id, handle: p.handle || '', avatar: p.avatar || '' };
    });

    nodeDegrees = {};
    nodeInVouch = {};
    nodeInDenounce = {};
    nodeOutVouch = {};
    nodeOutDenounce = {};
    nodeInFollow = {};
    nodeOutFollow = {};
    for (const n of nodes) { nodeDegrees[n.id] = 0; nodeInVouch[n.id] = 0; nodeInDenounce[n.id] = 0; nodeOutVouch[n.id] = 0; nodeOutDenounce[n.id] = 0; nodeInFollow[n.id] = 0; nodeOutFollow[n.id] = 0; }
    for (const link of links) {
        nodeDegrees[link.source] = (nodeDegrees[link.source] || 0) + 1;
        nodeDegrees[link.target] = (nodeDegrees[link.target] || 0) + 1;
        if (link.kind === 'vouch/vouch') {
            nodeOutVouch[link.source]++;
            nodeInVouch[link.target]++;
            if (link.mutual) { nodeInVouch[link.source]++; nodeOutVouch[link.target]++; }
        }
        else if (link.kind === 'vouch/denounce') {
            nodeOutDenounce[link.source]++;
            nodeInDenounce[link.target]++;
            if (link.mutual) { nodeInDenounce[link.source]++; nodeOutDenounce[link.target]++; }
        }
        else if (link.kind === 'vouch/mixed') {
            nodeOutVouch[link.source]++;
            nodeInVouch[link.target]++;
            nodeOutDenounce[link.source]++;
            nodeInDenounce[link.target]++;
            if (link.mutual) { nodeInVouch[link.source]++; nodeOutVouch[link.target]++; nodeInDenounce[link.source]++; nodeOutDenounce[link.target]++; }
        }
        else if (link.kind === 'follow') {
            nodeOutFollow[link.source]++;
            nodeInFollow[link.target]++;
        }
    }

    const isDark = isDarkMode();
    nodeColors = {};
    for (const n of nodes) {
        const vouches = nodeInVouch[n.id] || 0;
        const denounces = nodeInDenounce[n.id] || 0;
        const total = vouches + denounces;
        if (total === 0) { nodeColors[n.id] = isDark ? '#6e738d' : '#9ca0b0'; continue; }
        const ratio = denounces / total;
        if (ratio > 0.5) nodeColors[n.id] = isDark ? '#ed8796' : '#d20f39';
        else if (ratio > 0.1) nodeColors[n.id] = isDark ? '#eed49f' : '#df8e1d';
        else nodeColors[n.id] = isDark ? '#a6da95' : '#40a02b';
    }

    return { nodes, links };
}

function edgeColor(kind) {
    switch (kind) {
        case 'vouch/vouch': return isDarkMode() ? 'rgba(166,218,149,0.6)' : 'rgba(64,160,43,0.6)';
        case 'vouch/denounce': return isDarkMode() ? 'rgba(237,135,150,0.6)' : 'rgba(210,15,57,0.6)';
        case 'vouch/mixed': return isDarkMode() ? 'rgba(238,212,159,0.6)' : 'rgba(223,142,29,0.6)';
        case 'follow': return isDarkMode() ? 'rgba(198,160,246,0.55)' : 'rgba(136,57,239,0.55)';
        default: return 'rgba(107,114,128,0.3)';
    }
}

function edgeWidth(kind, mutual) {
    const base = (() => {
        switch (kind) {
            case 'vouch/vouch': return 1;
            case 'vouch/denounce': return 1;
            case 'vouch/mixed': return 1;
            case 'follow': return 1;
            default: return 0.4;
        }
    })();
    return mutual ? base + 1 : base;
}

// --- Highlight ---

function parseRgba(str) {
    const m = str.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/);
    if (m) return [parseFloat(m[1])/255, parseFloat(m[2])/255, parseFloat(m[3])/255, m[4] !== undefined ? parseFloat(m[4]) : 1];
    const hex = str.replace('#', '');
    if (hex.length >= 6) {
        const r = parseInt(hex.slice(0,2), 16);
        const g = parseInt(hex.slice(2,4), 16);
        const b = parseInt(hex.slice(4,6), 16);
        return [r/255, g/255, b/255, 1];
    }
    return [0.5, 0.5, 0.5, 1];
}

function computeHighlight(did) {
    const connectedNodes = new Set();
    connectedNodes.add(did);

    const connectedLinks = new Set();
    for (let i = 0; i < currentLinks.length; i++) {
        const l = currentLinks[i];
        if (l.source === did || l.target === did) {
            connectedNodes.add(l.source);
            connectedNodes.add(l.target);
            connectedLinks.add(i);
        }
    }

    return { did, connectedNodes, connectedLinks };
}

function applyHighlight() {
    if (!cosmograph) return;
    const internal = getInternalApi(cosmograph);
    const cosmos = internal?.cosmos;
    if (!cosmos) return;

    if (!highlightState) {
        cosmos.setConfigPartial({
            pointGreyoutColor: [-1, -1, -1, -1],
            isDarkenGreyout: false,
        });
        cosmos.unselectPoints();

        // Restore labels and link colors/widths
        const labels = internal.labels;
        if (labels?.labelsContainer) labels.labelsContainer.style.display = '';

        const graph = cosmos.graph;
        const lines = cosmos.lines;
        if (!graph || !lines) return;
        for (let i = 0; i < currentLinks.length; i++) {
            const c = parseRgba(edgeColor(currentLinks[i].kind));
            graph.linkColors[i*4] = c[0]; graph.linkColors[i*4+1] = c[1]; graph.linkColors[i*4+2] = c[2]; graph.linkColors[i*4+3] = c[3];
            graph.linkWidths[i] = edgeWidth(currentLinks[i].kind, currentLinks[i].mutual);
        }
        lines.updateColor();
        lines.updateWidth();
    } else {
        const dimColor = isDarkMode() ? [0.43, 0.45, 0.55, 0.12] : [0.61, 0.63, 0.69, 0.15];
        cosmos.setConfigPartial({
            pointGreyoutColor: dimColor,
            isDarkenGreyout: true,
        });

        // Hide all labels for greyed-out nodes
        const labels = internal.labels;
        if (labels?.labelsContainer) labels.labelsContainer.style.display = 'none';

        const indices = [];
        for (const id of highlightState.connectedNodes) {
            const idx = pointIdToIndex[id];
            if (idx !== undefined) indices.push(idx);
        }
        cosmos.selectPointsByIndices(indices);

        // Dim non-connected links by writing to linkColors/linkWidths directly
        const graph = cosmos.graph;
        const lines = cosmos.lines;
        if (!graph || !lines) return;

        for (let i = 0; i < currentLinks.length; i++) {
            if (highlightState.connectedLinks.has(i)) {
                const c = parseRgba(edgeColor(currentLinks[i].kind));
                graph.linkColors[i*4] = c[0]; graph.linkColors[i*4+1] = c[1]; graph.linkColors[i*4+2] = c[2]; graph.linkColors[i*4+3] = c[3];
                graph.linkWidths[i] = edgeWidth(currentLinks[i].kind, currentLinks[i].mutual) + 1;
            } else {
                const dim = parseRgba('rgba(107,114,128,0.05)');
                graph.linkColors[i*4] = dim[0]; graph.linkColors[i*4+1] = dim[1]; graph.linkColors[i*4+2] = dim[2]; graph.linkColors[i*4+3] = dim[3];
                graph.linkWidths[i] = 0.3;
            }
        }
        lines.updateColor();
        lines.updateWidth();
    }
}

function selectNode(did) {
    selectedDID = did;
    highlightState = computeHighlight(did);
    renderSidebar(did);
    sidebar.classList.add('open');
    requestAnimationFrame(() => applyHighlight());
}

function deselectNode() {
    selectedDID = null;
    highlightState = null;
    sidebar.classList.remove('open');
    applyHighlight();
}

// --- Sidebar ---

function avatarHtml(avatar, name, size = 40) {
    if (avatar) {
        return `<img class="sidebar-avatar" src="/api/proxy/avatar?url=${encodeURIComponent(avatar)}" width="${size}" height="${size}" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="sidebar-avatar-fallback" style="display:none;width:${size}px;height:${size}px;font-size:${Math.round(size*0.4)}px">${name.charAt(0).toUpperCase()}</div>`;
    }
    return `<div class="sidebar-avatar-fallback" style="width:${size}px;height:${size}px;font-size:${Math.round(size*0.4)}px">${name.charAt(0).toUpperCase()}</div>`;
}

let sidebarTab = 'outbound';

function renderSidebar(did) {
    const p = profileMap[did] || {};
    const handle = p.handle || '';
    const avatar = p.avatar || '';
    const name = handle || shortenDID(did);

    const inV = nodeInVouch[did] || 0;
    const inD = nodeInDenounce[did] || 0;
    const outV = nodeOutVouch[did] || 0;
    const outD = nodeOutDenounce[did] || 0;
    const inF = nodeInFollow[did] || 0;
    const outF = nodeOutFollow[did] || 0;
    const total = inV + inD;

    let trustHtml = '';
    if (total > 0 || outV + outD > 0) {
        const pct = total > 0 ? Math.round((inV / total) * 100) : null;
        let pctStr;
        if (pct !== null) {
            const c = pct > 90 ? 'kv' : pct > 50 ? 'ks' : 'kd';
            pctStr = `<span class="${c}">${pct}%</span>`;
        } else {
            pctStr = '<span style="color:var(--text-muted)">?</span>';
        }
        const inPart = total > 0 ? `<span class="kv">${inV}v</span> <span class="kd">${inD}d</span> in` : '';
        const outPart = outV + outD > 0 ? `<span class="kv">${outV}v</span> <span class="kd">${outD}d</span> out` : '';
        const sep = inPart && outPart ? ' · ' : '';
        trustHtml = `<div class="sidebar-stat">${pctStr} trusted · ${inPart}${sep}${outPart}</div>`;
    }
    let followHtml = '';
    if (inF > 0 || outF > 0) {
        const inPart = inF > 0 ? `<span class="kf">${inF}f</span> in` : '';
        const outPart = outF > 0 ? `<span class="kf">${outF}f</span> out` : '';
        const sep = inPart && outPart ? ' · ' : '';
        followHtml = `<div class="sidebar-stat">${inPart}${sep}${outPart}</div>`;
    }

    const profileLink = handle && handle !== '!'
        ? `<a class="sidebar-link" href="https://tangled.org/@${handle}" target="_blank" rel="noopener">${handle} ↗</a>`
        : '';

    // Classify links into outbound/inbound, then by vouch/denounce/follow
    const outVouched = [];
    const outDenounced = [];
    const outFollows = [];
    const inVouched = [];
    const inDenounced = [];
    const inFollows = [];

    for (const link of currentLinks) {
        if (link.source === did) {
            if (link.kind === 'vouch/denounce') outDenounced.push(link);
            else if (link.kind === 'vouch/mixed') { outVouched.push(link); outDenounced.push(link); }
            else if (link.kind === 'vouch/vouch') outVouched.push(link);
            else outFollows.push(link);
        } else if (link.target === did) {
            if (link.kind === 'vouch/denounce') inDenounced.push(link);
            else if (link.kind === 'vouch/mixed') { inVouched.push(link); inDenounced.push(link); }
            else if (link.kind === 'vouch/vouch') inVouched.push(link);
            else inFollows.push(link);
        }
    }

    const renderConnections = (links, direction) => {
        if (links.length === 0) return '<div class="sidebar-empty">None</div>';
        return links.map(l => {
            const otherId = direction === 'out' ? l.target : l.source;
            const otherP = profileMap[otherId] || {};
            const otherName = otherP.handle || shortenDID(otherId);
            const otherAvatar = otherP.avatar || '';
            const kindClass = l.kind === 'vouch/denounce' ? 'kd' : l.kind === 'vouch/vouch' ? 'kv' : l.kind === 'vouch/mixed' ? 'ks' : 'kf';
            const kindLabel = l.kind === 'vouch/denounce' ? 'denounces' : l.kind === 'vouch/vouch' ? 'vouches' : l.kind === 'vouch/mixed' ? 'mixed' : 'follows';
            const mutualTag = l.mutual ? ' <span class="sidebar-mutual">mutual</span>' : '';
            return `<div class="sidebar-conn" data-did="${otherId}">
                ${avatarHtml(otherAvatar, otherName, 28)}
                <div class="sidebar-conn-info">
                    <span class="sidebar-conn-name">${otherName}</span>
                    <span class="sidebar-conn-kind ${kindClass}">${kindLabel}${mutualTag}</span>
                </div>
            </div>`;
        }).join('');
    };

    const outTotal = outVouched.length + outDenounced.length + outFollows.length;
    const inTotal = inVouched.length + inDenounced.length + inFollows.length;
    const activeTab = sidebarTab;

    const tabContent = activeTab === 'outbound'
        ? `<div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kv">Vouched</span> <span class="sidebar-count">${outVouched.length}</span></div>
                ${renderConnections(outVouched, 'out')}
            </div>
            <div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kd">Denounced</span> <span class="sidebar-count">${outDenounced.length}</span></div>
                ${renderConnections(outDenounced, 'out')}
            </div>
            ${outFollows.length > 0 ? `<div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kf">Follows</span> <span class="sidebar-count">${outFollows.length}</span></div>
                ${renderConnections(outFollows, 'out')}
            </div>` : ''}`
        : `<div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kv">Vouched by</span> <span class="sidebar-count">${inVouched.length}</span></div>
                ${renderConnections(inVouched, 'in')}
            </div>
            <div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kd">Denounced by</span> <span class="sidebar-count">${inDenounced.length}</span></div>
                ${renderConnections(inDenounced, 'in')}
            </div>
            ${inFollows.length > 0 ? `<div class="sidebar-subsection">
                <div class="sidebar-subsection-title"><span class="kf">Followed by</span> <span class="sidebar-count">${inFollows.length}</span></div>
                ${renderConnections(inFollows, 'in')}
            </div>` : ''}`;

    sidebar.innerHTML = `
        <div class="sidebar-header">
            <div class="sidebar-profile">
                ${avatarHtml(avatar, name)}
                <div>
                    <div class="sidebar-name">${name}</div>
                    ${profileLink}
                    ${handle ? `<div class="sidebar-did">${shortenDID(did)}</div>` : ''}
                </div>
            </div>
            ${trustHtml}
            ${followHtml}
            <button class="sidebar-close" id="sidebar-close">✕</button>
        </div>
        <div class="sidebar-tabs">
            <button class="sidebar-tab ${activeTab === 'outbound' ? 'active' : ''}" data-tab="outbound">Outbound <span class="sidebar-count">${outTotal}</span></button>
            <button class="sidebar-tab ${activeTab === 'inbound' ? 'active' : ''}" data-tab="inbound">Inbound <span class="sidebar-count">${inTotal}</span></button>
        </div>
        <div class="sidebar-sections">
            ${tabContent}
        </div>
    `;

    document.getElementById('sidebar-close')?.addEventListener('click', deselectNode);

    sidebar.querySelectorAll('.sidebar-tab').forEach(tab => {
        tab.addEventListener('click', () => {
            sidebarTab = tab.dataset.tab;
            renderSidebar(did);
        });
    });

    sidebar.querySelectorAll('.sidebar-conn').forEach(el => {
        el.addEventListener('click', () => {
            const connDid = el.dataset.did;
            if (connDid) selectNode(connDid);
        });
    });
}

// --- Data loading ---

async function loadData() {
    const resp = await fetch('/api/graph');
    graphData = await resp.json();

    if (!graphData.edges || graphData.edges.length === 0) {
        loading.textContent = 'No data yet. Run backfill or ingest to collect records...';
        return;
    }

    for (const node of graphData.nodes) {
        if (node.handle || node.avatar) {
            profileMap[node.id] = { handle: node.handle || '', avatar: node.avatar || '' };
        }
    }

    loading.style.display = 'none';
    initGraph();
}

async function initGraph() {
    const { nodes, links } = buildGraph();

    if (nodes.length === 0) return;

    currentNodes = nodes;
    currentLinks = links;

    const rawPoints = nodes.map(n => ({
        id: n.id,
        color: nodeColors[n.id] || '#9ca0b0',
        size: Math.max(20, Math.min(50, 10 * Math.log2((nodeDegrees[n.id] || 0) + 2))),
        label: n.handle || '',
        imageUrl: n.avatar ? `/api/proxy/avatar?url=${encodeURIComponent(n.avatar)}` : '',
    }));

    const rawLinks = links.map((l, i) => ({
        source: l.source,
        target: l.target,
        color: edgeColor(l.kind),
        width: edgeWidth(l.kind, l.mutual),
        arrow: l.kind.startsWith('vouch/'),
        strength: l.kind === 'follow' ? 0.3 : 0.8,
    }));

    const dataConfig = {
        points: {
            pointIdBy: 'id',
            pointColorBy: 'color',
            pointColorStrategy: 'direct',
            pointSizeBy: 'size',
            pointSizeStrategy: 'direct',
            pointLabelBy: 'label',
            pointImageUrlBy: 'imageUrl',
            pointImageSize: 50,
            hidePointShapesForLoadedImages: true,
        },
        links: {
            linkSourceBy: 'source',
            linkTargetsBy: ['target'],
            linkColorBy: 'color',
            linkWidthBy: 'width',
            linkWidthStrategy: 'direct',
            linkArrowBy: 'arrow',
            linkStrengthBy: 'strength',
        },
    };

    const result = await prepareCosmographData(dataConfig, rawPoints, rawLinks);
    if (!result) return;

    const { points, links: prepLinks, cosmographConfig } = result;

    currentRawPoints = rawPoints;

    pointIdToIndex = {};
    for (let i = 0; i < rawPoints.length; i++) {
        pointIdToIndex[rawPoints[i].id] = i;
    }

    const isDark = isDarkMode();

    if (cosmograph) {
        await cosmograph.destroy();
        cosmograph = null;
    }

    cosmograph = new Cosmograph(container, {
        points,
        links: prepLinks,
        ...cosmographConfig,
        backgroundColor: isDark ? '#111827' : '#f1f5f9',
        showHoveredPointLabel: true,
        hoveredPointLabelClassName: 'hovered-label',
        focusPointOnClick: true,
        pointDefaultColor: isDark ? '#6e738d' : '#9ca0b0',
        pointColorStrategy: 'direct',
        pointSizeRange: [20, 50],
        linkDefaultColor: isDark ? 'rgba(107,114,128,0.2)' : 'rgba(107,114,128,0.2)',
        linkDefaultWidth: 1,
        linkWidthStrategy: 'direct',
        linkWidthRange: [1, 3],
        enableSimulation: true,
        onPointMouseOver: (pointIndex) => {
            const id = currentRawPoints[pointIndex]?.id;
            if (!id) return;
            if (highlightState && !highlightState.connectedNodes.has(id)) return;
            showNodeTooltip(id, pointIndex);
        },
        onPointMouseOut: () => {
            tooltip.style.display = 'none';
        },
        onPointClick: (pointIndex) => {
            const id = currentRawPoints[pointIndex]?.id;
            if (!id) return;
            if (selectedDID === id) {
                deselectNode();
            } else {
                selectNode(id);
            }
        },
        onBackgroundClick: () => {
            if (selectedDID) deselectNode();
        },
        onLinkMouseOver: (linkIndex) => {
            const link = currentLinks[linkIndex];
            if (!link) return;
            if (highlightState && !highlightState.connectedLinks.has(linkIndex)) return;
            showLinkTooltip(link);
        },
        onLinkMouseOut: () => {
            tooltip.style.display = 'none';
        },
        onGraphRebuilt: (stats) => {
            updateStats(currentNodes, currentLinks);
            cosmograph?.fitView(800, 50);
        },
    });

    document.addEventListener('keydown', e => {
        if (e.code === 'Space' && e.target === document.body) {
            e.preventDefault();
            cosmograph?.fitView(400, 30);
        }
        if (e.code === 'Escape' && selectedDID) {
            deselectNode();
        }
    });

    updateStats(currentNodes, currentLinks);

    if (highlightState) {
        applyHighlight();
    }
}

function showNodeTooltip(did, pointIndex) {
    const profile = profileMap[did] || {};
    const handle = profile.handle || '';
    const avatar = profile.avatar || '';
    const avatarHtml = avatar
        ? `<img class="tooltip-avatar" src="/api/proxy/avatar?url=${encodeURIComponent(avatar)}" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="tooltip-avatar-fallback" style="display:none">${(handle || did).charAt(0).toUpperCase()}</div>`
        : `<div class="tooltip-avatar-fallback">${(handle || shortenDID(did)).charAt(0).toUpperCase()}</div>`;

    const inV = nodeInVouch[did] || 0;
    const inD = nodeInDenounce[did] || 0;
    const outV = nodeOutVouch[did] || 0;
    const outD = nodeOutDenounce[did] || 0;
    const inF = nodeInFollow[did] || 0;
    const outF = nodeOutFollow[did] || 0;
    const total = inV + inD;
    let trustLine = '';
    if (total > 0 || outV + outD > 0) {
        const pct = total > 0 ? Math.round((inV / total) * 100) : null;
        let pctStr;
        if (pct !== null) {
            const c = pct > 90 ? 'kv' : pct > 50 ? 'ks' : 'kd';
            pctStr = `<span class="${c}">${pct}%</span>`;
        } else {
            pctStr = '<span style="color:var(--text-muted)">?</span>';
        }
        const inPart = total > 0 ? `<span class="kv">${inV}v</span> <span class="kd">${inD}d</span> in` : '';
        const outPart = outV + outD > 0 ? `<span class="kv">${outV}v</span> <span class="kd">${outD}d</span> out` : '';
        const sep = inPart && outPart ? ' · ' : '';
        trustLine = `<div style="margin-top:4px;font-size:0.6875rem">${pctStr} trusted · ${inPart}${sep}${outPart}</div>`;
    }
    let followLine = '';
    if (inF > 0 || outF > 0) {
        const inPart = inF > 0 ? `<span class="kf">${inF}f</span> in` : '';
        const outPart = outF > 0 ? `<span class="kf">${outF}f</span> out` : '';
        const sep = inPart && outPart ? ' · ' : '';
        followLine = `<div style="font-size:0.6875rem">${inPart}${sep}${outPart}</div>`;
    }

    tooltip.innerHTML = `
        <div class="tooltip-profile">
            ${avatarHtml}
            <div>
                <div class="tooltip-name">${handle || shortenDID(did)}</div>
                ${handle ? `<div class="tooltip-handle">${shortenDID(did)}</div>` : ''}
                ${trustLine}
                ${followLine}
            </div>
        </div>
    `;
    tooltip.style.display = 'block';
}

function formatReasons(link) {
    if (link.reasons && link.reasons.length > 0) {
        const withReasons = link.reasons.filter(r => r.reason);
        if (withReasons.length === 0) return '';
        return '<div style="margin-top:4px">' + withReasons.map(r => {
            const p = profileMap[r.source] || {};
            const name = p.handle || shortenDID(r.source);
            return `<div style="color:var(--text-muted);word-break:break-word"><span style="color:var(--text-secondary)">${name}:</span> ${r.reason}</div>`;
        }).join('') + '</div>';
    }
    if (link.reason) return `<div style="margin-top:4px;color:var(--text-muted);word-break:break-word">${link.reason}</div>`;
    return '';
}

function showLinkTooltip(link) {
    const srcId = link.source;
    const tgtId = link.target;
    const srcP = profileMap[srcId] || {};
    const tgtP = profileMap[tgtId] || {};
    const kindClass = link.kind === 'vouch/denounce' ? 'kd'
        : link.kind === 'vouch/vouch' ? 'kv'
        : link.kind === 'vouch/mixed' ? 'ks' : 'kf';
    const label = link.kind === 'vouch/denounce' ? 'denounce'
        : link.kind === 'vouch/vouch' ? 'vouch'
        : link.kind === 'vouch/mixed' ? 'mixed' : 'follow';
    const mutualTag = link.mutual ? ' (mutual)' : '';

    const srcAvatar = srcP.avatar
        ? `<img class="tooltip-avatar" src="/api/proxy/avatar?url=${encodeURIComponent(srcP.avatar)}" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="tooltip-avatar-fallback" style="display:none">${(srcP.handle || srcId).charAt(0).toUpperCase()}</div>`
        : `<div class="tooltip-avatar-fallback">${(srcP.handle || shortenDID(srcId)).charAt(0).toUpperCase()}</div>`;
    const tgtAvatar = tgtP.avatar
        ? `<img class="tooltip-avatar" src="/api/proxy/avatar?url=${encodeURIComponent(tgtP.avatar)}" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="tooltip-avatar-fallback" style="display:none">${(tgtP.handle || tgtId).charAt(0).toUpperCase()}</div>`
        : `<div class="tooltip-avatar-fallback">${(tgtP.handle || shortenDID(tgtId)).charAt(0).toUpperCase()}</div>`;

    tooltip.innerHTML = `
        <div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap">
            ${srcAvatar}
            <span class="did">${srcP.handle || shortenDID(srcId)}</span>
            <span class="${kindClass} action-word">${label}s${mutualTag}</span>
            ${tgtAvatar}
            <span class="did">${tgtP.handle || shortenDID(tgtId)}</span>
        </div>
        ${formatReasons(link)}
    `;
    tooltip.style.display = 'block';
}

function updateStats(nodes, links) {
    document.getElementById('stat-nodes').textContent = nodes.length;
    let vouches = 0, denounces = 0, mixed = 0, follows = 0;
    for (const l of links) {
        switch (l.kind) {
            case 'vouch/vouch': vouches++; break;
            case 'vouch/denounce': denounces++; break;
            case 'vouch/mixed': mixed++; break;
            case 'follow': follows++; break;
        }
    }
    document.getElementById('stat-vouches').textContent = vouches;
    document.getElementById('stat-denounces').textContent = denounces;
    document.getElementById('stat-mixed').textContent = mixed;
    document.getElementById('stat-follows').textContent = follows;
    headerStats.textContent = `${nodes.length} nodes / ${links.length} edges`;
}

async function rebuildGraph() {
    await initGraph();
}

// --- search ---
let searchTimeout = null;

searchInput.addEventListener('input', () => {
    clearTimeout(searchTimeout);
    const q = searchInput.value.trim();
    if (q.length < 2) { searchDropdown.style.display = 'none'; return; }
    searchTimeout = setTimeout(async () => {
        try {
            const resp = await fetch(`/api/search?q=${encodeURIComponent(q)}`);
            if (!resp.ok) return;
            const data = await resp.json();
            const actors = data.actors || [];
            if (actors.length === 0) { searchDropdown.style.display = 'none'; return; }
            searchDropdown.innerHTML = actors.map(a => {
                const avatar = a.avatar
                    ? `<img src="/api/proxy/avatar?url=${encodeURIComponent(a.avatar)}" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="search-fallback" style="display:none">${a.handle.charAt(0).toUpperCase()}</div>`
                    : `<div class="search-fallback">${a.handle.charAt(0).toUpperCase()}</div>`;
                return `<div class="search-item" data-did="${a.did}">
                    ${avatar}
                    <div class="search-info">
                        <div class="search-handle">${a.handle}</div>
                        <div class="search-did">${shortenDID(a.did)}</div>
                    </div>
                </div>`;
            }).join('');
            searchDropdown.style.display = 'block';
        } catch {}
    }, 250);
});

searchDropdown.addEventListener('click', e => {
    const item = e.target.closest('.search-item');
    if (!item) return;
    const did = item.dataset.did;
    searchInput.value = '';
    searchDropdown.style.display = 'none';
    if (cosmograph) {
        const idx = pointIdToIndex[did];
        if (idx !== undefined) {
            selectNode(did);
            cosmograph.zoomToPoint(idx, 800, 6);
        }
    }
});

document.addEventListener('click', e => {
    if (!document.getElementById('search-wrap').contains(e.target)) {
        searchDropdown.style.display = 'none';
    }
});

searchInput.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
        searchDropdown.style.display = 'none';
        searchInput.blur();
    }
});

// Tooltip follows mouse
document.addEventListener('mousemove', e => {
    if (tooltip.style.display === 'block') {
        tooltip.style.left = (e.clientX + 14) + 'px';
        tooltip.style.top = (e.clientY + 14) + 'px';
    }
});

loadData();

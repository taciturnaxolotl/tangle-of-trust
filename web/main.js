import { Cosmograph } from '@cosmograph/cosmograph';
import { prepareCosmographData } from '@cosmograph/cosmograph/data-kit';
import './style.css';

const container = document.getElementById('graph-container');
const loading = document.getElementById('loading');
const tooltip = document.getElementById('tooltip');
const headerStats = document.getElementById('header-stats');
const searchInput = document.getElementById('search-input');
const searchDropdown = document.getElementById('search-dropdown');

let graphData = null;
let profileMap = {};
let cosmograph = null;
let popup = null;
let pointIdToIndex = {};
let resolvingDIDs = new Set();
let currentRawPoints = [];
let currentLinks = [];
let currentNodes = [];

let nodeDegrees = {};
let nodeInVouch = {};
let nodeInDenounce = {};
let nodeOutVouch = {};
let nodeOutDenounce = {};
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

    // deduplicate mutual edges between same pair
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
                entry[kind] = { source: edge.source, target: edge.target, kind: edge.kind, reason: edge.reason, time: edge.time, mutual: false };
            } else {
                entry[kind].mutual = true;
            }
        } else {
            if (!entry.follows) entry.follows = [];
            entry.follows.push({ source: edge.source, target: edge.target, kind: edge.kind, reason: edge.reason, time: edge.time, mutual: false });
        }
    }

    for (const entry of pairIndex.values()) {
        if (entry.vouch && entry.denounce) {
            nodeSet.add(entry.vouch.source); nodeSet.add(entry.vouch.target);
            links.push({ source: entry.vouch.source, target: entry.vouch.target, kind: 'vouch/mixed', reason: entry.vouch.reason, time: entry.vouch.time, mutual: true });
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

    // pre-compute degrees and vouch/denounce counts
    nodeDegrees = {};
    nodeInVouch = {};
    nodeInDenounce = {};
    nodeOutVouch = {};
    nodeOutDenounce = {};
    for (const n of nodes) { nodeDegrees[n.id] = 0; nodeInVouch[n.id] = 0; nodeInDenounce[n.id] = 0; nodeOutVouch[n.id] = 0; nodeOutDenounce[n.id] = 0; }
    for (const link of links) {
        nodeDegrees[link.source] = (nodeDegrees[link.source] || 0) + 1;
        nodeDegrees[link.target] = (nodeDegrees[link.target] || 0) + 1;
        if (link.kind === 'vouch/vouch') { nodeInVouch[link.target]++; nodeOutVouch[link.source]++; }
        else if (link.kind === 'vouch/denounce') { nodeInDenounce[link.target]++; nodeOutDenounce[link.source]++; }
        else if (link.kind === 'vouch/mixed') { nodeInVouch[link.target]++; nodeInDenounce[link.target]++; nodeOutVouch[link.source]++; nodeOutDenounce[link.source]++; }
    }

    // pre-compute node colors
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
            case 'vouch/vouch': return 0.8;
            case 'vouch/denounce': return 0.6;
            case 'vouch/mixed': return 0.8;
            case 'follow': return 0.4;
            default: return 0.4;
        }
    })();
    return mutual ? base + 0.2 : base;
}

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

    // Build Cosmograph data format
    const rawPoints = nodes.map(n => ({
        id: n.id,
        color: nodeColors[n.id] || '#9ca0b0',
        size: Math.max(5, Math.min(20, 4 * Math.log2((nodeDegrees[n.id] || 0) + 2))),
        label: n.handle || '',
        imageUrl: n.avatar ? `/api/proxy/avatar?url=${encodeURIComponent(n.avatar)}` : '',
    }));

    const rawLinks = links.map(l => ({
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
            pointSizeBy: 'size',
            pointSizeStrategy: 'direct',
            pointLabelBy: 'label',
            pointImageUrlBy: 'imageUrl',
            pointImageSize: 20,
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

    // Store for callback access after filter toggles
    currentRawPoints = rawPoints;

    // Build index map for lookup
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
        selectPointOnClick: 'single',
        pointDefaultColor: isDark ? '#6e738d' : '#9ca0b0',
        pointSizeRange: [5, 20],
        linkDefaultColor: isDark ? 'rgba(107,114,128,0.2)' : 'rgba(107,114,128,0.2)',
        linkDefaultWidth: 0.5,
        linkWidthStrategy: 'direct',
        linkWidthRange: [0.4, 0.8],
        enableSimulation: true,
        onPointMouseOver: (pointIndex) => {
            const id = currentRawPoints[pointIndex]?.id;
            if (!id) return;
            showNodeTooltip(id, pointIndex);
        },
        onPointMouseOut: () => {
            tooltip.style.display = 'none';
        },
        onPointClick: (pointIndex) => {
            const id = currentRawPoints[pointIndex]?.id;
            if (!id) return;
            const p = profileMap[id];
            if (p?.handle && p.handle !== '!') {
                window.open(`https://tangled.org/@${p.handle}`, '_blank');
            }
        },
        onLinkMouseOver: (linkIndex) => {
            const link = currentLinks[linkIndex];
            if (!link) return;
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
    });

    updateStats(currentNodes, currentLinks);
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

    tooltip.innerHTML = `
        <div class="tooltip-profile">
            ${avatarHtml}
            <div>
                <div class="tooltip-name">${handle || shortenDID(did)}</div>
                ${handle ? `<div class="tooltip-handle">${shortenDID(did)}</div>` : ''}
                ${trustLine}
            </div>
        </div>
    `;
    tooltip.style.display = 'block';
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
        ${link.reason ? `<div style="margin-top:4px;color:var(--text-muted);word-break:break-word">${link.reason}</div>` : ''}
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

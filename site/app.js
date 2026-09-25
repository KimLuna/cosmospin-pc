const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];
const demoMode = new URLSearchParams(location.search).get('demo') === '1';

const state = {
  bridge: null,
  account: null,
  accountEmail: '',
  collections: [],
  expanded: new Set(),
  selected: new Map(), // objektId -> { copy, collection }
  recipient: null,
  sending: false,
  spin: { status: null, selected: null, active: null, completing: false, chosenIndex: null },
};

const els = {
  bridgeBadge: $('#bridgeBadge'), loginView: $('#loginView'), mainView: $('#mainView'),
  emailInput: $('#emailInput'), sendCodeBtn: $('#sendCodeBtn'), codeArea: $('#codeArea'),
  codeInput: $('#codeInput'), loginBtn: $('#loginBtn'), loginStatus: $('#loginStatus'),
  logoutBtn: $('#logoutBtn'), accountName: $('#accountName'), accountAddress: $('#accountAddress'),
  refreshBtn: $('#refreshBtn'), loadingLibrary: $('#loadingLibrary'), emptyLibrary: $('#emptyLibrary'),
  collectionGrid: $('#collectionGrid'), libraryCount: $('#libraryCount'), cardSearch: $('#cardSearch'),
  memberFilter: $('#memberFilter'), seasonFilter: $('#seasonFilter'), transferableOnly: $('#transferableOnly'),
  selectedCount: $('#selectedCount'), selectedSummary: $('#selectedSummary'), recipientInput: $('#recipientInput'),
  recipientSearchBtn: $('#recipientSearchBtn'), recipientResults: $('#recipientResults'), chosenRecipient: $('#chosenRecipient'),
  reviewBtn: $('#reviewBtn'), confirmModal: $('#confirmModal'), confirmRecipient: $('#confirmRecipient'), confirmList: $('#confirmList'),
  startTransferBtn: $('#startTransferBtn'), progressModal: $('#progressModal'), progressBar: $('#progressBar'),
  progressMeta: $('#progressMeta'), progressRows: $('#progressRows'), progressFooter: $('#progressFooter'),
  progressCloseBtn: $('#progressCloseBtn'), progressTitle: $('#progressTitle'), toast: $('#toast'),
  transferTabBtn: $('#transferTabBtn'), spinTabBtn: $('#spinTabBtn'), transferWorkspace: $('#transferWorkspace'), spinWorkspace: $('#spinWorkspace'),
  spinRefreshBtn: $('#spinRefreshBtn'), spinTicketCount: $('#spinTicketCount'), spinSafetyBadge: $('#spinSafetyBadge'),
  spinNextTicket: $('#spinNextTicket'), spinPointBalance: $('#spinPointBalance'), spinPrice: $('#spinPrice'),
  spinSearch: $('#spinSearch'), spinSeasonFilter: $('#spinSeasonFilter'), spinEmpty: $('#spinEmpty'),
  spinCollectionGrid: $('#spinCollectionGrid'), spinSelectedBox: $('#spinSelectedBox'), spinSelectedTitle: $('#spinSelectedTitle'),
  spinSelectedSerial: $('#spinSelectedSerial'), spinStartBtn: $('#spinStartBtn'), spinChooseView: $('#spinChooseView'),
  spinPickView: $('#spinPickView'), spinPickGrid: $('#spinPickGrid'), spinSessionLabel: $('#spinSessionLabel'),
  spinResultView: $('#spinResultView'), spinResultTitle: $('#spinResultTitle'), spinResultSub: $('#spinResultSub'),
  spinResultGrid: $('#spinResultGrid'), spinNextBtn: $('#spinNextBtn'),
};

const demoCollections = [
  {
    collectionNo: '301Z', season: 'Divine01', class: 'First', member: 'SeoYeon',
    frontImage: '', accentColor: '#b7ff5b', count: 12, transferableCount: 10,
    copies: [3, 18, 41, 127, 208, 332, 481, 610, 832, 977, 1024, 1311].map((serial, i) => ({
      serial, objektId: 910000 + i, tokenId: 910000 + i, transferable: i !== 1 && i !== 10,
      usedForGrid: i === 10, status: 'minted', tokenAddress: '0xDEMO'
    }))
  },
  {
    collectionNo: '601A', season: 'Atom01', class: 'Special', member: 'JiWoo',
    frontImage: '', accentColor: '#8b7bff', count: 7, transferableCount: 7,
    copies: [9, 44, 88, 201, 407, 612, 955].map((serial, i) => ({
      serial, objektId: 920000 + i, tokenId: 920000 + i, transferable: true, usedForGrid: false, status: 'minted', tokenAddress: '0xDEMO'
    }))
  },
  {
    collectionNo: '101B', season: 'Binary02', class: 'First', member: 'YooYeon',
    frontImage: '', accentColor: '#ff8fcf', count: 5, transferableCount: 4,
    copies: [12, 29, 311, 744, 1002].map((serial, i) => ({
      serial, objektId: 930000 + i, tokenId: 930000 + i, transferable: i !== 2, usedForGrid: false, status: 'minted', tokenAddress: '0xDEMO'
    }))
  }
];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const esc = (v = '') => String(v).replace(/[&<>'"]/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
const shortAddr = (v = '') => v.length > 14 ? `${v.slice(0, 8)}…${v.slice(-6)}` : v;
const normalizeSerial = (n) => `#${String(n).padStart(3, '0')}`;

function setBridgeBadge(text, kind = 'pending') {
  els.bridgeBadge.textContent = text;
  els.bridgeBadge.className = `status-badge ${kind}`;
}

function setLoginStatus(text, kind = '') {
  els.loginStatus.textContent = text;
  els.loginStatus.className = `status-text ${kind}`.trim();
}

let toastTimer;
function toast(text, kind = '') {
  clearTimeout(toastTimer);
  els.toast.textContent = text;
  els.toast.className = `toast ${kind}`.trim();
  toastTimer = setTimeout(() => els.toast.classList.add('hidden'), 4500);
}

async function callBridge(method, ...args) {
  if (!state.bridge?.[method]) throw new Error(`로컬 기능을 찾을 수 없습니다: ${method}`);
  const raw = await state.bridge[method](...args);
  return typeof raw === 'string' ? JSON.parse(raw) : raw;
}

async function apiRequest(path, payload = null, method = 'POST') {
  const init = { method, cache: 'no-store' };
  if (payload !== null) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(payload);
  }
  const response = await fetch(path, init);
  let data;
  try { data = await response.json(); }
  catch { data = null; }
  if (!response.ok) {
    throw new Error(data?.error || `요청 실패 (HTTP ${response.status})`);
  }
  return data;
}

async function initLocalApp() {
  if (demoMode) {
    setBridgeBadge('DEMO MODE', 'ready');
    state.bridge = {
      sendCode: async () => JSON.stringify({ok:true}),
      login: async () => JSON.stringify({nickname:'demo_user', address:'0xDEMOACCOUNT0000000000000000000000000001', eoa:'0xDEMOEOA'}),
      session: async () => JSON.stringify({authenticated:true, account:{nickname:'demo_user', address:'0xDEMOACCOUNT0000000000000000000000000001', eoa:'0xDEMOEOA'}}),
      collections: async () => JSON.stringify(demoCollections),
      searchUsers: async (q) => JSON.stringify([
        {id:1, nickname:`${q || 'friend'}_demo`, address:'0x1111111111111111111111111111111111111111'},
        {id:2, nickname:`${q || 'friend'}2`, address:'0x2222222222222222222222222222222222222222'}
      ]),
      transfer: async (id) => { await sleep(650); return JSON.stringify({hash:`0xdemo${id}`, objektId:id, tokenAddress:'0xDEMO'}); },
      spinStatus: async () => JSON.stringify({tickets:2,nextReceiveAt:new Date(Date.now()+3600000).toISOString(),price:600,pointBalance:1200,freePointBalance:0,spinableSeasons:['Divine01','Atom01','Binary02']}),
      spinStart: async (id) => { await sleep(800); return JSON.stringify({spinId:8264786,objektId:id,transferHash:`0xspin${id}`,slots:16}); },
      spinComplete: async (chosenIndex=7) => { await sleep(800); const winnerIndex=chosenIndex; const arr=Array.from({length:16},(_,i)=>i%5===0?null:{season:'Divine01',collectionNo:`${101+i}Z`,class:'First',member:['SeoYeon','YooYeon','JiWoo','SooMin'][i%4],thumbnailImage:'',frontImage:'',accentColor:'#b7ff5b',objektNo:i===winnerIndex?777:0}); return JSON.stringify({spinId:8264786,winnerIndex,selected:arr[winnerIndex],results:arr,tickets:1,nextReceiveAt:new Date(Date.now()+3600000).toISOString()}); },
      logout: async () => JSON.stringify({ok:true}),
    };
    return;
  }

  const health = await apiRequest('/api/health', null, 'GET');
  if (!health?.ok) throw new Error('로컬 실행 프로그램에 연결하지 못했습니다.');

  state.bridge = {
    sendCode: (email) => apiRequest('/api/send-code', { email }),
    login: (email, code) => apiRequest('/api/login', { email, code }),
    session: () => apiRequest('/api/session', null, 'GET'),
    collections: () => apiRequest('/api/collections', null, 'GET'),
    searchUsers: (query) => apiRequest('/api/search-users', { query }),
    transfer: (objektId, recipient) => apiRequest('/api/transfer', { objektId, recipient }),
    spinStatus: () => apiRequest('/api/spin/status', null, 'GET'),
    spinStart: (objektId) => apiRequest('/api/spin/start', { objektId }),
    spinComplete: (index) => apiRequest('/api/spin/complete', { index }),
    logout: () => apiRequest('/api/logout', {}),
  };
  setBridgeBadge('로컬 연결 완료', 'ready');
}

const SAVED_ACCOUNTS_KEY = 'cosmoSavedAccounts';
const PENDING_ACCOUNT_EMAIL_KEY = 'cosmoPendingAccountEmail';

function getSavedAccounts() {
  try {
    const raw = localStorage.getItem(SAVED_ACCOUNTS_KEY);
    const list = raw ? JSON.parse(raw) : [];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

function saveSavedAccounts(list) {
  try {
    localStorage.setItem(SAVED_ACCOUNTS_KEY, JSON.stringify(list));
  } catch {}
}

function findSavedEmail(account) {
  if (!account) return '';
  const address = String(account.address || account.eoa || '').toLowerCase();
  const nickname = String(account.nickname || '');
  const found = getSavedAccounts().find(item =>
    (address && String(item.address || '').toLowerCase() === address) ||
    (nickname && item.nickname === nickname)
  );
  return found?.email || '';
}

function saveAccountRecord(email, account) {
  email = String(email || '').trim();
  if (!email || !account) return;

  const list = getSavedAccounts();
  const normalized = email.toLowerCase();

  const record = {
    email,
    nickname: account.nickname || 'Cosmo user',
    address: account.address || account.eoa || '',
    eoa: account.eoa || '',
    updatedAt: Date.now(),
  };

  const idx = list.findIndex(item => String(item.email || '').toLowerCase() === normalized);
  if (idx >= 0) list[idx] = {...list[idx], ...record};
  else list.unshift(record);

  list.sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
  saveSavedAccounts(list.slice(0, 20));
}

function installAccountManager() {
  const topActions = document.querySelector('.top-actions');
  if (!topActions || document.getElementById('accountManagerBtn')) return;

  const button = document.createElement('button');
  button.id = 'accountManagerBtn';
  button.type = 'button';
  button.className = 'ghost';
  button.textContent = '계정 관리';
  topActions.insertBefore(button, els.logoutBtn);

  const modal = document.createElement('div');
  modal.id = 'accountManagerModal';
  modal.className = 'modal-backdrop hidden';
  modal.innerHTML = `
    <div class="modal-card account-manager-card" role="dialog" aria-modal="true" aria-labelledby="accountManagerTitle">
      <div class="modal-head">
        <div>
          <div class="eyebrow">ACCOUNT MANAGER</div>
          <h2 id="accountManagerTitle">계정 관리</h2>
        </div>
        <button id="accountManagerClose" class="icon-btn" type="button" aria-label="닫기">×</button>
      </div>

      <div id="accountManagerList"></div>

      <div class="account-manager-note">
        계정 이메일만 이 브라우저에 저장합니다. 인증번호와 서명키는 저장하지 않습니다.
      </div>

      <div class="modal-actions">
        <button id="accountManagerAdd" class="primary wide" type="button">＋ 새 계정 추가</button>
      </div>
    </div>
  `;
  document.body.appendChild(modal);

  const style = document.createElement('style');
  style.textContent = `
    .account-manager-card { width:min(620px,100%); }
    .account-manager-list { display:flex; flex-direction:column; gap:8px; }
    .account-manager-item {
      display:flex; align-items:center; justify-content:space-between; gap:12px;
      padding:12px; border:1px solid var(--line); border-radius:12px; background:#0e0e13;
    }
    .account-manager-item.current {
      border-color:rgba(166,255,0,.42);
      box-shadow:0 0 0 1px rgba(166,255,0,.06);
    }
    .account-manager-main { min-width:0; }
    .account-manager-main strong { display:block; color:var(--soft); font-size:13px; }
    .account-manager-main .email { display:block; color:var(--muted); font-size:10px; margin-top:3px; overflow-wrap:anywhere; }
    .account-manager-main code { display:block; color:var(--muted); font-size:9px; margin-top:3px; }
    .account-manager-actions { display:flex; gap:6px; flex:0 0 auto; }
    .account-manager-actions button { padding:7px 9px; font-size:10px; border-radius:8px; }
    .account-manager-current {
      display:inline-block; margin-left:6px; padding:3px 7px; border-radius:999px;
      background:rgba(166,255,0,.08); color:var(--accent); font-size:8px; font-weight:900;
    }
    .account-manager-empty {
      min-height:100px; display:grid; place-items:center; text-align:center;
      border:1px dashed var(--line); border-radius:12px; color:var(--muted); padding:18px;
    }
    .account-manager-note {
      margin-top:12px; padding:10px 12px; border:1px solid var(--line);
      border-radius:10px; color:var(--muted); font-size:10px; line-height:1.5;
    }
  `;
  document.head.appendChild(style);

  button.addEventListener('click', () => {
    renderAccountManager();
    modal.classList.remove('hidden');
  });

  document.getElementById('accountManagerClose').addEventListener('click', () => {
    modal.classList.add('hidden');
  });

  modal.addEventListener('click', async (e) => {
    if (e.target === modal) {
      modal.classList.add('hidden');
      return;
    }

    const useBtn = e.target.closest('[data-account-use]');
    if (useBtn) {
      const email = useBtn.dataset.accountUse;
      if (!email) return;

      try { await state.bridge?.logout?.(); } catch {}
      localStorage.setItem(PENDING_ACCOUNT_EMAIL_KEY, email);
      location.reload();
      return;
    }

    const deleteBtn = e.target.closest('[data-account-delete]');
    if (deleteBtn) {
      const email = deleteBtn.dataset.accountDelete;
      const ok = window.confirm(`${email}\n\n이 브라우저의 저장된 계정 목록에서 삭제할까요?`);
      if (!ok) return;

      const next = getSavedAccounts().filter(
        item => String(item.email || '').toLowerCase() !== String(email).toLowerCase()
      );
      saveSavedAccounts(next);
      renderAccountManager();
    }
  });

  document.getElementById('accountManagerAdd').addEventListener('click', async () => {
    try { await state.bridge?.logout?.(); } catch {}
    localStorage.removeItem(PENDING_ACCOUNT_EMAIL_KEY);
    location.reload();
  });
}

function renderAccountManager() {
  const root = document.getElementById('accountManagerList');
  if (!root) return;

  const list = getSavedAccounts();
  const currentEmail = String(state.accountEmail || '').toLowerCase();

  if (!list.length) {
    root.innerHTML = `
      <div class="account-manager-empty">
        아직 저장된 계정이 없습니다.<br>
        로그인하면 이 브라우저에 계정이 자동으로 추가됩니다.
      </div>
    `;
    return;
  }

  root.innerHTML = `
    <div class="account-manager-list">
      ${list.map(item => {
        const email = String(item.email || '');
        const current = email.toLowerCase() === currentEmail;
        return `
          <div class="account-manager-item ${current ? 'current' : ''}">
            <div class="account-manager-main">
              <strong>
                ${esc(item.nickname || 'Cosmo user')}
                ${current ? '<span class="account-manager-current">현재 계정</span>' : ''}
              </strong>
              <span class="email">${esc(email)}</span>
              ${item.address ? `<code>${esc(shortAddr(item.address))}</code>` : ''}
            </div>
            <div class="account-manager-actions">
              <button class="secondary" type="button" data-account-use="${esc(email)}">이 계정</button>
              <button class="ghost" type="button" data-account-delete="${esc(email)}">삭제</button>
            </div>
          </div>
        `;
      }).join('')}
    </div>
  `;
}

function setBusy(button, busy, busyText) {
  if (!button.dataset.original) button.dataset.original = button.textContent;
  button.disabled = busy;
  button.textContent = busy ? busyText : button.dataset.original;
}

async function sendCode() {
  const email = els.emailInput.value.trim();
  if (!email) return setLoginStatus('이메일을 입력해 주세요.', 'error');
  setBusy(els.sendCodeBtn, true, '발송 중…');
  setLoginStatus('인증번호를 요청하고 있습니다.');
  try {
    await callBridge('sendCode', email);
    els.codeArea.classList.remove('hidden');
    els.codeInput.focus();
    setLoginStatus('인증번호를 보냈습니다. 이메일을 확인해 주세요.', 'success');
  } catch (err) {
    setLoginStatus(String(err), 'error');
  } finally { setBusy(els.sendCodeBtn, false); }
}

function applyAccountUI(account, email = '') {
  state.account = account;
  state.accountEmail = email || findSavedEmail(account) || state.accountEmail;
  els.accountName.textContent = account?.nickname || 'Cosmo user';
  els.accountAddress.textContent = account?.address || account?.eoa || '-';
  els.loginView.classList.add('hidden');
  els.mainView.classList.remove('hidden');
  els.logoutBtn.classList.remove('hidden');
}

async function restoreSession() {
  if (!state.bridge?.session) return false;
  try {
    const session = await callBridge('session');
    if (!session?.authenticated || !session.account) return false;
    applyAccountUI(session.account, findSavedEmail(session.account));
    await loadCollections();
    await loadSpinStatus(true);
    return true;
  } catch {
    return false;
  }
}

async function login() {
  const email = els.emailInput.value.trim();
  const code = els.codeInput.value.trim();
  if (!email || !code) return setLoginStatus('이메일과 인증번호를 모두 입력해 주세요.', 'error');
  setBusy(els.loginBtn, true, '로그인 중…');
  setLoginStatus('Cosmo 로그인 및 전송용 지갑을 준비하고 있습니다.');
  try {
    const account = await callBridge('login', email, code);
    state.accountEmail = email;
    saveAccountRecord(email, account);
    applyAccountUI(account, email);
    renderAccountManager();
    await loadCollections();
    await loadSpinStatus(true);
  } catch (err) {
    setLoginStatus(String(err), 'error');
  } finally { setBusy(els.loginBtn, false); }
}

async function loadCollections() {
  els.loadingLibrary.classList.remove('hidden');
  els.collectionGrid.innerHTML = '';
  els.emptyLibrary.classList.add('hidden');
  try {
    state.collections = await callBridge('collections');
    state.selected.clear();
    state.expanded.clear();
    populateFilters();
    renderCollections();
    renderSelection();
  } catch (err) {
    toast(String(err), 'error');
  } finally { els.loadingLibrary.classList.add('hidden'); }
}

function populateFilters() {
  const members = [...new Set(state.collections.map(c => c.member).filter(Boolean))].sort((a,b) => a.localeCompare(b));
  const seasons = [...new Set(state.collections.map(c => c.season).filter(Boolean))].sort((a,b) => a.localeCompare(b));
  els.memberFilter.innerHTML = '<option value="">모든 멤버</option>' + members.map(v => `<option>${esc(v)}</option>`).join('');
  els.seasonFilter.innerHTML = '<option value="">모든 시즌</option>' + seasons.map(v => `<option>${esc(v)}</option>`).join('');
}

function filteredCollections() {
  const q = els.cardSearch.value.trim().toLowerCase();
  const member = els.memberFilter.value;
  const season = els.seasonFilter.value;
  const transferableOnly = els.transferableOnly.checked;
  return state.collections.filter(c => {
    if (member && c.member !== member) return false;
    if (season && c.season !== season) return false;
    if (transferableOnly && c.transferableCount < 1) return false;
    if (q && !`${c.member} ${c.collectionNo} ${c.season} ${c.class}`.toLowerCase().includes(q)) return false;
    return true;
  });
}

function collectionKey(c) { return `${c.member}::${c.collectionNo}::${c.season}`; }
function selectedForCollection(c) {
  return [...state.selected.values()].filter(x => x.collection === c);
}

function renderCollections() {
  const list = filteredCollections();
  els.libraryCount.textContent = `${list.length} collections`;
  els.emptyLibrary.classList.toggle('hidden', list.length !== 0);
  els.collectionGrid.innerHTML = list.map((c, idx) => {
    const key = collectionKey(c);
    const expanded = state.expanded.has(key);
    const selected = selectedForCollection(c);
    const image = c.frontImage
      ? `<img class="card-image" src="${esc(c.frontImage)}" alt="${esc(c.member)} ${esc(c.collectionNo)}" loading="lazy" />`
      : `<div class="card-image" style="background:linear-gradient(145deg, ${esc(c.accentColor || '#2c2c33')}33, #23232a)"></div>`;
    const copies = [...c.copies].sort((a,b) => a.serial - b.serial);
    return `<article class="collection-card ${selected.length ? 'has-selection' : ''}" data-index="${idx}" data-key="${esc(key)}">
      <div class="card-top" data-expand="${esc(key)}">
        ${image}
        <div class="card-info">
          <div class="card-title-line"><span class="card-member">${esc(c.member)}</span><span class="card-no">${esc(c.collectionNo)}</span></div>
          <div class="card-meta">${esc(c.season)} · ${esc(c.class)}</div>
          <div class="card-count"><strong>${c.count}</strong><span>owned · ${c.transferableCount} transferable</span></div>
          <div class="card-selected-line">${selected.length ? `선택 ${selected.length}장 · ${selected.map(x => normalizeSerial(x.copy.serial)).join(', ')}` : ''}</div>
          <div class="expand-hint">${expanded ? '시리얼 접기 ▲' : '시리얼 펼치기 ▼'}</div>
        </div>
      </div>
      ${expanded ? `<div class="serial-drawer">
        <div class="serial-tools">
          <input class="qty-box" data-qty="${esc(key)}" type="number" min="1" max="${c.transferableCount}" value="1" aria-label="선택 장수" />
          <button data-auto="low" data-key="${esc(key)}">낮은 시리얼</button>
          <button data-auto="high" data-key="${esc(key)}">높은 시리얼</button>
          <button data-auto="random" data-key="${esc(key)}">랜덤</button>
          <button data-auto="clear" data-key="${esc(key)}">이 카드 해제</button>
        </div>
        <div class="serial-grid">
          ${copies.map(copy => {
            const isSelected = state.selected.has(copy.objektId);
            const locked = !copy.transferable;
            const title = locked ? (copy.usedForGrid ? 'Grid 사용 중 또는 전송 불가' : '전송 불가') : `Objekt ID ${copy.objektId}`;
            return `<button class="serial-chip ${isSelected ? 'selected' : ''} ${locked ? 'locked' : ''}" data-serial-id="${copy.objektId}" data-key="${esc(key)}" ${locked ? 'disabled' : ''} title="${esc(title)}">${normalizeSerial(copy.serial)}</button>`;
          }).join('')}
        </div>
        <div class="serial-legend">취소선/흐린 시리얼은 전송 대상에서 제외됩니다.</div>
      </div>` : ''}
    </article>`;
  }).join('');
}

function findCollectionByKey(key) { return state.collections.find(c => collectionKey(c) === key); }

function toggleSerial(key, objektId) {
  const c = findCollectionByKey(key);
  const copy = c?.copies.find(x => x.objektId === objektId);
  if (!c || !copy?.transferable) return;
  if (state.selected.has(objektId)) state.selected.delete(objektId);
  else state.selected.set(objektId, {copy, collection:c});
  renderCollections();
  renderSelection();
}

function autoSelect(key, mode) {
  const c = findCollectionByKey(key);
  if (!c) return;
  for (const [id, item] of state.selected) if (item.collection === c) state.selected.delete(id);
  if (mode === 'clear') return finishAuto();
  const qtyInput = document.querySelector(`[data-qty="${CSS.escape(key)}"]`);
  const qty = Math.max(1, Math.min(c.transferableCount, Number(qtyInput?.value || 1)));
  let pool = c.copies.filter(x => x.transferable);
  if (mode === 'low') pool.sort((a,b) => a.serial - b.serial);
  if (mode === 'high') pool.sort((a,b) => b.serial - a.serial);
  if (mode === 'random') pool = pool.map(v => ({v, r:Math.random()})).sort((a,b)=>a.r-b.r).map(x=>x.v);
  pool.slice(0, qty).forEach(copy => state.selected.set(copy.objektId, {copy, collection:c}));
  finishAuto();
  function finishAuto() { renderCollections(); renderSelection(); }
}

function groupedSelection() {
  const groups = new Map();
  for (const item of state.selected.values()) {
    const key = collectionKey(item.collection);
    if (!groups.has(key)) groups.set(key, {collection:item.collection, items:[]});
    groups.get(key).items.push(item);
  }
  for (const g of groups.values()) g.items.sort((a,b) => a.copy.serial - b.copy.serial);
  return [...groups.values()];
}

function renderSelection() {
  els.selectedCount.textContent = `${state.selected.size}장`;
  const groups = groupedSelection();
  if (!groups.length) {
    els.selectedSummary.className = 'selected-summary empty-mini';
    els.selectedSummary.textContent = '시리얼을 선택하면 여기에 표시됩니다.';
  } else {
    els.selectedSummary.className = 'selected-summary';
    els.selectedSummary.innerHTML = groups.map(g => `<div class="summary-group">
      <div class="summary-group-head"><strong>${esc(g.collection.member)} ${esc(g.collection.collectionNo)}</strong><span>${g.items.length}장</span></div>
      <div class="summary-serials">${g.items.map(i => normalizeSerial(i.copy.serial)).join(' · ')}</div>
    </div>`).join('');
  }
  els.reviewBtn.disabled = state.selected.size === 0 || !state.recipient || state.sending;
}


function setMode(mode) {
  const spin = mode === 'spin';
  try { localStorage.setItem('cosmoToolMode', spin ? 'spin' : 'transfer'); } catch {}
  els.transferTabBtn?.classList.toggle('active', !spin);
  els.spinTabBtn?.classList.toggle('active', spin);
  els.transferWorkspace?.classList.toggle('hidden', spin);
  els.spinWorkspace?.classList.toggle('hidden', !spin);
  if (spin) loadSpinStatus(true);
}


function formatNextTicket(iso) {
  if (!iso) return '현재 최대 보유 또는 서버 대기시간 없음';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `다음 충전: ${d.toLocaleString('ko-KR')}`;
}

async function loadSpinStatus(quiet = false) {
  if (!state.bridge?.spinStatus || !state.account) return;
  if (!quiet) setBusy(els.spinRefreshBtn, true, '확인 중…');
  try {
    const status = await callBridge('spinStatus');
    state.spin.status = status;
    els.spinTicketCount.textContent = `${status.tickets} / 3`;
    els.spinNextTicket.textContent = formatNextTicket(status.nextReceiveAt);
    els.spinPointBalance.textContent = `${Number(status.pointBalance || 0).toLocaleString()} P`;
    els.spinPrice.textContent = `SPIN 가격 ${Number(status.price || 0).toLocaleString()} P`;
    if (status.pending?.phase === 'handoff-required') {
      els.spinSafetyBadge.textContent = '공식 앱에서 이어서 완료 필요';
      els.spinSafetyBadge.className = 'status-badge error';
    } else if (status.pending) {
      const phaseLabel = status.pending.phase === 'started' ? 'SPIN 시작됨 · 칸 선택 대기' : `SPIN 처리 중 · ${status.pending.phase}`;
      els.spinSafetyBadge.textContent = phaseLabel;
      els.spinSafetyBadge.className = 'status-badge pending';
    } else {
      els.spinSafetyBadge.textContent = '공식 서버 상태 확인 완료';
      els.spinSafetyBadge.className = 'status-badge ready';
    }
    populateSpinFilters();
    renderSpinCollections();
    renderSpinSelection();
    if (status.pending?.phase === 'started') showPendingSpinBoard(status.pending);
  } catch (err) {
    els.spinSafetyBadge.textContent = 'SPIN 상태 오류';
    els.spinSafetyBadge.className = 'status-badge error';
    if (!quiet) toast(String(err), 'error');
  } finally {
    if (!quiet) setBusy(els.spinRefreshBtn, false);
  }
}

function populateSpinFilters() {
  const seasons = state.spin.status?.spinableSeasons || [];
  const current = els.spinSeasonFilter.value;
  els.spinSeasonFilter.innerHTML = '<option value="">모든 Spin 가능 시즌</option>' + seasons.map(v => `<option>${esc(v)}</option>`).join('');
  if (seasons.includes(current)) els.spinSeasonFilter.value = current;
}

function spinableCollections() {
  const allowed = new Set(state.spin.status?.spinableSeasons || []);
  const q = els.spinSearch.value.trim().toLowerCase();
  const season = els.spinSeasonFilter.value;
  return state.collections.filter(c => {
    if (!allowed.has(c.season)) return false;
    if (c.transferableCount < 1) return false;
    if (season && c.season !== season) return false;
    if (q && !`${c.member} ${c.collectionNo} ${c.season} ${c.class}`.toLowerCase().includes(q)) return false;
    return true;
  });
}

function renderSpinCollections() {
  if (!els.spinCollectionGrid) return;
  const list = spinableCollections();
  els.spinEmpty.classList.toggle('hidden', list.length !== 0);
  els.spinCollectionGrid.innerHTML = list.map(c => {
    const copies = [...c.copies].filter(x => x.transferable).sort((a,b)=>a.serial-b.serial);
    const image = c.frontImage ? `<img src="${esc(c.frontImage)}" alt="${esc(c.member)} ${esc(c.collectionNo)}" loading="lazy">` : '<div class="spin-card-placeholder"></div>';
    return `<article class="spin-collection-card">
      <div class="spin-card-main">${image}<div><strong>${esc(c.member)} ${esc(c.collectionNo)}</strong><span>${esc(c.season)} · ${esc(c.class)}</span><small>${copies.length}장 사용 가능</small></div></div>
      <div class="spin-serial-grid">${copies.map(copy => {
        const selected = state.spin.selected?.copy.objektId === copy.objektId;
        return `<button type="button" class="serial-chip ${selected?'selected':''}" data-spin-objekt="${copy.objektId}" data-spin-key="${esc(collectionKey(c))}">${normalizeSerial(copy.serial)}</button>`;
      }).join('')}</div>
    </article>`;
  }).join('');
}

function chooseSpinSerial(key, objektId) {
  if (state.spin.active) return;
  const c = findCollectionByKey(key);
  const copy = c?.copies.find(x => x.objektId === objektId);
  if (!c || !copy?.transferable) return;
  if (!(state.spin.status?.spinableSeasons || []).includes(c.season)) return toast('서버가 Spin 가능으로 표시한 시즌이 아닙니다.', 'error');
  state.spin.selected = {collection:c, copy};
  renderSpinCollections();
  renderSpinSelection();
}

function renderSpinSelection() {
  const item = state.spin.selected;
  if (!item) {
    els.spinSelectedBox.classList.add('hidden');
    return;
  }
  els.spinSelectedBox.classList.remove('hidden');
  els.spinSelectedTitle.textContent = `${item.collection.member} ${item.collection.collectionNo}`;
  els.spinSelectedSerial.textContent = `${item.collection.season} · ${normalizeSerial(item.copy.serial)} · Objekt ${item.copy.objektId}`;
  const tickets = state.spin.status?.tickets ?? 0;
  els.spinStartBtn.disabled = tickets < 1 || !!state.spin.active;
  els.spinStartBtn.textContent = tickets < 1 ? 'Spin Ticket이 없습니다' : '이 시리얼로 SPIN 1회';
}

function renderSpinPickBoard() {
  const chosen = state.spin.chosenIndex;
  els.spinPickGrid.innerHTML = Array.from({length:16}, (_, i) => {
    const isChosen = chosen === i;
    return `<button type="button" class="spin-face-down ${isChosen?'chosen':''}" data-spin-slot="${i}" ${state.spin.completing || chosen !== null ? 'disabled' : ''} aria-label="${i+1}번 칸 선택">
      <span class="spin-slot-mark">S</span>
      <strong>${String(i+1).padStart(2,'0')}</strong>
      <small>${isChosen ? '내 선택' : '선택'}</small>
    </button>`;
  }).join('');
}

function showPendingSpinBoard(pending) {
  if (!pending || pending.phase !== 'started') return;
  if (state.spin.active?.spinId === pending.spinId) return;
  state.spin.active = {spinId:pending.spinId, objektId:pending.objektId, transferHash:pending.transferHash, slots:16};
  state.spin.chosenIndex = null;
  els.spinChooseView.classList.add('hidden');
  els.spinResultView.classList.add('hidden');
  els.spinPickView.classList.remove('hidden');
  els.spinSessionLabel.textContent = `SPIN #${pending.spinId} · 한 칸을 선택하세요`;
  renderSpinPickBoard();
}

async function startSpin() {
  const item = state.spin.selected;
  if (!item || state.spin.active) return;
  if ((state.spin.status?.tickets ?? 0) < 1) return toast('현재 서버 기준 Spin Ticket이 없습니다.', 'error');
  const message = `${item.collection.member} ${item.collection.collectionNo} ${normalizeSerial(item.copy.serial)}\n\n이 Objekt는 SPIN에 사용되며 되돌릴 수 없습니다.\nSpin Ticket 1장을 사용해 1회 진행할까요?`;
  if (!window.confirm(message)) return;

  setBusy(els.spinStartBtn, true, '서버 승인 · Objekt 이동 중…');
  try {
    const started = await callBridge('spinStart', item.copy.objektId);
    state.spin.active = started;
    state.spin.chosenIndex = null;
    state.spin.completing = false;
    els.spinChooseView.classList.add('hidden');
    els.spinResultView.classList.add('hidden');
    els.spinPickView.classList.remove('hidden');
    els.spinSessionLabel.textContent = `SPIN #${started.spinId} · 한 칸을 선택하세요`;
    renderSpinPickBoard();
    toast('SPIN 시작 완료. 16칸 중 원하는 칸을 하나 선택하세요.', 'success');
    await loadSpinStatus(true);
  } catch (err) {
    toast(String(err), 'error');
    await loadSpinStatus(true);
  } finally { setBusy(els.spinStartBtn, false); }
}

async function chooseSpinSlot(index) {
  if (!state.spin.active || state.spin.completing || state.spin.chosenIndex !== null) return;
  state.spin.chosenIndex = index;
  state.spin.completing = true;
  renderSpinPickBoard();
  els.spinSessionLabel.textContent = `${index + 1}번 선택 · 서버 결과 확인 중…`;
  try {
    const result = await callBridge('spinComplete', index);
    const chosenIndex = state.spin.chosenIndex;
    state.spin.active = null;
    state.spin.status = {...(state.spin.status||{}), tickets:result.tickets, nextReceiveAt:result.nextReceiveAt, pending:null};
    renderSpinResult(result, chosenIndex);
    await loadCollections();
    await loadSpinStatus(true);
  } catch (err) {
    toast(String(err), 'error');
    els.spinSessionLabel.textContent = `${index + 1}번 선택 · 결과 확정 실패`;
    els.spinSafetyBadge.textContent = '공식 Cosmo 앱에서 이어서 완료 필요';
    els.spinSafetyBadge.className = 'status-badge error';
    renderSpinPickBoard();
    Array.from(els.spinPickGrid.querySelectorAll('button')).forEach(btn => btn.disabled = true);
  } finally {
    state.spin.completing = false;
  }
}

function renderSpinResult(result, chosenIndex = state.spin.chosenIndex) {
  els.spinPickView.classList.add('hidden');
  els.spinChooseView.classList.add('hidden');
  els.spinResultView.classList.remove('hidden');
  const selected = result.selected;
  const chosenNo = Number.isInteger(chosenIndex) ? chosenIndex + 1 : null;
  const winnerNo = result.winnerIndex >= 0 ? result.winnerIndex + 1 : null;
  if (selected) {
    els.spinResultTitle.textContent = `${selected.member} ${selected.collectionNo}`;
    const marks = [chosenNo ? `내 선택 ${chosenNo}번` : '', winnerNo ? `획득 ${winnerNo}번` : ''].filter(Boolean).join(' · ');
    els.spinResultSub.textContent = `${selected.season} · ${selected.class}${selected.objektNo ? ` · Objekt #${selected.objektNo}` : ''}${marks ? ` · ${marks}` : ''}`;
  } else {
    els.spinResultTitle.textContent = 'SPIN 결과 공개';
    els.spinResultSub.textContent = `${chosenNo ? `내 선택 ${chosenNo}번 · ` : ''}서버가 내려준 16칸 전체 결과입니다.`;
  }
  els.spinResultGrid.innerHTML = result.results.map((card,i) => {
    const won = i === result.winnerIndex;
    const userChoice = i === chosenIndex;
    const cls = ['spin-result-cell', won?'won':'', userChoice?'user-choice':'', !card?'blank':''].filter(Boolean).join(' ');
    const badges = `${userChoice?'<span class="spin-result-badge choice">내 선택</span>':''}${won?'<span class="spin-result-badge win">획득</span>':''}`;
    if (!card) return `<div class="${cls}" style="--reveal-delay:${i*28}ms">${badges}<div class="spin-result-empty">빈칸</div><strong>${String(i+1).padStart(2,'0')}</strong><small>${userChoice?'내가 고른 칸':'빈 결과'}</small></div>`;
    const img = card.thumbnailImage || card.frontImage;
    return `<div class="${cls}" style="--reveal-delay:${i*28}ms">${badges}${img?`<img src="${esc(img)}" loading="lazy" alt="${esc(card.member)} ${esc(card.collectionNo)}">`:'<div class="spin-result-empty">Objekt</div>'}<strong>${esc(card.member)} ${esc(card.collectionNo)}</strong><span>${esc(card.season)} · ${esc(card.class)}</span><small>${String(i+1).padStart(2,'0')}${userChoice?' · 내 선택':''}${won?' · 획득':''}</small></div>`;
  }).join('');
  requestAnimationFrame(() => els.spinResultGrid.classList.add('revealed'));
}

async function resetSpinView() {
  state.spin.selected = null;
  state.spin.active = null;
  state.spin.chosenIndex = null;
  state.spin.completing = false;
  els.spinResultGrid.classList.remove('revealed');
  els.spinResultView.classList.add('hidden');
  els.spinPickView.classList.add('hidden');
  els.spinChooseView.classList.remove('hidden');
  await loadSpinStatus(true);
  renderSpinCollections();
  renderSpinSelection();
}

async function searchRecipient() {
  const q = els.recipientInput.value.trim();
  if (!q) return toast('받는 사람의 Cosmo 닉네임을 입력해 주세요.', 'error');
  setBusy(els.recipientSearchBtn, true, '검색 중…');
  state.recipient = null;
  els.chosenRecipient.classList.add('hidden');
  renderSelection();
  try {
    const users = await callBridge('searchUsers', q);
    els.recipientResults.classList.remove('hidden');
    els.recipientResults.innerHTML = users.length ? users.map((u, i) => `<button type="button" class="recipient-item" data-recipient-index="${i}">
      <strong>${esc(u.nickname)}</strong><code>${esc(u.address)}</code>
    </button>`).join('') : '<div class="empty-mini">검색 결과가 없습니다.</div>';
    els.recipientResults._users = users;
  } catch (err) { toast(String(err), 'error'); }
  finally { setBusy(els.recipientSearchBtn, false); }
}

function chooseRecipient(index) {
  const user = els.recipientResults._users?.[index];
  if (!user) return;
  state.recipient = user;
  els.recipientResults.classList.add('hidden');
  els.chosenRecipient.classList.remove('hidden');
  els.chosenRecipient.innerHTML = `<strong>${esc(user.nickname)}</strong><code>${esc(user.address)}</code>`;
  renderSelection();
}

function openReview() {
  if (!state.recipient || !state.selected.size) return;
  els.confirmRecipient.innerHTML = `<span class="muted">받는 사람</span><br><strong>${esc(state.recipient.nickname)}</strong><code>${esc(state.recipient.address)}</code>`;
  els.confirmList.innerHTML = groupedSelection().map(g => `<div class="confirm-group">
    <strong>${esc(g.collection.member)} ${esc(g.collection.collectionNo)}</strong><span>${g.items.length}장</span>
    <div class="serials">${g.items.map(i => normalizeSerial(i.copy.serial)).join(' · ')}</div>
  </div>`).join('');
  els.startTransferBtn.textContent = `${state.selected.size}장 전송 시작`;
  els.confirmModal.classList.remove('hidden');
}

function transferQueue() {
  return [...state.selected.values()].sort((a,b) => {
    const ak = `${a.collection.member} ${a.collection.collectionNo}`;
    const bk = `${b.collection.member} ${b.collection.collectionNo}`;
    return ak.localeCompare(bk) || a.copy.serial - b.copy.serial;
  });
}

async function startTransfer() {
  if (state.sending || !state.recipient || !state.selected.size) return;
  state.sending = true;
  els.confirmModal.classList.add('hidden');
  els.progressModal.classList.remove('hidden');
  els.progressFooter.classList.add('hidden');
  els.progressTitle.textContent = demoMode ? 'DEMO 전송 진행' : '전송 진행 중';
  els.progressBar.style.width = '0%';

  const queue = transferQueue();
  els.progressRows.innerHTML = queue.map((item, i) => `<div class="progress-row" data-progress="${i}">
    <span>${esc(item.collection.member)} ${esc(item.collection.collectionNo)} · ${normalizeSerial(item.copy.serial)}</span>
    <span class="state">대기</span>
  </div>`).join('');
  els.progressMeta.textContent = `0 / ${queue.length}`;

  let completed = 0;
  let failed = false;
  for (let i = 0; i < queue.length; i++) {
    const item = queue[i];
    const row = document.querySelector(`[data-progress="${i}"]`);
    row.classList.add('sending');
    row.querySelector('.state').textContent = '전송/확인 중';
    try {
      const result = await callBridge('transfer', item.copy.objektId, state.recipient.address);
      row.classList.remove('sending'); row.classList.add('success');
      row.querySelector('.state').textContent = '성공';
      row.title = result.hash || '';
      state.selected.delete(item.copy.objektId);
      completed++;
      els.progressMeta.textContent = `${completed} / ${queue.length}`;
      els.progressBar.style.width = `${(completed / queue.length) * 100}%`;
      if (!demoMode && i < queue.length - 1) await sleep(1500);
    } catch (err) {
      failed = true;
      row.classList.remove('sending'); row.classList.add('fail');
      row.querySelector('.state').textContent = '실패 · 중단';
      const detail = document.createElement('div');
      detail.className = 'error-detail';
      detail.textContent = String(err);
      row.appendChild(detail);
      for (let j = i + 1; j < queue.length; j++) {
        const pending = document.querySelector(`[data-progress="${j}"] .state`);
        if (pending) pending.textContent = '미전송';
      }
      break;
    }
  }

  state.sending = false;
  els.progressTitle.textContent = failed ? '전송이 중단되었습니다' : `전송 완료 · ${completed}장`;
  els.progressFooter.classList.remove('hidden');
  renderSelection();
}

async function closeProgress() {
  els.progressModal.classList.add('hidden');
  if (!demoMode) await loadCollections();
  else { renderCollections(); renderSelection(); }
}


els.spinRefreshBtn?.addEventListener('click', async () => { await loadCollections(); await loadSpinStatus(false); });
els.spinSearch?.addEventListener('input', renderSpinCollections);
els.spinSeasonFilter?.addEventListener('input', renderSpinCollections);
els.spinCollectionGrid?.addEventListener('click', e => {
  const b = e.target.closest('[data-spin-objekt]');
  if (b) chooseSpinSerial(b.dataset.spinKey, Number(b.dataset.spinObjekt));
});
els.spinStartBtn?.addEventListener('click', startSpin);
els.spinPickGrid?.addEventListener('click', e => {
  const slot = e.target.closest('[data-spin-slot]');
  if (slot) chooseSpinSlot(Number(slot.dataset.spinSlot));
});
els.spinNextBtn?.addEventListener('click', resetSpinView);

els.transferTabBtn?.addEventListener('click', () => setMode('transfer'));
els.spinTabBtn?.addEventListener('click', () => setMode('spin'));

els.sendCodeBtn.addEventListener('click', sendCode);
els.loginBtn.addEventListener('click', login);
els.codeInput.addEventListener('keydown', e => { if (e.key === 'Enter') login(); });
els.logoutBtn.addEventListener('click', async () => {
  try { await state.bridge?.logout?.(); } catch {}
  location.reload();
});
els.refreshBtn.addEventListener('click', loadCollections);
[els.cardSearch, els.memberFilter, els.seasonFilter, els.transferableOnly].forEach(el => el.addEventListener('input', renderCollections));

els.collectionGrid.addEventListener('click', (e) => {
  const expand = e.target.closest('[data-expand]');
  if (expand && !e.target.closest('button')) {
    const key = expand.dataset.expand;
    state.expanded.has(key) ? state.expanded.delete(key) : state.expanded.add(key);
    return renderCollections();
  }
  const serial = e.target.closest('[data-serial-id]');
  if (serial) return toggleSerial(serial.dataset.key, Number(serial.dataset.serialId));
  const auto = e.target.closest('[data-auto]');
  if (auto) return autoSelect(auto.dataset.key, auto.dataset.auto);
});

els.recipientSearchBtn.addEventListener('click', searchRecipient);
els.recipientInput.addEventListener('keydown', e => { if (e.key === 'Enter') searchRecipient(); });
els.recipientResults.addEventListener('click', e => {
  const item = e.target.closest('[data-recipient-index]');
  if (item) chooseRecipient(Number(item.dataset.recipientIndex));
});
els.reviewBtn.addEventListener('click', openReview);
els.startTransferBtn.addEventListener('click', startTransfer);
els.progressCloseBtn.addEventListener('click', closeProgress);
$$('[data-close]').forEach(btn => btn.addEventListener('click', () => $(`#${btn.dataset.close}`).classList.add('hidden')));

(async () => {
  try {
    installAccountManager();

    const pendingEmail = (() => {
      try { return localStorage.getItem(PENDING_ACCOUNT_EMAIL_KEY) || ''; }
      catch { return ''; }
    })();

    if (pendingEmail) {
      els.emailInput.value = pendingEmail;
      try { localStorage.removeItem(PENDING_ACCOUNT_EMAIL_KEY); } catch {}
      setLoginStatus('저장된 계정입니다. 인증번호를 받아 로그인하세요.');
    }

    await initLocalApp();
    if (!demoMode) setBridgeBadge('로컬 연결 완료', 'ready');
    const restored = await restoreSession();
    if (restored) {
      if (!demoMode) setBridgeBadge('로그인 유지 · 로컬 연결 완료', 'ready');
      renderAccountManager();
      let savedMode = 'transfer';
      try { savedMode = localStorage.getItem('cosmoToolMode') || 'transfer'; } catch {}
      if (state.spin.status?.pending || savedMode === 'spin') setMode('spin');
    }
  } catch (err) {
    setBridgeBadge('연결 오류', 'error');
    setLoginStatus(String(err), 'error');
    els.sendCodeBtn.disabled = true;
    els.loginBtn.disabled = true;
  }
})();

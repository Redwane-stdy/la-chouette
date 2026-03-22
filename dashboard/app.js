/**
 * LA CHOUETTE — MOTEUR DE CORRÉLATION v2.0
 * Dashboard JavaScript — vanilla pur, zéro dépendance externe.
 *
 * Flux d'état :
 *   WAITING (expérience non démarrée) → polling GET /api/experiment/ready
 *   RUNNING (expérience démarrée)     → polling toutes les 700ms sur tous les endpoints
 *   SOLVED                             → affichage final, boutons STOP/LOGS
 */

'use strict';

/* ============================================================================
   CONFIG
   ============================================================================ */

/** Toutes les requêtes passent par le proxy nginx (pas de CORS). */
const API_BASE        = '/api';

/** Intervalle de polling principal (ms) — recommandé : 700ms. */
const POLL_MS         = 700;

/** Intervalle de polling "ready" en état WAITING (ms). */
const READY_POLL_MS   = 1000;

/** Nombre max d'équations affichées dans le panneau. */
const MAX_EQ_SHOWN    = 50;

/** Nombre max de messages de bruit affichés. */
const MAX_NOISE_SHOWN = 30;

/** Durée de l'animation "nouveau" en ms (doit correspondre aux @keyframes CSS). */
const FLASH_DURATION  = 800;

/* ============================================================================
   ÉTAT GLOBAL
   ============================================================================ */

/** Phase actuelle du dashboard. */
let appPhase = 'waiting'; // 'waiting' | 'running'

/** Identifiants des variables déjà résolues (pour détecter les nouvelles résolutions). */
let knownResolvedVars = new Set();

/** Identifiants des équations déjà affichées (pour détecter les nouvelles). */
let knownEquationIds  = new Set();

/** Nombre de messages de bruit connus au dernier cycle. */
let knownNoiseCount   = 0;

/** L'API était-elle joignable au dernier cycle ? */
let apiWasOnline      = false;

/** Timer du polling WAITING. */
let readyPollTimer    = null;

/** Timer du polling RUNNING. */
let mainPollTimer     = null;

/* ============================================================================
   UTILITAIRES
   ============================================================================ */

/**
 * Renvoie l'heure locale HH:MM:SS.
 * @returns {string}
 */
function timeStr() {
  const d = new Date();
  return [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map(n => String(n).padStart(2, '0'))
    .join(':');
}

/**
 * Formate un nombre de secondes en "Xm XXs" ou "XXs".
 * @param {number} sec
 * @returns {string}
 */
function fmtElapsed(sec) {
  if (typeof sec !== 'number' || isNaN(sec)) return '--';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (h > 0) return `${h}h ${String(m).padStart(2,'0')}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2,'0')}s`;
  return `${s}s`;
}

/**
 * Échappe les caractères HTML dangereux pour éviter les injections XSS.
 * À utiliser avant toute injection via innerHTML.
 * @param {string} str
 * @returns {string}
 */
function esc(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

/**
 * Formate un coefficient et un nom de variable en texte d'équation.
 * Règles :
 *   - Premier terme positif : "2.50*x3"
 *   - Premier terme négatif : "-2.50*x3"
 *   - Termes suivants positifs : " + 1.04*x9"
 *   - Termes suivants négatifs : " + (-1.98)*x13"  (parenthèses pour lisibilité)
 *
 * @param {Object}  coeffs   - ex: { x1: 2.5, x3: -1.2 }
 * @param {number}  constant - membre droit
 * @returns {string}
 */
function formatEquation(coeffs, constant) {
  if (!coeffs || typeof coeffs !== 'object') return '(équation invalide)';

  const entries = Object.entries(coeffs).filter(([, v]) => v !== 0);
  if (entries.length === 0) return `0 = ${constant.toFixed(2)}`;

  let expr = '';
  entries.forEach(([varName, coeff], i) => {
    const absCoeff = Math.abs(coeff);
    const coeffStr = absCoeff.toFixed(2);

    if (i === 0) {
      // Premier terme
      if (coeff < 0) {
        expr += `-${coeffStr}*${varName}`;
      } else {
        expr += `${coeffStr}*${varName}`;
      }
    } else {
      // Termes suivants
      if (coeff < 0) {
        // Négatif : afficher entre parenthèses pour la lisibilité
        expr += ` + (-${coeffStr})*${varName}`;
      } else {
        expr += ` + ${coeffStr}*${varName}`;
      }
    }
  });

  return `${expr} = ${Number(constant).toFixed(2)}`;
}

/* ============================================================================
   FETCH HELPERS
   ============================================================================ */

/**
 * Fetch JSON générique avec timeout et gestion silencieuse des erreurs réseau.
 * Retourne null si l'API est indisponible.
 *
 * @param {string} url
 * @param {object} [options={}] - options fetch (method, body, etc.)
 * @param {number} [timeoutMs=3000]
 * @returns {Promise<any|null>}
 */
async function fetchJson(url, options = {}, timeoutMs = 3000) {
  const ctrl  = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const resp = await fetch(url, { ...options, signal: ctrl.signal });
    clearTimeout(timer);
    if (!resp.ok) {
      console.warn(`[CHOUETTE] HTTP ${resp.status} — ${url}`);
      return null;
    }
    // Réponses sans corps (204, etc.) — retourner objet vide
    const ct = resp.headers.get('content-type') || '';
    if (!ct.includes('application/json')) return {};
    return await resp.json();
  } catch (err) {
    clearTimeout(timer);
    if (err.name !== 'AbortError') {
      console.debug(`[CHOUETTE] fetch échoué (${url}):`, err.message);
    }
    return null;
  }
}

/**
 * Récupère en parallèle tous les endpoints nécessaires au dashboard RUNNING.
 * Un seul cycle de Promise.all pour minimiser la latence perçue.
 *
 * @returns {Promise<{status, variables, equations, noise, metrics}>}
 */
async function fetchAllData() {
  const [status, variables, equations, noise, metrics] = await Promise.all([
    fetchJson(`${API_BASE}/status`),
    fetchJson(`${API_BASE}/variables`),
    fetchJson(`${API_BASE}/equations`),
    fetchJson(`${API_BASE}/noise`),
    fetchJson(`${API_BASE}/metrics`),
  ]);

  return {
    status,
    variables : Array.isArray(variables)  ? variables  : [],
    equations : Array.isArray(equations)  ? equations  : [],
    noise     : Array.isArray(noise)      ? noise      : [],
    metrics,
  };
}

/* ============================================================================
   TRANSITIONS D'ÉTAT
   ============================================================================ */

/**
 * Bascule de l'écran WAITING vers l'écran RUNNING.
 * Arrête le polling "ready", démarre le polling principal.
 */
function transitionToRunning() {
  if (appPhase === 'running') return;
  appPhase = 'running';

  // Arrêter le polling WAITING
  clearInterval(readyPollTimer);

  // Afficher l'écran RUNNING
  document.getElementById('screen-waiting').style.display = 'none';
  document.getElementById('screen-running').style.display = 'flex';

  // Démarrer la boucle principale
  mainTick();
  mainPollTimer = setInterval(mainTick, POLL_MS);
}

/* ============================================================================
   POLLING WAITING — attend que l'expérience soit prête
   ============================================================================ */

/**
 * Vérifie périodiquement si l'expérience a été démarrée (par un autre client,
 * ou après un rechargement de page). Si ready=true, bascule vers RUNNING.
 */
async function waitingTick() {
  const data = await fetchJson(`${API_BASE}/experiment/ready`);
  if (data && data.ready === true) {
    transitionToRunning();
  }
}

/* ============================================================================
   ACTION — START
   ============================================================================ */

/**
 * Appelle POST /api/experiment/start puis bascule vers RUNNING.
 * Gère les erreurs et les états de chargement du bouton.
 */
async function handleStart() {
  const btn      = document.getElementById('btn-start');
  const errorEl  = document.getElementById('start-error');

  btn.disabled    = true;
  btn.querySelector('.btn-start-label').textContent = '... DÉMARRAGE ...';
  errorEl.style.display = 'none';

  try {
    const resp = await fetch(`${API_BASE}/experiment/start`, { method: 'POST' });
    if (resp.ok || resp.status === 200 || resp.status === 204 || resp.status === 409) {
      // 409 = déjà démarré — on bascule quand même
      transitionToRunning();
    } else {
      throw new Error(`HTTP ${resp.status}`);
    }
  } catch (err) {
    // Réactiver le bouton et afficher l'erreur
    btn.disabled = false;
    btn.querySelector('.btn-start-label').textContent = '&#9654; START EXPÉRIENCE';
    errorEl.textContent = `Erreur : impossible de contacter l'API (${err.message}). Vérifiez que docker compose est démarré.`;
    errorEl.style.display = 'block';
  }
}

/* ============================================================================
   ACTIONS — RESET / PURGE / MODALES
   ============================================================================ */

/**
 * Réinitialise l'expérience après confirmation.
 * Appelle POST /api/experiment/reset.
 */
async function handleReset() {
  const ok = window.confirm(
    'Réinitialiser l\'expérience ? Les données de résolution seront effacées.'
  );
  if (!ok) return;

  const resp = await fetchJson(`${API_BASE}/experiment/reset`, { method: 'POST' });
  if (resp !== null) {
    // Réinitialiser le tracking local
    knownResolvedVars = new Set();
    knownEquationIds  = new Set();
    knownNoiseCount   = 0;
    console.info('[CHOUETTE] Expérience réinitialisée.');
  } else {
    alert('Erreur lors de la réinitialisation. Consultez les logs.');
  }
}

/**
 * Purge totale avec double confirmation.
 * Appelle DELETE /api/experiment/purge.
 */
async function handlePurge() {
  const first = window.confirm(
    'PURGE : supprimer TOUTES les données ? Irréversible.'
  );
  if (!first) return;

  const typed = window.prompt('Tapez PURGE pour confirmer');
  if (typed !== 'PURGE') {
    alert('Purge annulée (mot de confirmation incorrect).');
    return;
  }

  const resp = await fetchJson(`${API_BASE}/experiment/purge`, { method: 'DELETE' });
  if (resp !== null) {
    knownResolvedVars = new Set();
    knownEquationIds  = new Set();
    knownNoiseCount   = 0;
    console.info('[CHOUETTE] Purge effectuée.');
  } else {
    alert('Erreur lors de la purge. Consultez les logs.');
  }
}

/** Affiche la modale STOP & SUPPRIMER. */
function showStopModal() {
  document.getElementById('modal-stop').style.display = 'flex';
}

/** Affiche la modale INSPECTER LES LOGS. */
function showLogsModal() {
  document.getElementById('modal-logs').style.display = 'flex';
}

/**
 * Ferme une modale par son id.
 * @param {string} id
 */
function closeModal(id) {
  document.getElementById(id).style.display = 'none';
}

/**
 * Copie un texte dans le presse-papier et donne un retour visuel au bouton.
 * @param {string} text
 * @param {HTMLElement} btn
 */
async function copyText(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const orig = btn.textContent;
    btn.textContent = '[ COPIÉ ✓ ]';
    btn.classList.add('copied');
    setTimeout(() => {
      btn.textContent = orig;
      btn.classList.remove('copied');
    }, 1800);
  } catch {
    // Fallback pour navigateurs sans clipboard API
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity  = '0';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    btn.textContent = '[ COPIÉ ✓ ]';
    setTimeout(() => { btn.textContent = '[ COPIER ]'; }, 1800);
  }
}

/* ============================================================================
   RENDERERS
   ============================================================================ */

/* ----------------------------------------------------------------------------
   Horloge
   ---------------------------------------------------------------------------- */

function renderClock() {
  const el = document.getElementById('clock');
  if (el) el.textContent = timeStr();
}

/* ----------------------------------------------------------------------------
   Indicateur API
   ---------------------------------------------------------------------------- */

/**
 * Met à jour le petit indicateur API en haut à droite.
 * @param {boolean} online
 */
function renderApiIndicator(online) {
  const el = document.getElementById('api-indicator');
  if (!el) return;
  if (online) {
    el.textContent = '✓ API';
    el.className   = 'api-indicator api-online';
  } else {
    el.textContent = '✗ API';
    el.className   = 'api-indicator api-offline';
  }
}

/* ----------------------------------------------------------------------------
   Panneau TOP — statut, progress, compteurs
   ---------------------------------------------------------------------------- */

/**
 * Table de correspondance statut → couleur CSS.
 */
const STATUS_CLASS = {
  COLLECTING : 'status-collecting',
  CORRELATING: 'status-correlating',
  CONVERGING : 'status-converging',
  SOLVED     : 'status-solved',
};

/**
 * Met à jour tout le panneau supérieur (status, progress bar, compteurs).
 *
 * @param {object|null} status   - réponse de GET /api/status
 * @param {object|null} metrics  - réponse de GET /api/metrics
 */
function renderTopPanel(status, metrics) {
  if (!status) return;

  const statusKey = (status.status || 'COLLECTING').toUpperCase();
  const cssClass  = STATUS_CLASS[statusKey] || 'status-collecting';

  /* -- Grand statut -- */
  const bigEl = document.getElementById('top-status-big');
  if (bigEl) {
    bigEl.textContent = statusKey;
    bigEl.className   = `top-status-big ${cssClass}`;
  }

  /* -- Statut dans le header aussi -- */
  const hdrEl = document.getElementById('hdr-status-text');
  if (hdrEl) {
    hdrEl.textContent = statusKey;
    hdrEl.className   = `hdr-status-text ${cssClass}`;
  }

  /* -- Barre de progression -- */
  const pct   = Math.min(100, Math.max(0, status.completionPct ?? 0));
  const fillEl = document.getElementById('top-progress-fill');
  const lblEl  = document.getElementById('top-progress-label');
  const varRes  = status.variablesResolved ?? 0;
  const varTot  = status.variablesTotal    ?? 15;
  if (fillEl) fillEl.style.width = `${pct}%`;
  if (lblEl)  lblEl.textContent  = `${Math.round(pct)}% (${varRes}/${varTot} variables résolues)`;

  /* -- Bannière SOLVED -- */
  const solvedBanner = document.getElementById('solved-banner');
  if (solvedBanner) {
    solvedBanner.style.display = (statusKey === 'SOLVED') ? '' : 'none';
  }

  /* -- Boutons SOLVED -- */
  const btnStop = document.getElementById('btn-stop-delete');
  const btnLogs = document.getElementById('btn-inspect-logs');
  if (btnStop) btnStop.style.display = (statusKey === 'SOLVED') ? '' : 'none';
  if (btnLogs) btnLogs.style.display = (statusKey === 'SOLVED') ? '' : 'none';

  /* -- Compteurs -- */
  const cntEq      = document.getElementById('cnt-eq');
  const cntVar     = document.getElementById('cnt-var');
  const cntElapsed = document.getElementById('cnt-elapsed');
  const cntNoise   = document.getElementById('cnt-noise');
  const cntEqps    = document.getElementById('cnt-eqps');

  if (cntEq)      cntEq.textContent      = status.equationsReceived ?? 0;
  if (cntVar)     cntVar.textContent     = `${varRes}/${varTot}`;
  if (cntElapsed) cntElapsed.textContent = fmtElapsed(status.elapsedSeconds);
  if (cntNoise)   cntNoise.textContent   = status.noiseMessagesReceived ?? 0;
  if (cntEqps && metrics) {
    const eps = metrics.equationsPerSecond;
    cntEqps.textContent = (typeof eps === 'number') ? eps.toFixed(2) : '--';
  }

  /* -- Message de statut -- */
  const msgEl = document.getElementById('top-status-msg');
  if (msgEl) {
    msgEl.textContent = status.statusMessage ||
      (statusKey === 'SOLVED' ? '★ Système résolu — corrélation complète !' : 'En cours d\'investigation...');
  }
}

/* ----------------------------------------------------------------------------
   Panneau VARIABLES
   ---------------------------------------------------------------------------- */

/**
 * Met à jour la liste des variables. Trie : résolues en tête, puis inconnues.
 * Anime visuellement les nouvelles résolutions.
 *
 * @param {Array} variables - tableau de variables depuis GET /api/variables
 */
function renderVariables(variables) {
  const listEl   = document.getElementById('vars-list');
  const countEl  = document.getElementById('vars-title-count');
  if (!listEl) return;

  if (!variables || variables.length === 0) {
    listEl.innerHTML = '<div class="placeholder">En attente des variables...</div>';
    if (countEl) countEl.textContent = '(0/15)';
    return;
  }

  // Détecter les nouvelles résolutions (pour l'animation)
  const newlyResolved = new Set();
  variables.forEach(v => {
    if (v.status === 'RESOLVED' && !knownResolvedVars.has(v.name)) {
      newlyResolved.add(v.name);
      knownResolvedVars.add(v.name);
    }
  });

  // Mettre à jour le compteur dans le titre
  const resolvedCount = variables.filter(v => v.status === 'RESOLVED').length;
  const totalCount    = variables.length;
  if (countEl) countEl.textContent = `(${resolvedCount}/${totalCount})`;

  // Tri : résolues d'abord, puis par nom (x1, x2 … x15)
  const sorted = [...variables].sort((a, b) => {
    const aR = a.status === 'RESOLVED' ? 0 : 1;
    const bR = b.status === 'RESOLVED' ? 0 : 1;
    if (aR !== bR) return aR - bR;
    // Tri numérique : extraire le numéro du nom (ex: x3 → 3)
    const aNum = parseInt((a.name || '').replace(/\D/g, ''), 10) || 0;
    const bNum = parseInt((b.name || '').replace(/\D/g, ''), 10) || 0;
    return aNum - bNum;
  });

  // Construction HTML
  const rows = sorted.map(v => {
    const isResolved = v.status === 'RESOLVED';
    const isNew      = newlyResolved.has(v.name);

    let rowClass = isResolved ? 'var-row var-resolved' : 'var-row var-unknown';
    if (isNew) rowClass += ' var-new-resolved';

    const namePad  = esc((v.name || '?').padEnd(3, ' '));
    let valueStr, checkMark, badge, errStr;

    if (isResolved) {
      const val     = typeof v.computedValue === 'number' ? v.computedValue : parseFloat(v.computedValue);
      valueStr      = isNaN(val) ? '??????????'.padStart(12) : val.toFixed(4).padStart(12, ' ');
      const absErr  = typeof v.absoluteError === 'number' ? v.absoluteError : 0;
      checkMark     = `<span class="var-check">&#10003;</span>`;
      badge         = `<span class="var-badge-resolved">[RÉSOLU]</span>`;
      errStr        = `<span class="var-err">err:${absErr.toFixed(6)}</span>`;
    } else {
      valueStr  = '???         ';
      checkMark = `<span class="var-unknown-mark">?</span>`;
      badge     = `<span class="var-badge-unknown">[INCONNU]</span>`;
      errStr    = '';
    }

    return `<div class="${rowClass}">
      <span class="var-name">${namePad}</span>
      <span class="var-eq">=</span>
      <span class="var-value">${esc(valueStr.trim())}</span>
      ${checkMark}
      ${badge}
      ${errStr}
    </div>`;
  });

  listEl.innerHTML = rows.join('');
}

/* ----------------------------------------------------------------------------
   Panneau ÉQUATIONS
   ---------------------------------------------------------------------------- */

/**
 * Source → classe CSS et label d'affichage.
 * @param {string} src
 * @returns {{ cls: string, label: string }}
 */
function sourceStyle(src) {
  switch ((src || '').toLowerCase()) {
    case 'alpha': return { cls: 'eq-source-alpha', label: '[ALPHA]' };
    case 'beta':  return { cls: 'eq-source-beta',  label: '[BETA]'  };
    case 'gamma': return { cls: 'eq-source-gamma', label: '[GAMMA]' };
    default:      return { cls: 'eq-source-alpha', label: `[${(src || '?').toUpperCase()}]` };
  }
}

/**
 * Met à jour la liste des équations.
 * Détecte les nouvelles pour animer leur apparition.
 * Limite à MAX_EQ_SHOWN entrées (les plus récentes en bas).
 *
 * @param {Array} equations
 */
function renderEquations(equations) {
  const listEl  = document.getElementById('equations-list');
  const countEl = document.getElementById('eq-title-count');
  if (!listEl) return;

  if (!equations || equations.length === 0) {
    listEl.innerHTML = '<div class="placeholder">En attente des équations...</div>';
    if (countEl) countEl.textContent = '(0/25)';
    return;
  }

  if (countEl) countEl.textContent = `(${equations.length}/25)`;

  // Tronquer si nécessaire — garder les plus récentes
  const visible = equations.slice(-MAX_EQ_SHOWN);

  // Vérifier si on était en bas avant le rendu (pour l'auto-scroll)
  const wasAtBottom = listEl.scrollHeight - listEl.clientHeight - listEl.scrollTop < 30;

  const rows = visible.map(eq => {
    const id      = eq.equationId || eq.id || '?';
    const src     = eq.source || 'unknown';
    const isNew   = !knownEquationIds.has(id);
    if (isNew) knownEquationIds.add(id);

    const { cls, label } = sourceStyle(src);
    const srcRowCls      = `eq-${src.toLowerCase()}`;
    const newCls         = isNew ? ' eq-new' : '';
    const formula        = formatEquation(eq.coefficients, eq.constant);

    return `<div class="eq-row ${srcRowCls}${newCls}">
      <span class="eq-id">${esc(id)}</span>
      <span class="${cls}">${esc(label)}</span>
      <span class="eq-formula">${esc(formula)}</span>
    </div>`;
  });

  listEl.innerHTML = rows.join('');

  // Auto-scroll vers le bas si on y était
  if (wasAtBottom) {
    listEl.scrollTop = listEl.scrollHeight;
  }
}

/* ----------------------------------------------------------------------------
   Panneau BRUIT
   ---------------------------------------------------------------------------- */

/**
 * Met à jour la liste des messages de bruit.
 * Conserve les MAX_NOISE_SHOWN plus récents.
 *
 * @param {Array} noiseMessages
 */
function renderNoise(noiseMessages) {
  const listEl  = document.getElementById('noise-list');
  const countEl = document.getElementById('noise-title-count');
  if (!listEl) return;

  if (!noiseMessages || noiseMessages.length === 0) {
    listEl.innerHTML = '<div class="placeholder">Aucun bruit reçu...</div>';
    if (countEl) countEl.textContent = '(0 msgs)';
    return;
  }

  if (countEl) countEl.textContent = `(${noiseMessages.length} msgs)`;

  const visible = noiseMessages.slice(-MAX_NOISE_SHOWN);
  const wasAtBottom = listEl.scrollHeight - listEl.clientHeight - listEl.scrollTop < 30;

  const rows = visible.map(n => {
    const src = esc(n.source || 'noise');
    const txt = esc(n.text   || n.message || JSON.stringify(n));
    return `<div class="noise-row">
      <span class="noise-src">[${src}]</span>
      <span class="noise-txt">${txt}</span>
    </div>`;
  });

  listEl.innerHTML = rows.join('');

  if (wasAtBottom) {
    listEl.scrollTop = listEl.scrollHeight;
  }
}

/* ============================================================================
   BOUCLE PRINCIPALE (phase RUNNING)
   ============================================================================ */

/**
 * Un cycle complet de polling :
 *   1. Récupérer toutes les données en parallèle
 *   2. Mettre à jour chaque panneau
 *   3. Mettre à jour les indicateurs globaux
 */
async function mainTick() {
  const data   = await fetchAllData();
  const online = data.status !== null || data.metrics !== null;

  renderApiIndicator(online);
  apiWasOnline = online;

  renderTopPanel(data.status, data.metrics);
  renderVariables(data.variables);
  renderEquations(data.equations);
  renderNoise(data.noise);
}

/* ============================================================================
   INITIALISATION
   ============================================================================ */

/**
 * Point d'entrée principal.
 * Démarre l'horloge, le polling WAITING, et attend le signal START.
 */
function init() {
  // Horloge — mise à jour chaque seconde, indépendamment du polling API
  setInterval(renderClock, 1000);
  renderClock();

  // Polling WAITING : vérifie si l'expérience est déjà démarrée
  // (utile si l'utilisateur ouvre le dashboard après le démarrage)
  readyPollTimer = setInterval(waitingTick, READY_POLL_MS);
  // Premier check immédiat
  waitingTick();
}

// Démarrer dès que le DOM est prêt
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}

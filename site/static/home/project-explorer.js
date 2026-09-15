const projectFiles = {
  connection: { path: 'connections/olist.yaml', label: '01 / CONNECT', detail: 'Connect to your data.' },
  source: { path: 'sources/olist.payments.yaml', label: '02 / DEFINE SOURCES', detail: 'Name the tables, files, and fields you need.' },
  model: { path: 'models/sales_orders.yaml', label: '03 / MODEL DATA', detail: 'Transform raw data with SQL.' },
  semantics: { path: 'semantic-models/sales.yaml', label: '04 / DEFINE METRICS', detail: 'Define the metrics and dimensions your team will use.' },
  pipeline: { path: 'pipelines/sales-refresh.yaml', label: '05 / REFRESH DATA', detail: 'Choose which models to refresh.' },
  dashboard: { path: 'dashboards/executive-sales.yaml', label: '06 / BUILD DASHBOARDS', detail: 'Turn your metrics into charts and tables.' }
};
const walkthrough = ['connection', 'source', 'model', 'semantics'];
const phaseDuration = 5500;

const explorer = document.querySelector('#project-explorer');
explorer.style.setProperty('--project-phase-duration', `${phaseDuration}ms`);
const tabList = explorer.querySelector('.project-tabs');
const tabs = [...tabList.querySelectorAll('[data-project-file]')];
const panel = explorer.querySelector('#project-file-panel');
const phaseCopy = document.querySelector('.project-phase-copy');
const phaseLabel = document.querySelector('#project-phase-label');
const phaseDetail = document.querySelector('#project-phase-detail');
const code = document.querySelector('#project-code');
const codeScroll = document.querySelector('#project-code-scroll');
const fileCache = new Map();
const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
let latestSelection = 0;
let phaseIndex = 0;
let inView = false;
let userPaused = false;
let timer;

function revealTab(tab) {
  const listBounds = tabList.getBoundingClientRect();
  const tabBounds = tab.getBoundingClientRect();
  tabList.scrollLeft += (tabBounds.left + tabBounds.width / 2) - (listBounds.left + listBounds.width / 2);
}

function addToken(parent, className, value) {
  const span = document.createElement('span');
  span.className = className;
  span.textContent = value;
  parent.append(span);
}

function renderLine(line, container) {
  if (/^\s*#/.test(line)) {
    addToken(container, 'project-syntax-comment', line);
    return;
  }
  const property = line.match(/^(\s*(?:-\s+)?)([\w.-]+)(:)(.*)$/);
  if (property) {
    container.append(document.createTextNode(property[1]));
    addToken(container, 'project-syntax-key', property[2]);
    container.append(document.createTextNode(property[3]));
    if (['kind', 'type', 'format', 'aggregation', 'semanticModel'].includes(property[2])) {
      addToken(container, 'project-syntax-value', property[4]);
    } else {
      container.append(document.createTextNode(property[4]));
    }
    return;
  }
  const sql = line.match(/^(\s*)(WITH|SELECT|FROM|WHERE|JOIN|LEFT JOIN|GROUP BY|ORDER BY|LIMIT)(\b.*)$/i);
  if (sql) {
    container.append(document.createTextNode(sql[1]));
    addToken(container, 'project-syntax-sql', sql[2]);
    container.append(document.createTextNode(sql[3]));
    return;
  }
  container.textContent = line;
}

function renderSource(source, animate = false) {
  const lines = source.trimEnd().split('\n');
  const fragment = document.createDocumentFragment();
  lines.forEach((line, index) => {
    const row = document.createElement('span');
    row.className = 'project-code-row';
    if (animate && index >= 2 && index < 18) {
      row.classList.add('is-entering');
      row.style.setProperty('--enter-order', index - 2);
    }
    const number = document.createElement('span');
    number.className = 'project-line-number';
    number.setAttribute('aria-hidden', 'true');
    number.textContent = String(index + 1).padStart(2, '0');
    const content = document.createElement('span');
    content.className = 'project-code-text';
    renderLine(line, content);
    row.append(number, content);
    fragment.append(row);
  });
  code.replaceChildren(fragment);
  codeScroll.scrollTo(0, 0);
}

async function selectFile(key, animate = false) {
  const file = projectFiles[key];
  if (!file) return;
  const selection = ++latestSelection;
  const selectedTab = tabs.find(tab => tab.dataset.projectFile === key);
  tabs.forEach(tab => {
    const selected = tab === selectedTab;
    tab.setAttribute('aria-selected', String(selected));
    tab.tabIndex = selected ? 0 : -1;
  });
  panel.setAttribute('aria-labelledby', selectedTab.id);
  revealTab(selectedTab);
  const phaseChanged = phaseLabel.textContent !== file.label || phaseDetail.textContent !== file.detail;
  if (phaseChanged) {
    phaseCopy.classList.remove('is-changing');
    void phaseCopy.offsetWidth;
  }
  phaseLabel.textContent = file.label;
  phaseDetail.textContent = file.detail;
  if (phaseChanged && !reducedMotion.matches) phaseCopy.classList.add('is-changing');
  document.querySelector('#project-file-path').textContent = `sales-project / ${file.path.replace('/', ' / ')}`;
  codeScroll.setAttribute('aria-label', `${file.path} source code`);

  if (fileCache.has(key)) {
    renderSource(fileCache.get(key), animate);
    return;
  }
  code.textContent = 'Loading file…';
  try {
    const response = await fetch(`/static/home/project-files/${file.path}`);
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const source = await response.text();
    fileCache.set(key, source);
    if (selection === latestSelection) renderSource(source, animate);
  } catch {
    if (selection === latestSelection) {
      code.textContent = 'Could not load this file.';
    }
  }
}

function canAdvance() {
  return inView && !userPaused && phaseIndex < walkthrough.length - 1 &&
    !reducedMotion.matches && !document.hidden &&
    !explorer.contains(document.activeElement);
}

function scheduleNextPhase() {
  clearTimeout(timer);
  explorer.classList.remove('is-autoplay');
  if (!canAdvance()) return;
  void explorer.offsetWidth;
  explorer.classList.add('is-autoplay');
  timer = setTimeout(() => {
    phaseIndex += 1;
    selectFile(walkthrough[phaseIndex], true);
    scheduleNextPhase();
  }, phaseDuration);
}

function pauseForSelection(key) {
  userPaused = true;
  clearTimeout(timer);
  explorer.classList.remove('is-autoplay');
  selectFile(key);
}

tabs.forEach((tab, index) => {
  tab.addEventListener('click', () => pauseForSelection(tab.dataset.projectFile));
  tab.addEventListener('keydown', event => {
    const nextIndex = event.key === 'ArrowRight' ? index + 1 : event.key === 'ArrowLeft' ? index - 1 : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1;
    if (nextIndex === -1 && !['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
    event.preventDefault();
    const next = tabs[(nextIndex + tabs.length) % tabs.length];
    next.focus({ preventScroll: true });
    pauseForSelection(next.dataset.projectFile);
  });
});

selectFile(walkthrough[0]);
if ('IntersectionObserver' in window) {
  new IntersectionObserver(([entry]) => {
    inView = entry.isIntersecting;
    scheduleNextPhase();
  }, { threshold: .3 }).observe(explorer.closest('.project-section'));
} else {
  inView = true;
  scheduleNextPhase();
}
explorer.addEventListener('focusin', scheduleNextPhase);
explorer.addEventListener('focusout', () => requestAnimationFrame(scheduleNextPhase));
codeScroll.addEventListener('pointerdown', () => {
  userPaused = true;
  clearTimeout(timer);
  explorer.classList.remove('is-autoplay');
});
document.addEventListener('visibilitychange', scheduleNextPhase);
reducedMotion.addEventListener('change', scheduleNextPhase);

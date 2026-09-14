const projectFiles = {
  connection: {
    path: 'connections/olist.yaml', step: '01 / CONNECT', title: 'Start with a connection.',
    summary: 'A managed connection tells LeapView how this example reads uploaded CSV data.'
  },
  source: {
    path: 'sources/olist.payments.yaml', step: '02 / DESCRIBE', title: 'Name the source.',
    summary: 'The source identifies the payments file, its format, and the fields it exposes.'
  },
  model: {
    path: 'models/sales_orders.yaml', step: '03 / TRANSFORM', title: 'Shape the data.',
    summary: 'A SQL model builds order-level sales facts, including the revenue field.'
  },
  semantics: {
    path: 'semantic-models/sales.yaml', step: '04 / DEFINE', title: 'Shared metrics, one definition.',
    summary: 'The semantic model names datasets, relationships, dimensions, and shared metrics.'
  },
  pipeline: {
    path: 'pipelines/sales-refresh.yaml', step: '05 / REFRESH', title: 'Keep the project current.',
    summary: 'A pipeline selects the sales semantic model for refresh.'
  },
  dashboard: {
    path: 'dashboards/executive-sales.yaml', step: '06 / PRESENT', title: 'Declare the dashboard.',
    summary: 'The dashboard uses the sales semantic model to define filters and visuals.'
  }
};

const explorer = document.querySelector('#project-explorer');
const buttons = [...explorer.querySelectorAll('[data-project-file]')];
const code = document.querySelector('#project-code');
const codeScroll = document.querySelector('#project-code-scroll');
const fileCache = new Map();
let latestSelection = 0;

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

function renderSource(source) {
  const lines = source.trimEnd().split('\n');
  const fragment = document.createDocumentFragment();
  lines.forEach((line, index) => {
    const row = document.createElement('span');
    row.className = 'project-code-row';
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
  document.querySelector('#project-line-count').textContent = `${lines.length} LINES`;
  codeScroll.scrollTo(0, 0);
}

async function selectFile(key) {
  const file = projectFiles[key];
  if (!file) return;
  const selection = ++latestSelection;
  buttons.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.projectFile === key)));
  document.querySelector('#project-tab-name').textContent = file.path.split('/').at(-1);
  document.querySelector('#project-file-step').textContent = file.step;
  document.querySelector('#project-file-title').textContent = file.title;
  document.querySelector('#project-file-summary').textContent = file.summary;
  document.querySelector('#project-source-link').href = `https://github.com/flidai/leapview/blob/main/dashboards/${file.path}`;
  codeScroll.setAttribute('aria-label', `${file.path} source code`);

  if (fileCache.has(key)) {
    renderSource(fileCache.get(key));
    return;
  }
  code.textContent = 'Loading file…';
  document.querySelector('#project-line-count').textContent = 'YAML';
  try {
    const response = await fetch(`/static/home/project-files/${file.path}`);
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const source = await response.text();
    fileCache.set(key, source);
    if (selection === latestSelection) renderSource(source);
  } catch {
    if (selection === latestSelection) {
      code.textContent = 'Could not load this file. View the source on GitHub instead.';
      document.querySelector('#project-file-summary').textContent = 'This local preview file could not be loaded.';
    }
  }
}

buttons.forEach((button, index) => {
  button.addEventListener('click', () => selectFile(button.dataset.projectFile));
  button.addEventListener('keydown', event => {
    const nextIndex = event.key === 'ArrowDown' ? index + 1 : event.key === 'ArrowUp' ? index - 1 : event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : -1;
    if (nextIndex === -1 && !['ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    const next = buttons[(nextIndex + buttons.length) % buttons.length];
    next.focus();
    selectFile(next.dataset.projectFile);
  });
});

selectFile('semantics');

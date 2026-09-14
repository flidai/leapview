const home = document.querySelector('.site-home');
const screenshot = home?.querySelector('#product-image');

if (home && screenshot) {
  const syncTheme = () => {
    const light = document.documentElement.style.colorScheme === 'light';
    home.classList.toggle('is-light', light);
    const source = `/static/product-dashboard-${light ? 'light' : 'dark'}.png`;
    if (screenshot.getAttribute('src') !== source) screenshot.setAttribute('src', source);
  };

  syncTheme();
  document.addEventListener('leapview-theme-applied', syncTheme);
}

// Let the sticky navigation take the color of the section passing beneath it.
// Geometry is measured on layout changes; scrolling only reads cached edges.
const header = document.querySelector('.site-header');
if (home && header) {
  const sections = [...home.children].filter((node) => node.matches('section'));
  const footer = document.querySelector('.site-footer');
  if (footer) sections.push(footer);
  let surfaces = [];
  let headerHeight = 0;
  let baseColor = '';

  const colorOf = (node) => {
    for (let element = node; element; element = element.parentElement) {
      const color = getComputedStyle(element).backgroundColor;
      if (color !== 'transparent' && color !== 'rgba(0, 0, 0, 0)') return color;
    }
    return getComputedStyle(document.documentElement).backgroundColor;
  };

  const wash = (color) => `color-mix(in srgb, ${color} 90%, transparent)`;
  const at = (position) => surfaces.find(({ top, bottom }) => position >= top && position < bottom);

  const paintHeader = () => {
    const top = window.scrollY;
    const upper = at(top);
    const lower = at(top + headerHeight - 1);
    const upperColor = upper?.color ?? baseColor;
    const lowerColor = lower?.color ?? upperColor;

    if (upperColor === lowerColor) {
      header.style.setProperty('--site-header-fill', wash(upperColor));
      header.style.removeProperty('--site-header-edge');
      return;
    }

    const split = Math.max(0, Math.min(headerHeight, Math.round((upper?.bottom ?? lower.top) - top)));
    header.style.setProperty('--site-header-fill', 'transparent');
    header.style.setProperty('--site-header-edge', `linear-gradient(to bottom, ${wash(upperColor)} ${split}px, ${wash(lowerColor)} ${split}px)`);
  };

  const survey = () => {
    headerHeight = header.getBoundingClientRect().height;
    baseColor = colorOf(home);
    surfaces = sections.map((node) => {
      const bounds = node.getBoundingClientRect();
      return { top: bounds.top + window.scrollY, bottom: bounds.bottom + window.scrollY, color: colorOf(node) };
    });
    paintHeader();
  };

  const resizeObserver = new ResizeObserver(survey);
  sections.forEach((section) => resizeObserver.observe(section));
  resizeObserver.observe(header);
  window.addEventListener('scroll', paintHeader, { passive: true });
  window.addEventListener('resize', survey);
  window.addEventListener('pageshow', survey);
  document.addEventListener('leapview-theme-applied', () => {
    requestAnimationFrame(survey);
    setTimeout(survey, 500);
  });
  survey();
}

const mission = home?.querySelector('#mission');
const subject = mission?.querySelector('.mission-subject');

if (mission && subject && 'IntersectionObserver' in window) {
  const phrases = ['business intelligence', 'data'];
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  let active = false;
  let phrase = 0;
  let phase = 'hold';
  let timer;

  const schedule = (delay) => {
    clearTimeout(timer);
    if (active && !document.hidden && !reducedMotion.matches) timer = setTimeout(tick, delay);
  };

  const tick = () => {
    if (phase === 'hold') {
      phase = 'erase';
      schedule(55);
      return;
    }
    if (phase === 'erase') {
      subject.textContent = subject.textContent.slice(0, -1);
      if (subject.textContent) {
        schedule(55);
      } else {
        phrase = (phrase + 1) % phrases.length;
        phase = 'type';
        schedule(260);
      }
      return;
    }
    const target = phrases[phrase];
    subject.textContent = target.slice(0, subject.textContent.length + 1);
    if (subject.textContent === target) {
      phase = 'hold';
      schedule(3000);
    } else {
      schedule(85);
    }
  };

  const syncMotion = () => {
    clearTimeout(timer);
    if (reducedMotion.matches) {
      subject.textContent = phrases[0];
      phrase = 0;
      phase = 'hold';
    } else {
      schedule(1800);
    }
  };

  new IntersectionObserver(([entry]) => {
    active = entry.isIntersecting;
    syncMotion();
  }, { rootMargin: '40px' }).observe(mission);
  document.addEventListener('visibilitychange', syncMotion);
  reducedMotion.addEventListener('change', syncMotion);
}

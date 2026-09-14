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

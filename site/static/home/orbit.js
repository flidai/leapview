const orbitStage = document.querySelector('#orbit-stage');
const orbitButtons = [...document.querySelectorAll('.orbit-node')];

// Rotate each ring, then counter-rotate its icons so labels remain upright.
const orbitRoutes = {
  inner: [['postgresql', 0], ['mysql', 130], ['sqlite', 210]],
  middle: [['amazons3', 15], ['microsoftazure', 87], ['googlecloudstorage', 159], ['cloudflare', 231], ['hetzner', 303]],
  outer: [['csv', 25], ['json', 65], ['apacheparquet', 105], ['excel', 145], ['vortex', 185], ['deltalake', 225], ['apacheiceberg', 265], ['lance', 305], ['ducklake', 345]]
};
const mobileAngles = {
  inner: [270, 60, 120],
  middle: [215, 325, 15, 90, 165]
};
const mobileOrbit = matchMedia('(max-width: 700px)');
const orbitLabels = { inner: 'Databases', middle: 'Object storage', outer: 'Files and lakehouse formats' };
const orbitNodes = document.querySelector('.orbit-nodes');
const buttonsByIntegration = new Map(orbitButtons.map(button => [button.dataset.integration, button]));
const anchors = [];
for (const [ring, route] of Object.entries(orbitRoutes)) {
  const track = document.createElement('div');
  track.className = `orbit-track orbit-track--${ring}`;
  track.setAttribute('role', 'group');
  track.setAttribute('aria-label', orbitLabels[ring]);
  const rotor = document.createElement('div');
  rotor.className = 'orbit-rotor';
  for (const [index, [integration, angle]] of route.entries()) {
    const anchor = document.createElement('div');
    anchor.className = 'orbit-anchor';
    anchors.push({ element: anchor, desktopAngle: angle, mobileAngle: mobileAngles[ring]?.[index] ?? angle });
    const counter = document.createElement('div');
    counter.className = 'orbit-counter';
    counter.append(buttonsByIntegration.get(integration));
    anchor.append(counter);
    rotor.append(anchor);
  }
  track.append(rotor);
  orbitNodes.append(track);
}
function syncOrbitAngles() {
  anchors.forEach(({ element, desktopAngle, mobileAngle }) => {
    element.style.setProperty('--angle', `${mobileOrbit.matches ? mobileAngle : desktopAngle}deg`);
  });
}
mobileOrbit.addEventListener('change', syncOrbitAngles);
syncOrbitAngles();
orbitStage.classList.add('orbit-ready');

orbitButtons.forEach(button => button.addEventListener('click', () => {
  const selected = button.getAttribute('aria-pressed') !== 'true';
  orbitButtons.forEach(other => other.setAttribute('aria-pressed', String(selected && other === button)));
}));

if ('IntersectionObserver' in window) {
  const observer = new IntersectionObserver(entries => {
    orbitStage.classList.toggle('is-in-view', entries[0].isIntersecting && !document.hidden);
  }, { rootMargin: '100px' });
  observer.observe(orbitStage);
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) orbitStage.classList.remove('is-in-view');
    else if (orbitStage.getBoundingClientRect().bottom > -100 && orbitStage.getBoundingClientRect().top < innerHeight + 100) orbitStage.classList.add('is-in-view');
  });
} else {
  orbitStage.classList.add('is-in-view');
}

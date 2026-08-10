// xbin.dev entry point. Page-level behavior — scrollspy nav, on-scroll
// reveals, pointer parallax — is set up first and must not depend on the
// component layer loading, so the elements are imported dynamically: if lit
// or a component ever fails, the page still reads complete and static.
const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;

for (const m of ['./xb-gl.js', './xb-shell-scene.js',
                 './xb-copy.js', './xb-lightbox.js']) {
  import(m).catch(err => console.error('xbin.dev: component failed:', m, err));
}

// ---- scrollspy: the active nav link wears the amber tab -------------------
{
  const links = [...document.querySelectorAll('nav .lnk[href^="#"]')];
  const byId = new Map(links.map(a => [a.getAttribute('href').slice(1), a]));
  const spy = new IntersectionObserver(entries => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      links.forEach(a => a.classList.remove('on'));
      byId.get(e.target.id)?.classList.add('on');
    }
  }, { rootMargin: '-30% 0px -55% 0px' });
  document.querySelectorAll('section[id]').forEach(s => spy.observe(s));
}

// ---- hero pointer: feed the shader, tilt the mock a few degrees -----------
{
  const hero = document.querySelector('.hero');
  const gl = document.querySelector('xb-gl');
  const mock = document.querySelector('xb-shell-scene');
  if (hero && !reduce) {
    let raf = 0;
    hero.addEventListener('pointermove', e => {
      if (e.pointerType === 'touch') return;
      const r = hero.getBoundingClientRect();
      const x = e.clientX - r.left, y = e.clientY - r.top;
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => {
        if (gl) gl.mouse = { x, y };
        if (mock) {
          const nx = x / r.width - 0.5, ny = y / r.height - 0.5;
          mock.style.transform =
            `perspective(1200px) rotateY(${nx * 3.2}deg) rotateX(${-ny * 2.4}deg)`;
        }
      });
    });
    hero.addEventListener('pointerleave', () => {
      if (mock) mock.style.transform = '';
    });
  }
}

// ---- reveals: sections and cards fade up as they enter --------------------
{
  const els = document.querySelectorAll('.reveal');
  if (reduce) {
    els.forEach(el => el.classList.add('in'));
  } else {
    const io = new IntersectionObserver(entries => {
      for (const e of entries) {
        if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); }
      }
    }, { threshold: 0.12 });
    els.forEach(el => io.observe(el));
  }
}

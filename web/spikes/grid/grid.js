// Phase 0 spike: DOM-virtualized grid with a scaled scrollbar.
const ROW_H = 28;
// Firefox stops honouring position:sticky near 8.9M px, below its 17.9M height limit.
const MAX_SCROLL_H = 8_000_000;
const OVERSCAN = 3;
// Rows are placed relative to a base row, so a small scroll writes one transform.
const REBASE_ROWS = 1000;

function createGrid(viewport, { rows, cols, cell }) {
  const spacer = viewport.querySelector('#spacer');
  const win = viewport.querySelector('#window');
  viewport.setAttribute('aria-rowcount', rows);
  viewport.setAttribute('aria-colcount', cols);

  const fullH = rows * ROW_H;
  spacer.style.height = Math.min(fullH, MAX_SCROLL_H) + 'px';

  const visible = Math.ceil(viewport.clientHeight / ROW_H) + OVERSCAN * 2;
  const pool = [];
  for (let i = 0; i < Math.min(visible, rows); i++) {
    const el = document.createElement('div');
    el.className = 'row';
    el.setAttribute('role', 'row');
    const texts = [];
    for (let c = 0; c < cols; c++) {
      const d = document.createElement('div');
      d.className = 'cell';
      d.setAttribute('role', 'gridcell');
      d.setAttribute('aria-colindex', c + 1);
      const t = document.createTextNode('');
      d.appendChild(t);
      el.appendChild(d);
      texts.push(t);
    }
    win.appendChild(el);
    pool.push({ el, texts, row: -1, offset: NaN });
  }

  // The grid owns its position in virtual pixels. The native scrollbar only mirrors it,
  // because at scale one scrollbar pixel covers several rows.
  const maxVirtual = () => Math.max(0, fullH - viewport.clientHeight);
  const maxNative = () => Math.max(0, viewport.scrollHeight - viewport.clientHeight);
  let virtualTop = 0;
  let expectedNative = 0;
  let base = 0;

  function moveTo(v) {
    virtualTop = Math.min(maxVirtual(), Math.max(0, v));
    expectedNative = maxVirtual() ? (virtualTop / maxVirtual()) * maxNative() : 0;
    viewport.scrollTop = expectedNative;
    schedule();
  }

  function render() {
    const top = virtualTop / ROW_H;
    const firstVisible = Math.floor(top);
    const first = Math.max(0, firstVisible - OVERSCAN);
    if (Math.abs(firstVisible - base) > REBASE_ROWS) base = firstVisible;
    for (let i = 0; i < pool.length; i++) {
      const r = first + i;
      const p = pool[r % pool.length];
      if (r >= rows) {
        if (p.row !== -1) { p.el.style.display = 'none'; p.row = -1; }
        continue;
      }
      if (p.row !== r) {
        if (p.row === -1) p.el.style.display = '';
        p.row = r;
        p.el.setAttribute('aria-rowindex', r + 1);
        for (let c = 0; c < cols; c++) p.texts[c].nodeValue = cell(r, c);
      }
      const offset = (r - base) * ROW_H;
      if (p.offset !== offset) {
        p.offset = offset;
        p.el.style.transform = `translateY(${offset}px)`;
      }
    }
    win.style.transform = `translateY(${(base - top) * ROW_H}px)`;
  }

  let queued = false;
  function schedule() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; render(); });
  }

  viewport.addEventListener('scroll', () => {
    // Our own write comes back as a scroll event, possibly rounded.
    if (Math.abs(viewport.scrollTop - expectedNative) <= 1) return;
    expectedNative = viewport.scrollTop;
    virtualTop = maxNative() ? (viewport.scrollTop / maxNative()) * maxVirtual() : 0;
    schedule();
  }, { passive: true });

  viewport.addEventListener('wheel', e => {
    e.preventDefault();
    const unit = e.deltaMode === 1 ? ROW_H : e.deltaMode === 2 ? viewport.clientHeight : 1;
    moveTo(virtualTop + e.deltaY * unit);
  }, { passive: false });

  render();

  return {
    render,
    // Rows actually in the DOM and on screen, for tests.
    visibleRows() {
      const vh = viewport.getBoundingClientRect();
      return pool
        .filter(p => p.row >= 0)
        .filter(p => { const b = p.el.getBoundingClientRect(); return b.bottom > vh.top && b.top < vh.bottom; })
        .map(p => p.row)
        .sort((a, b) => a - b);
    },
    domNodes: () => viewport.querySelectorAll('*').length,
    scrollToRow(r) { moveTo(r * ROW_H); },
    moveBy(px) { moveTo(virtualTop + px); },
    topRow: () => virtualTop / ROW_H,
  };
}

const board = document.querySelector('#architecture-board');
const pieces = [...board.querySelectorAll('.slab-piece')];
const callouts = [...board.querySelectorAll('.architecture-callout')];
const wires = [...board.querySelectorAll('.architecture-wire')];

function matchesPiece(element, piece) {
  return element.dataset.layer === piece.closest('.layer-slab').dataset.layer &&
    element.dataset.detail === piece.dataset.detail;
}

function highlight(piece) {
  pieces.forEach(item => item.classList.toggle('is-active', item === piece));
  callouts.forEach(item => item.classList.toggle('is-active', !!piece && matchesPiece(item, piece)));
  wires.forEach(item => item.classList.toggle('is-active', !!piece && matchesPiece(item, piece)));
}

function restoreFocusOrHover() {
  const focusedPiece = pieces.find(piece => piece === document.activeElement && piece.matches(':focus-visible'));
  highlight(focusedPiece || pieces.find(piece => piece.matches(':hover')) || null);
}

pieces.forEach(piece => {
  piece.addEventListener('pointerenter', () => highlight(piece));
  piece.addEventListener('pointerleave', restoreFocusOrHover);
  piece.addEventListener('focus', restoreFocusOrHover);
  piece.addEventListener('blur', restoreFocusOrHover);
});

callouts.forEach(callout => {
  callout.addEventListener('pointerenter', () => {
    highlight(pieces.find(piece => matchesPiece(callout, piece)) || null);
  });
  callout.addEventListener('pointerleave', restoreFocusOrHover);
});

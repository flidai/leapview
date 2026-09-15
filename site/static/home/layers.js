const board = document.querySelector('#architecture-board');
const pieces = [...board.querySelectorAll('.slab-piece')];
const callouts = [...board.querySelectorAll('.architecture-callout')];
const wireCanvas = board.querySelector('.architecture-wires');
const svgNamespace = 'http://www.w3.org/2000/svg';
let wires = [];

function matchesPiece(element, piece) {
  return element.dataset.layer === piece.closest('.layer-slab').dataset.layer &&
    element.dataset.detail === piece.dataset.detail;
}

function pointBetween(card, piece, boardRect) {
  const cardRect = card.getBoundingClientRect();
  const pieceRect = piece.getBoundingClientRect();
  const cardCenter = { x: cardRect.left + cardRect.width / 2, y: cardRect.top + cardRect.height / 2 };
  const pieceCenter = { x: pieceRect.left + pieceRect.width / 2, y: pieceRect.top + pieceRect.height / 2 };
  const horizontalGap = Math.max(pieceRect.left - cardRect.right, cardRect.left - pieceRect.right, 0);
  const verticalGap = Math.max(pieceRect.top - cardRect.bottom, cardRect.top - pieceRect.bottom, 0);
  const horizontal = horizontalGap > verticalGap;
  let start;
  let end;
  if (horizontal) {
    const cardIsLeft = cardCenter.x < pieceCenter.x;
    start = { x: cardIsLeft ? cardRect.right : cardRect.left, y: cardCenter.y };
    end = { x: cardIsLeft ? pieceRect.left + pieceRect.width * .12 : pieceRect.right - pieceRect.width * .12, y: pieceCenter.y };
  } else {
    const cardIsAbove = cardCenter.y < pieceCenter.y;
    start = { x: cardCenter.x, y: cardIsAbove ? cardRect.bottom : cardRect.top };
    end = { x: pieceCenter.x, y: cardIsAbove ? pieceRect.top + pieceRect.height * .18 : pieceRect.bottom - pieceRect.height * .18 };
  }
  start.x -= boardRect.left;
  start.y -= boardRect.top;
  end.x -= boardRect.left;
  end.y -= boardRect.top;
  return { start, end, horizontal };
}

function drawWires() {
  const boardRect = board.getBoundingClientRect();
  wireCanvas.setAttribute('viewBox', `0 0 ${boardRect.width} ${boardRect.height}`);
  wireCanvas.replaceChildren();
  wires = callouts.map(card => {
    const piece = pieces.find(item => matchesPiece(card, item));
    const { start, end, horizontal } = pointBetween(card, piece, boardRect);
    const bend = horizontal ? Math.max(30, Math.abs(end.x - start.x) * .48) : Math.max(30, Math.abs(end.y - start.y) * .48);
    const direction = horizontal ? Math.sign(end.x - start.x) : Math.sign(end.y - start.y);
    const controls = horizontal
      ? `${start.x + bend * direction} ${start.y}, ${end.x - bend * direction} ${end.y}`
      : `${start.x} ${start.y + bend * direction}, ${end.x} ${end.y - bend * direction}`;
    const wire = document.createElementNS(svgNamespace, 'g');
    wire.classList.add('architecture-wire');
    const path = document.createElementNS(svgNamespace, 'path');
    path.setAttribute('d', `M ${start.x} ${start.y} C ${controls}, ${end.x} ${end.y}`);
    const dot = document.createElementNS(svgNamespace, 'circle');
    dot.setAttribute('cx', end.x);
    dot.setAttribute('cy', end.y);
    dot.setAttribute('r', '3');
    wire.append(path, dot);
    wireCanvas.append(wire);
    return { card, piece, wire };
  });
  restoreFocusOrHover();
}

function highlight(piece) {
  pieces.forEach(item => item.classList.toggle('is-active', item === piece));
  callouts.forEach(item => item.classList.toggle('is-active', !!piece && matchesPiece(item, piece)));
  wires.forEach(item => item.wire.classList.toggle('is-active', !!piece && item.piece === piece));
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

const resizeObserver = new ResizeObserver(drawWires);
resizeObserver.observe(board);
resizeObserver.observe(board.querySelector('.architecture-stack'));
window.addEventListener('load', drawWires);
drawWires();

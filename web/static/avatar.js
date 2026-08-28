// 阿桥 (Aqiao) — the agent's face.
//
// The mark is a bridge that reads as a face: two lantern eyes above the deck,
// and the arch beneath the deck doubling as the mouth. That is real bridge
// geometry, so it stays legible as a bridge at 96px and as a face at 24px, which
// is the only size that matters — the avatar sits beside every message.
//
// Four moods, and one of them is the point. `serious` flattens the arch, so the
// face does not smile while the agent is telling somebody it stopped itself or
// that it cannot help. An interface that smiles through a refusal is doing the
// same thing the persona forbids in words.
//
// Inline SVG, no asset, no request. Colours come from CSS variables so it themes
// with the rest of the page.

const SMILE = "M15 28.5 Q24 36.5 33 28.5"; // the arch under the deck
const MOUTHS = {
  calm: SMILE,
  thinking: SMILE,
  listening: SMILE,
  // A flat span. Still a bridge, no longer smiling.
  serious: "M15 32.5 H33",
};

export const MOODS = Object.keys(MOUTHS);

export function avatar(mood = "calm", label = "阿桥") {
  const m = MOUTHS[mood] ? mood : "calm";
  return `
<svg class="oba-avatar mood-${m}" viewBox="0 0 48 48" role="img" aria-label="${escapeAttr(label)}">
  <rect class="av-bg" x="1.5" y="1.5" width="45" height="45" rx="13"/>
  <circle class="av-glow" cx="24" cy="19.5" r="12"/>
  <circle class="av-ink" cx="18.5" cy="20" r="2.4"/>
  <circle class="av-ink" cx="29.5" cy="20" r="2.4"/>
  <path class="av-line" d="M10 28.5 H38"/>
  <path class="av-line av-mouth" d="${MOUTHS[m]}"/>
  <path class="av-line av-thin" d="M13 28.5 V33.5 M35 28.5 V33.5"/>
  <path class="av-line av-thin av-water" d="M9 39.5 H39"/>
  <circle class="av-walker" cx="13" cy="25.8" r="1.7"/>
  <g class="av-ears">
    <path class="av-line av-thin" d="M37 17.5 q2.4 2.8 0 5.6"/>
    <path class="av-line av-thin" d="M40 15.5 q4 5.5 0 11"/>
  </g>
</svg>`.trim();
}

// setMood swaps the whole mark rather than mutating attributes: the markup is
// small, and one code path for "what does the avatar look like" is worth more
// than the handful of bytes saved.
export function setMood(slot, mood, label) {
  if (!slot) return;
  slot.innerHTML = avatar(mood, label);
}

// faviconDataURI is the same mark with the colours baked in, because a favicon
// cannot read the page's CSS variables. It is drawn for a light tab strip, which
// is what both light and dark browser chrome tends to use behind a favicon.
export function faviconDataURI() {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48">
<rect x="1.5" y="1.5" width="45" height="45" rx="13" fill="#e9eff8"/>
<circle cx="18.5" cy="20" r="2.6" fill="#1f5fa8"/>
<circle cx="29.5" cy="20" r="2.6" fill="#1f5fa8"/>
<g fill="none" stroke="#1f5fa8" stroke-width="2.6" stroke-linecap="round">
<path d="M10 28.5 H38"/><path d="M15 28.5 Q24 36.5 33 28.5"/>
</g>
<g fill="none" stroke="#9fb8d6" stroke-width="1.8" stroke-linecap="round">
<path d="M13 28.5 V33.5"/><path d="M35 28.5 V33.5"/><path d="M9 39.5 H39"/>
</g></svg>`;
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

function escapeAttr(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// 阿桥 (Aqiao) — the agent's face.
//
// One image, everywhere: the sidebar, the sign-in card, the landing page, and
// beside every message. Previously this drew an inline SVG mark — a bridge that
// read as a face — and the two lived side by side, the drawn mark in the small
// slots and the illustration in the large ones. Having two faces for one agent
// is having no face, so the illustration won and the mark is gone.
//
// ⚠️ What that cost, stated plainly because it was a deliberate design decision
// that this reverses: the drawn mark had four moods, and one of them earned its
// place — `serious` flattened the arch so the face did not smile while the agent
// was refusing, or saying it had stopped itself. A single image cannot do that.
//
// The mood is still carried on the element as `data-mood`, so nothing that reads
// it breaks and a future treatment can hang off it. Nothing is LOST from the
// answer itself: a blocked turn already says which rule stopped it and that
// nothing was done, in words. The face was a second, redundant signal — which is
// the only reason removing it is acceptable under "colour never carries meaning
// alone". docs/13-name-and-voice.md still describes the four moods and needs
// updating alongside this.

const MOODS = ["calm", "thinking", "listening", "serious"];
export { MOODS };

const SRC = "/mascot.png";

export function avatar(mood = "calm", label = "阿桥") {
  const m = MOODS.includes(mood) ? mood : "calm";
  // alt is empty and aria-hidden is set when there is no label to give: beside
  // every message this is decoration, and a screen reader announcing "阿桥"
  // before each of forty turns is noise, not information.
  return `<img class="oba-avatar mood-${m}" data-mood="${m}" src="${SRC}" alt=""
    aria-hidden="true" width="357" height="356" decoding="async">`.trim();
}

// setMood swaps the whole element rather than mutating attributes: the markup is
// small, and one code path for "what does the avatar look like" is worth more
// than the handful of bytes saved.
export function setMood(slot, mood, label) {
  if (!slot) return;
  slot.innerHTML = avatar(mood, label);
}

// The tab icon is the same face. It used to be a hand-drawn SVG data URI because
// the mark could not read the page's CSS variables; an image has no such problem
// and pointing at the file keeps one source for the face.
export function faviconDataURI() {
  return SRC;
}

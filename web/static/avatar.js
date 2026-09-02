// 阿桥 (Aqiao) — the agent's face.
//
// One character, everywhere: the sidebar, the sign-in card, the landing page,
// and beside every message. She is a white-haired figure in a white-and-gold
// uniform under a floating amber halo; the halo is the silhouette that survives
// being shrunk, which is why the crop below keeps it.
//
// Two crops of ONE reference sheet, not two designs:
//
//   /mascot.png       head, 360×360 — every avatar slot, from 32px up
//   /mascot-full.png  full figure, 340×800 — the landing page's "who 阿桥 is"
//                     section only, where there is room to show the whole
//                     character (index.html, .voice-art)
//
// ⚠️ Why that distinction matters, because an earlier note here forbade the
// thing this now does: the rule was never "one file", it was **one face**. What
// was removed back then was an inline SVG bridge-mark that was a genuinely
// different drawing from the illustration, so the product had two answers to
// "what does 阿桥 look like". A head crop and a full-body crop of the same
// character have one answer. Adding a second CHARACTER would break the rule
// again; adding a second framing of this one does not.
//
// ⚠️ Still true and still a cost: the drawn mark had four moods and one of them
// earned its place — `serious` flattened the arch so the face did not smile
// while the agent was refusing, or saying it had stopped itself. A single still
// image cannot do that, and the reference sheet ships no serious expression, so
// it is not restored here. Anything claiming the face changes when a turn is
// blocked is FALSE and must not be written into the interface copy; the strings
// that used to claim it (`home.bound.note`, `home.voice.p3`) were corrected
// alongside this.
//
// The mood is still carried on the element as `data-mood`, so nothing that reads
// it breaks and a future treatment can hang off it. Nothing is LOST from the
// answer itself: a blocked turn already says which rule stopped it and that
// nothing was done, in words. The face was a second, redundant signal — which is
// the only reason its absence is acceptable under "colour never carries meaning
// alone".

const MOODS = ["calm", "thinking", "listening", "serious"];
export { MOODS };

const SRC = "/mascot.png";

export function avatar(mood = "calm", label = "阿桥") {
  const m = MOODS.includes(mood) ? mood : "calm";
  // alt is empty and aria-hidden is set when there is no label to give: beside
  // every message this is decoration, and a screen reader announcing "阿桥"
  // before each of forty turns is noise, not information.
  return `<img class="oba-avatar mood-${m}" data-mood="${m}" src="${SRC}" alt=""
    aria-hidden="true" width="360" height="360" decoding="async">`.trim();
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
// and pointing at the file keeps one source for the face. The head crop, not the
// full figure: at 16px a full figure is a smudge.
export function faviconDataURI() {
  return SRC;
}

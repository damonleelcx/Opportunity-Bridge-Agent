// A raymarched liquid form behind the hero, on WebGL.
//
// The shader is ThreeUI's "Liquid Form", vendored rather than depended on.
//   source:  @designcodeio/threeui v1.1.0, lib-dist/shaders/liquid-form/
//   project: https://threeui.com/backgrounds/liquid-form
//   repo:    https://github.com/MengTo/threeui
//   licence: MIT
// The marching, the simplex noise and the light rig are theirs and unchanged.
// What is changed is listed under THEMING below.
//
// PORTED, NOT DROPPED IN. Upstream ships a React component. This project has no
// React, no bundler and no package.json -- the landing page is one ES module
// served out of a Go embed -- so the component's lifecycle is reproduced here
// with plain DOM: ResizeObserver for sizing, IntersectionObserver plus
// document.hidden for gating, a lerped pointer, and a dispose() that releases
// every GL object. Its uniform contract is kept exactly (u_res, u_time,
// u_mouse, u_morph, u_noise_scale, u_mouse_amount, u_metal, u_camera), so a
// future version of the upstream shader can be pasted in over SHADER.
//
// THEMING. Upstream is a fixed silver: its light rig hard-codes vec3(0.95,0.93,
// 0.90) for the key, vec3(0.40,0.42,0.45) for the rim and so on, which reads as
// chrome on any page. Those constants are now the --bub-* tokens, which are
// already themed per page -- gold on the dark theme, violet on the light one,
// both sampled from the reference stills. This file never asks which theme is
// in force; it reads what CSS resolved, so there is one source of truth.
//
// TRANSPARENCY. Upstream owns its whole background: alpha:false, and it paints
// a vignette behind the form. Here the hero's starfield and text scrim sit
// behind and have to stay visible, so the context is alpha:true, the background
// is dropped, and the form carries a coverage alpha with a soft falloff at the
// silhouette (upstream needs no falloff because it never composites).

export const LIQUID_FORM_DEFAULTS = {
  speed: 1,
  morph: 1,
  noiseScale: 1,
  mouseAmount: 0.15,
  metal: 1,
  camera: 5.5,
  tintHue: 220,
  tintAmount: 0,
  // Not upstream. The march is up to 70 steps and each step evaluates simplex
  // noise three times, so cost is per DEVICE pixel and it is the whole frame
  // budget. At full density a 1100px square on a 2x screen is 4.8M pixels of
  // that; at 0.62 it is 1.9M for a form soft enough that nobody can tell.
  resolutionScale: 0.62,
  maxPixelRatio: 1.25,
};

const VERTEX = `
attribute vec2 a_pos;
void main(){ gl_Position = vec4(a_pos, 0.0, 1.0); }
`;

const SHADER = `
precision highp float;
uniform vec2 u_res;
uniform float u_time;
uniform vec2 u_mouse;
uniform float u_morph;
uniform float u_noise_scale;
uniform float u_mouse_amount;
uniform float u_metal;
uniform float u_camera;

// The palette, per theme. See THEMING at the top of this file.
uniform vec3 u_lit;
uniform vec3 u_mid;
uniform vec3 u_deep;
uniform vec3 u_dark;
uniform vec3 u_hot;
uniform vec3 u_cold;

// Where the page clips this canvas, as a fraction of canvas height measured up
// from its bottom edge. The form is faded out above that line so it dissolves
// instead of being sliced: the canvas hangs below the hero and .bubble-wrap
// clips it with overflow:hidden, which cut a hard horizontal edge across the
// body. Computed in JS from the two rects, so it follows any viewport.
uniform float u_fade_from;

#define MAX_STEPS 70
#define MAX_DIST 20.0
#define SURF_DIST 0.002

vec3 mod289(vec3 x){return x-floor(x*(1.0/289.0))*289.0;}
vec4 mod289(vec4 x){return x-floor(x*(1.0/289.0))*289.0;}
vec4 permute(vec4 x){return mod289(((x*34.0)+1.0)*x);}
vec4 taylorInvSqrt(vec4 r){return 1.79284291400159-0.85373472095314*r;}
float snoise(vec3 v){
  const vec2 C=vec2(1.0/6.0,1.0/3.0);
  const vec4 D=vec4(0.0,0.5,1.0,2.0);
  vec3 i=floor(v+dot(v,C.yyy));
  vec3 x0=v-i+dot(i,C.xxx);
  vec3 g=step(x0.yzx,x0.xyz);
  vec3 l=1.0-g;
  vec3 i1=min(g.xyz,l.zxy);
  vec3 i2=max(g.xyz,l.zxy);
  vec3 x1=x0-i1+C.xxx;
  vec3 x2=x0-i2+C.yyy;
  vec3 x3=x0-D.yyy;
  i=mod289(i);
  vec4 p=permute(permute(permute(i.z+vec4(0.0,i1.z,i2.z,1.0))+i.y+vec4(0.0,i1.y,i2.y,1.0))+i.x+vec4(0.0,i1.x,i2.x,1.0));
  float n_=0.142857142857;
  vec3 ns=n_*D.wyz-D.xzx;
  vec4 j=p-49.0*floor(p*ns.z*ns.z);
  vec4 x_=floor(j*ns.z);
  vec4 y_=floor(j-7.0*x_);
  vec4 x=x_*ns.x+ns.yyyy;
  vec4 y=y_*ns.x+ns.yyyy;
  vec4 h=1.0-abs(x)-abs(y);
  vec4 b0=vec4(x.xy,y.xy);
  vec4 b1=vec4(x.zw,y.zw);
  vec4 s0=floor(b0)*2.0+1.0;
  vec4 s1=floor(b1)*2.0+1.0;
  vec4 sh=-step(h,vec4(0.0));
  vec4 a0=b0.xzyw+s0.xzyw*sh.xxyy;
  vec4 a1=b1.xzyw+s1.xzyw*sh.zzww;
  vec3 p0=vec3(a0.xy,h.x);
  vec3 p1=vec3(a0.zw,h.y);
  vec3 p2=vec3(a1.xy,h.z);
  vec3 p3=vec3(a1.zw,h.w);
  vec4 norm=taylorInvSqrt(vec4(dot(p0,p0),dot(p1,p1),dot(p2,p2),dot(p3,p3)));
  p0*=norm.x;p1*=norm.y;p2*=norm.z;p3*=norm.w;
  vec4 m=max(0.6-vec4(dot(x0,x0),dot(x1,x1),dot(x2,x2),dot(x3,x3)),0.0);
  m=m*m;
  return 42.0*dot(m*m,vec4(dot(p0,x0),dot(p1,x1),dot(p2,x2),dot(p3,x3)));
}

float map(vec3 p, float t) {
  float radius = 1.8;
  float morph = snoise(p * (0.8 * u_noise_scale) + t * 0.1) * 0.2;
  morph += snoise(p * (1.5 * u_noise_scale) - t * 0.05 + 10.0) * 0.08;
  morph += snoise(p * (3.0 * u_noise_scale) + t * 0.02) * 0.02;
  return length(p) - radius + morph * u_morph;
}

vec3 calcNormal(vec3 p, float t) {
  vec2 e = vec2(0.002, 0.0);
  return normalize(vec3(
    map(p+e.xyy, t) - map(p-e.xyy, t),
    map(p+e.yxy, t) - map(p-e.yxy, t),
    map(p+e.yyx, t) - map(p-e.yyx, t)
  ));
}

// Upstream's rig, with its four fixed greys replaced by the theme ramp. The
// weights and directions are untouched, so the form is lit identically -- only
// the colour of each light changes.
vec3 envLighting(vec3 rd, vec2 mouse) {
  vec3 col = u_dark * 0.35;
  vec3 keyDir = normalize(vec3(0.5 + mouse.x, 1.0 + mouse.y * 0.5, 1.2));
  float key = pow(max(dot(rd, keyDir), 0.0), 12.0);
  col += u_lit * key * 1.5;
  vec3 rimDir = normalize(vec3(-0.8, -0.2, -1.0));
  float rim = pow(max(dot(rd, rimDir), 0.0), 6.0);
  col += u_mid * rim * 0.8;
  vec3 fillDir = normalize(vec3(-1.0, 0.5, 0.5));
  float fill = pow(max(dot(rd, fillDir), 0.0), 3.0);
  col += u_deep * fill * 0.6;
  float panel = exp(-pow((rd.y - 0.2) * 4.0, 2.0)) * smoothstep(-0.5, 0.5, rd.z);
  col += u_cold * 0.55 * panel;
  return col;
}

// One smooth ramp, no flat section. The mask this replaces was a CSS
// linear-gradient that held solid to 84% and then fell away, and that corner in
// its slope read as a drawn line across the form (a Mach band). A single
// smoothstep has no such corner anywhere.
float bottomFade() {
  float vy = gl_FragCoord.y / max(u_res.y, 1.0);
  return smoothstep(u_fade_from, u_fade_from + 0.24, vy);
}

void main() {
  vec2 uv = (gl_FragCoord.xy - u_res * 0.5) / min(u_res.x, u_res.y);
  float t = u_time * 0.8;
  vec2 m = u_mouse * u_mouse_amount;
  vec3 ro = vec3(0.0, 0.0, u_camera);
  vec3 lookAt = vec3(m.x, m.y, 0.0);
  vec3 fwd = normalize(lookAt - ro);
  vec3 right = normalize(cross(vec3(0.0, 1.0, 0.0), fwd));
  vec3 up = cross(fwd, right);
  vec3 rd = normalize(fwd + uv.x * right + uv.y * up);

  vec3 col = vec3(0.0);
  float d = 0.0;
  float near = 1e9;
  for(int i=0; i<MAX_STEPS; i++) {
    vec3 p = ro + rd * d;
    float ds = map(p, t);
    near = min(near, abs(ds));
    d += ds;
    if(d > MAX_DIST || abs(ds) < SURF_DIST) break;
  }

  if(d < MAX_DIST) {
    vec3 p = ro + rd * d;
    vec3 n = calcNormal(p, t);
    vec3 ref = reflect(rd, n);
    float fresnel = pow(1.0 - max(dot(n, -rd), 0.0), 4.0);
    fresnel = mix(0.4, 1.0, fresnel);
    vec3 env = envLighting(ref, u_mouse);
    col = env * fresnel * 1.8 * u_metal;
    vec3 lightPos = normalize(vec3(0.5 + u_mouse.x, 1.0, 1.0));
    float spec = pow(max(dot(ref, lightPos), 0.0), 60.0);
    col += u_hot * spec * 2.0 * u_metal;
    float disp = map(p, t) - (length(p) - 1.8);
    col *= mix(0.7, 1.0, smoothstep(-0.1, 0.1, disp));
    col = col / (col + 0.5);
    col = pow(col, vec3(1.0/2.2));
    gl_FragColor = vec4(col, bottomFade());
    return;
  }

  // Missed. Upstream paints its background here; this one composites over the
  // page, so all that is left is a short falloff just outside the surface --
  // without it the silhouette is a 1-bit stair-step against the page.
  float halo = 1.0 - smoothstep(0.0, 0.055, near);
  gl_FragColor = vec4(u_hot, halo * 0.30 * bottomFade());
}
`;

function compile(gl, type, source) {
  const shader = gl.createShader(type);
  if (!shader) throw new Error("liquid form: unable to create shader");
  gl.shaderSource(shader, source);
  gl.compileShader(shader);
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(shader) || "shader compilation failed";
    gl.deleteShader(shader);
    throw new Error("liquid form: " + log);
  }
  return shader;
}

// The --bub-* tokens are "r, g, b" in tokens.css, already themed per page.
// Reading them keeps one source of truth for the palette.
function palette(root) {
  const cs = window.getComputedStyle(root);
  const read = (name, fallback) => {
    const parts = cs.getPropertyValue(name).trim().split(",").map(Number);
    if (parts.length !== 3 || parts.some((n) => !isFinite(n))) return fallback;
    return new Float32Array([parts[0] / 255, parts[1] / 255, parts[2] / 255]);
  };
  return {
    lit: read("--bub-lit", new Float32Array([0.95, 0.93, 0.89])),
    mid: read("--bub-mid", new Float32Array([0.78, 0.63, 0.36])),
    deep: read("--bub-deep", new Float32Array([0.29, 0.20, 0.09])),
    dark: read("--bub-dark", new Float32Array([0.08, 0.06, 0.04])),
    hot: read("--bub-hot", new Float32Array([1.0, 0.97, 0.91])),
    cold: read("--bub-cold", new Float32Array([0.12, 0.24, 0.47])),
  };
}

/**
 * Mounts the liquid form into `host`, drawing on `canvas`. Returns a dispose().
 *
 * Returns a no-op disposer when WebGL is missing or the shader fails to build:
 * the hero reads perfectly well without a background, so this must never throw
 * into the landing page's start-up path and take the rest of it down with it.
 */
export function mountLiquidForm(host, canvas, options) {
  if (!host || !canvas) return () => {};
  const opts = Object.assign({}, LIQUID_FORM_DEFAULTS, options || {});

  const gl = canvas.getContext("webgl", {
    alpha: true,
    antialias: false,
    premultipliedAlpha: false,
    powerPreference: "high-performance",
  });
  if (!gl) return () => {};

  let vertex = null, fragment = null, program = null;
  try {
    vertex = compile(gl, gl.VERTEX_SHADER, VERTEX);
    fragment = compile(gl, gl.FRAGMENT_SHADER, SHADER);
    program = gl.createProgram();
    if (!program) throw new Error("liquid form: unable to create program");
    gl.attachShader(program, vertex);
    gl.attachShader(program, fragment);
    gl.linkProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
      throw new Error("liquid form: " + (gl.getProgramInfoLog(program) || "link failed"));
    }
  } catch (err) {
    // Swallowed so the page still renders, but never silently.
    window.console.warn(err);
    if (vertex) gl.deleteShader(vertex);
    if (fragment) gl.deleteShader(fragment);
    if (program) gl.deleteProgram(program);
    return () => {};
  }

  gl.useProgram(program);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

  const buffer = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const position = gl.getAttribLocation(program, "a_pos");
  gl.enableVertexAttribArray(position);
  gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);

  const at = (name) => gl.getUniformLocation(program, name);
  const u = {
    res: at("u_res"), time: at("u_time"), mouse: at("u_mouse"),
    morph: at("u_morph"), noiseScale: at("u_noise_scale"),
    mouseAmount: at("u_mouse_amount"), metal: at("u_metal"), camera: at("u_camera"),
    lit: at("u_lit"), mid: at("u_mid"), deep: at("u_deep"),
    dark: at("u_dark"), hot: at("u_hot"), cold: at("u_cold"),
    fadeFrom: at("u_fade_from"),
  };

  const root = document.documentElement;
  let colors = palette(root);

  let fadeFrom = 0;
  let targetX = 0, targetY = 0, mouseX = 0, mouseY = 0;
  let frame = 0;
  let visible = true;
  const startedAt = window.performance.now();
  const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function draw(now) {
    mouseX += (targetX - mouseX) * 0.05;
    mouseY += (targetY - mouseY) * 0.05;
    gl.uniform2f(u.res, canvas.width, canvas.height);
    gl.uniform1f(u.time, (now - startedAt) * 0.001 * opts.speed);
    gl.uniform2f(u.mouse, mouseX, mouseY);
    gl.uniform1f(u.morph, opts.morph);
    gl.uniform1f(u.noiseScale, opts.noiseScale);
    gl.uniform1f(u.mouseAmount, opts.mouseAmount);
    gl.uniform1f(u.metal, opts.metal);
    gl.uniform1f(u.camera, opts.camera);
    gl.uniform3fv(u.lit, colors.lit);
    gl.uniform3fv(u.mid, colors.mid);
    gl.uniform3fv(u.deep, colors.deep);
    gl.uniform3fv(u.dark, colors.dark);
    gl.uniform3fv(u.hot, colors.hot);
    gl.uniform3fv(u.cold, colors.cold);
    gl.uniform1f(u.fadeFrom, fadeFrom);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  const reread = () => { colors = palette(root); if (still) draw(0); };

  const resize = () => {
    // The CANVAS's box, not the host's. Upstream's canvas fills its host so the
    // two are the same; here the host is the whole hero and the canvas is a
    // square inside it, and sizing the drawing buffer from the host would hand
    // the shader a u_res with the wrong aspect -- it normalises uv by
    // min(width, height), so the form would be stretched across the difference.
    const bounds = canvas.getBoundingClientRect();
    const ratio = Math.min(window.devicePixelRatio || 1, opts.maxPixelRatio) * opts.resolutionScale;
    canvas.width = Math.max(1, Math.round(bounds.width * ratio));
    canvas.height = Math.max(1, Math.round(bounds.height * ratio));
    gl.viewport(0, 0, canvas.width, canvas.height);
    // How much of the canvas hangs below the clipping wrap. Negative when the
    // canvas sits entirely inside it, which makes the smoothstep finish below
    // the visible area and the fade a no-op -- exactly what is wanted then.
    const clip = host.getBoundingClientRect();
    fadeFrom = bounds.height > 0 ? (bounds.bottom - clip.bottom) / bounds.height : 0;
    if (still) draw(0);
  };

  const pointer = (event) => {
    const bounds = canvas.getBoundingClientRect();
    targetX = ((event.clientX - bounds.left) / Math.max(1, bounds.width)) * 2 - 1;
    targetY = -(((event.clientY - bounds.top) / Math.max(1, bounds.height)) * 2 - 1);
  };

  const loop = (now) => {
    draw(now);
    frame = visible && !document.hidden ? window.requestAnimationFrame(loop) : 0;
  };

  const resizeObserver = new window.ResizeObserver(resize);
  resizeObserver.observe(canvas);

  // Reduced motion gets one frame, held: the form is the hero's subject, and
  // what the preference asks for is that it stop moving, not that it vanish.
  const intersection = new window.IntersectionObserver((entries) => {
    visible = entries[0] ? entries[0].isIntersecting : true;
    if (still) return;
    if (visible && !frame) frame = window.requestAnimationFrame(loop);
    if (!visible && frame) { window.cancelAnimationFrame(frame); frame = 0; }
  });
  intersection.observe(host);

  const themes = new window.MutationObserver(reread);
  themes.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
  const scheme = window.matchMedia("(prefers-color-scheme: dark)");
  scheme.addEventListener("change", reread);

  if (!still) canvas.addEventListener("pointermove", pointer, { passive: true });

  if (opts.tintAmount > 0) {
    canvas.style.filter =
      "sepia(" + opts.tintAmount + ") saturate(" + (1 + opts.tintAmount * 5) +
      ") hue-rotate(" + (opts.tintHue - 35) + "deg)";
  }

  resize();
  if (still) draw(0);
  else frame = window.requestAnimationFrame(loop);

  return function dispose() {
    if (frame) window.cancelAnimationFrame(frame);
    resizeObserver.disconnect();
    intersection.disconnect();
    themes.disconnect();
    scheme.removeEventListener("change", reread);
    canvas.removeEventListener("pointermove", pointer);
    gl.deleteBuffer(buffer);
    gl.deleteShader(vertex);
    gl.deleteShader(fragment);
    gl.deleteProgram(program);
  };
}

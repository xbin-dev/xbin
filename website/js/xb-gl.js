// <xb-gl> — the hero's live backdrop: a WebGL2 fragment shader drawing a
// field of chamfered tile cells with amber pulses traveling the wires between
// them. Pointer-reactive, pauses off-screen, renders a single static frame
// under prefers-reduced-motion, and hides itself (revealing the CSS fallback
// gradient) when WebGL2 is unavailable.
import { LitElement, html, css } from 'lit';

const VERT = `#version 300 es
layout(location=0) in vec2 a;
void main(){ gl_Position = vec4(a, 0., 1.); }`;

const FRAG = `#version 300 es
precision highp float;
uniform vec2  u_res;
uniform float u_t;
uniform vec2  u_mouse;   // px, y-down
out vec4 outColor;

// palette — keep in sync with the hero panel in index.html
const vec3 BG    = vec3(0.066, 0.074, 0.094); // #11131a
const vec3 LINE  = vec3(0.212, 0.235, 0.271); // --border
const vec3 AMBER = vec3(0.961, 0.651, 0.137); // --accent

float hash(vec2 p){ return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }

float sdBox(vec2 p, vec2 b){
  vec2 d = abs(p) - b;
  return length(max(d, 0.)) + min(max(d.x, d.y), 0.);
}

// rectangle with the top-right and bottom-left corners cut at 45°
float sdCutRect(vec2 p, vec2 b, float c){
  float d = sdBox(p, b);
  float cut1 = ( p.x + p.y - (b.x + b.y - c)) / 1.4142135;
  float cut2 = (-p.x - p.y - (b.x + b.y - c)) / 1.4142135;
  return max(d, max(cut1, cut2));
}

void main(){
  vec2 p  = gl_FragCoord.xy;
  vec2 uv = p / u_res;

  // the visual lives on the right; fade out under the copy column
  float mask = smoothstep(0.02, 0.58, uv.x) * 0.92 + 0.08;

  float cs = 96.0;                       // cell pitch, px
  vec2 id  = floor(p / cs);
  vec2 q   = mod(p, cs) - cs * 0.5;

  // --- chamfered cell lattice -------------------------------------------
  float d  = sdCutRect(q, vec2(cs * 0.5) - 1.5, 22.0);
  float aa = max(fwidth(d), 0.75);
  float edge = 1.0 - smoothstep(0.0, aa * 1.6, abs(d));

  // a few cells wake up at a time and pulse amber
  float h   = hash(id);
  float ph  = fract(h * 7.13 + u_t * (0.05 + h * 0.05));
  float on  = step(0.93, h);
  float pulse = smoothstep(0.0, 0.06, ph) * smoothstep(0.30, 0.06, ph) * on;

  vec3 col = BG;
  col += LINE * edge * 0.10 * mask;
  col += AMBER * edge * pulse * 0.55 * mask;
  col += AMBER * exp(-max(d, 0.0) / 22.0) * pulse * 0.030 * mask; // interior glow

  // --- pulses riding the grid lines --------------------------------------
  float lx = abs(q.x) - (cs * 0.5 - 1.5);        // distance past vertical edge
  float ly = abs(q.y) - (cs * 0.5 - 1.5);
  float hx = hash(vec2(id.y, 9.1));
  float vy = hash(vec2(id.x, 4.7));
  // horizontal runs
  float gh  = exp(-pow(max(ly, -1.5) * 1.4, 2.0));
  float dh  = smoothstep(0.86, 0.99, fract(p.x * 0.004 - u_t * (0.10 + hx * 0.22) + hx * 9.0));
  col += AMBER * gh * dh * step(0.72, hx) * 0.35 * mask;
  // vertical runs
  float gv  = exp(-pow(max(lx, -1.5) * 1.4, 2.0));
  float dv  = smoothstep(0.88, 0.995, fract(p.y * 0.005 + u_t * (0.08 + vy * 0.20) + vy * 7.0));
  col += AMBER * gv * dv * step(0.78, vy) * 0.28 * mask;

  // --- fine dot grid + pointer glow + vignette ---------------------------
  vec2 g = mod(p, 26.0) - 13.0;
  col += LINE * smoothstep(1.4, 0.4, length(g)) * 0.07 * mask;
  col += AMBER * exp(-distance(p, u_mouse) / 240.0) * 0.055;
  col *= mix(0.55, 1.0, smoothstep(1.3, 0.30, length(uv - vec2(0.62, 0.45)) * 1.35));

  outColor = vec4(col, 1.0);
}`;

class XbGl extends LitElement {
  static properties = { mouse: { type: Object, attribute: false } };
  static styles = css`
    :host { position: absolute; inset: 0; display: block; pointer-events: none; }
    canvas { width: 100%; height: 100%; display: block; }
  `;

  constructor() {
    super();
    this.mouse = null;              // {x, y} in element px, y-down
    this._raf = 0;
    this._running = false;
    this._t0 = performance.now();
    this._reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  render() { return html`<canvas part="canvas"></canvas>`; }

  firstUpdated() {
    const canvas = this.shadowRoot.querySelector('canvas');
    const gl = canvas.getContext('webgl2', { antialias: true, alpha: false });
    if (!gl) { this.style.display = 'none'; return; }   // CSS fallback shows through
    this._canvas = canvas; this._gl = gl;
    if (!this._program(gl)) { this.style.display = 'none'; return; }

    this._resize = () => {
      const dpr = Math.min(devicePixelRatio || 1, 2);
      const w = Math.max(1, Math.round(this.clientWidth * dpr));
      const hgt = Math.max(1, Math.round(this.clientHeight * dpr));
      if (canvas.width !== w || canvas.height !== hgt) {
        canvas.width = w; canvas.height = hgt;
        gl.viewport(0, 0, w, hgt);
      }
    };
    this._ro = new ResizeObserver(() => { this._resize(); if (this._reduce) this._frame(); });
    this._ro.observe(this);

    this._io = new IntersectionObserver(es => {
      es[0].isIntersecting ? this._start() : this._stop();
    }, { threshold: 0.02 });
    this._io.observe(this);

    this._onVis = () => document.hidden ? this._stop() : this._start();
    document.addEventListener('visibilitychange', this._onVis);

    canvas.addEventListener('webglcontextlost', e => {
      e.preventDefault(); this._stop(); this.style.display = 'none';
    });

    this._resize();
    if (this._reduce) this._frame();                    // one static frame
  }

  disconnectedCallback() {
    this._stop();
    this._ro?.disconnect(); this._io?.disconnect();
    document.removeEventListener('visibilitychange', this._onVis);
    super.disconnectedCallback();
  }

  _program(gl) {
    const mk = (type, src) => {
      const s = gl.createShader(type);
      gl.shaderSource(s, src); gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) {
        console.error('xb-gl:', gl.getShaderInfoLog(s)); return null;
      }
      return s;
    };
    const vs = mk(gl.VERTEX_SHADER, VERT), fs = mk(gl.FRAGMENT_SHADER, FRAG);
    if (!vs || !fs) return false;
    const pr = gl.createProgram();
    gl.attachShader(pr, vs); gl.attachShader(pr, fs); gl.linkProgram(pr);
    if (!gl.getProgramParameter(pr, gl.LINK_STATUS)) {
      console.error('xb-gl:', gl.getProgramInfoLog(pr)); return false;
    }
    gl.useProgram(pr);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    this._uRes = gl.getUniformLocation(pr, 'u_res');
    this._uT = gl.getUniformLocation(pr, 'u_t');
    this._uM = gl.getUniformLocation(pr, 'u_mouse');
    return true;
  }

  _start() {
    if (this._running || this._reduce || !this._gl) return;
    this._running = true;
    const loop = () => {
      if (!this._running) return;
      this._frame();
      this._raf = requestAnimationFrame(loop);
    };
    this._raf = requestAnimationFrame(loop);
  }

  _stop() {
    this._running = false;
    cancelAnimationFrame(this._raf);
  }

  _frame() {
    const gl = this._gl, c = this._canvas;
    gl.uniform2f(this._uRes, c.width, c.height);
    gl.uniform1f(this._uT, (performance.now() - this._t0) / 1000);
    const dpr = Math.min(devicePixelRatio || 1, 2);
    const m = this.mouse || { x: c.width * 0.72 / dpr, y: c.height * 0.4 / dpr };
    gl.uniform2f(this._uM, m.x * dpr, (this.clientHeight - m.y) * dpr);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }
}

customElements.define('xb-gl', XbGl);

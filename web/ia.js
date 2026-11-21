// ia.js: "Mejorar foto" on the phone's graphics card, with WebGL2 fragment shaders (WebGPU needs https, this page
// is plain http). The model comes as a list of convolutions (ia.json, made by an export script from the PyTorch
// model) and its weights as float16 (ia.bin). Today: SPAN "4x Nomos8k span otf medium" (CC-BY-4.0, by Helaman,
// openmodeldb.info/models/4x-Nomos8k-span-otf-medium).
//
// Each tensor is a texture array of RGBA16F, 4 channels per layer. A convolution reads 1..4 tensors (joined along
// the channels), 1x1 or 3x3, and writes up to 8 groups of 4 output channels per pass (one uniform block of weights
// per group). Activations: none, SiLU, PReLU, and SPAN's attention ((r + res) * (sigmoid(r) - 0.5)); "inAct" applies
// SiLU to the inputs. Outside the picture every convolution writes 0 (PyTorch's zero padding at each layer).
// The picture is done in tiles with a border of "halo" pixels (one per 3x3 convolution), so tiles join without seams.
// IA.upscale(source, model {graph, weights}, progress(fraction, seconds), opt) -> canvas 4x as big, or parts to opt.onTile.
var IA = (function () {
  var TILE_DEF = 256;

  function halfToFloat(u16) {
    var out = new Float32Array(u16.length);
    for (var i = 0; i < u16.length; i++) {
      var h = u16[i], s = h & 0x8000 ? -1 : 1, e = (h >> 10) & 31, m = h & 1023;
      out[i] = e == 0 ? s * m * Math.pow(2, -24) : e == 31 ? (m ? NaN : s * Infinity) : s * (1 + m / 1024) * Math.pow(2, e - 15);
    }
    return out;
  }

  // the weights of each op, in the order of the export script
  function load(graph, buf) {
    var f = halfToFloat(new Uint16Array(buf)), p = 0;
    function take(n) { var a = f.subarray(p, p + n); p += n; return a }
    graph.ops.forEach(function (op) {
      op.w = take(op.cout * op.cin * op.k * op.k); op.b = take(op.cout); op.alpha = op.prelu ? take(op.cout) : null;
    });
    if (p != f.length) throw Error('modelo: tamaño inesperado');
  }

  // channels per tensor (the picture: 3)
  function channels(graph) {
    var c = {x: 3};
    graph.ops.forEach(function (op) { c[op.out] = op.cout });
    return c;
  }

  // one group of 4 output channels (o): for each input tensor, each of its layers, each tap: 4 columns
  // (input channel -> the 4 output channels); then bias; then the PReLU slope (1 = none)
  function block(op, o, ch) {
    var kk = op.k * op.k, per = 2, d;
    op.in.forEach(function (n) { per += Math.ceil(ch[n] / 4) * kk * 4 });
    d = new Float32Array(per * 4);
    var q = 0, off = 0, j;
    op.in.forEach(function (n) {
      var L = Math.ceil(ch[n] / 4);
      for (var l = 0; l < L; l++) for (var t = 0; t < kk; t++) for (var k = 0; k < 4; k++) {
        for (j = 0; j < 4; j++) {
          var co = 4 * o + j, ci = 4 * l + k;
          d[q * 4 + j] = ci < ch[n] && co < op.cout ? op.w[(co * op.cin + off + ci) * kk + t] : 0;
        }
        q++;
      }
      off += ch[n];
    });
    for (j = 0; j < 4; j++) {
      d[q * 4 + j] = 4 * o + j < op.cout ? op.b[4 * o + j] : 0;
      d[(q + 1) * 4 + j] = op.alpha && 4 * o + j < op.cout ? op.alpha[4 * o + j] : 1;
    }
    return d;
  }

  // the shader of one pass: inputs (layers each), k, n output groups, activations, residual tensor
  function convSource(layers, k, n, act, inAct, res, prec) {
    var kk = k * k, per = 2, decl = '', acc = '', body = '', fin = '', o;
    layers.forEach(function (L) { per += L * kk * 4 });
    for (o = 0; o < n; o++) {
      decl += 'layout(std140) uniform W' + o + '{vec4 w' + o + '[' + per + '];};\nlayout(location=' + o + ') out vec4 o' + o + ';\n';
      acc += 'vec4 a' + o + '=w' + o + '[' + (per - 2) + '];\n';
      body += 'a' + o + '+=w' + o + '[b]*v.x+w' + o + '[b+1]*v.y+w' + o + '[b+2]*v.z+w' + o + '[b+3]*v.w;\n';
      var r = 'a' + o;
      if (act == 'silu') r = 'a' + o + '/(1.0+exp(-a' + o + '))';
      else if (act == 'prelu') r = 'max(a' + o + ',0.0)+w' + o + '[' + (per - 1) + ']*min(a' + o + ',0.0)';
      else if (act == 'spab') r = '(a' + o + '+texelFetch(res,ivec3(p,first+' + o + '),0))*(1.0/(1.0+exp(-a' + o + '))-0.5)';
      fin += 'o' + o + '=inside?' + r + ':vec4(0);\n';
    }
    var src = '#version 300 es\nprecision ' + (prec || 'highp') + ' float;precision highp int;precision highp sampler2DArray;\n' +
      'uniform ivec4 img;uniform int first;\n'; // img: tile origin in the picture (x,y), picture size (w,h); first: first output group
    layers.forEach(function (L, i) { src += 'uniform sampler2DArray in' + i + ';\n' });
    if (res) src += 'uniform sampler2DArray res;\n';
    src += decl + 'void main(){ivec2 p=ivec2(gl_FragCoord.xy),q=p+img.xy,sz=textureSize(in0,0).xy;\n' +
      'bool inside=all(greaterThanEqual(q,ivec2(0)))&&all(lessThan(q,img.zw));\n' + acc + 'int b=0;vec4 v;\n';
    var base = 0;
    layers.forEach(function (L, i) {
      src += 'for(int l=0;l<' + L + ';l++)for(int t=0;t<' + kk + ';t++){' +
        (k == 3 ? 'ivec2 r=p+ivec2(t-t/3*3-1,t/3-1);' : 'ivec2 r=p;') + 'b=' + base + '+(l*' + kk + '+t)*4;\n' +
        'v=all(greaterThanEqual(r,ivec2(0)))&&all(lessThan(r,sz))?texelFetch(in' + i + ',ivec3(r,l),0):vec4(0);\n' +
        (inAct == 'silu' ? 'v=v/(1.0+exp(-v));\n' : '') + body + '}\n';
      base += L * kk * 4;
    });
    return src + fin + '}';
  }

  // pixel shuffle x4 (channel c*16 + row*4 + column), plus the input made 4x bigger when the model adds it
  function shufSource(addInput) {
    return '#version 300 es\nprecision highp float;precision highp int;precision highp sampler2DArray;\n' +
      'uniform sampler2DArray feat,src;uniform int halo;out vec4 o;\n' +
      'float pick(vec4 v,int c){return c==0?v.x:c==1?v.y:c==2?v.z:v.w;}\n' +
      'void main(){ivec2 P=ivec2(gl_FragCoord.xy),p=P/4+halo,s=P-P/4*4;int ch=s.y*4+s.x;\n' +
      'vec3 r=vec3(pick(texelFetch(feat,ivec3(p,ch/4),0),ch-ch/4*4),pick(texelFetch(feat,ivec3(p,4+ch/4),0),ch-ch/4*4),' +
      'pick(texelFetch(feat,ivec3(p,8+ch/4),0),ch-ch/4*4));\n' +
      (addInput ? 'r+=texelFetch(src,ivec3(p,0),0).rgb;\n' : '') + 'o=vec4(clamp(r,0.0,1.0),1.0);}';
  }
  var VERT = '#version 300 es\nvoid main(){vec2 p=vec2(gl_VertexID&1,gl_VertexID>>1)*4.0-1.0;gl_Position=vec4(p,0,1);}';

  function program(gl, fs) {
    function sh(t, s) {
      var x = gl.createShader(t); gl.shaderSource(x, s); gl.compileShader(x);
      if (!gl.getShaderParameter(x, gl.COMPILE_STATUS)) throw Error('shader: ' + gl.getShaderInfoLog(x));
      return x;
    }
    var p = gl.createProgram(); gl.attachShader(p, sh(gl.VERTEX_SHADER, VERT)); gl.attachShader(p, sh(gl.FRAGMENT_SHADER, fs));
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw Error('programa: ' + gl.getProgramInfoLog(p));
    return p;
  }

  function wait(gl) { // let the graphics card finish without blocking the page
    var s = gl.fenceSync(gl.SYNC_GPU_COMMANDS_COMPLETE, 0); gl.flush();
    return new Promise(function (res) {
      (function poll() {
        var r = gl.clientWaitSync(s, 0, 0);
        if (r == gl.TIMEOUT_EXPIRED) { setTimeout(poll, 4); return }
        gl.deleteSync(s); res();
      })();
    });
  }

  async function upscale(source, model, progress, opt) {
    opt = opt || {};
    var g = model.graph, TILE = opt.tile || TILE_DEF, HALO = g.halo, SC = g.scale;
    if (!g.ops[0].w) load(g, model.weights);
    var ch = channels(g), W = source.width, H = source.height;
    var cv = document.createElement('canvas'); cv.width = cv.height = 1;
    var gl = cv.getContext('webgl2', {antialias: false, depth: false, premultipliedAlpha: false});
    if (!gl) throw Error('este navegador no tiene WebGL2');
    if (!gl.getExtension('EXT_color_buffer_float')) throw Error('la placa no puede escribir en float16');
    var mdb = Math.min(gl.getParameter(gl.MAX_DRAW_BUFFERS), gl.getParameter(gl.MAX_COLOR_ATTACHMENTS));
    var mub = gl.getParameter(gl.MAX_FRAGMENT_UNIFORM_BLOCKS), mts = gl.getParameter(gl.MAX_TEXTURE_IMAGE_UNITS);
    var N = [8, 4, 2, 1].filter(function (n) { return n <= mdb && n <= mub && (!opt.n || n <= opt.n) })[0];

    // tensors: one texture array each
    var S = TILE + 2 * HALO, tex = {};
    function arr(layers) {
      var t = gl.createTexture(); gl.bindTexture(gl.TEXTURE_2D_ARRAY, t);
      gl.texStorage3D(gl.TEXTURE_2D_ARRAY, 1, gl.RGBA16F, S, S, layers);
      [gl.TEXTURE_MIN_FILTER, gl.TEXTURE_MAG_FILTER].forEach(function (f) { gl.texParameteri(gl.TEXTURE_2D_ARRAY, f, gl.NEAREST) });
      return t;
    }
    Object.keys(ch).forEach(function (n) { tex[n] = arr(Math.ceil(ch[n] / 4)) });

    // passes: programs (cached by their shape) and the weights of each output group
    var progs = {}, passes = [];
    g.ops.forEach(function (op) {
      var layers = op.in.map(function (n) { return Math.ceil(ch[n] / 4) }), groups = Math.ceil(op.cout / 4);
      if (layers.length + (op.res ? 1 : 0) > mts) throw Error('la placa tiene pocas texturas');
      var n = Math.min(N, groups), key = [layers.join(','), op.k, n, op.act, op.inAct, !!op.res].join('|'), o;
      if (!progs[key]) {
        var pr = program(gl, convSource(layers, op.k, n, op.act, op.inAct, op.res, opt.prec));
        for (o = 0; o < n; o++) gl.uniformBlockBinding(pr, gl.getUniformBlockIndex(pr, 'W' + o), o);
        progs[key] = pr;
      }
      var bufs = [];
      for (o = 0; o < groups; o++) {
        var buf = gl.createBuffer(); gl.bindBuffer(gl.UNIFORM_BUFFER, buf);
        gl.bufferData(gl.UNIFORM_BUFFER, block(op, o, ch), gl.STATIC_DRAW); bufs.push(buf);
      }
      for (o = 0; o < groups; o += n) passes.push({op: op, prog: progs[key], n: n, used: Math.min(n, groups - o), o0: o, bufs: bufs.slice(o, o + n)});
    });
    var progShuf = program(gl, shufSource(g.final.addInput));

    var out = gl.createTexture(); gl.bindTexture(gl.TEXTURE_2D, out); // one tile's result
    gl.texStorage2D(gl.TEXTURE_2D, 1, gl.RGBA8, SC * TILE, SC * TILE);
    var fb = gl.createFramebuffer(), vao = gl.createVertexArray(); gl.bindVertexArray(vao);

    // the source picture as floats (0..1, or normalized like the model was trained)
    var c2 = document.createElement('canvas'); c2.width = W; c2.height = H;
    var x2 = c2.getContext('2d', {willReadFrequently: true}); x2.drawImage(source, 0, 0, W, H);
    var px = x2.getImageData(0, 0, W, H).data, tin = new Float32Array(S * S * 4);
    var nm = g.norm ? g.norm.mean : [0, 0, 0], nr = g.norm ? g.norm.range : 1;

    var result = null, rx = null;
    if (!opt.onTile) { result = document.createElement('canvas'); result.width = SC * W; result.height = SC * H; rx = result.getContext('2d') }
    var nx = Math.ceil(W / TILE), ny = Math.ceil(H / TILE), done = 0, t0 = performance.now(), todo = [], tx, ty;
    for (ty = 0; ty < ny; ty++) for (tx = 0; tx < nx; tx++)
      if (!(opt.skip && opt.skip(SC * tx * TILE, SC * ty * TILE)))
        todo.push({tx: tx, ty: ty, cx: (tx * TILE + Math.min(TILE, W - tx * TILE) / 2) / W, cy: (ty * TILE + Math.min(TILE, H - ty * TILE) / 2) / H});
    var total = todo.length;
    while (todo.length) {
      if (opt.abort && opt.abort()) break;
      // opt.next picks the next part (e.g. the one nearest to what the person looks at)
      var tile = todo.splice(opt.next ? opt.next(todo) : 0, 1)[0]; tx = tile.tx; ty = tile.ty;
      var x0 = tx * TILE - HALO, y0 = ty * TILE - HALO;
      for (var y = 0; y < S; y++) for (var x = 0; x < S; x++) {
        var X = x0 + x, Y = y0 + y, i = (y * S + x) * 4;
        if (X < 0 || Y < 0 || X >= W || Y >= H) { tin[i] = tin[i + 1] = tin[i + 2] = tin[i + 3] = 0; continue }
        var j = (Y * W + X) * 4;
        tin[i] = (px[j] / 255 - nm[0]) * nr; tin[i + 1] = (px[j + 1] / 255 - nm[1]) * nr; tin[i + 2] = (px[j + 2] / 255 - nm[2]) * nr; tin[i + 3] = 0;
      }
      gl.bindTexture(gl.TEXTURE_2D_ARRAY, tex.x);
      gl.texSubImage3D(gl.TEXTURE_2D_ARRAY, 0, 0, 0, 0, S, S, 1, gl.RGBA, gl.FLOAT, tin);

      gl.bindFramebuffer(gl.FRAMEBUFFER, fb); gl.viewport(0, 0, S, S);
      for (var pi = 0; pi < passes.length; pi++) {
        var ps = passes[pi], op = ps.op, a;
        for (a = 0; a < N; a++) gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0 + a, a < ps.used ? tex[op.out] : null, 0, ps.o0 + a);
        gl.drawBuffers([0, 1, 2, 3, 4, 5, 6, 7].slice(0, ps.n).map(function (a) { return a < ps.used ? gl.COLOR_ATTACHMENT0 + a : gl.NONE }));
        gl.useProgram(ps.prog);
        for (var o = 0; o < ps.n; o++) gl.bindBufferBase(gl.UNIFORM_BUFFER, o, ps.bufs[o] || ps.bufs[0]); // unused outputs: any buffer
        op.in.forEach(function (n, k) {
          gl.activeTexture(gl.TEXTURE0 + k); gl.bindTexture(gl.TEXTURE_2D_ARRAY, tex[n]);
          gl.uniform1i(gl.getUniformLocation(ps.prog, 'in' + k), k);
        });
        if (op.res) {
          gl.activeTexture(gl.TEXTURE0 + op.in.length); gl.bindTexture(gl.TEXTURE_2D_ARRAY, tex[op.res]);
          gl.uniform1i(gl.getUniformLocation(ps.prog, 'res'), op.in.length);
        }
        gl.uniform4i(gl.getUniformLocation(ps.prog, 'img'), x0, y0, W, H);
        gl.uniform1i(gl.getUniformLocation(ps.prog, 'first'), ps.o0);
        gl.drawArrays(gl.TRIANGLES, 0, 3);
        if ((pi & 7) == 7) await wait(gl);
      }
      for (a = 0; a < N; a++) gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0 + a, null, 0, 0);
      if (opt.dump) opt.dump.forEach(function (n) { // tests: layer 0 of a tensor (the whole tile, with its border)
        gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, tex[n], 0, 0); gl.readBuffer(gl.COLOR_ATTACHMENT0);
        var f = new Float32Array(S * S * 4); gl.readPixels(0, 0, S, S, gl.RGBA, gl.FLOAT, f); (opt.dumped = opt.dumped || {})[n] = {S: S, halo: HALO, f: f};
        gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, null, 0, 0);
      });
      // shuffle this tile's core into the result
      var cw = Math.min(TILE, W - tx * TILE), chh = Math.min(TILE, H - ty * TILE);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, out, 0);
      gl.drawBuffers([gl.COLOR_ATTACHMENT0]);
      gl.viewport(0, 0, SC * cw, SC * chh);
      gl.useProgram(progShuf);
      gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D_ARRAY, tex[g.final.in]);
      gl.activeTexture(gl.TEXTURE1); gl.bindTexture(gl.TEXTURE_2D_ARRAY, tex.x);
      gl.uniform1i(gl.getUniformLocation(progShuf, 'feat'), 0); gl.uniform1i(gl.getUniformLocation(progShuf, 'src'), 1);
      gl.uniform1i(gl.getUniformLocation(progShuf, 'halo'), HALO);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
      await wait(gl);
      var rp = new Uint8ClampedArray(SC * SC * cw * chh * 4);
      gl.readPixels(0, 0, SC * cw, SC * chh, gl.RGBA, gl.UNSIGNED_BYTE, rp);
      var part = new ImageData(rp, SC * cw, SC * chh);
      if (opt.onTile) opt.onTile(SC * tx * TILE, SC * ty * TILE, part); else rx.putImageData(part, SC * tx * TILE, SC * ty * TILE);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, null, 0);
      done++;
      if (progress) progress(done / total, (performance.now() - t0) / 1000);
    }
    var lose = gl.getExtension('WEBGL_lose_context'); if (lose) lose.loseContext();
    return result;
  }
  return {upscale: upscale, TILE: TILE_DEF};
})();

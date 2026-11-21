// ia.js: "Mejorar foto" on the phone's graphics card. Real-ESRGAN realesr-general-x4v3 (SRVGGNetCompact,
// BSD-3, github.com/xinntao/Real-ESRGAN): 34 3x3 convolutions of 64 channels with PReLU, then pixel shuffle x4
// plus the input made 4x bigger (nearest). WebGL2 fragment shaders (WebGPU needs https, this page is plain http).
// Activations: texture arrays of RGBA16F, 4 channels per layer. Weights: uniform blocks.
// The picture is done in tiles with a border of 34 pixels (one per convolution), so tiles join without seams.
// iaUpscale(source, weights ArrayBuffer (float16), progress(fraction)) -> Promise of a canvas 4x as big.
var IA = (function () {
  var CONVS = 34, HALO = 34, TILE_DEF = 256;

  function halfToFloat(u16) {
    var out = new Float32Array(u16.length);
    for (var i = 0; i < u16.length; i++) {
      var h = u16[i], s = h & 0x8000 ? -1 : 1, e = (h >> 10) & 31, m = h & 1023;
      out[i] = e == 0 ? s * m * Math.pow(2, -24) : e == 31 ? (m ? NaN : s * Infinity) : s * (1 + m / 1024) * Math.pow(2, e - 15);
    }
    return out;
  }

  // the model's layers: [{cin, cout, w (cout,cin,3,3), b, alpha|null}]
  function layers(buf) {
    var f = halfToFloat(new Uint16Array(buf)), p = 0, L = [];
    function take(n) { var a = f.subarray(p, p + n); p += n; return a }
    for (var k = 0; k < CONVS; k++) {
      var cin = k == 0 ? 3 : 64, cout = k == CONVS - 1 ? 48 : 64;
      var w = take(cout * cin * 9), b = take(cout), a = k == CONVS - 1 ? null : take(cout);
      L.push({cin: cin, cout: cout, w: w, b: b, alpha: a});
    }
    if (p != f.length) throw Error('modelo: tamaño inesperado');
    return L;
  }

  // the uniform block of one pass: for each of the n outputs (4 channels each, from o0): for each input layer,
  // for each of the 9 taps: 4 columns (input channel 4l+k -> the 4 output channels); then bias; then alpha.
  function block(ly, o0, n, cin4) {
    var per = cin4 * 9 * 4 + 2, d = new Float32Array(n * per * 4);
    for (var o = 0; o < n; o++) {
      var base = o * per * 4;
      for (var l = 0; l < cin4; l++) for (var t = 0; t < 9; t++) for (var k = 0; k < 4; k++) for (var j = 0; j < 4; j++) {
        var co = 4 * (o0 + o) + j, ci = 4 * l + k;
        d[base + ((l * 9 + t) * 4 + k) * 4 + j] = ci < ly.cin ? ly.w[(co * ly.cin + ci) * 9 + t] : 0;
      }
      for (j = 0; j < 4; j++) {
        d[base + (per - 2) * 4 + j] = ly.b[4 * (o0 + o) + j];
        d[base + (per - 1) * 4 + j] = ly.alpha ? ly.alpha[4 * (o0 + o) + j] : 1; // alpha 1 = no PReLU
      }
    }
    return d;
  }

  // one pass: n output groups (4 channels each); group o's weights in its own uniform block Wo (one block can be
  // as small as 16 KB, one group needs 9.2 KB)
  function convSource(cin4, n, prec) {
    var per = cin4 * 36 + 2, decl = '', acc = '', body = '', fin = '';
    for (var o = 0; o < n; o++) {
      decl += 'layout(std140) uniform W' + o + '{vec4 w' + o + '[' + per + '];};\nlayout(location=' + o + ') out vec4 o' + o + ';\n';
      acc += 'vec4 a' + o + '=w' + o + '[' + (per - 2) + '];\n';
      body += 'a' + o + '+=w' + o + '[b]*v.x+w' + o + '[b+1]*v.y+w' + o + '[b+2]*v.z+w' + o + '[b+3]*v.w;\n';
      fin += 'o' + o + '=inside?max(a' + o + ',0.0)+w' + o + '[' + (per - 1) + ']*min(a' + o + ',0.0):vec4(0);\n';
    }
    return '#version 300 es\nprecision ' + (prec || 'highp') + ' float;precision highp int;precision highp sampler2DArray;\n' +
      'uniform sampler2DArray inp;uniform ivec4 img;\n' + decl + // img: tile origin in the picture (x,y), picture size (w,h)
      'void main(){ivec2 p=ivec2(gl_FragCoord.xy),q=p+img.xy,sz=textureSize(inp,0).xy;\n' +
      'bool inside=all(greaterThanEqual(q,ivec2(0)))&&all(lessThan(q,img.zw));\n' + acc +
      'for(int l=0;l<' + cin4 + ';l++)for(int t=0;t<9;t++){ivec2 r=p+ivec2(t-t/3*3-1,t/3-1);int b=(l*9+t)*4;\n' +
      'vec4 v=all(greaterThanEqual(r,ivec2(0)))&&all(lessThan(r,sz))?texelFetch(inp,ivec3(r,l),0):vec4(0);\n' + body + '}\n' + fin + '}';
  }

  // pixel shuffle x4 + the input (nearest), into the big output (RGBA8)
  var SHUF = '#version 300 es\nprecision highp float;precision highp int;precision highp sampler2DArray;\n' +
    'uniform sampler2DArray feat,src;uniform ivec2 org;uniform int halo;out vec4 o;\n' +
    'float pick(vec4 v,int c){return c==0?v.x:c==1?v.y:c==2?v.z:v.w;}\n' +
    'void main(){ivec2 P=ivec2(gl_FragCoord.xy)-org,p=P/4+halo,s=P-P/4*4;int ch=s.y*4+s.x;\n' +
    'vec3 r=vec3(pick(texelFetch(feat,ivec3(p,ch/4),0),ch-ch/4*4),pick(texelFetch(feat,ivec3(p,4+ch/4),0),ch-ch/4*4),' +
    'pick(texelFetch(feat,ivec3(p,8+ch/4),0),ch-ch/4*4));\n' +
    'o=vec4(clamp(r+texelFetch(src,ivec3(p,0),0).rgb,0.0,1.0),1.0);}';
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

  function maxSize() {
    var gl = document.createElement('canvas').getContext('webgl2');
    return gl ? gl.getParameter(gl.MAX_TEXTURE_SIZE) : 0;
  }

  async function upscale(source, weights, progress, opt) {
    opt = opt || {};
    var TILE = opt.tile || TILE_DEF;
    var W = source.width, H = source.height;
    var cv = document.createElement('canvas'); cv.width = cv.height = 1;
    var gl = cv.getContext('webgl2', {antialias: false, depth: false, premultipliedAlpha: false});
    if (!gl) throw Error('este navegador no tiene WebGL2');
    if (!gl.getExtension('EXT_color_buffer_float')) throw Error('la placa no puede escribir en float16');
        var mdb = Math.min(gl.getParameter(gl.MAX_DRAW_BUFFERS), gl.getParameter(gl.MAX_COLOR_ATTACHMENTS)), mub = gl.getParameter(gl.MAX_FRAGMENT_UNIFORM_BLOCKS);
    var N = [8, 4, 2, 1].filter(function (n) { return n <= mdb && n <= mub && (!opt.n || n <= opt.n) })[0];
    var L = layers(weights);

    // programs, and the weights of each layer's output groups (one uniform buffer each), made once
    var progFirst = program(gl, convSource(1, 1)), progMid = program(gl, convSource(16, N, opt.prec)), progShuf = program(gl, SHUF);
    [progFirst, progMid].forEach(function (p, i) {
      for (var o = 0; o < (i ? N : 1); o++) gl.uniformBlockBinding(p, gl.getUniformBlockIndex(p, 'W' + o), o);
    });
    var passes = [];
    L.forEach(function (ly, k) {
      var cin4 = k == 0 ? 1 : 16, n = k == 0 ? 1 : N, bufs = [];
      for (var o = 0; o < ly.cout / 4; o++) {
        var buf = gl.createBuffer(); gl.bindBuffer(gl.UNIFORM_BUFFER, buf);
        gl.bufferData(gl.UNIFORM_BUFFER, block(ly, o, 1, cin4), gl.STATIC_DRAW); bufs.push(buf);
      }
      for (o = 0; o < bufs.length; o += n)
        passes.push({k: k, prog: k == 0 ? progFirst : progMid, bufs: bufs.slice(o, o + n), o0: o, n: Math.min(n, bufs.length - o)});
    });

    var S = TILE + 2 * HALO;
    function arr(layers) {
      var t = gl.createTexture(); gl.bindTexture(gl.TEXTURE_2D_ARRAY, t);
      gl.texStorage3D(gl.TEXTURE_2D_ARRAY, 1, gl.RGBA16F, S, S, layers);
      [gl.TEXTURE_MIN_FILTER, gl.TEXTURE_MAG_FILTER].forEach(function (f) { gl.texParameteri(gl.TEXTURE_2D_ARRAY, f, gl.NEAREST) });
      return t;
    }
    var src = arr(1), ping = [arr(16), arr(16)];
    var out = gl.createTexture(); gl.bindTexture(gl.TEXTURE_2D, out); // one tile's result
    gl.texStorage2D(gl.TEXTURE_2D, 1, gl.RGBA8, 4 * TILE, 4 * TILE);
    var fb = gl.createFramebuffer(), vao = gl.createVertexArray(); gl.bindVertexArray(vao);

    // the source picture as floats 0..1
    var c2 = document.createElement('canvas'); c2.width = W; c2.height = H;
    var x2 = c2.getContext('2d', {willReadFrequently: true}); x2.drawImage(source, 0, 0, W, H);
    var px = x2.getImageData(0, 0, W, H).data;
    var tin = new Float32Array(S * S * 4);

    // the result: given to opt.onTile(x, y, ImageData) part by part, or put together in one canvas
    var result = null, rx = null;
    if (!opt.onTile) { result = document.createElement('canvas'); result.width = 4 * W; result.height = 4 * H; rx = result.getContext('2d') }
    var nx = Math.ceil(W / TILE), ny = Math.ceil(H / TILE), done = 0, t0 = performance.now(), todo = [];
    for (var ty = 0; ty < ny; ty++) for (var tx = 0; tx < nx; tx++)
      if (!(opt.skip && opt.skip(4 * tx * TILE, 4 * ty * TILE))) todo.push({tx: tx, ty: ty, cx: (tx * TILE + Math.min(TILE, W - tx * TILE) / 2) / W, cy: (ty * TILE + Math.min(TILE, H - ty * TILE) / 2) / H});
    var total = todo.length;
    while (todo.length) {
      if (opt.abort && opt.abort()) break;
      // opt.next picks the next part (e.g. the one nearest to what the person looks at)
      var tile = todo.splice(opt.next ? opt.next(todo) : 0, 1)[0]; tx = tile.tx; ty = tile.ty;
      {
        var x0 = tx * TILE - HALO, y0 = ty * TILE - HALO;
        for (var y = 0; y < S; y++) for (var x = 0; x < S; x++) {
          var X = x0 + x, Y = y0 + y, i = (y * S + x) * 4;
          if (X < 0 || Y < 0 || X >= W || Y >= H) { tin[i] = tin[i + 1] = tin[i + 2] = tin[i + 3] = 0; continue }
          var j = (Y * W + X) * 4; tin[i] = px[j] / 255; tin[i + 1] = px[j + 1] / 255; tin[i + 2] = px[j + 2] / 255; tin[i + 3] = 0;
        }
        gl.bindTexture(gl.TEXTURE_2D_ARRAY, src);
        gl.texSubImage3D(gl.TEXTURE_2D_ARRAY, 0, 0, 0, 0, S, S, 1, gl.RGBA, gl.FLOAT, tin);

        gl.bindFramebuffer(gl.FRAMEBUFFER, fb); gl.viewport(0, 0, S, S);
        var cur = 0;
        for (var pi = 0; pi < passes.length; pi++) {
          var ps = passes[pi], inT = ps.k == 0 ? src : ping[cur], outT = ping[ps.k == 0 ? 0 : 1 - cur];
          for (var a = 0; a < N; a++) gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0 + a, a < ps.n ? outT : null, 0, ps.o0 + a);
          gl.drawBuffers([0, 1, 2, 3, 4, 5, 6, 7].slice(0, ps.n).map(function (a) { return gl.COLOR_ATTACHMENT0 + a }));
          gl.useProgram(ps.prog); for (var o = 0; o < (ps.k ? N : 1); o++) gl.bindBufferBase(gl.UNIFORM_BUFFER, o, ps.bufs[o] || ps.bufs[0]); // unused outputs: any buffer
          gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D_ARRAY, inT);
          gl.uniform1i(gl.getUniformLocation(ps.prog, 'inp'), 0);
          gl.uniform4i(gl.getUniformLocation(ps.prog, 'img'), x0, y0, W, H);
          gl.drawArrays(gl.TRIANGLES, 0, 3);
          var next = passes[pi + 1];
          if (!next || next.k != ps.k) { cur = ps.k == 0 ? 0 : 1 - cur; if ((ps.k & 7) == 7) await wait(gl) }
        }
        for (a = 1; a < N; a++) gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0 + a, null, 0, 0);
        // shuffle this tile's core into the big output
        var cw = Math.min(TILE, W - tx * TILE), ch = Math.min(TILE, H - ty * TILE);
        gl.framebufferTextureLayer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, null, 0, 0);
        gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, out, 0);
        gl.drawBuffers([gl.COLOR_ATTACHMENT0]);
        gl.viewport(0, 0, 4 * cw, 4 * ch);
        gl.useProgram(progShuf);
        gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D_ARRAY, ping[cur]);
        gl.activeTexture(gl.TEXTURE1); gl.bindTexture(gl.TEXTURE_2D_ARRAY, src);
        gl.uniform1i(gl.getUniformLocation(progShuf, 'feat'), 0); gl.uniform1i(gl.getUniformLocation(progShuf, 'src'), 1);
        gl.uniform2i(gl.getUniformLocation(progShuf, 'org'), 0, 0);
        gl.uniform1i(gl.getUniformLocation(progShuf, 'halo'), HALO);
        gl.drawArrays(gl.TRIANGLES, 0, 3);
        await wait(gl);
        var rp = new Uint8ClampedArray(16 * cw * ch * 4);
        gl.readPixels(0, 0, 4 * cw, 4 * ch, gl.RGBA, gl.UNSIGNED_BYTE, rp);
        var part = new ImageData(rp, 4 * cw, 4 * ch);
        if (opt.onTile) opt.onTile(4 * tx * TILE, 4 * ty * TILE, part); else rx.putImageData(part, 4 * tx * TILE, 4 * ty * TILE);
        gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, null, 0);
        done++;
        if (progress) progress(done / total, (performance.now() - t0) / 1000);
      }
    }
    var lose = gl.getExtension('WEBGL_lose_context'); if (lose) lose.loseContext();
    return result;
  }
  return {upscale: upscale, maxSize: maxSize, layers: layers, TILE: TILE_DEF};
})();

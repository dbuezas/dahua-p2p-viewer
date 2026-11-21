# ia_export.py <model.pth|.safetensors> <ia.bin> <ia.json>: makes the "Mejorar foto" model files for web/ia.js.
# Needs torch and spandrel (pip install torch spandrel). Today: 4x-Nomos8k-span-otf-medium from openmodeldb.info.
import sys, json, torch, numpy as np
from spandrel import ModelLoader
m = ModelLoader().load_from_file(sys.argv[1]).eval()
net, arch = m.model, m.architecture.name
W, ops = [], []
def add(a):
    a = a.detach().float().numpy().ravel(); W.append(a.astype(np.float16)); return int(a.size)
def conv(c, inputs, out, k, act="none", in_act="none", res=None, alpha=None):
    if hasattr(c, "eval_conv"):
        c.update_params(); c = c.eval_conv
    op = {"in": inputs, "out": out, "k": k, "cin": c.weight.shape[1], "cout": c.weight.shape[0], "act": act, "inAct": in_act}
    if res: op["res"] = res
    add(c.weight); add(c.bias)
    if alpha is not None: add(alpha); op["prelu"] = True
    ops.append(op)
g = {"arch": arch, "scale": m.scale}
if arch == "SPAN":
    if net.is_norm: g["norm"] = {"mean": [float(v) for v in net.mean.view(-1)], "range": float(net.img_range)}
    conv(net.conv_1, ["x"], "F", 3)
    prev = "F"
    for i in range(1, 7):
        b = getattr(net, f"block_{i}")
        if i == 6:  # its first output also goes to the end: after SiLU (SPAB's SiLU is in place, so out1 has it)
            conv(b.c1_r, [prev], "o16", 3, act="silu"); conv(b.c2_r, ["o16"], "t2", 3, act="silu")
        else:
            conv(b.c1_r, [prev], "t1", 3, act="silu"); conv(b.c2_r, ["t1"], "t2", 3, act="silu")
        out = "b1" if i == 1 else ("c" if prev != "c" else "d")
        conv(b.c3_r, ["t2"], out, 3, act="spab", res=prev)
        prev = out
    conv(net.conv_2, [prev], "b6c", 3)
    conv(net.conv_cat, ["F", "b6c", "b1", "o16"], "cat", 1)
    conv(net.upsampler[0], ["cat"], "up", 3)
    g["final"] = {"in": "up", "addInput": False}
else:
    raise SystemExit("solo SPAN por ahora: " + arch)
g["ops"] = ops
halo = sum(1 for o in ops if o["k"] == 3)
g["halo"] = halo
open(sys.argv[2], "wb").write(np.concatenate(W).tobytes())
json.dump(g, open(sys.argv[3], "w"), indent=1)
print(arch, "ops", len(ops), "halo", halo, "floats", sum(a.size for a in W))

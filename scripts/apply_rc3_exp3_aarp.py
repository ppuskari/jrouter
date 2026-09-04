from pathlib import Path

path = Path("router/aarp.go")
text = path.read_text(encoding="utf-8")
old = "\trespFrame.Dst = targ.Hardware\n\treturn a.send(respFrame)\n"
new = """\trespFrame.Dst = targ.Hardware\n\tif err := a.send(respFrame); err != nil {\n\t\treturn err\n\t}\n\tif err := mirrorAARPResponse(a.port, respFrame); err != nil {\n\t\ta.logger.Warn(\n\t\t\t\"AARP: experimental Windows SendToRx mirror failed\",\n\t\t\t\"error\", err,\n\t\t)\n\t}\n\treturn nil\n"""

if new in text:
    raise SystemExit(0)
if old not in text:
    raise SystemExit("expected AARP response send pattern not found")

path.write_text(text.replace(old, new, 1), encoding="utf-8")

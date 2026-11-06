"""Copy a base GGUF's end-of-generation tokens and chat template onto a fine-tune.

    copy_gguf_tokens.py <llama.cpp dir> <base.gguf> <in.gguf> <out.gguf>

llama.cpp's converter takes the end token from the tokenizer config, which
for Gemma 4 names <eos>, while the model ends each turn with <turn|>.
llama-server stops only on end tokens, so a fine-tune converted that way
keeps generating to the output cap. The base GGUF the probes run has it
right; the fine-tune shares its tokenizer, so its token ids carry over.
"""
import os
import subprocess
import sys
import tempfile
from pathlib import Path

SPECIAL = ["eos", "eot", "eom"]


def main():
    llama, base, src, out = sys.argv[1:5]
    gguf_py = Path(llama).resolve() / "gguf-py"
    sys.path.insert(0, str(gguf_py))
    import gguf

    r = gguf.GGUFReader(base)

    def field(key):
        f = r.fields.get(key)
        return f.parts[f.data[0]] if f else None

    args = []
    for name in SPECIAL:
        v = field(f"tokenizer.ggml.{name}_token_id")
        if v is not None:
            args += ["--special-token-by-id", name, str(int(v[0]))]
    template = field("tokenizer.chat_template")
    tmp = None
    if template is not None:
        fd, tmp = tempfile.mkstemp(suffix=".jinja")
        with os.fdopen(fd, "wb") as f:
            f.write(bytes(template))
        args += ["--chat-template-file", tmp]
    try:
        env = {**os.environ, "PYTHONPATH": str(gguf_py)}
        subprocess.run([sys.executable, str(gguf_py / "gguf" / "scripts" / "gguf_new_metadata.py"), src, out, "--force"] + args,
                       check=True, env=env)
    finally:
        if tmp:
            os.unlink(tmp)
    print("from " + os.path.basename(base) + ": " + " ".join(args[i + 1] + "=" + args[i + 2] for i in range(0, len(args), 3) if args[i] == "--special-token-by-id"))


if __name__ == "__main__":
    main()

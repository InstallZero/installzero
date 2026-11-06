"""llama.cpp's convert_hf_to_gguf.py with config fields transformers dropped put back.

    convert_gguf.py <llama.cpp dir> <convert_hf_to_gguf.py args...>

The converter reads hparams through transformers' AutoConfig. When the venv's
transformers is newer than the llama.cpp tag, AutoConfig can silently drop
fields the converter still reads (transformers 5.22 drops Gemma 4's
text_config.global_head_dim), so missing keys are filled from config.json.
"""
import json
import runpy
import sys
from pathlib import Path


def fill(cfg, raw):
    for k, v in raw.items():
        if k not in cfg:
            cfg[k] = v
        elif isinstance(v, dict) and isinstance(cfg[k], dict):
            fill(cfg[k], v)
    return cfg


def main():
    llama = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(llama))
    from conversion.base import ModelBase

    load = ModelBase.load_hparams

    def load_hparams(dir_model, is_mistral_format):
        cfg = load(dir_model, is_mistral_format)
        raw = Path(dir_model) / "config.json"
        if not is_mistral_format and raw.exists():
            fill(cfg, json.loads(raw.read_text(encoding="utf-8")))
        return cfg

    ModelBase.load_hparams = staticmethod(load_hparams)
    script = llama / "convert_hf_to_gguf.py"
    sys.argv = [str(script)] + sys.argv[2:]
    runpy.run_path(str(script), run_name="__main__")


if __name__ == "__main__":
    main()

"""LoRA fine-tune of a chat model on an SFT set from Export-SftDataset.ps1.

Each example is a probe conversation up to one reply; the loss covers only
that reply (the last message), so the model learns what to do next from the
context, not to reproduce tool output. Conversations are rendered with the
base model's own chat template, tools included, so the result speaks the
format llama-server will parse.

Prints lines the eval worker relays to the console:
  PHASE: <phase> | <message>
  STEP: <step>/<total> loss <x>
  METRIC: {json}
Writes <out>/adapter, <out>/merged (unless --no-merge) and <out>/metrics.json.

On Hugging Face Jobs (hf_job.py) --data is a dataset repo id and --push-to a
model repo that receives adapter/, merged/ and metrics.json, since the job's
disk goes away with it.
"""
import argparse
import json
import math
import os
import random
import sys
import time

ARGS = None


def phase(name, msg=""):
    print(f"PHASE: {name}" + (f" | {msg}" if msg else ""), flush=True)


def metric(**kv):
    print("METRIC: " + json.dumps(kv), flush=True)


def load_jsonl(path, limit=0):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            if line.strip():
                rows.append(json.loads(line))
                if limit and len(rows) >= limit:
                    break
    return rows


def normalize(messages):
    """OpenAI-style messages -> what HF chat templates expect (arguments as dicts)."""
    out = []
    for m in messages:
        m = dict(m)
        if m.get("content") is None:
            m["content"] = ""
        if m.get("tool_calls"):
            calls = []
            for c in m["tool_calls"]:
                fn = dict(c["function"])
                a = fn.get("arguments")
                if isinstance(a, str):
                    try:
                        fn["arguments"] = json.loads(a) if a.strip() else {}
                    except json.JSONDecodeError:
                        fn["arguments"] = {"raw": a}
                calls.append({"id": c.get("id"), "type": "function", "function": fn})
            m["tool_calls"] = calls
        if not ARGS.reasoning:
            m.pop("reasoning_content", None)
        out.append(m)
    return out


def encode(tok, ex, tools, max_len):
    msgs = normalize(ex["messages"])
    prompt = tok.apply_chat_template(msgs[:-1], tools=tools, add_generation_prompt=True, tokenize=False)
    full = tok.apply_chat_template(msgs, tools=tools, tokenize=False)
    if not full.startswith(prompt):
        n = 0
        while n < min(len(prompt), len(full)) and prompt[n] == full[n]:
            n += 1
        prompt = full[:n]
    p_ids = tok(prompt, add_special_tokens=False)["input_ids"]
    f_ids = tok(full, add_special_tokens=False)["input_ids"]
    if len(f_ids) > max_len:
        return None, len(f_ids)
    labels = [-100] * len(p_ids) + f_ids[len(p_ids):]
    if all(l == -100 for l in labels):
        return None, len(f_ids)
    return {"input_ids": f_ids, "attention_mask": [1] * len(f_ids), "labels": labels[: len(f_ids)]}, len(f_ids)


LORA_PROJ = r"(q_proj|k_proj|v_proj|o_proj|gate_proj|up_proj|down_proj)"


def lora_targets(model):
    """Only the language model's projections: in multimodal checkpoints the
    audio and vision towers are not used by the probe and would bloat the
    adapter (and the merge) for nothing."""
    names = [n for n, _ in model.named_modules()]
    if any(".language_model." in n or n.startswith("language_model.") for n in names):
        return rf".*language_model.*\.{LORA_PROJ}"
    return "all-linear"


def park_on_cpu(model):
    """Gemma 3n/4 keep a per-layer embedding table (2.8B parameters, 5.6 GB in
    bf16; 4-bit loading never quantizes embeddings) that is only read row by
    row: it stays in CPU RAM and just the looked-up rows go to the GPU. The
    vision and audio towers are never called on text. Returns parameters moved."""
    moved = 0
    for name, mod in model.named_modules():
        leaf = name.rsplit(".", 1)[-1]
        if leaf == "embed_tokens_per_layer":
            dev = next(mod.parameters()).device
            mod.to("cpu")
            fwd = mod.forward
            mod.forward = lambda ids, _f=fwd, _d=dev: _f(ids.to("cpu")).to(_d, non_blocking=True)
        elif leaf in ("vision_tower", "audio_tower", "embed_vision", "embed_audio"):
            mod.to("cpu")
        else:
            continue
        moved += sum(p.numel() for p in mod.parameters())
    return moved


def pct(v, p):
    if not v:
        return 0
    v = sorted(v)
    return v[min(len(v) - 1, int(p * len(v)))]


def main():
    global ARGS
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True, help="SFT set folder (train.jsonl, val.jsonl, tools.json) or a Hugging Face dataset repo with them")
    ap.add_argument("--push-to", default="", help="Hugging Face model repo for adapter/, merged/ and metrics.json")
    ap.add_argument("--base", required=True, help="Hugging Face model id or local folder")
    ap.add_argument("--out", required=True)
    ap.add_argument("--epochs", type=float, default=2)
    ap.add_argument("--lr", type=float, default=1e-4)
    ap.add_argument("--lora-r", type=int, default=16)
    ap.add_argument("--lora-alpha", type=int, default=32)
    ap.add_argument("--lora-dropout", type=float, default=0.05)
    ap.add_argument("--max-len", type=int, default=16384)
    ap.add_argument("--batch", type=int, default=1)
    ap.add_argument("--grad-accum", type=int, default=16)
    ap.add_argument("--qlora", action="store_true", help="load the base in 4-bit (bitsandbytes)")
    ap.add_argument("--max-examples", type=int, default=0, help="cap train examples (smoke tests)")
    ap.add_argument("--val-examples", type=int, default=200)
    ap.add_argument("--reasoning", action="store_true", help="keep reasoning_content in targets")
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--no-merge", action="store_true")
    ap.add_argument("--save-every", type=float, default=0.25,
                    help="with --push-to: epochs between checkpoints pushed to checkpoint/, which a rerun with the same settings resumes from (0: off)")
    ARGS = ap.parse_args()
    a = ARGS
    run_args = {k: v for k, v in vars(a).items() if k not in ("data", "out", "save_every", "no_merge")}

    import torch
    from transformers import (AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig, DataCollatorForSeq2Seq,
                              Trainer, TrainerCallback, TrainingArguments)
    from peft import LoraConfig, PeftModel, get_peft_model

    random.seed(a.seed)
    torch.manual_seed(a.seed)
    os.makedirs(a.out, exist_ok=True)
    token = os.environ.get("HF_TOKEN") or None
    if not torch.cuda.is_available():
        sys.exit("CUDA is not available to torch; training needs the GPU")
    metric(gpu=torch.cuda.get_device_name(0), vram_gb=round(torch.cuda.get_device_properties(0).total_memory / 2**30, 1))

    data_name = os.path.basename(os.path.normpath(a.data))
    if not os.path.isdir(a.data):
        from huggingface_hub import snapshot_download
        phase("setup", f"downloading dataset {a.data}")
        a.data = snapshot_download(a.data, repo_type="dataset", token=token, allow_patterns=["*.jsonl", "*.json"])

    phase("tokenizing", a.base)
    tok = AutoTokenizer.from_pretrained(a.base, token=token)
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    tools = json.load(open(os.path.join(a.data, "tools.json"), encoding="utf-8"))
    sets = {}
    val_limit = min(a.val_examples, max(20, a.max_examples // 4)) if a.max_examples else a.val_examples
    for split, limit in (("train", a.max_examples), ("val", val_limit)):
        rows = load_jsonl(os.path.join(a.data, f"{split}.jsonl"))
        if split == "val" and limit and len(rows) > limit:
            rows = random.sample(rows, limit)
        elif limit:
            rows = rows[:limit]
        enc, lens, too_long = [], [], 0
        for i, ex in enumerate(rows):
            e, n = encode(tok, ex, tools, a.max_len)
            lens.append(n)
            if e is None:
                too_long += 1
            else:
                enc.append(e)
            if i and i % 500 == 0:
                phase("tokenizing", f"{split} {i}/{len(rows)}")
        sets[split] = enc
        metric(split=split, examples=len(rows), used=len(enc), over_max_len=too_long,
               tokens_p50=pct(lens, 0.5), tokens_p90=pct(lens, 0.9), tokens_max=max(lens) if lens else 0,
               target_tokens=sum(sum(1 for l in e["labels"] if l != -100) for e in enc))
    if not sets["train"]:
        sys.exit("no training examples fit --max-len")

    phase("loading", a.base + (" (4-bit)" if a.qlora else " (bf16)"))
    # No attn_implementation: multimodal checkpoints (Gemma 4) carry audio and
    # vision towers that do not all support SDPA; transformers picks per tower.
    kw = {"dtype": torch.bfloat16, "device_map": {"": 0}, "token": token}
    if a.qlora:
        kw["quantization_config"] = BitsAndBytesConfig(load_in_4bit=True, bnb_4bit_quant_type="nf4",
                                                       bnb_4bit_compute_dtype=torch.bfloat16, bnb_4bit_use_double_quant=True)
    model = AutoModelForCausalLM.from_pretrained(a.base, **kw)
    model.config.use_cache = False
    if torch.cuda.get_device_properties(0).total_memory < 40 * 2**30:
        moved = park_on_cpu(model)
        if moved:
            torch.cuda.empty_cache()
            metric(params_on_cpu=moved)
    # Not peft's prepare_model_for_kbit_training: it upcasts every
    # non-quantized parameter to fp32, embeddings included (16 GB for Gemma 4 E4B).
    for prm in model.parameters():
        prm.requires_grad = False
    model.gradient_checkpointing_enable(gradient_checkpointing_kwargs={"use_reentrant": False})
    model.enable_input_require_grads()
    metric(vram_after_load_gb=round(torch.cuda.memory_allocated() / 2**30, 1))
    model = get_peft_model(model, LoraConfig(r=a.lora_r, lora_alpha=a.lora_alpha, lora_dropout=a.lora_dropout,
                                             target_modules=lora_targets(model), task_type="CAUSAL_LM"))
    trainable = sum(p.numel() for p in model.parameters() if p.requires_grad)
    metric(trainable_params=trainable)

    steps_per_epoch = math.ceil(len(sets["train"]) / (a.batch * a.grad_accum))
    total = max(1, math.ceil(steps_per_epoch * a.epochs))
    t0 = time.time()

    # Logits only from the first target token on: with a 262K vocabulary
    # (Gemma), logits over a 15K-token context alone exceed 16 GB, and only the
    # reply at the end carries loss.
    class TailTrainer(Trainer):
        def compute_loss(self, model, inputs, return_outputs=False, num_items_in_batch=None):
            labels = inputs.pop("labels")
            first = int((labels != -100).float().argmax(dim=1).min())
            keep = labels.shape[1] - first + 1
            out = model(**inputs, logits_to_keep=keep, use_cache=False)
            logits = out.logits[:, :-1, :].float()
            tgt = labels[:, labels.shape[1] - keep + 1:].to(logits.device)
            loss = torch.nn.functional.cross_entropy(logits.reshape(-1, logits.size(-1)), tgt.reshape(-1), ignore_index=-100)
            return (loss, out) if return_outputs else loss

    class Relay(TrainerCallback):
        def on_log(self, args, state, control, logs=None, **kw):
            logs = logs or {}
            if "loss" in logs:
                print(f"STEP: {state.global_step}/{state.max_steps or total} loss {logs['loss']:.4f} peak {torch.cuda.max_memory_allocated() / 2**30:.1f} GB", flush=True)
            if "eval_loss" in logs:
                metric(step=state.global_step, eval_loss=round(logs["eval_loss"], 4))

    # A remote job dies with its container: push each checkpoint to the model
    # repo so a rerun with the same settings resumes instead of starting over.
    save_steps = max(1, round(steps_per_epoch * a.save_every)) if a.push_to and a.save_every > 0 else 0
    resume = None
    if save_steps:
        from huggingface_hub import HfApi, snapshot_download
        hub = HfApi(token=token)
        try:
            files = hub.list_repo_files(a.push_to)
        except Exception:
            files = []
        if "checkpoint/run_args.json" in files:
            ck = snapshot_download(a.push_to, allow_patterns=["checkpoint/*"], local_dir=os.path.join(a.out, "resume"), token=token)
            ck = os.path.join(ck, "checkpoint")
            if json.load(open(os.path.join(ck, "run_args.json"), encoding="utf-8")) == json.loads(json.dumps(run_args)):
                resume = ck
                state = json.load(open(os.path.join(ck, "trainer_state.json"), encoding="utf-8"))
                phase("training", f"resuming from the checkpoint at step {state['global_step']}/{total}")
            else:
                print(f"{a.push_to} has a checkpoint from other settings; starting over", flush=True)

    class PushCheckpoint(TrainerCallback):
        def on_save(self, args, state, control, **kw):
            d = os.path.join(args.output_dir, f"checkpoint-{state.global_step}")
            try:
                json.dump(run_args, open(os.path.join(d, "run_args.json"), "w", encoding="utf-8"))
                hub.create_repo(a.push_to, private=True, exist_ok=True)
                hub.upload_folder(repo_id=a.push_to, folder_path=d, path_in_repo="checkpoint", delete_patterns=["*"],
                                  commit_message=f"checkpoint at step {state.global_step}/{total}")
                print(f"checkpoint at step {state.global_step}/{total} pushed to {a.push_to}", flush=True)
            except Exception as e:
                print(f"(could not push the checkpoint at step {state.global_step}: {e}; training continues)", flush=True)

    targs = TrainingArguments(
        output_dir=os.path.join(a.out, "checkpoints"), num_train_epochs=a.epochs, learning_rate=a.lr,
        per_device_train_batch_size=a.batch, per_device_eval_batch_size=1, gradient_accumulation_steps=a.grad_accum,
        lr_scheduler_type="cosine", warmup_steps=max(1, round(0.03 * total)), weight_decay=0.0, bf16=True, logging_steps=1,
        eval_strategy="steps" if sets["val"] else "no", eval_steps=max(10, total // 6),
        save_strategy="steps" if save_steps else "no", save_steps=save_steps or None, save_total_limit=1,
        report_to=[], seed=a.seed, optim="paged_adamw_8bit" if a.qlora else "adamw_torch_fused",
        gradient_checkpointing=True, remove_unused_columns=False, disable_tqdm=True)
    # Optimizer steps are 16+ examples apart, minutes each on a laptop GPU:
    # report every example (at most every 20 s) with a time-left estimate.
    total_micro = max(1, math.ceil(len(sets["train"]) / a.batch * a.epochs))

    class Progress(TrainerCallback):
        def __init__(self):
            self.n, self.n0, self.last, self.t0 = 0, 0, 0.0, time.time()

        def emit(self, force=False):
            now = time.time()
            if not force and now - self.last < 20:
                return
            done = self.n - self.n0
            eta = int((now - self.t0) / done * (total_micro - self.n)) if done > 0 else -1
            print(f"PROGRESS: {min(self.n, total_micro)}/{total_micro} eta {eta}", flush=True)
            self.last = now

        def on_train_begin(self, args, state, control, **kw):
            self.t0 = time.time()
            self.n = self.n0 = state.global_step * a.grad_accum
            self.emit(True)

        def on_substep_end(self, args, state, control, **kw):
            self.n += 1
            self.emit()

        def on_step_end(self, args, state, control, **kw):
            self.n += 1
            self.emit(True)

    trainer = TailTrainer(model=model, args=targs, train_dataset=sets["train"], eval_dataset=sets["val"] or None,
                      data_collator=DataCollatorForSeq2Seq(tok, label_pad_token_id=-100, padding=True),
                      callbacks=[Relay(), Progress()] + ([PushCheckpoint()] if save_steps else []))
    trainer.model_accepts_loss_kwargs = False
    phase("training", f"{len(sets['train'])} examples, {total} steps" + (f", checkpoint every {save_steps}" if save_steps else ""))
    out = trainer.train(resume_from_checkpoint=resume)
    metric(peak_vram_gb=round(torch.cuda.max_memory_allocated() / 2**30, 1))
    final = {"train_loss": round(out.training_loss, 4), "steps": out.global_step, "seconds": round(time.time() - t0)}
    if sets["val"]:
        final["eval_loss"] = round(trainer.evaluate()["eval_loss"], 4)
    metric(**final)

    adapter = os.path.join(a.out, "adapter")
    model.save_pretrained(adapter)
    tok.save_pretrained(adapter)
    json.dump({**final, "args": vars(a)}, open(os.path.join(a.out, "metrics.json"), "w"), indent=2)
    if not a.no_merge:
        phase("merging", "LoRA into the bf16 base (CPU)")
        del trainer, model
        torch.cuda.empty_cache()
        base = AutoModelForCausalLM.from_pretrained(a.base, dtype=torch.bfloat16, device_map={"": "cpu"}, token=token)
        merged = PeftModel.from_pretrained(base, adapter).merge_and_unload()
        merged.save_pretrained(os.path.join(a.out, "merged"), safe_serialization=True)
        tok.save_pretrained(os.path.join(a.out, "merged"))

    if a.push_to:
        from huggingface_hub import HfApi
        phase("uploading", a.push_to)
        api = HfApi(token=token)
        api.create_repo(a.push_to, private=True, exist_ok=True)
        api.upload_folder(repo_id=a.push_to, folder_path=a.out, allow_patterns=["adapter/*", "merged/*", "metrics.json"],
                          delete_patterns=["checkpoint/*"], commit_message=f"{a.base} on {data_name}")
        phase("uploaded", a.push_to)


if __name__ == "__main__":
    main()

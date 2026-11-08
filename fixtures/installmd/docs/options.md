---
name: options
---

# Block options

timeout= and phase= are options of a block, not conditions: they do not make
the first install block more specific than the gpu=nvidia one.

## Step 1: images - Images

```install bash timeout=90m phase=build
docker compose build
```

```install bash gpu=nvidia
docker compose -f compose.gpu.yml build
```

```verify bash phase=verify
docker image inspect demo
```

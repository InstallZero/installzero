---
name: ph
---
## Step 1: clone - Clone: into place
Why: first
Why: second
```install bash
git clone x {{REPO_DIR}}
cd {{INSTALL_DIR}}/{{REPO_DIR}}
```
```verify bash
test -d {{REPO_DIR}} && test -d {{OTHER}}
```

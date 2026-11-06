---
name: variants
---
## Step 1: gpu - GPU stack
```requires
os: linux, windows
gpu: nvidia  # only NVIDIA
min_ram_gb: 16
min_driver: 550.1
```
```detect bash
nvidia-smi
```
```install bash gpu=nvidia
apt install cuda
```
```install bash os=linux arch=arm64
apt install cuda-arm
```
```install powershell os=windows
winget install cuda
```
```install
echo default
```
```verify bash
nvcc --version
```
```verify powershell os=windows
nvcc --version
```
## Step 2: disk - Disk
```requires
min_disk_gb: 100
```
```detect bash
true
```
```verify
true
```
## Step 3: mac - Mac only
```requires
os: macos
```
```install bash
brew install x
```
```verify bash
x
```

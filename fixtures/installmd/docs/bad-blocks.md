---
name: bad
---
## Step 1: a - A
```run bash
x
```
```install fish
x
```
```verify bash

```
## Step 2: b - B
```detect bash
test -f /x
exit 0
```
```verify bash
if [ -f /x ]; then exit 0; fi
exit 0
```

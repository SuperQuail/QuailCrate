---
type: ci
release: false
packages:
  go@quailcrate: patch
---

Populate the Windows Go module cache before a verification-only run can save it under the shared build key.

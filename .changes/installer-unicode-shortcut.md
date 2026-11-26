---
type: fix
scope: installer
packages:
  go@quailcrate: patch
---

Use Unicode IShellLinkW COM APIs for Windows Start Menu shortcuts so installs to paths outside the system ANSI code page succeed, and surface silent setup errors in installer smoke tests.

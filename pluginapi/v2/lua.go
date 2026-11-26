package v2

import _ "embed"

// LuaUI is the versioned construction library exposed as require("quailcrate.ui.v2").
//
//go:embed ui.lua
var LuaUI string

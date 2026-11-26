package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	lua "github.com/yuin/gopher-lua"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type luaRuntime struct {
	state      *lua.LState
	entry      *lua.LFunction
	array      *lua.LTable
	null       *lua.LUserData
	packageDir string
	modules    map[string]lua.LValue
}

func openLua(ctx context.Context, pkg Package) (*luaRuntime, error) {
	state := lua.NewState(lua.Options{
		SkipOpenLibs:     true,
		CallStackSize:    256,
		RegistrySize:     8192,
		RegistryMaxSize:  32768,
		RegistryGrowStep: 1024,
	})
	worker := &luaRuntime{state: state, array: state.NewTable(), null: state.NewUserData(), packageDir: pkg.Dir, modules: make(map[string]lua.LValue)}
	state.SetContext(ctx)
	defer state.RemoveContext()
	for _, library := range []struct {
		name string
		open lua.LGFunction
	}{{"", lua.OpenBase}, {"table", lua.OpenTable}, {"string", lua.OpenString}, {"math", lua.OpenMath}} {
		if err := state.CallByParam(lua.P{Fn: state.NewFunction(library.open), NRet: 0, Protect: true}, lua.LString(library.name)); err != nil {
			state.Close()
			return nil, err
		}
	}
	for _, name := range []string{"collectgarbage", "dofile", "load", "loadfile", "loadstring", "getfenv", "setfenv", "module", "print", "newproxy"} {
		state.SetGlobal(name, lua.LNil)
	}
	state.SetGlobal("require", state.NewFunction(worker.require))
	worker.configureJSON()
	worker.limitStringRepeat()
	value, err := worker.load(pkg.Entry)
	if err != nil {
		state.Close()
		return nil, err
	}
	entry, ok := value.(*lua.LFunction)
	if !ok {
		state.Close()
		return nil, errors.New("Lua entry must return a request function")
	}
	worker.entry = entry
	return worker, nil
}

func (worker *luaRuntime) load(path string) (lua.LValue, error) {
	data, err := readBounded(path, 512<<10)
	if err != nil {
		return nil, err
	}
	function, err := worker.state.Load(bytes.NewReader(data), "@"+path)
	if err != nil {
		return nil, err
	}
	return worker.invoke(function)
}

func (worker *luaRuntime) require(state *lua.LState) int {
	name := state.CheckString(1)
	if !pluginIDPattern.MatchString(name) || len(name) > 128 {
		state.RaiseError("invalid package module name %q", name)
		return 0
	}
	if module, exists := worker.modules[name]; exists {
		if module == nil {
			state.RaiseError("circular package module %q", name)
			return 0
		}
		state.Push(module)
		return 1
	}
	worker.modules[name] = nil
	var module lua.LValue
	var err error
	if name == "quailcrate.ui.v2" {
		var fn *lua.LFunction
		fn, err = state.Load(strings.NewReader(v2.LuaUI), "@quailcrate.ui.v2")
		if err == nil {
			module, err = worker.invoke(fn)
		}
	} else {
		var path string
		path, err = resolveFile(worker.packageDir, strings.ReplaceAll(name, ".", "/")+".lua")
		if err == nil {
			module, err = worker.load(path)
		}
	}
	if err != nil {
		delete(worker.modules, name)
		state.RaiseError("package module %q: %v", name, err)
		return 0
	}
	if module == lua.LNil {
		module = lua.LTrue
	}
	worker.modules[name] = module
	state.Push(module)
	return 1
}

func (worker *luaRuntime) limitStringRepeat() {
	library := worker.state.GetGlobal("string").(*lua.LTable)
	worker.state.SetField(library, "rep", worker.state.NewFunction(func(state *lua.LState) int {
		value := state.CheckString(1)
		count := state.CheckInt(2)
		if count <= 0 || value == "" {
			state.Push(lua.LString(""))
			return 1
		}
		if count > v1.MaxMessageSize/len(value) {
			state.RaiseError("string.rep output exceeds size limit")
			return 0
		}
		state.Push(lua.LString(strings.Repeat(value, count)))
		return 1
	}))
	worker.state.SetField(library, "dump", lua.LNil)
}

func (worker *luaRuntime) configureJSON() {
	state := worker.state
	table := state.NewTable()
	state.SetField(table, "null", worker.null)
	state.SetField(table, "array", state.NewFunction(func(state *lua.LState) int {
		array := state.NewTable()
		if state.GetTop() > 0 {
			array = state.CheckTable(1)
		}
		state.SetMetatable(array, worker.array)
		state.Push(array)
		return 1
	}))
	state.SetGlobal("json", table)
}

func (worker *luaRuntime) Call(ctx context.Context, input []byte) ([]byte, error) {
	worker.state.SetContext(ctx)
	defer worker.state.RemoveContext()
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	request, err := worker.fromJSON(parsed, 0)
	if err != nil {
		return nil, err
	}
	response, err := worker.invoke(worker.entry, request)
	if err != nil {
		return nil, err
	}
	budget := v1.MaxMessageSize
	value, err := worker.toJSON(ctx, response, 0, &budget, make(map[*lua.LTable]bool))
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (worker *luaRuntime) Close() error {
	worker.state.Close()
	return nil
}

func (worker *luaRuntime) invoke(function *lua.LFunction, args ...lua.LValue) (lua.LValue, error) {
	if err := worker.state.CallByParam(lua.P{Fn: function, NRet: 1, Protect: true}, args...); err != nil {
		return nil, err
	}
	value := worker.state.Get(-1)
	worker.state.Pop(1)
	return value, nil
}

func (worker *luaRuntime) fromJSON(value any, depth int) (lua.LValue, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting exceeds 64 levels")
	}
	switch value := value.(type) {
	case nil:
		return worker.null, nil
	case string:
		return lua.LString(value), nil
	case bool:
		return lua.LBool(value), nil
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		return lua.LNumber(number), err
	case []any:
		table := worker.state.NewTable()
		worker.state.SetMetatable(table, worker.array)
		for i, field := range value {
			item, err := worker.fromJSON(field, depth+1)
			if err != nil {
				return nil, err
			}
			table.RawSetInt(i+1, item)
		}
		return table, nil
	case map[string]any:
		table := worker.state.NewTable()
		for key, field := range value {
			item, err := worker.fromJSON(field, depth+1)
			if err != nil {
				return nil, err
			}
			table.RawSetString(key, item)
		}
		return table, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

func (worker *luaRuntime) toJSON(ctx context.Context, value lua.LValue, depth int, budget *int, ancestors map[*lua.LTable]bool) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*budget -= 2
	if depth > 64 || *budget < 0 {
		return nil, errors.New("Lua response exceeds size or nesting limits")
	}
	if value == lua.LNil || value == worker.null {
		return nil, nil
	}
	switch value := value.(type) {
	case lua.LBool:
		return bool(value), nil
	case lua.LNumber:
		return float64(value), nil
	case lua.LString:
		*budget -= len(value)
		if *budget < 0 || !utf8.ValidString(string(value)) {
			return nil, errors.New("Lua response exceeds size limit or has invalid UTF-8")
		}
		return string(value), nil
	case *lua.LTable:
		if ancestors[value] {
			return nil, errors.New("Lua response contains a cycle")
		}
		ancestors[value] = true
		defer delete(ancestors, value)
		return worker.tableJSON(ctx, value, depth, budget, ancestors)
	default:
		return nil, fmt.Errorf("Lua %s cannot represent a JSON value", value.Type())
	}
}

func (worker *luaRuntime) tableJSON(ctx context.Context, table *lua.LTable, depth int, budget *int, ancestors map[*lua.LTable]bool) (any, error) {
	object := make(map[string]any)
	array := make(map[int]any)
	var conversionError error
	table.ForEach(func(key, value lua.LValue) {
		if conversionError != nil {
			return
		}
		var item any
		item, conversionError = worker.toJSON(ctx, value, depth+1, budget, ancestors)
		if conversionError != nil {
			return
		}
		switch key := key.(type) {
		case lua.LString:
			*budget -= len(key)
			if *budget < 0 || !utf8.ValidString(string(key)) {
				conversionError = errors.New("Lua response exceeds size limit or has an invalid object key")
				return
			}
			object[string(key)] = item
		case lua.LNumber:
			if key < 1 || key > v1.MaxMessageSize || lua.LNumber(int(key)) != key {
				conversionError = errors.New("Lua array index is not a valid positive integer")
				return
			}
			array[int(key)] = item
		default:
			conversionError = errors.New("Lua response table has an invalid key type")
		}
	})
	if conversionError != nil {
		return nil, conversionError
	}
	if len(array) == 0 && worker.state.GetMetatable(table) != worker.array {
		return object, nil
	}
	if len(object) > 0 {
		return nil, errors.New("Lua table mixes object keys and array indices")
	}
	items := make([]any, len(array))
	for i := range items {
		item, present := array[i+1]
		if !present {
			return nil, errors.New("Lua array contains a missing index")
		}
		items[i] = item
	}
	return items, nil
}

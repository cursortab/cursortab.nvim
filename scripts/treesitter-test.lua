-- Run from the repository root: nvim --headless -u NONE -l scripts/treesitter-test.lua
vim.opt.rtp:prepend(vim.fn.getcwd())

local treesitter = require("cursortab.treesitter")

local bufnr = vim.api.nvim_create_buf(true, false)
vim.bo[bufnr].filetype = "lua"

-- An empty buffer has no node under the cursor. The Go client decodes the
-- returned Lua value into map[string]any; an empty table arrives as a msgpack
-- array and fails to decode, while nil arrives as msgpack nil and is handled.
local context = treesitter.get_context(bufnr, 1, 0, 3)
assert(context == nil, "expected nil, got " .. vim.inspect(context))

local encoded = vim.mpack.encode({})
assert(encoded:byte(1) == 0x90, "empty Lua table must encode as a msgpack array")

print("treesitter tests passed")

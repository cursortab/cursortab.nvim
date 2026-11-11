-- Run from the repository root: nvim --headless -u NONE -l scripts/config-test.lua
vim.opt.rtp:prepend(vim.fn.getcwd())

local config = require("cursortab.config")

assert(config.setup({ provider = { type = "fim" } }).provider.fim_tokens == nil)

local tokens = {
	prefix = "<|fim_prefix|>",
	suffix = "<|fim_suffix|>",
	middle = "<|fim_middle|>",
}
local cfg = config.setup({ provider = { type = "fim", fim_tokens = tokens } })
assert(vim.deep_equal(cfg.provider.fim_tokens, tokens))

cfg = config.setup({ provider = { type = "fim", model = "Qwen2.5-Coder", fim_tokens = tokens } })
assert(cfg.provider.fim_tokens.repo_name == "<|repo_name|>")
assert(cfg.provider.fim_tokens.file_sep == "<|file_sep|>")
assert(tokens.repo_name == nil)

local explicit = vim.tbl_extend("force", tokens, { repo_name = "<repo>", file_sep = "<file>" })
cfg = config.setup({ provider = { type = "fim", fim_tokens = explicit } })
assert(vim.deep_equal(cfg.provider.fim_tokens, explicit))

local function expect_error(provider, message)
	local ok, err = pcall(config.setup, { provider = provider })
	assert(not ok and tostring(err):find(message, 1, true), tostring(err))
end

expect_error({ type = "fim", fim_tokens = { prefix = "p", suffix = "s" } }, "provider.fim_tokens.middle is required")
expect_error({ type = "fim", fim_tokens = vim.tbl_extend("force", tokens, { typo = "x" }) }, "Unknown config option: provider.fim_tokens.typo")
expect_error({ type = "fim", fim_tokens = vim.tbl_extend("force", tokens, { repo_name = false }) }, "provider.fim_tokens.repo_name must be a string")
expect_error({ type = "fim", typo = true }, "Unknown config option: provider.typo")
assert(config.setup({ provider = { type = "fim" } }).provider.fim_tokens == nil)

-- top_p is passed to providers that accept it; 0 leaves the provider default.
assert(config.setup({ provider = { type = "fim", top_p = 1 } }).provider.top_p == 1)
expect_error({ type = "fim", top_p = 1.5 }, "provider.top_p must be between 0 and 1")
expect_error({ type = "fim", top_p = -0.1 }, "provider.top_p must be between 0 and 1")
print("config tests passed")

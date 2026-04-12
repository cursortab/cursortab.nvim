local M = {}

-- Get LSP client by name
---@param client_names string[] List of client names (if more than one plugin registers the same name, but same underlying LSP client)
---@return vim.lsp.Client|nil
function M.find_lsp_client(client_names)
	for _, client_name in ipairs(client_names) do
		local clients = vim.lsp.get_clients({ name = client_name })
		if #clients > 0 then
			return clients[1]
		end
	end

	return nil
end

-- Get LSP client for given LSP client names
---@param client_names string[] List of client names (if more than one plugin registers the same name, but same underlying LSP client)
---@return vim.lsp.Client|nil
function M.get_lsp_client(client_names)
	local client = M.find_lsp_client(client_names)

	if not client then
		return {}
	end
	local bufnr = vim.api.nvim_get_current_buf()
	if not vim.lsp.buf_is_attached(bufnr, client.id) then
		vim.lsp.buf_attach_client(bufnr, client.id)
	end
	return { { id = client.id } }
end

function M.send_lsp_event(client_names, event, params)
	local client = M.find_lsp_client(client_names)

	if not client then
		return
	end

	client:notify(event, params)
end

-- Send request to LSP client
---@param client_names string[] List of client names (if more than one plugin registers the same name, but same underlying LSP client)
---@param params { chanID: number, reqID: number, uri: string }
function M.send_nes_request(client_names, params)
	local chanID = params.chanID
	local reqID = params.reqID
	local uri = params.uri

	local client = M.find_lsp_client(client_names)
	if not client then
		vim.fn.rpcnotify(chanID, "cursortab_copilot_response", reqID, "[]", "no copilot client")
		return
	end

	local bufnr = vim.api.nvim_get_current_buf()

	-- Use the LSP protocol version (what Neovim sends in didChange), not b:changedtick.
	-- These can diverge; using the wrong one causes Copilot to compute edits against
	-- a mismatched document, producing wrong line numbers.
	local version = vim.lsp.util.buf_versions[bufnr] or vim.b[bufnr].changedtick

	-- Full document sync: correct any accumulated incremental change tracking drift.
	-- Only send when the version changed since our last sync (skip no-op resyncs).
	local last_sync = vim.b[bufnr]._cursortab_copilot_sync
	if last_sync ~= version then
		local all_lines = vim.api.nvim_buf_get_lines(bufnr, 0, -1, false)
		local full_text = table.concat(all_lines, "\n")
		-- Range end uses INT32_MAX to cover the entire document regardless of length
		client:notify("textDocument/didChange", {
			textDocument = { uri = uri, version = version },
			contentChanges = {
				{
					range = {
						start = { line = 0, character = 0 },
						["end"] = { line = 2147483647, character = 0 },
					},
					text = full_text,
				},
			},
		})
		vim.b[bufnr]._cursortab_copilot_sync = version
	end

	local pos_params = vim.lsp.util.make_position_params(0, "utf-16")
	pos_params.textDocument.version = version

	client:request("textDocument/copilotInlineEdit", pos_params, function(err, result)
		local edits_json = "[]"
		local err_msg = ""
		if err then
			err_msg = vim.json.encode(err)
		elseif result and result.edits then
			edits_json = vim.json.encode(result.edits)
		end
		vim.fn.rpcnotify(chanID, "cursortab_copilot_response", reqID, edits_json, err_msg)
	end)
end

function M.windsurf_get_info()
	local ok, codeium = pcall(require, 'codeium')
	if not ok or not codeium.s then
		return { healthy = false }
	end
	local server = codeium.s
	if not server.healthy or not server.port then
		return { healthy = false }
	end
	local api_key = ""
	local ok_cfg, cfg = pcall(require, 'codeium.config')
	if ok_cfg and cfg and cfg.options and cfg.options.config_path then
		local config_path = cfg.options.config_path
		if vim.fn.filereadable(config_path) == 1 then
			local ok_json, config_data = pcall(vim.json.decode, table.concat(vim.fn.readfile(config_path), "\n"))
			if ok_json and config_data then
				api_key = config_data.api_key or ""
			end
		end
	end
	return {
		healthy = true,
		port = server.port,
		api_key = api_key,
	}
end

return M

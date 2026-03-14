local M = {}

local scope_types = {
	function_declaration = true,
	function_definition = true,
	method_declaration = true,
	method_definition = true,
	class_declaration = true,
	class_definition = true,
	type_declaration = true,
	type_spec = true,
	impl_item = true,
	func_literal = true,
	arrow_function = true,
	function_item = true,
}

local import_types = {
	import_declaration = true,
	import_statement = true,
	use_declaration = true,
	preproc_include = true,
	import_spec = true,
	import_spec_list = true,
}

---Get treesitter scope context around the cursor position.
---@param bufnr integer Buffer number
---@param row integer 1-indexed cursor row
---@param col integer 0-indexed cursor column
---@param max_siblings integer Maximum sibling nodes to return
---@return table|nil
function M.get_context(bufnr, row, col, max_siblings)
	row = row - 1 -- convert to 0-indexed

	local ok, parser = pcall(vim.treesitter.get_parser, bufnr)
	if not ok or not parser then
		return nil
	end

	local cursor_node = vim.treesitter.get_node({ bufnr = bufnr, pos = { row, col } })
	if not cursor_node then
		return {}
	end

	-- Walk up to find enclosing scope
	local enclosing = nil
	local node = cursor_node ---@type TSNode?
	while node do
		if scope_types[node:type()] then
			enclosing = node
			break
		end
		node = node:parent()
	end

	local enclosing_sig = ""
	if enclosing then
		local start_row = enclosing:start()
		local line = vim.api.nvim_buf_get_lines(bufnr, start_row, start_row + 1, false)[1] or ""
		enclosing_sig = line
	end

	-- Get sibling scope nodes from the enclosing scope's parent
	local siblings = {}
	local parent = enclosing and enclosing:parent()
	if parent then
		for child in parent:iter_children() do
			if scope_types[child:type()] and child ~= enclosing then
				local s_row = child:start()
				local line = vim.api.nvim_buf_get_lines(bufnr, s_row, s_row + 1, false)[1] or ""
				local name = ""
				local name_node = child:field("name")[1]
				if name_node then
					name = vim.treesitter.get_node_text(name_node, bufnr)
				end
				table.insert(siblings, { name = name, signature = line, line = s_row + 1 })
			end
		end
		if #siblings > max_siblings then
			table.sort(siblings, function(a, b)
				return math.abs(a.line - 1 - row) < math.abs(b.line - 1 - row)
			end)
			local trimmed = {}
			for i = 1, max_siblings do
				trimmed[i] = siblings[i]
			end
			siblings = trimmed
		end
	end

	-- Collect imports from root
	local imports = {}
	local root = parser:trees()[1]:root()
	for child in root:iter_children() do
		if import_types[child:type()] then
			local s_row, _, e_row = child:start(), nil, child:end_()
			local lines = vim.api.nvim_buf_get_lines(bufnr, s_row, e_row + 1, false)
			table.insert(imports, table.concat(lines, "\n"))
		end
	end

	-- Collect ancestor node line ranges (innermost to outermost)
	-- Used by providers to snap editable/context regions to syntax boundaries
	local syntax_ranges = {}
	node = cursor_node
	while node do
		local s_row = node:start()
		local e_row = node:end_()
		-- Only include nodes that span multiple lines (meaningful boundaries)
		if e_row > s_row then
			table.insert(syntax_ranges, { start_line = s_row + 1, end_line = e_row + 1 })
		end
		node = node:parent()
	end

	return {
		enclosing_signature = enclosing_sig,
		siblings = siblings,
		imports = imports,
		syntax_ranges = syntax_ranges,
	}
end

return M

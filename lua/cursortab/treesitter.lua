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

-- Number of top-level nodes scanned for imports.
local max_import_scan = 500

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
		-- nil, not {}: an empty table encodes as a msgpack array, which the Go
		-- client cannot decode into a map.
		return nil
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

	-- Collect the max_siblings scope nodes nearest to the enclosing scope,
	-- walking outward from it so the cost does not grow with the file. They
	-- are returned in source order.
	local siblings = {}
	if enclosing then
		local function symbol(n)
			local s_row = n:start()
			local line = vim.api.nvim_buf_get_lines(bufnr, s_row, s_row + 1, false)[1] or ""
			local name_node = n:field("name")[1]
			local name = name_node and vim.treesitter.get_node_text(name_node, bufnr) or ""
			return { name = name, signature = line, line = s_row + 1 }
		end
		local function next_scope(n, forward)
			repeat
				if forward then
					n = n:next_sibling()
				else
					n = n:prev_sibling()
				end
			until not n or scope_types[n:type()]
			return n
		end
		local before = next_scope(enclosing, false)
		local after = next_scope(enclosing, true)
		while #siblings < max_siblings and (before or after) do
			if before and (not after or row - before:start() <= after:start() - row) then
				table.insert(siblings, symbol(before))
				before = next_scope(before, false)
			else
				table.insert(siblings, symbol(after))
				after = next_scope(after, true)
			end
		end
		table.sort(siblings, function(x, y)
			return x.line < y.line
		end)
	end

	-- Collect imports from the first max_import_scan nodes of root, so the
	-- cost stays bounded in huge files.
	local imports = {}
	local root = parser:trees()[1]:root()
	local scanned = 0
	for child in root:iter_children() do
		scanned = scanned + 1
		if scanned > max_import_scan then
			break
		end
		if import_types[child:type()] then
			local s_row, _, e_row, e_col = child:range()
			-- A node that ends at column 0 ends with the line break before it.
			if e_col == 0 and e_row > s_row then
				e_row = e_row - 1
			end
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

---Get treesitter node types from cursor to root.
---@param bufnr integer Buffer number
---@param row integer 1-indexed cursor row
---@param col integer 0-indexed cursor column
---@return string[]
function M.cursor_scopes(bufnr, row, col)
	row = row - 1 -- convert to 0-indexed
	-- Query the character just typed, not the position after it
	if col > 0 then
		col = col - 1
	end

	local node = vim.treesitter.get_node({ bufnr = bufnr, pos = { row, col } })
	if not node then
		return {}
	end

	local scopes = {}
	while node do
		scopes[#scopes + 1] = node:type()
		node = node:parent()
	end
	return scopes
end

return M

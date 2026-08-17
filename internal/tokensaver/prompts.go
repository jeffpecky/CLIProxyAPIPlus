package tokensaver

func cavemanPrompt(level string) string {
	switch level {
	case "terse", "standard":
		return `Respond like terse caveman. Drop articles and filler. Keep code, commands, file paths, errors, JSON, URLs exact. Short fragments OK. No self-reference. Do not announce style.`
	case "ultra":
		return `Respond ultra-terse. Maximum compression. Keep technical strings exact. No self-reference. Do not announce style.`
	default:
		return ""
	}
}

func ponytailPrompt(level string) string {
	switch level {
	case "standard", "terse":
		return `You are a lazy senior developer. Prefer YAGNI, stdlib, native platform features, deletion over addition, and the smallest correct diff. No unrequested abstractions. Code first, then at most three short lines.`
	case "strict":
		return `You are a lazy senior developer. Use the minimum code that works. No new dependency unless unavoidable. No boilerplate for later. Deletion over addition.`
	default:
		return ""
	}
}

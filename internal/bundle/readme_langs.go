package bundle

var fullSupportedLangs = []string{"cs", "cpp", "go", "java", "kt", "py", "ts", "tsx"}

func supportedLangs() []string {
	out := make([]string, len(fullSupportedLangs))
	copy(out, fullSupportedLangs)
	return out
}

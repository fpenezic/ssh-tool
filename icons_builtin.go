package main

// Built-in icon names and palette colours, mirrored from the frontend so the
// MCP tools can validate what a model asks for and list the choices in the
// tool schema.
//
// The source of truth is frontend/src/lib/builtinIcons.ts (names) and
// frontend/src/lib/palette.ts (colours). TestBuiltinIconsMatchFrontend parses
// both files and fails if this list drifts, so adding an icon there without
// adding it here is a test failure rather than a silent gap.
//
// Validation matters because nothing downstream rejects a bad name: the store
// writes whatever string it is given, and the tree falls back to a generic
// glyph for anything it cannot resolve. An invented name like "postgres"
// would therefore be accepted and then quietly render as nothing.

// builtinIconLabels maps icon name -> the label shown in the icon picker.
var builtinIconLabels = map[string]string{
	"server":              "Server",
	"database":            "Database",
	"container":           "Container / Docker",
	"boxes":               "Cluster / Kubernetes",
	"cloud":               "Cloud",
	"router":              "Router",
	"network":             "Network",
	"wifi":                "Wi-Fi",
	"globe":               "Globe / Public",
	"earth":               "Earth / Region",
	"hard-drive":          "Storage / Disk",
	"hard-drive-download": "NAS / Backup",
	"cpu":                 "CPU / Compute",
	"monitor":             "Workstation / Windows",
	"laptop":              "Laptop",
	"smartphone":          "Phone / Mobile",
	"apple":               "Apple / macOS",
	"printer":             "Printer",
	"cctv":                "Camera / NVR",
	"terminal":            "Shell / Linux",
	"shield-check":        "Security / Firewall",
	"lock":                "Lock / Vault",
	"key":                 "Key / Auth",
	"git-branch":          "Git / VCS",
	"box":                 "Box / App",
	"package":             "Package / Registry",
	"layers":              "Layers / Stack",
	"mail":                "Mail",
	"activity":            "Monitoring",
	"gauge":               "Metrics / Dashboard",
	"bug":                 "Debug / Staging",
	"flame":               "Hot / Production",
	"zap":                 "Fast / Edge",
	"rocket":              "Deploy / Launch",
	"cog":                 "Config / Service",
	"house":               "Home / Lab",
	"building":            "Office / Datacenter",
	"folder":              "Folder",
	"bot":                 "AI / Claude Code",
	"sparkles":            "AI / Assistant",
	"radio-tower":         "Console server / Serial-over-IP",
	"plug":                "Serial / Direct",
	"lightbulb":           "IoT / Smart home / Idea"}

// iconPaletteColors are the named colours an icon can carry. A hex value like
// "#ff0000" also works downstream, but the tools accept only these names: a
// model picking arbitrary hex produces a tree with no shared visual language.
var iconPaletteColors = []string{
	"red", "orange", "yellow", "green", "teal", "blue", "mauve", "pink",
}

// validIconName reports whether name is a known built-in icon.
func validIconName(name string) bool {
	_, ok := builtinIconLabels[name]
	return ok
}

// validIconColor reports whether color is one of the palette names. An empty
// string is valid and means "no explicit colour" (the tree's default).
func validIconColor(color string) bool {
	if color == "" {
		return true
	}
	for _, c := range iconPaletteColors {
		if c == color {
			return true
		}
	}
	return false
}

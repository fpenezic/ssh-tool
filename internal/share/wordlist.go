package share

// fingerprintWords is a 256-word list used to render a certificate
// fingerprint as four spoken words (8 bits each -> 32 bits from the first four
// bytes of the SHA-256). Humans compare "cobalt-otter-viola-medley" over the
// phone reliably; they compare hex prefixes carelessly (glancing at the first
// and last few chars). This is the same reason the PGP word list and BIP-39
// exist; the list is kept small and dependency-free on purpose.
//
// Words are short, phonetically distinct, and unambiguous when spelled out.
// The list MUST stay exactly 256 entries and MUST NOT be reordered - the index
// is derived from the fingerprint bytes, so any change silently alters every
// existing fingerprint's words (which is precisely the alarming event the
// words are meant to make visible). Treat it as append-only-forbidden.
var fingerprintWords = [256]string{
	"amber", "anchor", "apple", "arch", "arrow", "artist", "aspen", "atlas",
	"autumn", "bacon", "badge", "bamboo", "banjo", "basil", "beacon", "beagle",
	"bison", "blaze", "bloom", "bolt", "bonus", "boron", "boulder", "bramble",
	"brass", "bravo", "breeze", "bridge", "bronze", "brook", "buffalo", "bugle",
	"cabin", "cactus", "camel", "candle", "canyon", "carbon", "cargo", "carol",
	"cedar", "cello", "cinder", "citrus", "clay", "clever", "cliff", "cobalt",
	"cocoa", "comet", "compass", "coral", "cosmos", "cotton", "cougar", "crane",
	"crimson", "crystal", "cyan", "cypress", "daisy", "dawn", "delta", "denim",
	"diamond", "domino", "dragon", "dune", "eagle", "echo", "ember", "emerald",
	"engine", "ermine", "falcon", "fable", "fennel", "fern", "ferry", "fiber",
	"fjord", "flame", "flint", "flora", "forest", "fossil", "fox", "gadget",
	"galaxy", "garden", "garnet", "gecko", "ginger", "glacier", "glider", "gold",
	"granite", "grape", "gravel", "grotto", "harbor", "hazel", "helix", "heron",
	"hickory", "honey", "hornet", "husky", "indigo", "iris", "island", "ivory",
	"jade", "jasper", "jetty", "jigsaw", "jolt", "jungle", "juniper", "kayak",
	"kernel", "kestrel", "kettle", "kiwi", "koala", "krypton", "lagoon", "lantern",
	"lava", "ledger", "lemon", "lentil", "lilac", "lily", "linen", "lion",
	"llama", "lobby", "lotus", "lumber", "lunar", "lynx", "magnet", "mango",
	"maple", "marble", "marlin", "meadow", "medley", "melon", "meteor", "mimosa",
	"mint", "mirror", "mocha", "monsoon", "moss", "mosaic", "mustang", "nectar",
	"needle", "neon", "nickel", "nimbus", "nomad", "nutmeg", "oasis", "ocean",
	"olive", "onyx", "opal", "orbit", "orchid", "osprey", "otter", "oxide",
	"paddle", "palm", "panda", "papaya", "parcel", "pastel", "peach", "pearl",
	"pebble", "pelican", "pepper", "pewter", "phoenix", "pigment", "pilot", "pine",
	"pixel", "planet", "plaza", "plum", "pollen", "poppy", "prairie", "prism",
	"pumice", "quartz", "quiver", "radar", "raft", "raven", "ribbon", "ridge",
	"rimu", "river", "robin", "rocket", "rose", "ruby", "saffron", "sage",
	"salmon", "sandal", "sapphire", "satin", "scarab", "sequoia", "shadow", "shale",
	"sienna", "signal", "silver", "sitka", "slate", "sonar", "sorrel", "spark",
	"spruce", "squid", "stellar", "stork", "summit", "sunset", "syrup", "tango",
	"tapir", "teak", "temple", "thistle", "thorn", "thunder", "tiger", "timber",
	"topaz", "torch", "tundra", "turbine", "umber", "valley", "velvet", "viola",
}

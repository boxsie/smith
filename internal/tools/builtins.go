package tools

// BuiltinToolIDs is the authoritative list of runner-provided tool IDs.
// App and lib tools must not shadow these (shadowing is a validation error).
var BuiltinToolIDs = []string{
	"project.read",
	"project.list",
	"project.find",
	"proposal.write",
}

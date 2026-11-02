package agent

// StackKitRuntimeUpdate is a published StackKits runtime the node does not
// run yet. Techstack serves it only once the owner approved it and the
// governed `stackkit upgrade` compiled the node's plan with it.
type StackKitRuntimeUpdate struct {
	Version string
	SHA256  string
}

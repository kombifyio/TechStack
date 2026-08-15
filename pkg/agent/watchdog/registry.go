package watchdog

// DefaultRecipes returns the five curated self-heal recipes that ship
// with the agent.  All are enabled and auto-executable by default.
func DefaultRecipes() []Recipe {
	return []Recipe{
		NewServiceRestart(),
		NewTunnelReconnect(),
		NewCertRotate(),
		NewDiskCleanup(),
		NewOOMRecovery(),
	}
}

// RecipeByName looks up a recipe by its Name() in the provided slice.
func RecipeByName(recipes []Recipe, name string) (Recipe, bool) {
	for _, r := range recipes {
		if r.Name() == name {
			return r, true
		}
	}
	return nil, false
}

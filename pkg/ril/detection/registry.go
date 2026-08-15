package detection

// DefaultEngines returns the five detection engines that ship with
// the Techstack-Core backend.
func DefaultEngines() []Engine {
	return []Engine{
		NewUpdateScanner(),
		NewErrorMatcher(),
		NewResourceMonitor(),
		NewSecurityScanner(),
		NewDriftDetector(),
	}
}

//go:build darwin

package sandbox

func currentAvailability() Availability {
	return availabilityFor(BackendSeatbelt, "sandbox-exec", executableLookup)
}

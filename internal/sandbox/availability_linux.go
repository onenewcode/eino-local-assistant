//go:build linux

package sandbox

func currentAvailability() Availability {
	return availabilityFor(BackendBubblewrap, "bwrap", executableLookup)
}
